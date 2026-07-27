package proxy

import (
	"net/http"
	"time"
)

type Runtime interface {
	HandleControl(w http.ResponseWriter, r *http.Request) bool
	PickRule(path string) *Rule
	LoadRAM(key string, now int64) (Entry, bool)
	LoadDisk(key string) (Entry, bool)
	PromoteRAM(key string, ent Entry)
	DeleteKey(key string)
	FetchFromOrigin(r *http.Request) (Entry, bool, string, error)
	Store(key string, ent Entry)
	RevalidateAsync(key, path, query string)
	DebugHeaders() DebugHeaderSet
	WriteEntryWithStats(w http.ResponseWriter, ent Entry, wait0, reason string)
}

type Controller struct {
	rt Runtime
}

func NewController(rt Runtime) *Controller {
	return &Controller{rt: rt}
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	if c.rt.HandleControl(w, r) {
		return
	}

	path := r.URL.Path
	rule := c.rt.PickRule(path)
	varyByQueryParams := []string(nil)
	if rule != nil {
		varyByQueryParams = rule.VaryByQueryParams
	}
	cacheQuery := CanonicalCacheQuery(r.URL.RawQuery, varyByQueryParams)
	key := JoinCacheKey(path, cacheQuery)

	if rule != nil {
		if rule.Bypass {
			c.proxyPass(w, r, "bypass", "bypass-rule")
			return
		}
		if HasAnyCookie(r, rule.BypassWhenCookies) {
			c.proxyPass(w, r, "ignore-by-cookie", "bypass-cookie")
			return
		}
	}

	if r.Method != http.MethodGet {
		c.proxyPass(w, r, "bypass", "non-get-method")
		return
	}

	now := time.Now().Unix()
	if ent, ok := c.rt.LoadRAM(key, now); ok {
		if !ent.Inactive {
			c.rt.WriteEntryWithStats(w, ent, "hit", "")
			if rule != nil && rule.Expiration > 0 && IsStale(ent, rule.Expiration) {
				c.rt.RevalidateAsync(key, path, cacheQuery)
			}
			return
		}
	}

	if ent, ok := c.rt.LoadDisk(key); ok {
		if !ent.Inactive {
			c.rt.PromoteRAM(key, ent)
			c.rt.WriteEntryWithStats(w, ent, "hit", "")
			if rule != nil && rule.Expiration > 0 && IsStale(ent, rule.Expiration) {
				c.rt.RevalidateAsync(key, path, cacheQuery)
			}
			return
		}
	}

	respEnt, cacheable, statusKind, err := c.rt.FetchFromOrigin(r)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway", "origin-error", c.rt.DebugHeaders())
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if statusKind == "ignore-by-status" {
		c.rt.DeleteKey(key)
		c.rt.WriteEntryWithStats(w, respEnt, "ignore-by-status", "non-cacheable-status")
		return
	}
	if !cacheable {
		c.rt.WriteEntryWithStats(w, respEnt, "bypass", "non-cacheable-cache-control")
		return
	}

	var cachableContentTypes []string
	if rule != nil {
		cachableContentTypes = rule.CachableContentTypes
	}
	if !IsCachableContentType(respEnt.Header.Get("Content-Type"), cachableContentTypes) {
		c.rt.WriteEntryWithStats(w, respEnt, "bypass", "non-cacheable-content-type")
		return
	}

	c.rt.Store(key, respEnt)
	c.rt.WriteEntryWithStats(w, respEnt, "miss", "")
}

func (c *Controller) proxyPass(w http.ResponseWriter, r *http.Request, wait0, reason string) {
	ent, _, _, err := c.rt.FetchFromOrigin(r)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway", "origin-error", c.rt.DebugHeaders())
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	c.rt.WriteEntryWithStats(w, ent, wait0, reason)
}
