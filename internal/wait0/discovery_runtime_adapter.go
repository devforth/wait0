package wait0

import (
	"net/http"

	"wait0/internal/wait0/discovery"
)

type discoveryRuntimeAdapter struct {
	s *Service
}

func newDiscoveryRuntimeAdapter(s *Service) discovery.Runtime {
	return &discoveryRuntimeAdapter{s: s}
}

func (a *discoveryRuntimeAdapter) PickRule(path string) *discovery.Rule {
	r := a.s.pickRule(path)
	if r == nil {
		return nil
	}
	return &discovery.Rule{Bypass: r.Bypass}
}

func (a *discoveryRuntimeAdapter) PeekRAM(path string) (discovery.Entry, bool) {
	ent, ok := a.s.ram.Peek(path)
	if !ok {
		return discovery.Entry{}, false
	}
	return discovery.Entry{Inactive: ent.Inactive}, true
}

func (a *discoveryRuntimeAdapter) PeekDisk(path string) (discovery.Entry, bool) {
	ent, ok := a.s.disk.Peek(path)
	if !ok {
		return discovery.Entry{}, false
	}
	return discovery.Entry{Inactive: ent.Inactive}, true
}

func (a *discoveryRuntimeAdapter) PutDisk(path string, ent discovery.Entry) {
	a.s.disk.PutAsync(path, CacheEntry{
		Status:       ent.Status,
		Header:       ent.Header,
		Body:         ent.Body,
		StoredAt:     ent.StoredAt,
		Hash32:       ent.Hash32,
		Inactive:     ent.Inactive,
		DiscoveredBy: ent.DiscoveredBy,
	})
}

func (a *discoveryRuntimeAdapter) Do(req *http.Request) (*http.Response, error) {
	return a.s.httpClient.Do(req)
}
