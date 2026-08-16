package urlpersist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"wait0/internal/wait0/proxy"
)

// fakeRuntime implements Runtime. Every field is guarded because the flush and
// restore goroutines touch it concurrently under `go test -race`.
type fakeRuntime struct {
	mu sync.Mutex

	seeded []Record
	reject map[string]bool
	access map[string]int64
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		reject: map[string]bool{},
		access: map[string]int64{},
	}
}

func (f *fakeRuntime) Seed(rec Record) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeded = append(f.seeded, rec.clone())
	return !f.reject[rec.Path]
}

func (f *fakeRuntime) SnapshotAccessTimes() map[string]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int64, len(f.access))
	for k, v := range f.access {
		out[k] = v
	}
	return out
}

func (f *fakeRuntime) rejectPath(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reject[path] = true
}

func (f *fakeRuntime) setAccess(key string, ts int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access[key] = ts
}

func (f *fakeRuntime) seededPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.seeded))
	for _, rec := range f.seeded {
		out = append(out, rec.Path)
	}
	return out
}

func (f *fakeRuntime) seededRecords() []Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Record(nil), f.seeded...)
}

func (f *fakeRuntime) seededPath(path string) bool {
	for _, got := range f.seededPaths() {
		if got == path {
			return true
		}
	}
	return false
}

type ctrlLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *ctrlLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (l *ctrlLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *ctrlLogger) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *ctrlLogger) contains(sub string) bool {
	for _, line := range l.all() {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

type harness struct {
	c      *Controller
	rt     *fakeRuntime
	log    *ctrlLogger
	wg     *sync.WaitGroup
	stopCh chan struct{}
	file   string

	stopOnce sync.Once
}

// newHarness builds a Controller with a usable default config. mutate may
// adjust the config before the controller is constructed.
func newHarness(t *testing.T, mutate func(cfg *Config)) *harness {
	t.Helper()

	h := &harness{
		rt:     newFakeRuntime(),
		log:    &ctrlLogger{},
		wg:     &sync.WaitGroup{},
		stopCh: make(chan struct{}),
		file:   filepath.Join(t.TempDir(), "state", "urls.yaml"),
	}

	cfg := Config{
		Enabled:             true,
		File:                h.file,
		Origin:              "http://origin.local",
		FlushEvery:          time.Hour,
		RestoreOnStart:      true,
		MaxURLs:             0,
		ForgetAfterFailures: 3,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h.file = cfg.File
	h.c = NewController(cfg, h.rt, h.stopCh, h.wg, h.log)

	t.Cleanup(func() {
		h.stop()
		h.waitWG(t)
	})
	return h
}

func (h *harness) stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
}

func (h *harness) waitWG(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitgroup did not drain")
	}
}

// seed installs a record with an exact LastSeen/Seen without marking the
// registry dirty, so flush()'s dirty bookkeeping stays observable.
func (h *harness) seed(rec Record, lastSeen, seen int64) string {
	stored := rec.clone()
	stored.LastSeen = lastSeen
	stored.Seen = seen
	key := stored.CacheKey()

	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	h.c.insertLocked(key, &stored)
	return key
}

func (h *harness) record(t *testing.T, key string) Record {
	t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	rec, ok := h.c.byKey[key]
	if !ok {
		t.Fatalf("record %q is not in the registry; keys=%v", key, h.keysLocked())
	}
	return rec.clone()
}

func (h *harness) has(key string) bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	_, ok := h.c.byKey[key]
	return ok
}

func (h *harness) keys() []string {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.keysLocked()
}

