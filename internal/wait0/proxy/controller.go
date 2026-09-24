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
	ResolveVariant(manifest Entry, r *http.Request) (key string, values []string, err error)
	DeleteKey(key string)
	FetchFromOrigin(r *http.Request, allowSharedCacheWithCookies bool) (Entry, bool, string, error)
	Store(key string, r *http.Request, ent Entry) (Entry, error)
	RevalidateAsync(key, path, query string, headers http.Header, host string)
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
	allowSharedCacheWithCookies := rule != nil && rule.AllowSharedCacheWithCookies
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
		if HasAnyRequestHeader(r, rule.BypassWhenRequestHeaders) {
			c.proxyPass(w, r, "ignore-by-request-header", "bypass-request-header")
			return
		}
	}

	if r.Method != http.MethodGet {
		c.proxyPass(w, r, "bypass", "non-get-method")
		return
	}

	now := time.Now().Unix()
	root, ok := c.rt.LoadRAM(key, now)
	if !ok {
		if diskEnt, diskOK := c.rt.LoadDisk(key); diskOK {
			c.rt.PromoteRAM(key, diskEnt)
			root, ok = diskEnt, true
		}
	}

	deleteKey := key
	if ok && !root.Inactive {
		selectedKey := key
		selected := root
		if root.VariantKind == "manifest" {
			var err error
			selectedKey, _, err = c.rt.ResolveVariant(root, r)
			if err != nil {
				// A declaration can be fixed by the next origin response, so an
				// evaluation failure is a cache miss rather than a gateway error.
				deleteKey = ""
			} else {
				deleteKey = selectedKey
				selected, ok = c.rt.LoadRAM(selectedKey, now)
				if !ok {
					if diskEnt, diskOK := c.rt.LoadDisk(selectedKey); diskOK {
						c.rt.PromoteRAM(selectedKey, diskEnt)
						selected, ok = diskEnt, true
					}
				}
			}
		}
		if ok && selected.VariantKind != "manifest" && !selected.Inactive {
			reason := CachedResponseCacheabilityReason(r.Header, selected, allowSharedCacheWithCookies)
			if reason == "" {
				c.rt.WriteEntryWithStats(w, selected, "hit", "")
				if rule != nil && rule.Expiration > 0 && IsStale(selected, rule.Expiration) {
					revalidationHeaders := CloneHeader(selected.VariantRequestHeaders)
					c.rt.RevalidateAsync(selectedKey, path, cacheQuery, revalidationHeaders, revalidationHeaders.Get("Host"))
				}
				return
			}
			if reason != CacheabilityCredentials {
				c.rt.DeleteKey(selectedKey)
			}
		}
	}

	respEnt, cacheable, statusKind, err := c.rt.FetchFromOrigin(r, allowSharedCacheWithCookies)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway", "origin-error", c.rt.DebugHeaders())
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if statusKind == "ignore-by-status" {
		if deleteKey != "" {
			c.rt.DeleteKey(deleteKey)
		}
		c.rt.WriteEntryWithStats(w, respEnt, "ignore-by-status", "non-cacheable-status")
		return
	}
	if !cacheable {
		reason := statusKind
		if reason == "" || reason == "ok" {
			reason = CacheabilityCacheControl
		}
		c.rt.WriteEntryWithStats(w, respEnt, "bypass", reason)
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

	storedEnt, err := c.rt.Store(key, r, respEnt)
	if err != nil {
		c.rt.WriteEntryWithStats(w, respEnt, "bypass", "cache-variant-expression-error")
		return
	}
	c.rt.WriteEntryWithStats(w, storedEnt, "miss", "")
}

func (c *Controller) proxyPass(w http.ResponseWriter, r *http.Request, wait0, reason string) {
	ent, _, _, err := c.rt.FetchFromOrigin(r, false)
	if err != nil {
		SetWait0Headers(w.Header(), "bad-gateway", "origin-error", c.rt.DebugHeaders())
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	c.rt.WriteEntryWithStats(w, ent, wait0, reason)
}
