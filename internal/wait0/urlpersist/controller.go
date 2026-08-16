package urlpersist

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// restoreChunk bounds how many seeds are written before the restore goroutine
// re-checks stopCh. Seeding enqueues onto the disk writer's channel, which the
// request path also uses, so restore stays chunked rather than blasting the
// whole file through in one go.
const restoreChunk = 256

type Controller struct {
	cfg    Config
	rt     Runtime
	stopCh <-chan struct{}
	wg     *sync.WaitGroup
	logger Logger

	mu    sync.Mutex
	byKey map[string]*Record
	// byBase indexes records by their variant-free key so that deleting a
	// manifest, which tears down a whole variant family, can be accounted for
	// without scanning every record.
	byBase    map[string]map[string]struct{}
	dirty     bool
	restored  int
	lastFlush int64

	// restoring is set from Start until the file has been read into the
	// registry, and loaded closes at the same moment. Flushing before then
	// would write a half-loaded registry back over the file, destroying the
	// records not yet read — and because the write also rotates the intact
	// previous generation into .bak, the loss compounds on the next restart
	// instead of healing.
	restoring  atomic.Bool
	loaded     chan struct{}
	loadedOnce sync.Once
}

func (c *Controller) markLoaded() {
	c.loadedOnce.Do(func() {
		c.restoring.Store(false)
		close(c.loaded)
	})
}

func NewController(cfg Config, rt Runtime, stopCh <-chan struct{}, wg *sync.WaitGroup, logger Logger) *Controller {
	return &Controller{
		cfg:    cfg,
		rt:     rt,
		stopCh: stopCh,
		wg:     wg,
		logger: logger,
		byKey:  make(map[string]*Record),
		byBase: make(map[string]map[string]struct{}),
		loaded: make(chan struct{}),
	}
}

// Start launches the restore and flush goroutines. Both register on the
// service WaitGroup so the final flush completes before the cache is closed.
func (c *Controller) Start() {
	if c == nil || !c.cfg.Enabled {
		return
	}

	c.restoring.Store(true)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer c.markLoaded()
		c.restore()
	}()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.flushLoop()
	}()
}

// NoteStored records a URL that was just written to the cache. It runs on the
// request path, so it does nothing but take one lock and touch one map.
func (c *Controller) NoteStored(key string, rec Record) {
	if c == nil || !c.cfg.Enabled || key == "" || rec.Path == "" {
		return
	}
	now := time.Now().Unix()

	c.mu.Lock()
	defer c.mu.Unlock()

	if cur, ok := c.byKey[key]; ok {
		cur.LastSeen = now
		cur.Failures = 0
		if rec.DiscoveredBy != "" {
			cur.DiscoveredBy = rec.DiscoveredBy
		}
		if rec.Variant != nil {
			cur.Variant = rec.Variant
		}
		c.dirty = true
		c.pruneSupersededLocked(cur.BaseCacheKey(), cur.Variant, key)
		return
	}

	stored := rec.clone()
	stored.LastSeen = now
	stored.Seen = 1
	stored.Failures = 0
	c.insertLocked(key, &stored)
	c.dirty = true
	c.pruneSupersededLocked(stored.BaseCacheKey(), stored.Variant, key)
}

// NoteDeleted records that a URL stopped being cacheable. The key may be a
// variant child or a base key whose manifest deletion removes the whole family.
func (c *Controller) NoteDeleted(key string) {
	if c == nil || !c.cfg.Enabled || key == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// The key is a variant child, or a base key whose manifest deletion takes
	// the whole family with it. Both shapes can be live at once while a family
	// is collapsing, so account for both rather than stopping at the first.
	// Collect before failing: failLocked removes records, which mutates byBase.
	doomed := make([]string, 0, len(c.byBase[key])+1)
	if _, ok := c.byKey[key]; ok {
		doomed = append(doomed, key)
	}
	for child := range c.byBase[key] {
		if child == key {
			continue
		}
		doomed = append(doomed, child)
	}
	for _, child := range doomed {
		c.failLocked(child)
	}
}

func (c *Controller) Stats() Stats {
	if c == nil || !c.cfg.Enabled {
		return Stats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Records:       len(c.byKey),
		Restored:      c.restored,
		LastFlushUnix: c.lastFlush,
	}
}

func (c *Controller) logf(format string, v ...any) {
	if c.logger == nil {
		return
	}
	c.logger.Printf(format, v...)
}

func (c *Controller) insertLocked(key string, rec *Record) {
	c.byKey[key] = rec
	base := rec.BaseCacheKey()
	children := c.byBase[base]
	if children == nil {
		children = make(map[string]struct{})
		c.byBase[base] = children
	}
	children[key] = struct{}{}
}

func (c *Controller) removeLocked(key string) {
	rec, ok := c.byKey[key]
	if !ok {
		return
	}
	delete(c.byKey, key)
	base := rec.BaseCacheKey()
	children := c.byBase[base]
	delete(children, key)
	if len(children) == 0 {
		delete(c.byBase, base)
	}
}

func (c *Controller) failLocked(key string) {
	rec, ok := c.byKey[key]
	if !ok {
		return
	}
	rec.Failures++
	c.dirty = true
	if c.cfg.ForgetAfterFailures <= 0 || rec.Failures >= c.cfg.ForgetAfterFailures {
		c.removeLocked(key)
	}
}