func (h *harness) keysLocked() []string {
	out := make([]string, 0, len(h.c.byKey))
	for key := range h.c.byKey {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (h *harness) baseChildren(base string) []string {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	out := make([]string, 0, len(h.c.byBase[base]))
	for key := range h.c.byBase[base] {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (h *harness) dirty() bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.c.dirty
}

// persisted reads the file back through the package's own reader so the test
// asserts on records rather than on YAML text.
func (h *harness) persisted(t *testing.T) []Record {
	t.Helper()
	if _, err := os.Stat(h.file); err != nil {
		t.Fatalf("stat %q: %v", h.file, err)
	}
	return readFile(h.file, "", nil)
}

func variantRecord(path, query, fingerprint string, values []string, headers map[string][]string) Record {
	return Record{
		Path:  path,
		Query: query,
		Variant: &Variant{
			Fingerprint: fingerprint,
			Values:      values,
			Headers:     headers,
		},
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestController_NilAndDisabledAreNoOps(t *testing.T) {
	t.Run("nil controller", func(t *testing.T) {
		var c *Controller
		c.Start()
		c.NoteStored("/a", Record{Path: "/a"})
		c.NoteDeleted("/a")
		if got := c.Stats(); got != (Stats{}) {
			t.Fatalf("Stats() on nil controller = %+v, want zero", got)
		}
	})

	t.Run("disabled controller", func(t *testing.T) {
		// FlushEvery is deliberately zero: a disabled Start must not reach
		// time.NewTicker, which would panic.
		h := newHarness(t, func(cfg *Config) {
			cfg.Enabled = false
			cfg.FlushEvery = 0
			cfg.RestoreOnStart = true
		})

		h.c.Start()
		h.waitWG(t)

		h.c.NoteStored("/a", Record{Path: "/a"})
		h.c.NoteDeleted("/a")

		if got := h.c.Stats(); got != (Stats{}) {
			t.Fatalf("Stats() on disabled controller = %+v, want zero", got)
		}
		if keys := h.keys(); len(keys) != 0 {
			t.Fatalf("disabled controller recorded %v", keys)
		}
		if _, err := os.Stat(h.file); !os.IsNotExist(err) {
			t.Fatalf("disabled controller touched %q (err=%v)", h.file, err)
		}
		if len(h.rt.seededPaths()) != 0 {
			t.Fatalf("disabled controller seeded %v", h.rt.seededPaths())
		}
	})

	t.Run("nil logger", func(t *testing.T) {
		// Logging is optional: a controller built without a logger must still
		// restore and flush instead of panicking on the summary line.
		rt := newFakeRuntime()
		file := filepath.Join(t.TempDir(), "urls.yaml")
		if err := writeFile(file, "http://origin.local", time.Now(), []Record{{Path: "/a", LastSeen: 5, Seen: 1}}); err != nil {
			t.Fatalf("writeFile: %v", err)
		}
		c := NewController(Config{Enabled: true, File: file, FlushEvery: time.Hour, RestoreOnStart: true}, rt, make(chan struct{}), &sync.WaitGroup{}, nil)

		c.restore()
		c.flush()

		if !rt.seededPath("/a") {
			t.Fatalf("seeded = %v, want /a", rt.seededPaths())
		}
		if got := c.Stats().Restored; got != 1 {
			t.Fatalf("Restored = %d, want 1", got)
		}
	})

	t.Run("empty key or path", func(t *testing.T) {
		h := newHarness(t, nil)
		h.c.NoteStored("", Record{Path: "/a"})
		h.c.NoteStored("/a", Record{Path: ""})
		h.c.NoteDeleted("")
		if keys := h.keys(); len(keys) != 0 {
			t.Fatalf("registry = %v, want empty", keys)
		}
		if h.dirty() {
			t.Fatal("ignored calls marked the registry dirty")
		}
	})
}

func TestController_NoteStored_InsertsThenUpdates(t *testing.T) {
	h := newHarness(t, nil)

	before := time.Now().Unix()
	h.c.NoteStored("/a", Record{Path: "/a", DiscoveredBy: "user"})
	after := time.Now().Unix()

	rec := h.record(t, "/a")
	if rec.Seen != 1 {
		t.Fatalf("Seen = %d, want 1", rec.Seen)
	}
	if rec.Failures != 0 {
		t.Fatalf("Failures = %d, want 0", rec.Failures)
	}
	if rec.DiscoveredBy != "user" {
		t.Fatalf("DiscoveredBy = %q, want user", rec.DiscoveredBy)
	}
	if rec.LastSeen < before || rec.LastSeen > after {
		t.Fatalf("LastSeen = %d, want within [%d,%d]", rec.LastSeen, before, after)
	}
	if !h.dirty() {
		t.Fatal("insert did not mark the registry dirty")
	}

	// A second store for the same key updates in place.
	h.c.NoteStored("/a", Record{Path: "/a", DiscoveredBy: "sitemap"})
	if got := h.c.Stats().Records; got != 1 {
		t.Fatalf("Records = %d, want 1 (no duplicate)", got)
	}
	rec = h.record(t, "/a")
	if rec.Seen != 1 {
		t.Fatalf("Seen after update = %d, want 1 (only flush counts hits)", rec.Seen)
	}
	if rec.DiscoveredBy != "sitemap" {
		t.Fatalf("DiscoveredBy after update = %q, want sitemap", rec.DiscoveredBy)
	}

	// An empty DiscoveredBy must not erase the known origin of the URL.
	h.c.NoteStored("/a", Record{Path: "/a"})
	if got := h.record(t, "/a").DiscoveredBy; got != "sitemap" {
		t.Fatalf("DiscoveredBy after blank update = %q, want sitemap", got)
	}

	// A query string is part of the identity, so it is a different record.
	h.c.NoteStored("/a?page=2", Record{Path: "/a", Query: "page=2"})
	if got := h.c.Stats().Records; got != 2 {
		t.Fatalf("Records = %d, want 2", got)
	}
}

func TestController_NoteStored_DedupesByCacheKey(t *testing.T) {
	h := newHarness(t, nil)

	// The projection is what is persisted, but identity is still the child key,
	// so a thousand User-Agent strings that evaluate to "mobile" are one record.
	base := "/a"
	key := proxy.JoinVariantCacheKey(base, "gen1", []string{"mobile"})
	for i := 0; i < 50; i++ {
		h.c.NoteStored(key, variantRecord("/a", "", "gen1", []string{"mobile"}, map[string][]string{
			"User-Agent": {fmt.Sprintf("Mozilla/5.0 device-%d", i)},
		}))
	}

	if got := h.c.Stats().Records; got != 1 {
		t.Fatalf("Records = %d, want 1", got)
	}
	rec := h.record(t, key)
	if rec.Seen != 1 {
		t.Fatalf("Seen = %d, want 1", rec.Seen)
	}
	if rec.Variant == nil {
		t.Fatal("variant projection was dropped")
	}
	// The newest projection wins so the replayed request stays representative.
	if got := rec.Variant.Headers["User-Agent"]; len(got) != 1 || got[0] != "Mozilla/5.0 device-49" {
		t.Fatalf("User-Agent projection = %v, want the latest one", got)
	}
	if rec.CacheKey() != key {
		t.Fatalf("CacheKey() = %q, want %q", rec.CacheKey(), key)
	}
	if children := h.baseChildren(base); len(children) != 1 || children[0] != key {
		t.Fatalf("byBase[%q] = %v", base, children)
	}
}

func TestController_NoteStored_PrunesSupersededGeneration(t *testing.T) {
	h := newHarness(t, nil)

	oldMobile := proxy.JoinVariantCacheKey("/a", "gen1", []string{"mobile"})
	oldDesktop := proxy.JoinVariantCacheKey("/a", "gen1", []string{"desktop"})
	otherBase := proxy.JoinVariantCacheKey("/b", "gen1", []string{"mobile"})

	h.c.NoteStored(oldMobile, variantRecord("/a", "", "gen1", []string{"mobile"}, nil))
	h.c.NoteStored(oldDesktop, variantRecord("/a", "", "gen1", []string{"desktop"}, nil))
	h.c.NoteStored(otherBase, variantRecord("/b", "", "gen1", []string{"mobile"}, nil))
	// A plain record on a base key that now varies is dead: the cache holds a
	// manifest there, so the record could never be seeded back.
	h.c.NoteStored("/a", Record{Path: "/a"})

	newMobile := proxy.JoinVariantCacheKey("/a", "gen2", []string{"mobile"})
	h.c.NoteStored(newMobile, variantRecord("/a", "", "gen2", []string{"mobile"}, nil))

	want := []string{newMobile, otherBase}
	sort.Strings(want)
	if got := h.keys(); !equalStrings(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if h.has(oldMobile) || h.has(oldDesktop) {
		t.Fatal("superseded generation was not pruned")
	}

	wantChildren := []string{newMobile}
	if got := h.baseChildren("/a"); !equalStrings(got, wantChildren) {
		t.Fatalf("byBase[/a] = %v, want %v", got, wantChildren)
	}
	if got := h.baseChildren("/b"); !equalStrings(got, []string{otherBase}) {
		t.Fatalf("byBase[/b] = %v, want %v", got, []string{otherBase})
	}
}

func TestController_NoteStored_ResetsFailures(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = 3 })

	h.c.NoteStored("/a", Record{Path: "/a"})
	h.c.NoteDeleted("/a")
	h.c.NoteDeleted("/a")
	if got := h.record(t, "/a").Failures; got != 2 {
		t.Fatalf("Failures = %d, want 2", got)
	}

	h.c.NoteStored("/a", Record{Path: "/a"})
	if got := h.record(t, "/a").Failures; got != 0 {
		t.Fatalf("Failures after NoteStored = %d, want 0", got)
	}

	// Proof the reset is real: two more failures still leave the record alive.
	h.c.NoteDeleted("/a")
	h.c.NoteDeleted("/a")
	if !h.has("/a") {
		t.Fatal("record was dropped even though NoteStored reset the failure count")
	}
}

func TestController_NoteDeleted_ChildBaseAndUnknown(t *testing.T) {
	mobile := proxy.JoinVariantCacheKey("/a", "gen1", []string{"mobile"})
	desktop := proxy.JoinVariantCacheKey("/a", "gen1", []string{"desktop"})
	other := proxy.JoinVariantCacheKey("/b", "gen1", []string{"mobile"})

	t.Run("child key fails only that record", func(t *testing.T) {
		h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = 3 })
		h.c.NoteStored(mobile, variantRecord("/a", "", "gen1", []string{"mobile"}, nil))
		h.c.NoteStored(desktop, variantRecord("/a", "", "gen1", []string{"desktop"}, nil))

		h.c.NoteDeleted(mobile)

		if got := h.record(t, mobile).Failures; got != 1 {
			t.Fatalf("mobile Failures = %d, want 1", got)
		}
		if got := h.record(t, desktop).Failures; got != 0 {
			t.Fatalf("desktop Failures = %d, want 0", got)
		}
	})

	t.Run("base key fails the whole family", func(t *testing.T) {
		h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = 2 })
		h.c.NoteStored(mobile, variantRecord("/a", "", "gen1", []string{"mobile"}, nil))
		h.c.NoteStored(desktop, variantRecord("/a", "", "gen1", []string{"desktop"}, nil))
		h.c.NoteStored(other, variantRecord("/b", "", "gen1", []string{"mobile"}, nil))

		// Deleting the manifest tears down every child at once.
		h.c.NoteDeleted("/a")

		if got := h.record(t, mobile).Failures; got != 1 {
			t.Fatalf("mobile Failures = %d, want 1", got)
		}
		if got := h.record(t, desktop).Failures; got != 1 {
			t.Fatalf("desktop Failures = %d, want 1", got)
		}
		if got := h.record(t, other).Failures; got != 0 {
			t.Fatalf("record under another base was failed: %d", got)
		}

		h.c.NoteDeleted("/a")
		if h.has(mobile) || h.has(desktop) {
			t.Fatalf("family survived the second teardown: %v", h.keys())
		}
		if !h.has(other) {
			t.Fatal("record under another base was dropped")
		}
		if children := h.baseChildren("/a"); len(children) != 0 {
			t.Fatalf("byBase[/a] = %v, want empty", children)
		}
	})

	t.Run("unknown key is a no-op", func(t *testing.T) {
		h := newHarness(t, nil)
		h.c.NoteStored("/a", Record{Path: "/a"})
		h.c.mu.Lock()
		h.c.dirty = false
		h.c.mu.Unlock()

		h.c.NoteDeleted("/nope")
		h.c.NoteDeleted(proxy.JoinVariantCacheKey("/nope", "gen1", []string{"x"}))

		if got := h.c.Stats().Records; got != 1 {
			t.Fatalf("Records = %d, want 1", got)
		}
		if h.dirty() {
			t.Fatal("deleting an unknown key marked the registry dirty")
		}
	})
}

