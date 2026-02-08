package wait0

import (
	"bytes"
	"context"
	"encoding/gob"
	"hash/crc32"
	"io"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type Service struct {
	cfg Config

	httpClient *http.Client

	ram  *ramCache
	disk *diskCache

	bgSem chan struct{}

	stopCh chan struct{}
	wg     sync.WaitGroup

	overflowLog *rateLimitedLogger

	stats *statsCollector
}

func NewService(cfg Config) (*Service, error) {
	ramMax, err := parseBytes(cfg.Storage.RAM.Max)
	if err != nil {
		return nil, err
	}
	diskMax, err := parseBytes(cfg.Storage.Disk.Max)
	if err != nil {
		return nil, err
	}
	disk, err := newDiskCache("./data/leveldb", diskMax)
	if err != nil {
		return nil, err
	}

	s := &Service{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		ram:         newRAMCache(ramMax),
		disk:        disk,
		bgSem:       make(chan struct{}, 32),
		stopCh:      make(chan struct{}),
		overflowLog: newRateLimitedLogger(1 * time.Minute),
	}

	if cfg.Logging.logStatsEveryDur > 0 {
		s.stats = newStatsCollector()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.statsLoop(cfg.Logging.logStatsEveryDur)
		}()
	}

	warmEvery := minWarmInterval(cfg.Rules)
	if warmEvery > 0 {
		log.Printf("warmup tick interval: %s (min warmUp among rules)", warmEvery)

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.warmupLoop(warmEvery)
		}()
	}

	return s, nil
}

func (s *Service) Close() {
	close(s.stopCh)
	s.wg.Wait()
	s.disk.close()
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

func minWarmInterval(rules []Rule) time.Duration {
	var out time.Duration
	for _, r := range rules {
		if r.warmDur <= 0 {
			continue
		}
		if out == 0 || r.warmDur < out {
			out = r.warmDur
		}
	}
	return out
}

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	key := path

	rule := s.pickRule(path)
	if rule != nil {
		if rule.Bypass {
			s.proxyPass(w, r, "bypass")
			return
		}
		if hasAnyCookie(r, rule.BypassWhenCookies) {
			s.proxyPass(w, r, "ignore-by-cookie")
			return
		}
	}

	if r.Method != http.MethodGet {
		s.proxyPass(w, r, "bypass")
		return
	}

	now := time.Now().Unix()
	if ent, ok := s.ram.Get(key, now); ok {
		s.writeEntryWithStats(w, ent, "hit")
		if rule != nil && rule.expDur > 0 && isStale(ent, rule.expDur) {
			s.revalidateAsync(key, r, rule)
		}
		return
	}

	if ent, ok := s.disk.Get(key); ok {
		s.ram.Put(key, ent, s.disk, s.overflowLog)
		s.writeEntryWithStats(w, ent, "hit")
		if rule != nil && rule.expDur > 0 && isStale(ent, rule.expDur) {
			s.revalidateAsync(key, r, rule)
		}
		return
	}

	// miss
	respEnt, cacheable, statusKind, err := s.fetchFromOrigin(r)
	if err != nil {
		setWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if statusKind == "ignore-by-status" {
		s.ram.Delete(key)
		s.disk.Delete(key)
		s.writeEntryWithStats(w, respEnt, "ignore-by-status")
		return
	}
	if !cacheable {
		s.writeEntryWithStats(w, respEnt, "bypass")
		return
	}

	s.store(key, respEnt)
	s.writeEntryWithStats(w, respEnt, "miss")
}

func (s *Service) pickRule(path string) *Rule {
	for i := range s.cfg.Rules {
		r := &s.cfg.Rules[i]
		if r.Matches(path) {
			return r
		}
	}
	return nil
}

func isStale(ent CacheEntry, exp time.Duration) bool {
	stored := time.Unix(ent.StoredAt, 0)
	return time.Since(stored) > exp
}

func hasAnyCookie(r *http.Request, names []string) bool {
	if len(names) == 0 {
		return false
	}
	need := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			need[n] = struct{}{}
		}
	}
	for _, c := range r.Cookies() {
		if _, ok := need[c.Name]; ok {
			return true
		}
	}
	return false
}

func writeEntry(w http.ResponseWriter, ent CacheEntry, wait0 string) {
	// copy headers
	for k, vs := range ent.Header {
		if strings.EqualFold(k, "x-wait0") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	setWait0Headers(w.Header(), wait0)
	w.WriteHeader(ent.Status)
	_, _ = w.Write(ent.Body)
}

func setWait0Headers(h http.Header, wait0 string) {
	if wait0 != "" {
		h.Set("X-Wait0", wait0)
	}
	// If this is used from a browser in a CORS context, custom headers are not
	// readable by JS unless explicitly exposed.
	ensureExposedHeader(h, "X-Wait0")
}

func ensureExposedHeader(h http.Header, name string) {
	if name == "" {
		return
	}

	const expose = "Access-Control-Expose-Headers"
	cur := h.Values(expose)
	if len(cur) == 0 {
		h.Set(expose, name)
		return
	}

	// Merge into a single comma-separated value.
	merged := strings.Join(cur, ",")
	for _, part := range strings.Split(merged, ",") {
		if strings.EqualFold(strings.TrimSpace(part), name) {
			return
		}
	}

	h.Set(expose, strings.TrimSpace(merged)+", "+name)
}

func (s *Service) proxyPass(w http.ResponseWriter, r *http.Request, wait0 string) {
	ent, _, _, err := s.fetchFromOrigin(r)
	if err != nil {
		setWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	s.writeEntryWithStats(w, ent, wait0)
}

func (s *Service) writeEntryWithStats(w http.ResponseWriter, ent CacheEntry, wait0 string) {
	writeEntry(w, ent, wait0)
	if s.stats != nil {
		switch wait0 {
		case "hit", "miss":
			s.stats.Observe(len(ent.Body))
		}
	}
}

func (s *Service) statsLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			ss := s.stats.Snapshot()
			cachedPaths := s.cachedPathsCount()
			ramTotal := uint64(s.ram.TotalSize())
			diskTotal := uint64(s.disk.TotalSize())
			log.Printf(
				"Cached: Paths: %d, RAM usage: %s, Disk usage: %s, Resp Min/avg/max %s/%s/%s",
				cachedPaths,
				formatBytes(ramTotal),
				formatBytes(diskTotal),
				formatBytes(ss.MinRespBytes),
				formatBytes(ss.AvgRespBytes),
				formatBytes(ss.MaxRespBytes),
			)
		}
	}
}

