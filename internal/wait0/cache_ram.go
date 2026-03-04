package wait0

import (
	"sync"
	"time"
)

type ramItem struct {
	key        string
	ent        CacheEntry
	size       int64
	lastAccess int64
	prev       *ramItem
	next       *ramItem
}

type ramCache struct {
	maxBytes int64

	mu    sync.Mutex
	items map[string]*ramItem
	head  *ramItem
	tail  *ramItem
	total int64
}

func newRAMCache(maxBytes int64) *ramCache {
	return &ramCache{maxBytes: maxBytes, items: map[string]*ramItem{}}
}

func (c *ramCache) TotalSize() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *ramCache) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.items))
	for k := range c.items {
		out = append(out, k)
	}
	return out
}

func (c *ramCache) Peek(key string) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return CacheEntry{}, false
	}
	return it.ent, true
}

func (c *ramCache) Get(key string, nowUnix int64) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return CacheEntry{}, false
	}
	if it.ent.Inactive {
		return CacheEntry{}, false
	}
	it.lastAccess = nowUnix
	c.moveToFront(it)
	return it.ent, true
}

func (c *ramCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return
	}
	c.remove(it)
	delete(c.items, key)
	c.total -= it.size
}

func (c *ramCache) Put(key string, ent CacheEntry, disk *diskCache, overflowLog *rateLimitedLogger) {
	b, err := encodeGob(ent)
	if err != nil {
		return
	}
	sz := int64(len(b))

	if c.maxBytes > 0 && sz > c.maxBytes {
		// too big for RAM, try disk only
		if disk != nil {
			disk.PutAsync(key, ent)
		}
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix()

	if it, ok := c.items[key]; ok {
		c.total -= it.size
		it.ent = ent
		it.size = sz
		it.lastAccess = now
		c.total += sz
		c.moveToFront(it)
		return
	}

	for c.maxBytes > 0 && c.total+sz > c.maxBytes {
		c.evictToDiskLocked(disk)
		if c.tail == nil {
			break
		}
		if c.total+sz <= c.maxBytes {
			break
		}
		// if still overflowing, drop 10% and log (rate-limited)
		overflowLog.Printf("RAM cache overflow, evicting")
	}

	it := &ramItem{key: key, ent: ent, size: sz, lastAccess: now}
	c.items[key] = it
	c.addToFront(it)
	c.total += sz
}

func (c *ramCache) SnapshotAccessTimes() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.items))
	for k, it := range c.items {
		out[k] = it.lastAccess
	}
	return out
}

func (c *ramCache) evictToDiskLocked(disk *diskCache) {
	// move 10% least-recently-used
	count := len(c.items)
	if count == 0 {
		return
	}
	n := max(count/10, 1)
	for range n {
		it := c.tail
		if it == nil {
			return
		}
		if disk != nil {
			disk.PutAsync(it.key, it.ent)
		}
		c.remove(it)
		delete(c.items, it.key)
		c.total -= it.size
	}
}

func (c *ramCache) addToFront(it *ramItem) {
	it.prev = nil
	it.next = c.head
	if c.head != nil {
		c.head.prev = it
	}
	c.head = it
	if c.tail == nil {
		c.tail = it
	}
}

func (c *ramCache) remove(it *ramItem) {
	if it.prev != nil {
		it.prev.next = it.next
	} else {
		c.head = it.next
	}
	if it.next != nil {
		it.next.prev = it.prev
	} else {
		c.tail = it.prev
	}
	it.prev, it.next = nil, nil
}

func (c *ramCache) moveToFront(it *ramItem) {
	if c.head == it {
		return
	}
	c.remove(it)
	c.addToFront(it)
}