func TestController_ForgetAfterFailures(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		deletes   int
		wantAlive bool
		wantFails int
	}{
		{name: "zero drops on first failure", threshold: 0, deletes: 1, wantAlive: false},
		{name: "negative drops on first failure", threshold: -1, deletes: 1, wantAlive: false},
		{name: "three survives first", threshold: 3, deletes: 1, wantAlive: true, wantFails: 1},
		{name: "three survives second", threshold: 3, deletes: 2, wantAlive: true, wantFails: 2},
		{name: "three drops on third", threshold: 3, deletes: 3, wantAlive: false},
		{name: "two survives one", threshold: 2, deletes: 1, wantAlive: true, wantFails: 1},
		{name: "two drops on second", threshold: 2, deletes: 2, wantAlive: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = tc.threshold })
			h.c.NoteStored("/a", Record{Path: "/a"})

			for i := 0; i < tc.deletes; i++ {
				h.c.NoteDeleted("/a")
			}

			if got := h.has("/a"); got != tc.wantAlive {
				t.Fatalf("record alive = %v, want %v", got, tc.wantAlive)
			}
			if !tc.wantAlive {
				if children := h.baseChildren("/a"); len(children) != 0 {
					t.Fatalf("byBase[/a] = %v, want empty after drop", children)
				}
				return
			}
			if got := h.record(t, "/a").Failures; got != tc.wantFails {
				t.Fatalf("Failures = %d, want %d", got, tc.wantFails)
			}
		})
	}
}