func (s *Service) cachedPathsCount() int {
	// Union count without building a combined map.
	ramKeys := s.ram.Keys()
	diskCount := s.disk.KeyCount()
	intersect := 0
	for _, k := range ramKeys {
		if s.disk.HasKey(k) {
			intersect++
		}
	}
	return len(ramKeys) + diskCount - intersect
}

func (s *Service) fetchFromOrigin(r *http.Request) (CacheEntry, bool, string, error) {
	originURL := s.cfg.Server.Origin + r.URL.RequestURI()
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return CacheEntry{}, false, "", err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return CacheEntry{}, false, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CacheEntry{}, false, "", err
	}

	ent := CacheEntry{
		Status:   resp.StatusCode,
		Header:   cloneHeader(resp.Header),
		Body:     body,
		StoredAt: time.Now().Unix(),
	}
	ent.Header.Del("Content-Length")
	ent.Hash32 = crc32.ChecksumIEEE(body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ent, false, "ignore-by-status", nil
	}

	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	cacheable := true
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		cacheable = false
	}
	return ent, cacheable, "ok", nil
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		vv := make([]string, len(vs))
		copy(vv, vs)
		out[k] = vv
	}
	return out
}

func (s *Service) store(key string, ent CacheEntry) {
	s.ram.Put(key, ent, s.disk, s.overflowLog)
	// also persist to disk for durability (async)
	s.disk.PutAsync(key, ent)
}

func (s *Service) revalidateAsync(key string, r *http.Request, rule *Rule) {
	select {
	case s.bgSem <- struct{}{}:
		// ok
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	path := r.URL.Path
	query := r.URL.RawQuery

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.bgSem }()
		defer cancel()

		s.revalidateOnce(ctx, key, path, query)
	}()
}

func (s *Service) revalidateOnce(ctx context.Context, key, path, query string) {
	uri := path
	if query != "" {
		uri = uri + "?" + query
	}
	originURL := s.cfg.Server.Origin + uri

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Dbg-Revalidate-At", time.Now().UTC().Format(time.RFC3339Nano))
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.ram.Delete(key)
		s.disk.Delete(key)
		return
	}
	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	cacheable := true
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		cacheable = false
	}
	if !cacheable {
		s.ram.Delete(key)
		s.disk.Delete(key)
		return
	}

	newEnt := CacheEntry{
		Status:   resp.StatusCode,
		Header:   cloneHeader(resp.Header),
		Body:     body,
		StoredAt: time.Now().Unix(),
		Hash32:   crc32.ChecksumIEEE(body),
	}
	newEnt.Header.Del("Content-Length")

	cur, ok := s.ram.Peek(key)
	if !ok {
		cur, ok = s.disk.Peek(key)
	}
	if ok && cur.Hash32 == newEnt.Hash32 {
		return
	}

	s.ram.Put(key, newEnt, s.disk, s.overflowLog)
	s.disk.PutAsync(key, newEnt)
}

func (s *Service) warmupLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			keys := s.allKeysSnapshot()
			for _, key := range keys {
				select {
				case <-s.stopCh:
					return
				default:
				}
				s.warmKey(key)
			}
		}
	}
}

func (s *Service) warmKey(key string) {
	select {
	case s.bgSem <- struct{}{}:
	default:
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.bgSem }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.revalidateOnce(ctx, key, key, "")
	}()
}

func (s *Service) allKeysSnapshot() []string {
	m := map[string]struct{}{}
	for _, k := range s.ram.Keys() {
		m[k] = struct{}{}
	}
	for _, k := range s.disk.Keys() {
		m[k] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- disk cache ----

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

func newDiskCache(path string, maxBytes int64) (*diskCache, error) {
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

	n := len(items) / 10
	if n < 1 {
		n = 1
	}

	for i := 0; i < n && i < len(items); i++ {
		d.applyDelete(items[i].key)
	}
}

// ---- ram cache ----

type ramItem struct {
	key  string
	ent  CacheEntry
	size int64
	prev *ramItem
	next *ramItem
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

	if it, ok := c.items[key]; ok {
		c.total -= it.size
		it.ent = ent
		it.size = sz
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

	it := &ramItem{key: key, ent: ent, size: sz}
	c.items[key] = it
	c.addToFront(it)
	c.total += sz
}

func (c *ramCache) evictToDiskLocked(disk *diskCache) {
	// move 10% least-recently-used
	count := len(c.items)
	if count == 0 {
		return
	}
	n := count / 10
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
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

// ---- encoding ----

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGob(b []byte, v any) error {
	dec := gob.NewDecoder(bytes.NewReader(b))
	return dec.Decode(v)
}

func init() {
	// Ensure http.Header is registered for gob.
	gob.Register(http.Header{})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
