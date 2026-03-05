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
	return CacheEntry{
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
}

func fromWait0Entry(ent CacheEntry) cache.Entry {
	return cache.Entry{
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
}