func TestController_Flush_MergesAccessTimes(t *testing.T) {
	tests := []struct {
		name         string
		access       int64
		wantLastSeen int64
		wantSeen     int64
		wantWritten  bool
	}{
		{name: "newer advances and counts", access: 150, wantLastSeen: 150, wantSeen: 6, wantWritten: true},
		{name: "equal changes nothing", access: 100, wantLastSeen: 100, wantSeen: 5},
		{name: "older changes nothing", access: 50, wantLastSeen: 100, wantSeen: 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			key := h.seed(Record{Path: "/a"}, 100, 5)
			h.rt.setAccess(key, tc.access)
			// An access time for a key nobody persisted must be ignored.
			h.rt.setAccess("/never-stored", 9999)

			h.c.flush()

			rec := h.record(t, key)
			if rec.LastSeen != tc.wantLastSeen {
				t.Fatalf("LastSeen = %d, want %d", rec.LastSeen, tc.wantLastSeen)
			}
			if rec.Seen != tc.wantSeen {
				t.Fatalf("Seen = %d, want %d", rec.Seen, tc.wantSeen)
			}
			if h.has("/never-stored") {
				t.Fatal("flush invented a record from an unknown access key")
			}

			_, err := os.Stat(h.file)
			if written := err == nil; written != tc.wantWritten {
				t.Fatalf("file written = %v, want %v (stat err=%v)", written, tc.wantWritten, err)
			}
		})
	}
}

