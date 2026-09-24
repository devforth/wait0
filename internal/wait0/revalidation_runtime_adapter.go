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

func (a *revalidationRuntimeAdapter) StoreResponse(target revalidation.Target, ent revalidation.Entry) (revalidation.Entry, error) {
	v := fromRevalEntry(ent)
	baseKey := proxy.BaseCacheKey(target.Key)
	_, values, err := a.s.storeCacheableResponse(baseKey, target.Headers, target.Host, v)
	if err != nil {
		if a.s.errorLog != nil {
			a.s.errorLog.Printf("Cache-Variant expression error: path=%q err=%q", target.Path, err.Error())
		}
		return revalidation.Entry{}, err
	}
	if len(values) > 0 {
		v.VariantKind = variantKindResponse
		v.VariantBaseKey = baseKey
		v.VariantValues = append([]string(nil), values...)
	}
	return toRevalEntry(v), nil
}

func (a *revalidationRuntimeAdapter) Delete(key string) {
	a.s.deleteCacheKey(key)
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

func (a *revalidationRuntimeAdapter) CachableContentTypes(path string) []string {
	rule := a.s.pickRule(path)
	if rule == nil {
		return proxy.DefaultCachableContentTypes()
	}
	return append([]string(nil), rule.CachableContentTypes...)
}

func (a *revalidationRuntimeAdapter) AllowSharedCacheWithCookies(path string) bool {
	rule := a.s.pickRule(path)
	return rule != nil && rule.AllowSharedCacheWithCookies
}

func (a *revalidationRuntimeAdapter) ResolveVariantTarget(baseKey string, headers http.Header, host string) (string, bool, error) {
	manifest, ok := a.s.peekCacheEntry(baseKey)
	if !ok || manifest.VariantKind != variantKindManifest {
		return baseKey, false, nil
	}
	key, _, _, err := a.s.resolveVariant(manifest, headers, host)
	return key, true, err
}

func (a *revalidationRuntimeAdapter) SendRevalidateMarkers() bool {
	return a.s.sendRevalidateMarkers
}

func (a *revalidationRuntimeAdapter) DebugHeaderEnabled(name string) bool {
	return a.s.debugHeaders.Enabled(name)
}

func (a *revalidationRuntimeAdapter) RandomString(n int) string {
	return randomString(n)
}

func toRevalEntry(ent CacheEntry) revalidation.Entry {
	return revalidation.Entry{
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

func fromRevalEntry(ent revalidation.Entry) CacheEntry {
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
