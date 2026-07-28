package wait0

import "wait0/internal/wait0/cache"

type ramCache struct {
	inner *cache.RAM
}

func newRAMCache(maxBytes int64) *ramCache {
	return &ramCache{inner: cache.NewRAM(maxBytes)}
}

func (c *ramCache) TotalSize() int64 {
	return c.inner.TotalSize()
}

func (c *ramCache) Keys() []string {
	return c.inner.Keys()
}

func (c *ramCache) Peek(key string) (CacheEntry, bool) {
	ent, ok := c.inner.Peek(key)
	if !ok {
		return CacheEntry{}, false
	}
	return toWait0Entry(ent), true
}

func (c *ramCache) Get(key string, nowUnix int64) (CacheEntry, bool) {
	ent, ok := c.inner.Get(key, nowUnix)
	if !ok {
		return CacheEntry{}, false
	}
	return toWait0Entry(ent), true
}

func (c *ramCache) Delete(key string) {
	c.inner.Delete(key)
}

func (c *ramCache) Put(key string, ent CacheEntry, disk *diskCache, overflowLog cache.Logger) {
	var d *cache.Disk
	if disk != nil {
		d = disk.inner
	}
	c.inner.Put(key, fromWait0Entry(ent), d, overflowLog)
}

func (c *ramCache) SnapshotAccessTimes() map[string]int64 {
	return c.inner.SnapshotAccessTimes()
}

func (c *ramCache) MetaSnapshot() map[string]cache.EntryMeta {
	return c.inner.MetaSnapshot()
}

func (c *ramCache) setLastAccessForTest(key string, ts int64) bool {
	return c.inner.SetLastAccessForTest(key, ts)
}

func toWait0Entry(ent cache.Entry) CacheEntry {
	out := CacheEntry{
		Status:        ent.Status,
		Header:        ent.Header,
		Body:          ent.Body,
		StoredAt:      ent.StoredAt,
		Hash32:        ent.Hash32,
		Inactive:      ent.Inactive,
		DiscoveredBy:  ent.DiscoveredBy,
		RevalidatedAt: ent.RevalidatedAt,
		RevalidatedBy: ent.RevalidatedBy,
	}
	if ent.Variant != nil {
		out.VariantKind = ent.Variant.Kind
		out.VariantExpressions = append([]string(nil), ent.Variant.Expressions...)
		out.VariantFingerprint = ent.Variant.Fingerprint
		out.VariantHeaderNames = append([]string(nil), ent.Variant.HeaderNames...)
		out.VariantBaseKey = ent.Variant.BaseKey
		out.VariantValues = append([]string(nil), ent.Variant.Values...)
		out.VariantRequestHeaders = cloneHTTPHeader(ent.Variant.RequestHeaders)
	}
	return out
}

func fromWait0Entry(ent CacheEntry) cache.Entry {
	out := cache.Entry{
		Status:        ent.Status,
		Header:        ent.Header,
		Body:          ent.Body,
		StoredAt:      ent.StoredAt,
		Hash32:        ent.Hash32,
		Inactive:      ent.Inactive,
		DiscoveredBy:  ent.DiscoveredBy,
		RevalidatedAt: ent.RevalidatedAt,
		RevalidatedBy: ent.RevalidatedBy,
	}
	if ent.VariantKind != "" {
		out.Variant = &cache.VariantData{
			Kind:           ent.VariantKind,
			Expressions:    append([]string(nil), ent.VariantExpressions...),
			Fingerprint:    ent.VariantFingerprint,
			HeaderNames:    append([]string(nil), ent.VariantHeaderNames...),
			BaseKey:        ent.VariantBaseKey,
			Values:         append([]string(nil), ent.VariantValues...),
			RequestHeaders: cloneHTTPHeader(ent.VariantRequestHeaders),
		}
	}
	return out
}

func cloneHTTPHeader(h map[string][]string) map[string][]string {
	if h == nil {
		return nil
	}
	out := make(map[string][]string, len(h))
	for key, values := range h {
		out[key] = append([]string(nil), values...)
	}
	return out
}