func TestController_Flush_SkipsWriteWhenNotDirty(t *testing.T) {
	h := newHarness(t, nil)
	h.seed(Record{Path: "/a"}, 100, 1)

	// Nothing marked the registry dirty, so no file may appear at all.
	h.c.flush()
	if _, err := os.Stat(h.file); !os.IsNotExist(err) {
		t.Fatalf("clean flush wrote %q (err=%v)", h.file, err)
	}
	if got := h.c.Stats().LastFlushUnix; got != 0 {
		t.Fatalf("LastFlushUnix = %d, want 0", got)
	}

	// Now dirty it and flush for real.
	h.c.NoteStored("/b", Record{Path: "/b"})
	h.c.flush()

	info, err := os.Stat(h.file)
	if err != nil {
		t.Fatalf("dirty flush did not write the file: %v", err)
	}
	if got := len(h.persisted(t)); got != 2 {
		t.Fatalf("persisted records = %d, want 2", got)
	}
	if got := h.c.Stats().LastFlushUnix; got == 0 {
		t.Fatal("LastFlushUnix was not updated")
	}
	if h.dirty() {
		t.Fatal("dirty flag survived a successful flush")
	}

	// A second clean flush must not rewrite: writeFile rotates the previous
	// generation into .bak, so the absence of .bak proves nothing was written.
	h.c.flush()
	after, err := os.Stat(h.file)
	if err != nil {
		t.Fatalf("stat after clean flush: %v", err)
	}
	if !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("clean flush rewrote the file: %v -> %v", info.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(backupPath(h.file)); !os.IsNotExist(err) {
		t.Fatalf("clean flush rotated a backup (err=%v)", err)
	}
}

func TestController_Flush_KeepsDirtyWhenWriteFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	h := newHarness(t, func(cfg *Config) { cfg.File = filepath.Join(blocker, "urls.yaml") })
	h.c.NoteStored("/a", Record{Path: "/a"})

	h.c.flush()

	if !h.dirty() {
		t.Fatal("a failed write must leave the registry dirty so the next tick retries")
	}
	if got := h.c.Stats().LastFlushUnix; got != 0 {
		t.Fatalf("LastFlushUnix = %d, want 0 after a failed write", got)
	}
	if !h.log.contains("failed") {
		t.Fatalf("expected a write failure log, got %v", h.log.all())
	}
}

func TestController_Flush_EnforcesMaxURLsByLastSeen(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.MaxURLs = 2 })

	// A hot-but-stale record must lose to a fresh one: eviction is LRU on
	// LastSeen, never on the hit counter, or new URLs could never get in.
	stale := h.seed(Record{Path: "/stale"}, 100, 9999)
	middle := h.seed(Record{Path: "/middle"}, 200, 1)
	fresh := h.seed(Record{Path: "/fresh"}, 300, 1)
	h.c.mu.Lock()
	h.c.dirty = true
	h.c.mu.Unlock()

	h.c.flush()

	records := h.persisted(t)
	if len(records) != 2 {
		t.Fatalf("persisted %d records, want 2: %+v", len(records), records)
	}
	if records[0].Path != "/fresh" || records[1].Path != "/middle" {
		t.Fatalf("persisted order = %q,%q want /fresh,/middle", records[0].Path, records[1].Path)
	}
	if h.has(stale) {
		t.Fatal("the least recently seen record survived truncation")
	}
	if !h.has(middle) || !h.has(fresh) {
		t.Fatalf("truncation dropped a live record: %v", h.keys())
	}
	if got := h.c.Stats().Records; got != 2 {
		t.Fatalf("Records = %d, want 2", got)
	}
	if children := h.baseChildren("/stale"); len(children) != 0 {
		t.Fatalf("byBase[/stale] = %v, want empty", children)
	}
}

