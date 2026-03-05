package cache

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
	Size         int64
	LastAccess   int64
	StatsSize    int64
	Inactive     bool
	DiscoveredBy string
	LastRefresh  int64
}

type diskOp struct {
	putKey string
	putEnt *Entry
	delKey string
}

type Disk struct {
	maxBytes int64

	db *leveldb.DB

	mu        sync.Mutex
	index     map[string]diskMeta
	totalSize int64

	ops  chan diskOp
	done chan struct{}
}

func NewDisk(path string, maxBytes int64, invalidateOnStart bool) (*Disk, error) {
	if invalidateOnStart {
		_ = os.RemoveAll(path)
	}
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	d := &Disk{
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

func (d *Disk) Close() {
	close(d.ops)
	<-d.done
	_ = d.db.Close()
}

func (d *Disk) SnapshotAccessTimes() map[string]int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]int64, len(d.index))
	for k, m := range d.index {
		out[k] = m.LastAccess
	}
	return out
}

func (d *Disk) MetaSnapshot() map[string]EntryMeta {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]EntryMeta, len(d.index))
	for k, m := range d.index {
		lastRefresh := m.LastRefresh
		if lastRefresh <= 0 {
			// Backward compatibility for metadata written before LastRefresh existed.
			lastRefresh = m.LastAccess * int64(time.Second)
		}
		size := m.StatsSize
		if size <= 0 {
			size = m.Size
		}
		out[k] = EntryMeta{
			Size:                size,
			Inactive:            m.Inactive,
			DiscoveredBy:        m.DiscoveredBy,
			LastRefreshUnixNano: lastRefresh,
		}
	}
	return out
}

func (d *Disk) TotalSize() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.totalSize
}

func (d *Disk) KeyCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.index)
}

func (d *Disk) HasKey(key string) bool {
	d.mu.Lock()
	_, ok := d.index[key]
	d.mu.Unlock()
	return ok
}

func (d *Disk) Keys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.index))
	for k := range d.index {
		out = append(out, k)
	}
	return out
}

func (d *Disk) Peek(key string) (Entry, bool) {
	b, err := d.db.Get([]byte("e:"+key), nil)
	if err != nil {
		return Entry{}, false
	}
	var ent Entry
	if err := decodeGob(b, &ent); err != nil {
		return Entry{}, false
	}
	return ent, true
}

func (d *Disk) Get(key string) (Entry, bool) {
	ent, ok := d.Peek(key)
	if !ok {
		return Entry{}, false
	}
	if ent.Inactive {
		return Entry{}, false
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
		d.ops <- diskOp{putKey: key, putEnt: nil}
	}
	return ent, true
}

func (d *Disk) PutAsync(key string, ent Entry) {
	clone := ent
	d.ops <- diskOp{putKey: key, putEnt: &clone}
}

func (d *Disk) Delete(key string) {
	d.ops <- diskOp{delKey: key}
}

func (d *Disk) EvictSomeForTest() {
	d.evictSome()
}

func (d *Disk) loadIndex() error {
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

func (d *Disk) writerLoop() {
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

func (d *Disk) applyPutOrTouch(key string, ent *Entry) {
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
		statsSize := EntryLogicalSize(*ent)
		lastRefresh := ent.RevalidatedAt
		if lastRefresh <= 0 && ent.StoredAt > 0 {
			lastRefresh = ent.StoredAt * int64(time.Second)
		}

		d.mu.Lock()
		old := d.index[key]
		if old.Size > 0 {
			d.totalSize -= old.Size
		}
		meta.Size = size
		meta.LastAccess = now
		meta.StatsSize = statsSize
		meta.Inactive = ent.Inactive
		meta.DiscoveredBy = ent.DiscoveredBy
		meta.LastRefresh = lastRefresh
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

func (d *Disk) applyDelete(key string) {
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

func (d *Disk) evictSome() {
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
