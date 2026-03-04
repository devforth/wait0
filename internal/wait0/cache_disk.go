package wait0

import "wait0/internal/wait0/cache"

type diskCache struct {
	inner *cache.Disk
}

func newDiskCache(path string, maxBytes int64, invalidateOnStart bool) (*diskCache, error) {
	d, err := cache.NewDisk(path, maxBytes, invalidateOnStart)
	if err != nil {
		return nil, err
	}
	return &diskCache{inner: d}, nil
}

func (d *diskCache) close() {
	d.inner.Close()
}

func (d *diskCache) SnapshotAccessTimes() map[string]int64 {
	return d.inner.SnapshotAccessTimes()
}

func (d *diskCache) TotalSize() int64 {
	return d.inner.TotalSize()
}

func (d *diskCache) KeyCount() int {
	return d.inner.KeyCount()
}

func (d *diskCache) HasKey(key string) bool {
	return d.inner.HasKey(key)
}

func (d *diskCache) Keys() []string {
	return d.inner.Keys()
}

func (d *diskCache) Peek(key string) (CacheEntry, bool) {
	ent, ok := d.inner.Peek(key)
	if !ok {
		return CacheEntry{}, false
	}
	return toWait0Entry(ent), true
}

func (d *diskCache) Get(key string) (CacheEntry, bool) {
	ent, ok := d.inner.Get(key)
	if !ok {
		return CacheEntry{}, false
	}
	return toWait0Entry(ent), true
}

func (d *diskCache) PutAsync(key string, ent CacheEntry) {
	d.inner.PutAsync(key, fromWait0Entry(ent))
}

func (d *diskCache) Delete(key string) {
	d.inner.Delete(key)
}

func (d *diskCache) evictSome() {
	d.inner.EvictSomeForTest()
}
