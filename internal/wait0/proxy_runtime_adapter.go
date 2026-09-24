package wait0

import (
	"net/http"

	"wait0/internal/wait0/dashboard"
	"wait0/internal/wait0/invalidation"
	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
	"wait0/internal/wait0/statapi"
)

type proxyRuntimeAdapter struct {
	s *Service

	fetcher proxy.Fetcher
}

func newProxyRuntimeAdapter(s *Service) proxy.Runtime {
	return &proxyRuntimeAdapter{
		s: s,
		fetcher: proxy.Fetcher{
			Client: s.httpClient,
			Origin: s.cfg.Server.Origin,
		},
	}
}

func (a *proxyRuntimeAdapter) HandleControl(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case invalidation.EndpointPath:
		if a.s.inv == nil {
			http.NotFound(w, r)
		} else {
			a.s.inv.Handle(w, r)
		}
		return true
	case statapi.EndpointPath, statapi.EndpointPath + "/":
		if a.s.stat == nil {
			http.NotFound(w, r)
		} else {
			a.s.stat.Handle(w, r)
		}
		return true
	case dashboard.EndpointPath, dashboard.EndpointPath + "/", dashboard.StatsEndpointPath, dashboard.InvalidateEndpointPath:
		if a.s.dash == nil {
			http.NotFound(w, r)
		} else {
			a.s.dash.Handle(w, r)
		}
		return true
	default:
		return false
	}
}

func (a *proxyRuntimeAdapter) PickRule(path string) *proxy.Rule {
	r := a.s.pickRule(path)
	if r == nil {
		return nil
	}
	return &proxy.Rule{
		Bypass:                      r.Bypass,
		BypassWhenCookies:           append([]string(nil), r.BypassWhenCookies...),
		AllowSharedCacheWithCookies: r.AllowSharedCacheWithCookies,
		BypassWhenRequestHeaders:    append([]string(nil), r.BypassWhenRequestHeaders...),
		CachableContentTypes:        append([]string(nil), r.CachableContentTypes...),
		VaryByQueryParams:           append([]string(nil), r.VaryByQueryParams...),
		Expiration:                  r.expDur,
	}
}

func (a *proxyRuntimeAdapter) LoadRAM(key string, now int64) (proxy.Entry, bool) {
	ent, ok := a.s.ram.Get(key, now)
	if !ok {
		return proxy.Entry{}, false
	}
	return toProxyEntry(ent), true
}

func (a *proxyRuntimeAdapter) LoadDisk(key string) (proxy.Entry, bool) {
	ent, ok := a.s.disk.Get(key)
	if !ok {
		return proxy.Entry{}, false
	}
	return toProxyEntry(ent), true
}

func (a *proxyRuntimeAdapter) PromoteRAM(key string, ent proxy.Entry) {
	a.s.ram.Put(key, fromProxyEntry(ent), a.s.disk, a.s.overflowLog)
}

func (a *proxyRuntimeAdapter) ResolveVariant(manifest proxy.Entry, r *http.Request) (string, []string, error) {
	key, values, _, err := a.s.resolveVariant(fromProxyEntry(manifest), r.Header, r.Host)
	return key, values, err
}

func (a *proxyRuntimeAdapter) DeleteKey(key string) {
	a.s.deleteCacheKey(key)
}

func (a *proxyRuntimeAdapter) FetchFromOrigin(r *http.Request, allowSharedCacheWithCookies bool) (proxy.Entry, bool, string, error) {
	return a.fetcher.FetchFromOrigin(r, allowSharedCacheWithCookies)
}

func (a *proxyRuntimeAdapter) Store(key string, r *http.Request, ent proxy.Entry) (proxy.Entry, error) {
	v := fromProxyEntry(ent)
	storedKey, values, err := a.s.storeCacheableResponse(key, r.Header, r.Host, v)
	if err != nil {
		if a.s.errorLog != nil {
			a.s.errorLog.Printf("Cache-Variant expression error: path=%q err=%q", r.URL.Path, err.Error())
		}
		return proxy.Entry{}, err
	}
	if storedKey != key {
		v.VariantKind = variantKindResponse
		v.VariantBaseKey = key
		v.VariantValues = append([]string(nil), values...)
	}
	return toProxyEntry(v), nil
}

func (a *proxyRuntimeAdapter) RevalidateAsync(key, path, query string, headers http.Header, host string) {
	if a.s.reval == nil {
		return
	}
	a.s.reval.Async(revalidation.Target{Key: key, Path: path, Query: query, Headers: headers, Host: host}, "user")
}

func (a *proxyRuntimeAdapter) DebugHeaders() proxy.DebugHeaderSet {
	return a.s.debugHeaders
}

func (a *proxyRuntimeAdapter) WriteEntryWithStats(w http.ResponseWriter, ent proxy.Entry, wait0, reason string) {
	proxy.WriteEntry(w, ent, wait0, reason, a.s.debugHeaders)
	if a.s.stats != nil {
		switch wait0 {
		case "hit", "miss":
			a.s.stats.Observe(len(ent.Body))
		}
	}
}

func toProxyEntry(ent CacheEntry) proxy.Entry {
	return proxy.Entry{
		Status:                ent.Status,
		Header:                proxy.CloneHeader(ent.Header),
		Body:                  append([]byte(nil), ent.Body...),
		StoredAt:              ent.StoredAt,
		Hash32:                ent.Hash32,
		Inactive:              ent.Inactive,
		DiscoveredBy:          ent.DiscoveredBy,
		RevalidatedAt:         ent.RevalidatedAt,
		RevalidatedBy:         ent.RevalidatedBy,
		VariantKind:           ent.VariantKind,
		VariantExpressions:    append([]string(nil), ent.VariantExpressions...),
		VariantFingerprint:    ent.VariantFingerprint,
		VariantHeaderNames:    append([]string(nil), ent.VariantHeaderNames...),
		VariantBaseKey:        ent.VariantBaseKey,
		VariantValues:         append([]string(nil), ent.VariantValues...),
		VariantRequestHeaders: proxy.CloneHeader(ent.VariantRequestHeaders),
	}
}

func fromProxyEntry(ent proxy.Entry) CacheEntry {
	return CacheEntry{
		Status:                ent.Status,
		Header:                proxy.CloneHeader(ent.Header),
		Body:                  append([]byte(nil), ent.Body...),
		StoredAt:              ent.StoredAt,
		Hash32:                ent.Hash32,
		Inactive:              ent.Inactive,
		DiscoveredBy:          ent.DiscoveredBy,
		RevalidatedAt:         ent.RevalidatedAt,
		RevalidatedBy:         ent.RevalidatedBy,
		VariantKind:           ent.VariantKind,
		VariantExpressions:    append([]string(nil), ent.VariantExpressions...),
		VariantFingerprint:    ent.VariantFingerprint,
		VariantHeaderNames:    append([]string(nil), ent.VariantHeaderNames...),
		VariantBaseKey:        ent.VariantBaseKey,
		VariantValues:         append([]string(nil), ent.VariantValues...),
		VariantRequestHeaders: proxy.CloneHeader(ent.VariantRequestHeaders),
	}
}
