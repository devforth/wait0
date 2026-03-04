package wait0

import (
	"bytes"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type diskMeta struct {
	Size       int64
	LastAccess int64
}

type diskOp struct {
	putKey string
	putEnt *CacheEntry
	delKey string
}

type diskCache struct {
	maxBytes int64

	db *leveldb.DB

	mu        sync.Mutex
	index     map[string]diskMeta
	totalSize int64

	ops  chan diskOp
	done chan struct{}
}

func (d *diskCache) SnapshotAccessTimes() map[string]int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]int64, len(d.index))
	for k, m := range d.index {
		out[k] = m.LastAccess
	}
	return out
}

func (d *diskCache) TotalSize() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.totalSize
}

func (d *diskCache) KeyCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.index)
}

func (d *diskCache) HasKey(key string) bool {
	d.mu.Lock()
	_, ok := d.index[key]
	d.mu.Unlock()
	return ok
}

func newDiskCache(path string, maxBytes int64, invalidateOnStart bool) (*diskCache, error) {
	if invalidateOnStart {
		// Efficient invalidation: remove the DB directory (no key iteration).
		// Ignore errors when it doesn't exist.
		_ = os.RemoveAll(path)
	}
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	d := &diskCache{
		maxBytes: maxBytes,
		db:       db,
		index:    map[string]diskMeta{},
		ops:      make(chan diskOp, 1024),
		done:     make(chan struct{}),
	}
	if err := d.loadIndex(); err != nil {
		_ = db.Close()
		return nil, err
	}
	go d.writerLoop()
	return d, nil
}

func (d *diskCache) close() {
	close(d.ops)
	<-d.done
	_ = d.db.Close()
}

func (d *diskCache) loadIndex() error {
	it := d.db.NewIterator(util.BytesPrefix([]byte("m:")), nil)
	defer it.Release()

	var total int64
	idx := map[string]diskMeta{}
	for it.Next() {
		key := string(bytes.TrimPrefix(it.Key(), []byte("m:")))
		var meta diskMeta
		if err := decodeGob(it.Value(), &meta); err != nil {
			continue
		}
		idx[key] = meta
		total += meta.Size
	}
	if err := it.Error(); err != nil {
		return err
	}
	d.mu.Lock()
	d.index = idx
	d.totalSize = total
	d.mu.Unlock()
	return nil
}

func (d *diskCache) Keys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.index))
	for k := range d.index {
		out = append(out, k)
	}
	return out
}

func (d *diskCache) Peek(key string) (CacheEntry, bool) {
	b, err := d.db.Get([]byte("e:"+key), nil)
	if err != nil {
		return CacheEntry{}, false
	}
	var ent CacheEntry
	if err := decodeGob(b, &ent); err != nil {
		return CacheEntry{}, false
	}
	return ent, true
}

func (d *diskCache) Get(key string) (CacheEntry, bool) {
	ent, ok := d.Peek(key)
	if !ok {
		return CacheEntry{}, false
	}
	if ent.Inactive {
		return CacheEntry{}, false
	}
	now := time.Now().Unix()
	d.mu.Lock()
	meta, exists := d.index[key]
	if exists {
		meta.LastAccess = now
		d.index[key] = meta
	}
	d.mu.Unlock()
	if exists {
		d.ops <- diskOp{putKey: key, putEnt: nil} // meta touch
	}
	return ent, true
}

func (d *diskCache) PutAsync(key string, ent CacheEntry) {
	clone := ent
	d.ops <- diskOp{putKey: key, putEnt: &clone}
}

func (d *diskCache) Delete(key string) {
	d.ops <- diskOp{delKey: key}
}

func (d *diskCache) writerLoop() {
	defer close(d.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	for op := range d.ops {
		if op.delKey != "" {
			d.applyDelete(op.delKey)
			continue
		}
		if op.putKey != "" {
			d.applyPutOrTouch(op.putKey, op.putEnt)
		}
	}
}

func (d *diskCache) applyPutOrTouch(key string, ent *CacheEntry) {
	now := time.Now().Unix()

	d.mu.Lock()
	meta := d.index[key]
	d.mu.Unlock()

	batch := new(leveldb.Batch)

	if ent != nil {
		b, err := encodeGob(*ent)
		if err != nil {
			return
		}
		size := int64(len(b))

		// update totals/index
		d.mu.Lock()
		old := d.index[key]
		if old.Size > 0 {
			d.totalSize -= old.Size
		}
		meta.Size = size
		meta.LastAccess = now
		d.index[key] = meta
		d.totalSize += size
		total := d.totalSize
		max := d.maxBytes
		d.mu.Unlock()

		batch.Put([]byte("e:"+key), b)
		mb, _ := encodeGob(meta)
		batch.Put([]byte("m:"+key), mb)
		_ = d.db.Write(batch, nil)

		if total > max {
			d.evictSome()
		}
		return
	}

	// touch only
	if meta.Size == 0 {
		return
	}
	meta.LastAccess = now
	d.mu.Lock()
	d.index[key] = meta
	d.mu.Unlock()
	mb, _ := encodeGob(meta)
	batch.Put([]byte("m:"+key), mb)
	_ = d.db.Write(batch, nil)
}

func (d *diskCache) applyDelete(key string) {
	batch := new(leveldb.Batch)
	batch.Delete([]byte("e:" + key))
	batch.Delete([]byte("m:" + key))
	_ = d.db.Write(batch, nil)

	d.mu.Lock()
	if meta, ok := d.index[key]; ok {
		d.totalSize -= meta.Size
		delete(d.index, key)
	}
	d.mu.Unlock()
}

func (d *diskCache) evictSome() {
	d.mu.Lock()
	items := make([]struct {
		key string
		m   diskMeta
	}, 0, len(d.index))
	for k, m := range d.index {
		items = append(items, struct {
			key string
			m   diskMeta
		}{k, m})
	}
	d.mu.Unlock()

	sort.Slice(items, func(i, j int) bool {
		return items[i].m.LastAccess < items[j].m.LastAccess
	})

	n := max(len(items)/10, 1)

	for i := 0; i < n && i < len(items); i++ {
		d.applyDelete(items[i].key)
	}
}
