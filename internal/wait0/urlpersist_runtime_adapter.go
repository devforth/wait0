package wait0

import (
	"net/http"
	"time"

	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/urlpersist"
)

type urlpersistRuntimeAdapter struct {
	s *Service
}

func newURLPersistRuntimeAdapter(s *Service) urlpersist.Runtime {
	return &urlpersistRuntimeAdapter{s: s}
}

// Seed writes the inactive placeholder that makes a remembered URL visible to
// warmup. It deliberately does not fetch anything: the existing warmup loop
// already knows how to turn an inactive entry into real content, with its own
// concurrency budget and pacing.
func (a *urlpersistRuntimeAdapter) Seed(rec urlpersist.Record) bool {
	// The rule set can change between runs, so a remembered URL is re-checked
	// against the current rules exactly as sitemap discovery does.
	rule := a.s.pickRule(rec.Path)
	if rule == nil || rule.Bypass {
		return false
	}

	// A seed is an empty placeholder, so it must never land on a key that
	// already holds real content — a URL served during boot, before restore
	// reached its record, would otherwise be blanked out.
	key := rec.CacheKey()
	if _, ok := a.s.ram.Peek(key); ok {
		return true
	}
	if a.s.disk.HasKey(key) {
		return true
	}

	baseKey := rec.BaseCacheKey()
	discoveredBy := rec.DiscoveredBy
	if discoveredBy == "" {
		discoveredBy = "user"
	}

	ent := CacheEntry{
		Status:       http.StatusOK,
		Header:       make(http.Header),
		Body:         []byte{},
		StoredAt:     time.Now().UTC().Unix(),
		Hash32:       0,
		Inactive:     true,
		DiscoveredBy: discoveredBy,
	}

	if rec.Variant == nil {
		a.s.disk.PutAsyncWithAccess(baseKey, ent, rec.LastSeen)
		return true
	}

	// VariantKind must be set or the whole variant sub-struct, including the
	// request headers that make the URL replayable, is dropped on write.
	ent.VariantKind = variantKindResponse
	ent.VariantFingerprint = rec.Variant.Fingerprint
	ent.VariantBaseKey = baseKey
	ent.VariantValues = append([]string(nil), rec.Variant.Values...)
	ent.VariantRequestHeaders = proxy.CloneHeader(http.Header(rec.Variant.Headers))

	childKey := key

	// Registering the seed in the family map is what lets a superseded
	// generation be reclaimed when the origin changes its Cache-Variant
	// declarations. Take the same lock storeCacheableResponse takes so the
	// put and the registration cannot interleave with a concurrent store.
	lock := a.s.variantLock(baseKey)
	lock.Lock()
	a.s.disk.PutAsyncWithAccess(childKey, ent, rec.LastSeen)
	a.s.addVariantChild(baseKey, childKey)
	lock.Unlock()
	return true
}

func (a *urlpersistRuntimeAdapter) SnapshotAccessTimes() map[string]int64 {
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

// notePersistedStore forwards a successful cache write to the URL persister.
// It is called from storeCacheableResponse, which is the single point every
// fetched origin response passes through, so bypassed requests, non-GET
// requests and responses rejected by the status, Cache-Control or content-type
// gates never reach it.
func (s *Service) notePersistedStore(key, baseKey string, ent CacheEntry) {
	if s.urlp == nil {
		return
	}
	path, query := proxy.SplitCacheKey(baseKey)
	rec := urlpersist.Record{
		Path:         path,
		Query:        query,
		DiscoveredBy: ent.DiscoveredBy,
	}
	if ent.VariantKind == variantKindResponse {
		rec.Variant = &urlpersist.Variant{
			Fingerprint: ent.VariantFingerprint,
			Values:      append([]string(nil), ent.VariantValues...),
			Headers:     map[string][]string(proxy.CloneHeader(ent.VariantRequestHeaders)),
		}
	}
	s.urlp.NoteStored(key, rec)
}

func (s *Service) notePersistedDelete(key string) {
	if s.urlp == nil {
		return
	}
	s.urlp.NoteDeleted(key)
}
