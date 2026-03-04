package wait0

import (
	"net/http"

	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
)

type revalidationRuntimeAdapter struct {
	s *Service
}

func newRevalidationRuntimeAdapter(s *Service) revalidation.Runtime {
	return &revalidationRuntimeAdapter{s: s}
}

func (a *revalidationRuntimeAdapter) Peek(key string) (revalidation.Entry, bool) {
	if ent, ok := a.s.ram.Peek(key); ok {
		return toRevalEntry(ent), true
	}
	if ent, ok := a.s.disk.Peek(key); ok {
		return toRevalEntry(ent), true
	}
	return revalidation.Entry{}, false
}

func (a *revalidationRuntimeAdapter) Put(key string, ent revalidation.Entry) {
	v := fromRevalEntry(ent)
	a.s.ram.Put(key, v, a.s.disk, a.s.overflowLog)
	a.s.disk.PutAsync(key, v)
}

func (a *revalidationRuntimeAdapter) Delete(key string) {
	a.s.ram.Delete(key)
	a.s.disk.Delete(key)
}

func (a *revalidationRuntimeAdapter) SnapshotAccessTimes() map[string]int64 {
	ram := a.s.ram.SnapshotAccessTimes()
	disk := a.s.disk.SnapshotAccessTimes()
	out := make(map[string]int64, len(ram)+len(disk))
	for k, ts := range disk {
		out[k] = ts
	}
	for k, ts := range ram {
		if cur, ok := out[k]; !ok || ts > cur {
			out[k] = ts
		}
	}
	return out
}

func (a *revalidationRuntimeAdapter) AllKeys() []string {
	m := map[string]struct{}{}
	for _, k := range a.s.ram.Keys() {
		m[k] = struct{}{}
	}
	for _, k := range a.s.disk.Keys() {
		m[k] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (a *revalidationRuntimeAdapter) Origin() string {
	return a.s.cfg.Server.Origin
}

func (a *revalidationRuntimeAdapter) Do(req *http.Request) (*http.Response, error) {
	return a.s.httpClient.Do(req)
}

func (a *revalidationRuntimeAdapter) SendRevalidateMarkers() bool {
	return a.s.sendRevalidateMarkers
}

func (a *revalidationRuntimeAdapter) RandomString(n int) string {
	return randomString(n)
}

func toRevalEntry(ent CacheEntry) revalidation.Entry {
	return revalidation.Entry{
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

func fromRevalEntry(ent revalidation.Entry) CacheEntry {
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