func TestController_Flush_SnapshotOrderedByLastSeenDesc(t *testing.T) {
	h := newHarness(t, nil)

	h.seed(Record{Path: "/z"}, 300, 1)
	h.seed(Record{Path: "/b"}, 200, 1)
	h.seed(Record{Path: "/a"}, 200, 1)
	first := h.seed(variantRecord("/c", "", "gen1", []string{"mobile"}, nil), 100, 1)
	second := h.seed(variantRecord("/c", "", "gen1", []string{"desktop"}, nil), 100, 1)
	h.c.mu.Lock()
	h.c.dirty = true
	h.c.mu.Unlock()

	h.c.flush()

	records := h.persisted(t)
	if len(records) != 5 {
		t.Fatalf("persisted %d records, want 5", len(records))
	}
	for i := 1; i < len(records); i++ {
		if records[i-1].LastSeen < records[i].LastSeen {
			t.Fatalf("records are not ordered by LastSeen desc: %d before %d", records[i-1].LastSeen, records[i].LastSeen)
		}
	}
	// Ties break on path, then on the cache key, so the file is reproducible.
	gotPaths := []string{records[0].Path, records[1].Path, records[2].Path}
	if !equalStrings(gotPaths, []string{"/z", "/a", "/b"}) {
		t.Fatalf("leading paths = %v, want [/z /a /b]", gotPaths)
	}
	tieKeys := []string{records[3].CacheKey(), records[4].CacheKey()}
	wantTie := []string{first, second}
	sort.Strings(wantTie)
	if !equalStrings(tieKeys, wantTie) {
		t.Fatalf("tie order = %v, want %v", tieKeys, wantTie)
	}
}

func TestController_Restore_SeedsSkipsAndLogs(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.RestoreOnStart = true })

	records := []Record{
		{Path: "/kept", LastSeen: 500, Seen: 7, DiscoveredBy: "sitemap"},
		{Path: "/rejected", LastSeen: 400, Seen: 3},
		variantRecord("/variant", "page=2", "gen1", []string{"mobile"}, map[string][]string{"User-Agent": {"iPhone"}}),
	}
	records[2].LastSeen = 300
	records[2].Seen = 1
	if err := writeFile(h.file, "http://origin.local", time.Now(), records); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	h.rt.rejectPath("/rejected")

	h.c.restore()

	seeded := h.rt.seededPaths()
	sort.Strings(seeded)
	if !equalStrings(seeded, []string{"/kept", "/rejected", "/variant"}) {
		t.Fatalf("seeded paths = %v", seeded)
	}
	if got := h.c.Stats().Restored; got != 2 {
		t.Fatalf("Restored = %d, want 2 (the rejected one does not count)", got)
	}
	if !h.log.contains("urlPersister restored: records=3 seeded=2 skipped=1") {
		t.Fatalf("missing restore summary, log = %v", h.log.all())
	}
	if !h.dirty() {
		t.Fatal("restore did not mark the registry dirty")
	}

	// The seeded record keeps the persisted identity, including the variant
	// projection warmup replays.
	var variantSeed Record
	for _, rec := range h.rt.seededRecords() {
		if rec.Path == "/variant" {
			variantSeed = rec
		}
	}
	if variantSeed.Variant == nil {
		t.Fatalf("variant projection was not passed to Seed: %+v", variantSeed)
	}
	if variantSeed.Query != "page=2" || variantSeed.LastSeen != 300 {
		t.Fatalf("seeded variant = %+v", variantSeed)
	}
	if got := variantSeed.Variant.Headers["User-Agent"]; len(got) != 1 || got[0] != "iPhone" {
		t.Fatalf("seeded projection headers = %v", got)
	}

	kept := h.record(t, "/kept")
	if kept.LastSeen != 500 || kept.Seen != 7 || kept.DiscoveredBy != "sitemap" {
		t.Fatalf("restored record = %+v, want the persisted values", kept)
	}
	if got := h.record(t, records[2].CacheKey()); got.Variant == nil {
		t.Fatal("restored variant record lost its projection")
	}
}

