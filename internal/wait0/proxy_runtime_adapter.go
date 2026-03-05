package wait0

import (
	"net/http"

	"wait0/internal/wait0/invalidation"
	"wait0/internal/wait0/proxy"
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
		Bypass:            r.Bypass,
		BypassWhenCookies: append([]string(nil), r.BypassWhenCookies...),
		Expiration:        r.expDur,
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

func (a *proxyRuntimeAdapter) DeleteKey(key string) {
	a.s.ram.Delete(key)
	a.s.disk.Delete(key)
}

func (a *proxyRuntimeAdapter) FetchFromOrigin(r *http.Request) (proxy.Entry, bool, string, error) {
	return a.fetcher.FetchFromOrigin(r)
}

func (a *proxyRuntimeAdapter) Store(key string, ent proxy.Entry) {
	v := fromProxyEntry(ent)
	a.s.ram.Put(key, v, a.s.disk, a.s.overflowLog)
	a.s.disk.PutAsync(key, v)
}

func (a *proxyRuntimeAdapter) RevalidateAsync(key, path, query string) {
	if a.s.reval == nil {
		return
	}
	a.s.reval.Async(key, path, query, "user")
}

func (a *proxyRuntimeAdapter) WriteEntryWithStats(w http.ResponseWriter, ent proxy.Entry, wait0 string) {
	proxy.WriteEntry(w, ent, wait0)
	if a.s.stats != nil {
		switch wait0 {
		case "hit", "miss":
			a.s.stats.Observe(len(ent.Body))
		}
	}
}

func toProxyEntry(ent CacheEntry) proxy.Entry {
	return proxy.Entry{
		Status:        ent.Status,
		Header:        proxy.CloneHeader(ent.Header),
		Body:          append([]byte(nil), ent.Body...),
		StoredAt:      ent.StoredAt,
		Hash32:        ent.Hash32,
		Inactive:      ent.Inactive,
		DiscoveredBy:  ent.DiscoveredBy,
		RevalidatedAt: ent.RevalidatedAt,
		RevalidatedBy: ent.RevalidatedBy,
	}
}

func fromProxyEntry(ent proxy.Entry) CacheEntry {
	return CacheEntry{
		Status:        ent.Status,
		Header:        proxy.CloneHeader(ent.Header),
		Body:          append([]byte(nil), ent.Body...),
		StoredAt:      ent.StoredAt,
		Hash32:        ent.Hash32,
		Inactive:      ent.Inactive,
		DiscoveredBy:  ent.DiscoveredBy,
		RevalidatedAt: ent.RevalidatedAt,
		RevalidatedBy: ent.RevalidatedBy,
	}
}
