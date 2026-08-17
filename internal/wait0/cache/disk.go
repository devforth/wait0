package cache

import (
	"bytes"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type diskMeta struct {
	Size           int64
	LastAccess     int64
	StatsSize      int64
	Inactive       bool
	DiscoveredBy   string
	LastRefresh    int64
	VariantKind    string
	VariantBaseKey string
	VariantValues  []string

	// BodyHash, BodyLen and ShapeHash fingerprint the blob stored under "e:" so
	// a store carrying an identical response can skip rewriting it. All three
	// are zero in metadata written before they existed, which reads as "no
	// match" and makes the first store after an upgrade rewrite the blob.
	BodyHash  uint32
	BodyLen   int64
	ShapeHash uint32

	// StoredAt and RevalidatedBy mirror the stamps inside the blob, which a
	// skipped body rewrite leaves untouched. Peek overlays these on the way out
	// so a key whose body has stopped changing never looks stale.
	StoredAt      int64
	RevalidatedBy string
}

type diskOp struct {
	putKey string
	putEnt *Entry
	delKey string
	// accessUnix overrides the last-access stamp for this put. Zero keeps the
	// default of "now", which is what every live write wants; restoring a
	// remembered URL passes the time it was actually last used so warmup order
	// survives a restart.
	accessUnix int64
}

type Disk struct {
	maxBytes int64

	db *leveldb.DB

	mu        sync.Mutex
	index     map[string]diskMeta
	totalSize int64

	// skippedBodyWrites counts stores that reused the blob already on disk. It
	// exists so tests can assert the redundant-write gate actually engaged
	// rather than infer it from timing.
	skippedBodyWrites atomic.Int64

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
			StorageSize:         m.Size,
			Inactive:            m.Inactive,
			DiscoveredBy:        m.DiscoveredBy,
			LastRefreshUnixNano: lastRefresh,
			StoredAtUnix:        m.StoredAt,
			VariantKind:         m.VariantKind,
			VariantBaseKey:      m.VariantBaseKey,
			VariantValues:       append([]string(nil), m.VariantValues...),
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
	d.mu.Lock()
	meta, known := d.index[key]
	d.mu.Unlock()
	if known {
		restoreStamps(&ent, meta)
	}
	return ent, true
}

// restoreStamps overlays the freshness stamps held in the metadata record, which
// every store rewrites, over the ones baked into the blob, which a store
// carrying an unchanged response does not.
//
// This is what keeps the redundant-write gate from being a regression: without
// it a key whose body has stopped changing would keep serving the StoredAt of
// its last real change, proxy.IsStale would call it stale forever, and every
// request that reached the disk tier would kick off a background revalidation.
func restoreStamps(ent *Entry, meta diskMeta) {
	if meta.StoredAt > ent.StoredAt {
		ent.StoredAt = meta.StoredAt
	}
	if meta.LastRefresh > ent.RevalidatedAt {
		ent.RevalidatedAt = meta.LastRefresh
		if meta.RevalidatedBy != "" {
			ent.RevalidatedBy = meta.RevalidatedBy
		}
	}
}

// SkippedBodyWrites reports how many stores reused the blob already on disk.
func (d *Disk) SkippedBodyWrites() int64 {
	return d.skippedBodyWrites.Load()
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

// PutAsyncWithAccess stores ent with an explicit last-access time instead of
// the write time.
func (d *Disk) PutAsyncWithAccess(key string, ent Entry, accessUnix int64) {
	clone := ent
	d.ops <- diskOp{putKey: key, putEnt: &clone, accessUnix: accessUnix}
}

func (d *Disk) Delete(key string) {
	d.ops <- diskOp{delKey: key}
}

func (d *Disk) EvictSomeForTest() {
	d.evictSome()
}

// loadIndex rebuilds the in-memory index from the persisted metadata records.
//
// A metadata record whose blob is missing is dropped rather than indexed. Stores
// reuse the blob a metadata record describes instead of rewriting it, so an
// indexed key with no blob would never be repaired and would answer every read
// with a miss forever. Batches are atomic, so the pairing can only break when an
// unsynced journal tail is lost to a machine crash, and open is the one moment
// that is observable.
func (d *Disk) loadIndex() error {
	it := d.db.NewIterator(util.BytesPrefix([]byte("m:")), nil)
	defer it.Release()

	var total int64
	idx := map[string]diskMeta{}
	orphans := make([][]byte, 0)
	for it.Next() {
		key := string(bytes.TrimPrefix(it.Key(), []byte("m:")))
		var meta diskMeta
		if err := decodeGob(it.Value(), &meta); err != nil {
			continue
		}
		if ok, err := d.db.Has([]byte("e:"+key), nil); err != nil || !ok {
			orphans = append(orphans, append([]byte("m:"), key...))
			continue
		}
		idx[key] = meta
		total += meta.Size
	}
	if err := it.Error(); err != nil {
		return err
	}
	if len(orphans) > 0 {
		batch := new(leveldb.Batch)
		for _, key := range orphans {
			batch.Delete(key)
		}
		_ = d.db.Write(batch, nil)
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
			d.applyPutOrTouch(op.putKey, op.putEnt, op.accessUnix)
		}
	}
}

func (d *Disk) applyPutOrTouch(key string, ent *Entry, accessUnix int64) {
	now := time.Now().Unix()
	if accessUnix > 0 {
		now = accessUnix
	}

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
		bodyLen := int64(len(ent.Body))
		shape := entryShapeHash(*ent)

		d.mu.Lock()
		old := d.index[key]
		// A store that carries the response already on disk refreshes only the
		// metadata record, so the blob and therefore the accounted size stay put.
		reuseBlob := blobUpToDate(old, ent.Hash32, bodyLen, shape)
		if reuseBlob {
			size = old.Size
		}
		if old.Size > 0 {
			d.totalSize -= old.Size
		}
		meta.Size = size
		meta.LastAccess = now
		meta.StatsSize = statsSize
		meta.Inactive = ent.Inactive
		meta.DiscoveredBy = ent.DiscoveredBy
		meta.LastRefresh = lastRefresh
		meta.StoredAt = ent.StoredAt
		meta.RevalidatedBy = ent.RevalidatedBy
		meta.BodyHash = ent.Hash32
		meta.BodyLen = bodyLen
		meta.ShapeHash = shape
		meta.VariantKind = ""
		meta.VariantBaseKey = ""
		meta.VariantValues = nil
		if ent.Variant != nil {
			meta.VariantKind = ent.Variant.Kind
			meta.VariantBaseKey = ent.Variant.BaseKey
			meta.VariantValues = append([]string(nil), ent.Variant.Values...)
		}
		d.index[key] = meta
		d.totalSize += size
		total := d.totalSize
		max := d.maxBytes
		d.mu.Unlock()

		if reuseBlob {
			d.skippedBodyWrites.Add(1)
		} else {
			batch.Put([]byte("e:"+key), b)
		}
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
