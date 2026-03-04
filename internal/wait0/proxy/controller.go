package proxy

import (
	"net/http"
	"time"
)

type Runtime interface {
	HandleInvalidation(w http.ResponseWriter, r *http.Request) bool
	PickRule(path string) *Rule
	LoadRAM(key string, now int64) (Entry, bool)
	LoadDisk(key string) (Entry, bool)
	PromoteRAM(key string, ent Entry)
	DeleteKey(key string)
	FetchFromOrigin(r *http.Request) (Entry, bool, string, error)
	Store(key string, ent Entry)
	RevalidateAsync(key, path, query string)
	WriteEntryWithStats(w http.ResponseWriter, ent Entry, wait0 string)
}

type Controller struct {
	rt Runtime
}

func NewController(rt Runtime) *Controller {
	return &Controller{rt: rt}
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	if c.rt.HandleInvalidation(w, r) {
		return
	}

	path := r.URL.Path
	key := path
	rule := c.rt.PickRule(path)

	if rule != nil {
		if rule.Bypass {
			c.proxyPass(w, r, "bypass")
			return
		}
		if HasAnyCookie(r, rule.BypassWhenCookies) {
			c.proxyPass(w, r, "ignore-by-cookie")
			return
		}
	}

	if r.Method != http.MethodGet {
		c.proxyPass(w, r, "bypass")
		return
	}

	now := time.Now().Unix()
	if ent, ok := c.rt.LoadRAM(key, now); ok {
		if !ent.Inactive {
			c.rt.WriteEntryWithStats(w, ent, "hit")
			if rule != nil && rule.Expiration > 0 && IsStale(ent, rule.Expiration) {
				c.rt.RevalidateAsync(key, r.URL.Path, r.URL.RawQuery)
			}
			return
		}
	}

	if ent, ok := c.rt.LoadDisk(key); ok {
		if !ent.Inactive {
			c.rt.PromoteRAM(key, ent)
			c.rt.WriteEntryWithStats(w, ent, "hit")
			if rule != nil && rule.Expiration > 0 && IsStale(ent, rule.Expiration) {
				c.rt.RevalidateAsync(key, r.URL.Path, r.URL.RawQuery)
			}
			return
		}
	}

	respEnt, cacheable, statusKind, err := c.rt.FetchFromOrigin(r)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if statusKind == "ignore-by-status" {
		c.rt.DeleteKey(key)
		c.rt.WriteEntryWithStats(w, respEnt, "ignore-by-status")
		return
	}
	if !cacheable {
		c.rt.WriteEntryWithStats(w, respEnt, "bypass")
		return
	}

	c.rt.Store(key, respEnt)
	c.rt.WriteEntryWithStats(w, respEnt, "miss")
}

func (c *Controller) proxyPass(w http.ResponseWriter, r *http.Request, wait0 string) {
	ent, _, _, err := c.rt.FetchFromOrigin(r)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	c.rt.WriteEntryWithStats(w, ent, wait0)
}
