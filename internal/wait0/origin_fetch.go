package wait0

import (
	"net/http"

	"wait0/internal/wait0/proxy"
)

func (s *Service) fetchFromOrigin(r *http.Request) (CacheEntry, bool, string, error) {
	f := proxy.Fetcher{Client: s.httpClient, Origin: s.cfg.Server.Origin}
	ent, cacheable, statusKind, err := f.FetchFromOrigin(r)
	if err != nil {
		return CacheEntry{}, false, "", err
	}
	return fromProxyEntry(ent), cacheable, statusKind, nil
}

func copyHeaders(dst, src http.Header) {
	proxy.CopyHeaders(dst, src)
}

func cloneHeader(h http.Header) http.Header {
	return proxy.CloneHeader(h)
}

func (s *Service) store(key string, ent CacheEntry) {
	s.ram.Put(key, ent, s.disk, s.overflowLog)
	s.disk.PutAsync(key, ent)
}
