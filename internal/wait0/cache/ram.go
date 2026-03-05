package cache

import (
	"sync"
	"time"
)

type Logger interface {
	Printf(format string, args ...any)
}

type ramItem struct {
	key        string
	ent        Entry
	size       int64
	statsSize  int64
	lastAccess int64
	prev       *ramItem
	next       *ramItem
}

type RAM struct {
	maxBytes int64

	mu    sync.Mutex
	items map[string]*ramItem
	head  *ramItem
	tail  *ramItem
	total int64
}

func NewRAM(maxBytes int64) *RAM {
	return &RAM{maxBytes: maxBytes, items: map[string]*ramItem{}}
}

func (c *RAM) TotalSize() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *RAM) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.items))
	for k := range c.items {
		out = append(out, k)
	}
	return out
}

func (c *RAM) Peek(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	return it.ent, true
}

func (c *RAM) Get(key string, nowUnix int64) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	if it.ent.Inactive {
		return Entry{}, false
	}
	it.lastAccess = nowUnix
	c.moveToFront(it)
	return it.ent, true
}

func (c *RAM) Delete(key string) {
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

func (c *RAM) Put(key string, ent Entry, disk *Disk, overflowLog Logger) {
	b, err := encodeGob(ent)
	if err != nil {
		return
	}
	sz := int64(len(b))
	statsSize := EntryLogicalSize(ent)

	if c.maxBytes > 0 && sz > c.maxBytes {
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
		it.statsSize = statsSize
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
		if overflowLog != nil {
			overflowLog.Printf("RAM cache overflow, evicting")
		}
	}

	it := &ramItem{key: key, ent: ent, size: sz, statsSize: statsSize, lastAccess: now}
	c.items[key] = it
	c.addToFront(it)
	c.total += sz
}

func (c *RAM) SnapshotAccessTimes() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.items))
	for k, it := range c.items {
		out[k] = it.lastAccess
	}
	return out
}

func (c *RAM) MetaSnapshot() map[string]EntryMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]EntryMeta, len(c.items))
	for k, it := range c.items {
		lastRefresh := it.ent.RevalidatedAt
		if lastRefresh <= 0 && it.ent.StoredAt > 0 {
			lastRefresh = it.ent.StoredAt * int64(time.Second)
		}
		out[k] = EntryMeta{
			Size:                it.statsSize,
			Inactive:            it.ent.Inactive,
			DiscoveredBy:        it.ent.DiscoveredBy,
			LastRefreshUnixNano: lastRefresh,
			StoredAtUnix:        it.ent.StoredAt,
		}
	}
	return out
}

func (c *RAM) SetLastAccessForTest(key string, ts int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return false
	}
	it.lastAccess = ts
	return true
}

func (c *RAM) evictToDiskLocked(disk *Disk) {
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

func (c *RAM) addToFront(it *ramItem) {
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

func (c *RAM) remove(it *ramItem) {
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

func (c *RAM) moveToFront(it *ramItem) {
	if c.head == it {
		return
	}
	c.remove(it)
	c.addToFront(it)
}