// pruneSupersededLocked drops the sibling records a store just invalidated,
// mirroring what replaceVariantChildren does to the cache. Two cases: the
// origin changed its Cache-Variant declarations, so every key built from the
// old fingerprint is dead; or it stopped declaring them entirely, so the whole
// variant family collapses into one plain record. Without this the file
// accumulates a dead family per origin deploy and keeps re-seeding child keys
// no manifest points at.
func (c *Controller) pruneSupersededLocked(base string, variant *Variant, keep string) {
	stale := make([]string, 0)
	for child := range c.byBase[base] {
		if child == keep {
			continue
		}
		rec, ok := c.byKey[child]
		if !ok {
			continue
		}
		switch {
		case rec.Variant == nil:
			// A plain record for a URL that now varies: the base key holds a
			// manifest in the cache, so the plain record can never be seeded back.
			if variant != nil {
				stale = append(stale, child)
			}
		case variant == nil || rec.Variant.Fingerprint != variant.Fingerprint:
			stale = append(stale, child)
		}
	}
	for _, child := range stale {
		c.removeLocked(child)
	}
}

// restore reads the file into the registry and then seeds the cache so warmup
// can refill it. The two steps are deliberately separate: the whole list is
// published first, in one pass, so that a flush firing mid-restore — or a
// shutdown that cuts seeding short — still writes back everything that was
// read. Seeding is the slow part and is the only part that gives up on stop.
//
// It runs even when RestoreOnStart is false. Loading is what stops the first
// flush from overwriting the file with an empty registry; only the seeding is
// conditional.
func (c *Controller) restore() {
	start := time.Now()
	records := readFile(c.cfg.File, c.cfg.Origin, c.logger)

	// pending holds independent copies, so seeding never reads a record the
	// registry has since handed to NoteStored or flush.
	pending := make([]Record, 0, len(records))
	dropped := 0

	c.mu.Lock()
	for i := range records {
		rec := records[i].clone()
		if c.cfg.ForgetAfterFailures > 0 && rec.Failures >= c.cfg.ForgetAfterFailures {
			dropped++
			continue
		}
		key := rec.CacheKey()
		if _, known := c.byKey[key]; known {
			// A live request stored this URL first; the fresh record wins.
			continue
		}
		c.insertLocked(key, &rec)
		pending = append(pending, rec)
	}
	c.mu.Unlock()
	// The registry now holds the whole file, so flushing is safe again even
	// though seeding has not started.
	c.markLoaded()

	if !c.cfg.RestoreOnStart {
		if len(records) > 0 {
			c.logf("urlPersister loaded: records=%d (restoreOnStart is false, cache not seeded)", len(pending))
		}
		return
	}

	seeded, skipped := 0, 0
	for i := range pending {
		if i%restoreChunk == 0 {
			select {
			case <-c.stopCh:
				c.logf("urlPersister restore interrupted: seeded=%d of %d", seeded, len(pending))
				return
			default:
			}
		}

		if c.rt.Seed(pending[i]) {
			seeded++
			continue
		}
		// The rules no longer cover this URL, so drop it rather than carrying a
		// permanently unseedable record forward in every future file.
		skipped++
		c.mu.Lock()
		c.removeLocked(pending[i].CacheKey())
		c.mu.Unlock()
	}

	if len(records) == 0 {
		return
	}

	c.mu.Lock()
	c.restored = seeded
	c.dirty = true
	c.mu.Unlock()

	c.logf(
		"urlPersister restored: records=%d seeded=%d skipped=%d dropped=%d took=%s",
		len(records), seeded, skipped, dropped, time.Since(start).Truncate(time.Millisecond),
	)
}

func (c *Controller) flushLoop() {
	every := c.cfg.FlushEvery
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			// Loading is a bounded local read, so waiting for it here is safe and
			// keeps the final flush from truncating the file.
			<-c.loaded
			c.flush()
			return
		case <-t.C:
			c.flush()
		}
	}
}

func (c *Controller) flush() {
	// Never write a registry that has not finished loading: the write would
	// replace the file with a prefix of itself and rotate the intact copy into
	// .bak, where the next write destroys it too.
	if c.restoring.Load() {
		return
	}

	access := c.rt.SnapshotAccessTimes()

	c.mu.Lock()
	for key, ts := range access {
		rec, ok := c.byKey[key]
		if !ok || ts <= rec.LastSeen {
			continue
		}
		rec.LastSeen = ts
		rec.Seen++
		c.dirty = true
	}
	if !c.dirty {
		c.mu.Unlock()
		return
	}
	snapshot := c.snapshotLocked()
	c.dirty = false
	c.mu.Unlock()

	now := time.Now()
	if err := writeFile(c.cfg.File, c.cfg.Origin, now, snapshot); err != nil {
		c.logf("urlPersister: writing %q failed: %v", c.cfg.File, err)
		c.mu.Lock()
		c.dirty = true
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	c.lastFlush = now.Unix()
	c.mu.Unlock()
}

// snapshotLocked orders the registry most-recently-used first and enforces
// maxUrls, evicting the least recently used. This is the same policy the disk
// cache uses, so the list and the tier it feeds stay in step. Ordering by hit
// count instead would permanently starve new URLs once the file filled up.
func (c *Controller) snapshotLocked() []Record {
	keys := make([]string, 0, len(c.byKey))
	for key := range c.byKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := c.byKey[keys[i]], c.byKey[keys[j]]
		if a.LastSeen != b.LastSeen {
			return a.LastSeen > b.LastSeen
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return keys[i] < keys[j]
	})

	if c.cfg.MaxURLs > 0 && len(keys) > c.cfg.MaxURLs {
		for _, key := range keys[c.cfg.MaxURLs:] {
			c.removeLocked(key)
		}
		keys = keys[:c.cfg.MaxURLs]
	}

	out := make([]Record, 0, len(keys))
	for _, key := range keys {
		out = append(out, c.byKey[key].clone())
	}
	return out
}