func TestController_Restore_SkipsRecordsAtFailureThreshold(t *testing.T) {
	t.Run("threshold reached", func(t *testing.T) {
		h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = 3 })
		if err := writeFile(h.file, "http://origin.local", time.Now(), []Record{
			{Path: "/burned", Failures: 3, LastSeen: 10},
			{Path: "/almost", Failures: 2, LastSeen: 11},
		}); err != nil {
			t.Fatalf("writeFile: %v", err)
		}

		h.c.restore()

		if h.rt.seededPath("/burned") {
			t.Fatalf("a record at the failure threshold was seeded: %v", h.rt.seededPaths())
		}
		if !h.rt.seededPath("/almost") {
			t.Fatalf("a record below the threshold was not seeded: %v", h.rt.seededPaths())
		}
		if h.has("/burned") {
			t.Fatal("a record at the failure threshold was kept in the registry")
		}
		// skipped counts records the rules rejected; dropped counts records the
		// failure threshold retired. They are separate outcomes.
		if !h.log.contains("records=2 seeded=1 skipped=0 dropped=1") {
			t.Fatalf("unexpected restore summary: %v", h.log.all())
		}
	})

	t.Run("threshold zero never skips on failures", func(t *testing.T) {
		// ForgetAfterFailures=0 forgets on the first failure at runtime, but
		// a file written before the setting changed must still be replayable.
		h := newHarness(t, func(cfg *Config) { cfg.ForgetAfterFailures = 0 })
		if err := writeFile(h.file, "http://origin.local", time.Now(), []Record{{Path: "/burned", Failures: 5}}); err != nil {
			t.Fatalf("writeFile: %v", err)
		}

		h.c.restore()

		if !h.rt.seededPath("/burned") {
			t.Fatalf("seeded = %v, want /burned", h.rt.seededPaths())
		}
	})
}

func TestController_Restore_DoesNotClobberConcurrentStore(t *testing.T) {
	h := newHarness(t, nil)
	if err := writeFile(h.file, "http://origin.local", time.Now(), []Record{
		{Path: "/live", LastSeen: 1000, Seen: 42, Failures: 1},
		{Path: "/cold", LastSeen: 900, Seen: 1},
	}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	// A live request stores the URL before restore reaches it.
	before := time.Now().Unix()
	h.c.NoteStored("/live", Record{Path: "/live", DiscoveredBy: "user"})

	h.c.restore()

	live := h.record(t, "/live")
	if live.Seen != 1 || live.Failures != 0 || live.LastSeen < before {
		t.Fatalf("restore clobbered the fresh record: %+v", live)
	}
	if live.DiscoveredBy != "user" {
		t.Fatalf("DiscoveredBy = %q, want user", live.DiscoveredBy)
	}
	if h.rt.seededPath("/live") {
		t.Fatalf("restore re-seeded an already cached URL: %v", h.rt.seededPaths())
	}
	if !h.rt.seededPath("/cold") {
		t.Fatalf("seeded = %v, want /cold", h.rt.seededPaths())
	}
	// The known record is neither seeded nor skipped in the summary.
	if !h.log.contains("records=2 seeded=1 skipped=0") {
		t.Fatalf("unexpected restore summary: %v", h.log.all())
	}
}

func TestController_Restore_MissingFileAndStopSignal(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		h := newHarness(t, nil)

		h.c.restore()

		if len(h.rt.seededPaths()) != 0 {
			t.Fatalf("seeded %v from a missing file", h.rt.seededPaths())
		}
		if h.log.count() != 0 {
			t.Fatalf("a missing file logged %v", h.log.all())
		}
		if got := h.c.Stats(); got.Records != 0 || got.Restored != 0 {
			t.Fatalf("Stats = %+v, want zero", got)
		}
	})

	t.Run("corrupt file never fails startup", func(t *testing.T) {
		h := newHarness(t, nil)
		if err := os.MkdirAll(filepath.Dir(h.file), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(h.file, []byte("\t\tnot: [valid: yaml"), 0o600); err != nil {
			t.Fatalf("write corrupt file: %v", err)
		}

		h.c.restore()

		if len(h.rt.seededPaths()) != 0 {
			t.Fatalf("seeded %v from a corrupt file", h.rt.seededPaths())
		}
		if got := h.c.Stats().Records; got != 0 {
			t.Fatalf("Records = %d, want 0", got)
		}
	})

	t.Run("stop signal aborts before seeding", func(t *testing.T) {
		h := newHarness(t, nil)
		if err := writeFile(h.file, "http://origin.local", time.Now(), []Record{{Path: "/a"}, {Path: "/b"}}); err != nil {
			t.Fatalf("writeFile: %v", err)
		}
		h.stop()

		h.c.restore()

		if len(h.rt.seededPaths()) != 0 {
			t.Fatalf("seeded %v after stop", h.rt.seededPaths())
		}
		// The records are still loaded into the registry even though seeding was
		// abandoned, so a shutdown flush writes the whole list back rather than a
		// truncated prefix of it.
		if got := h.keys(); len(got) != 2 {
			t.Fatalf("aborted restore left %v in the registry, want both records", got)
		}
		if !h.log.contains("restore interrupted") {
			t.Fatalf("aborted restore logged %v, want an interruption line", h.log.all())
		}
	})
}

func TestController_Start_RestoresThenFlushesOnStop(t *testing.T) {
	h := newHarness(t, func(cfg *Config) {
		cfg.RestoreOnStart = true
		cfg.FlushEvery = time.Hour // only the stop-triggered final flush runs
	})
	if err := writeFile(h.file, "http://origin.local", time.Unix(1, 0), []Record{
		{Path: "/remembered", LastSeen: 1000, Seen: 4},
	}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	h.c.Start()
	waitFor(t, "restore to finish", func() bool { return h.c.Stats().Restored == 1 })

	// The live request path keeps working while the registry is restored.
	h.c.NoteStored("/hot", Record{Path: "/hot", DiscoveredBy: "user"})
	h.rt.setAccess("/remembered", 2000)

	h.stop()
	h.waitWG(t)

	records := h.persisted(t)
	if len(records) != 2 {
		t.Fatalf("persisted %d records, want 2: %+v", len(records), records)
	}
	byPath := map[string]Record{}
	for _, rec := range records {
		byPath[rec.Path] = rec
	}
	remembered, ok := byPath["/remembered"]
	if !ok {
		t.Fatalf("restored record was not written back: %+v", records)
	}
	if remembered.LastSeen != 2000 || remembered.Seen != 5 {
		t.Fatalf("final flush did not merge access times: %+v", remembered)
	}
	if _, ok := byPath["/hot"]; !ok {
		t.Fatalf("record stored during the run was not written back: %+v", records)
	}
	if got := h.c.Stats().LastFlushUnix; got == 0 {
		t.Fatal("the final flush did not record a flush time")
	}
	if _, err := os.Stat(backupPath(h.file)); err != nil {
		t.Fatalf("the previous generation was not rotated to .bak: %v", err)
	}
}

func TestController_Start_PeriodicFlush(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.FlushEvery = 10 * time.Millisecond })

	h.c.Start()
	h.c.NoteStored("/a", Record{Path: "/a"})

	waitFor(t, "the ticker to flush", func() bool {
		_, err := os.Stat(h.file)
		return err == nil
	})
	waitFor(t, "the flush timestamp", func() bool { return h.c.Stats().LastFlushUnix != 0 })

	if got := len(h.persisted(t)); got != 1 {
		t.Fatalf("persisted %d records, want 1", got)
	}

	h.stop()
	h.waitWG(t)
}

func TestController_Stats(t *testing.T) {
	h := newHarness(t, nil)
	if got := h.c.Stats(); got != (Stats{}) {
		t.Fatalf("Stats on an empty controller = %+v, want zero", got)
	}

	h.c.NoteStored("/a", Record{Path: "/a"})
	h.c.NoteStored("/b", Record{Path: "/b"})
	h.c.NoteDeleted("/b")
	h.c.NoteDeleted("/b")
	h.c.NoteDeleted("/b") // ForgetAfterFailures defaults to 3 in the harness

	got := h.c.Stats()
	if got.Records != 1 {
		t.Fatalf("Records = %d, want 1", got.Records)
	}
	if got.Restored != 0 {
		t.Fatalf("Restored = %d, want 0", got.Restored)
	}
	if got.LastFlushUnix != 0 {
		t.Fatalf("LastFlushUnix = %d, want 0 before any flush", got.LastFlushUnix)
	}

	h.c.flush()
	if got := h.c.Stats().LastFlushUnix; got == 0 {
		t.Fatal("LastFlushUnix is still zero after a flush")
	}
}

func TestController_ConcurrentNotesAndFlushes(t *testing.T) {
	h := newHarness(t, func(cfg *Config) { cfg.MaxURLs = 20 })

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				key := fmt.Sprintf("/p-%d-%d", worker, i%10)
				h.c.NoteStored(key, Record{Path: key, DiscoveredBy: "user"})
				h.rt.setAccess(key, int64(2000+i))
				if i%7 == 0 {
					h.c.NoteDeleted(key)
				}
				if i%11 == 0 {
					h.c.flush()
				}
				h.c.Stats()
			}
		}(worker)
	}
	wg.Wait()

	h.c.flush()
	if got := h.c.Stats().Records; got > 20 {
		t.Fatalf("Records = %d, want at most MaxURLs=20", got)
	}
	if got := len(h.persisted(t)); got > 20 {
		t.Fatalf("persisted %d records, want at most 20", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
