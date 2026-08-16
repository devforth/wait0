package urlpersist

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"wait0/internal/wait0/proxy"
)

// This file holds regression tests for bugs an adversarial review found in the
// urlPersister feature. Each test is written so that reverting the fix it
// guards makes it fail. Helpers are prefixed with reg so nothing here can
// collide with controller_test.go or file_test.go, whose helpers are reused
// wherever they fit.

const regOrigin = "http://origin.local"

// regRuntime is a Runtime whose Seed can be slowed down or blocked, which is
// what makes a flush land in the middle of a restore. Every field is guarded
// because restore, flush and the test goroutine all touch it under -race.
type regRuntime struct {
	mu     sync.Mutex
	seeded []Record
	access map[string]int64

	// hook runs at the top of Seed, outside the lock. It is installed before
	// Start and never replaced, so reading it here needs no synchronization.
	hook func(Record)
}

func regNewRuntime(hook func(Record)) *regRuntime {
	return &regRuntime{access: map[string]int64{}, hook: hook}
}

func (r *regRuntime) Seed(rec Record) bool {
	if r.hook != nil {
		r.hook(rec)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seeded = append(r.seeded, rec.clone())
	return true
}

func (r *regRuntime) SnapshotAccessTimes() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int64, len(r.access))
	for key, ts := range r.access {
		out[key] = ts
	}
	return out
}

func (r *regRuntime) seededCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seeded)
}

func (r *regRuntime) seededKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seeded))
	for _, rec := range r.seeded {
		out = append(out, rec.CacheKey())
	}
	sort.Strings(out)
	return out
}

// regHarness is newHarness with an injectable Runtime, which the existing one
// deliberately does not offer.
type regHarness struct {
	c      *Controller
	rt     *regRuntime
	log    *ctrlLogger
	wg     *sync.WaitGroup
	stopCh chan struct{}
	file   string

	stopOnce sync.Once
}

func regNewHarness(t *testing.T, rt *regRuntime, mutate func(cfg *Config)) *regHarness {
	t.Helper()

	if rt == nil {
		rt = regNewRuntime(nil)
	}
	h := &regHarness{
		rt:     rt,
		log:    &ctrlLogger{},
		wg:     &sync.WaitGroup{},
		stopCh: make(chan struct{}),
		file:   filepath.Join(t.TempDir(), "state", "urls.yaml"),
	}

	cfg := Config{
		Enabled:             true,
		File:                h.file,
		Origin:              regOrigin,
		FlushEvery:          time.Hour,
		RestoreOnStart:      true,
		MaxURLs:             0,
		ForgetAfterFailures: 3,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	h.file = cfg.File
	h.c = NewController(cfg, rt, h.stopCh, h.wg, h.log)

	t.Cleanup(func() {
		h.stop()
		h.waitWG(t)
	})
	return h
}

func (h *regHarness) stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
}

func (h *regHarness) waitWG(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("waitgroup did not drain")
	}
}

func (h *regHarness) write(t *testing.T, records []Record) {
	t.Helper()
	if err := writeFile(h.file, h.c.cfg.Origin, time.Now(), records); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
}

// primary decodes the file itself rather than going through readFile, whose
// backup fallback would hide a truncated primary.
func (h *regHarness) primary(t *testing.T) []Record {
	t.Helper()
	records, err := decodeFile(h.file, "")
	if err != nil {
		t.Fatalf("decode %q: %v", h.file, err)
	}
	return records
}

func (h *regHarness) backup(t *testing.T) ([]Record, bool) {
	t.Helper()
	records, err := decodeFile(backupPath(h.file), "")
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("decode %q: %v", backupPath(h.file), err)
	}
	return records, true
}

func (h *regHarness) has(key string) bool {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	_, ok := h.c.byKey[key]
	return ok
}

func (h *regHarness) baseChildren(base string) []string {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	out := make([]string, 0, len(h.c.byBase[base]))
	for key := range h.c.byBase[base] {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func regRecords(n int) []Record {
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Record{
			Path:         fmt.Sprintf("/r-%03d", i),
			LastSeen:     int64(1700000000 + n - i),
			Seen:         1,
			DiscoveredBy: "user",
		})
	}
	return out
}

// regAssertHoldsAll fails when any of want is missing from got. It reports how
// many records survived, because the bug it guards against writes a prefix.
func regAssertHoldsAll(t *testing.T, what string, got, want []Record) {
	t.Helper()
	have := make(map[string]struct{}, len(got))
	for _, rec := range got {
		have[rec.CacheKey()] = struct{}{}
	}
	missing := make([]string, 0)
	for _, rec := range want {
		if _, ok := have[rec.CacheKey()]; !ok {
			missing = append(missing, rec.CacheKey())
		}
	}
	if len(missing) == 0 {
		return
	}
	shown := missing
	if len(shown) > 5 {
		shown = shown[:5]
	}
	t.Fatalf("%s holds %d records and lost %d of the %d persisted ones (e.g. %v)",
		what, len(got), len(missing), len(want), shown)
}

func regWaitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A flush that fires while restore is still running used to write back only the
// records loaded so far, replacing the file with a prefix of itself. Because
// writeFile rotates the previous generation into .bak, the intact copy was then
// destroyed by the next write too, so the loss was permanent.
func TestRegression_FlushDuringRestoreNeverTruncatesTheFile(t *testing.T) {
	const total = 500

	t.Run("stop immediately after start", func(t *testing.T) {
		records := regRecords(total)
		// A slow Seed makes seeding the long part of restore, so a shutdown that
		// arrives right after Start lands in the middle of it.
		rt := regNewRuntime(func(Record) { time.Sleep(time.Millisecond) })
		h := regNewHarness(t, rt, func(cfg *Config) { cfg.FlushEvery = time.Millisecond })
		h.write(t, records)

		h.c.Start()
		// Live traffic dirties the registry, which is what makes the shutdown
		// flush actually write: a clean registry is never persisted.
		h.c.NoteStored("/hot", Record{Path: "/hot", DiscoveredBy: "user"})
		h.stop()
		h.waitWG(t)

		got := h.primary(t)
		regAssertHoldsAll(t, "the file after a shutdown during restore", got, records)

		// The .bak generation is the recovery copy: it must never be the
		// truncated one either.
		if backup, ok := h.backup(t); ok {
			regAssertHoldsAll(t, "the .bak generation", backup, records)
		}
	})

	t.Run("periodic flush while seeding", func(t *testing.T) {
		records := regRecords(total)

		entered := make(chan struct{})
		release := make(chan struct{})
		var enterOnce, releaseOnce sync.Once
		releaseSeeding := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseSeeding()

		// The first Seed parks, so the flush below is guaranteed to run while
		// restore is only one record into seeding.
		rt := regNewRuntime(func(Record) {
			enterOnce.Do(func() {
				close(entered)
				<-release
			})
		})
		h := regNewHarness(t, rt, func(cfg *Config) { cfg.FlushEvery = 5 * time.Millisecond })
		h.write(t, records)

		h.c.Start()
		regWaitClosed(t, "restore to reach the first Seed", entered)

		h.c.NoteStored("/hot", Record{Path: "/hot", DiscoveredBy: "user"})
		waitFor(t, "a periodic flush", func() bool { return h.c.Stats().LastFlushUnix != 0 })

		// Seeding is still parked, so this is exactly the mid-restore flush.
		regAssertHoldsAll(t, "the file after a flush mid-restore", h.primary(t), records)
		if backup, ok := h.backup(t); ok {
			regAssertHoldsAll(t, "the .bak generation", backup, records)
		}

		releaseSeeding()
		waitFor(t, "restore to finish", func() bool { return h.c.Stats().Restored > 0 })
		h.stop()
		h.waitWG(t)

		regAssertHoldsAll(t, "the file after shutdown", h.primary(t), records)
	})
}

// restoreOnStart:false used to skip loading the file altogether, so the flush
// loop started on an empty registry and the first write erased everything that
// was remembered. Loading is unconditional now; only seeding is optional.
func TestRegression_RestoreOnStartFalseKeepsTheFile(t *testing.T) {
	variant := variantRecord("/c", "lang=de", "gen1", []string{"mobile"}, map[string][]string{"User-Agent": {"iPhone"}})
	variant.LastSeen = 800
	variant.Seen = 3
	records := []Record{
		{Path: "/a", LastSeen: 1000, Seen: 4, DiscoveredBy: "sitemap"},
		{Path: "/b", Query: "page=2", LastSeen: 900, Seen: 2, DiscoveredBy: "user"},
		variant,
	}

	rt := regNewRuntime(nil)
	h := regNewHarness(t, rt, func(cfg *Config) {
		cfg.RestoreOnStart = false
		cfg.FlushEvery = 5 * time.Millisecond
	})
	h.write(t, records)

	h.c.Start()
	waitFor(t, "the file to be loaded into the registry", func() bool {
		return h.c.Stats().Records == len(records)
	})

	// A live request dirties the registry, which is what triggers the write that
	// used to erase the remembered URLs.
	h.c.NoteStored("/hot", Record{Path: "/hot", DiscoveredBy: "user"})
	waitFor(t, "a periodic flush", func() bool { return h.c.Stats().LastFlushUnix != 0 })

	h.stop()
	h.waitWG(t)

	got := h.primary(t)
	regAssertHoldsAll(t, "the file", got, records)
	if len(got) != len(records)+1 {
		t.Fatalf("persisted %d records, want the %d remembered ones plus /hot", len(got), len(records))
	}

	// Nothing may be seeded: that is the whole meaning of restoreOnStart:false.
	if n := rt.seededCount(); n != 0 {
		t.Fatalf("restoreOnStart:false seeded %d records: %v", n, rt.seededKeys())
	}
	if got := h.c.Stats().Restored; got != 0 {
		t.Fatalf("Restored = %d, want 0", got)
	}

	// The registry holds them too, so the next flush writes them back again.
	for _, rec := range records {
		if !h.has(rec.CacheKey()) {
			t.Fatalf("record %q was not loaded into the registry", rec.CacheKey())
		}
	}
	if !h.log.contains("restoreOnStart is false") {
		t.Fatalf("expected a loaded-but-not-seeded log line, got %v", h.log.all())
	}
}

// restore used to publish &rec into byKey and then read that very struct
// unlocked when calling Seed, so a concurrent NoteStored/NoteDeleted on the same
// key was a data race. Seeding now works from independent copies collected under
// the lock. The assertion for the fix is `go test -race` itself; the checks
// below only prove the concurrent run stayed consistent.
func TestRegression_RestoreSeedsFromCopiesNotRegistryPointers(t *testing.T) {
	const total = 64
	records := regRecords(total)

	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseSeeding := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSeeding()

	rt := regNewRuntime(func(Record) {
		enterOnce.Do(func() {
			close(entered)
			<-release
		})
	})
	h := regNewHarness(t, rt, func(cfg *Config) {
		cfg.FlushEvery = 2 * time.Millisecond
		// High enough that the deletes below never retire a record, so the final
		// state is exactly the loaded set.
		cfg.ForgetAfterFailures = 1 << 20
	})
	h.write(t, records)

	h.c.Start()
	regWaitClosed(t, "restore to reach the first Seed", entered)

	// Hammer the very records restore is walking, from another goroutine.
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for pass := 0; pass < 8; pass++ {
			for i := range records {
				key := records[i].CacheKey()
				h.c.NoteStored(key, Record{Path: records[i].Path, DiscoveredBy: "user"})
				h.c.NoteDeleted(key)
				runtime.Gosched()
			}
		}
	}()

	releaseSeeding()
	writers.Wait()
	waitFor(t, "restore to finish", func() bool { return h.c.Stats().Restored > 0 })

	h.stop()
	h.waitWG(t)

	if got := h.c.Stats().Records; got != total {
		t.Fatalf("Records = %d, want the %d loaded records", got, total)
	}
	if got := rt.seededCount(); got != total {
		t.Fatalf("seeded %d records, want %d", got, total)
	}
	regAssertHoldsAll(t, "the file", h.primary(t), records)
}

// header.Count arrives from an untrusted file and was used directly as a slice
// capacity: a negative value panicked make, a huge one was an out-of-memory
// vector. It is a hint now, clamped before it reaches make.
func TestRegression_DecodeFileClampsTheCountHint(t *testing.T) {
	const body = "---\n" +
		"path: /a\n" +
		"lastSeen: 5\n" +
		"seen: 1\n" +
		"---\n" +
		"path: /b\n" +
		"query: page=2\n" +
		"lastSeen: 6\n" +
		"seen: 2\n"

	tests := []struct {
		name  string
		count string
	}{
		{name: "negative", count: "-1"},
		{name: "very negative", count: "-999999999999"},
		{name: "absurdly large", count: "999999999999"},
		{name: "one past the clamp", count: strconv.Itoa(maxCountHint + 1)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := fileTestPaths(t)
			fileTestWriteRaw(t, path, "version: 1\n"+
				"generatedAt: \"2023-11-14T22:13:20Z\"\n"+
				"origin: "+fileTestOrigin+"\n"+
				"count: "+tc.count+"\n"+
				body)

			// A bad hint must neither panic nor lose the records that follow it.
			got, err := decodeFile(path, fileTestOrigin)
			if err != nil {
				t.Fatalf("decodeFile: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("records = %+v, want both records", got)
			}
			if got[0].Path != "/a" || got[1].Path != "/b" || got[1].Query != "page=2" {
				t.Fatalf("records = %+v", got)
			}

			// And through readFile, which is what restore actually calls.
			log := &fileCaptureLogger{}
			if records := readFile(path, fileTestOrigin, log); len(records) != 2 {
				t.Fatalf("readFile records = %+v, want both records", records)
			}
			if n := log.count(); n != 0 {
				t.Fatalf("a clamped count hint logged %d lines: %v", n, log.snapshot())
			}
		})
	}
}

// The persisted origin was written but never checked, so pointing wait0 at a
// different origin restored URLs that belong to another site.
func TestRegression_FileOriginIsValidated(t *testing.T) {
	const (
		originA = "https://a.origin.test"
		originB = "https://b.origin.test"
	)
	records := []Record{
		{Path: "/a", LastSeen: 10, Seen: 1},
		{Path: "/b", Query: "page=2", LastSeen: 9, Seen: 1},
	}

	t.Run("a foreign origin yields no records and says why", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		if err := writeFile(path, originA, fileTestNow, records); err != nil {
			t.Fatalf("writeFile: %v", err)
		}

		_, err := decodeFile(path, originB)
		if err == nil {
			t.Fatal("decodeFile accepted a file written for another origin")
		}
		if !strings.Contains(err.Error(), originA) || !strings.Contains(err.Error(), originB) {
			t.Fatalf("error = %v, want it to name both origins", err)
		}

		log := &fileCaptureLogger{}
		if got := readFile(path, originB, log); len(got) != 0 {
			t.Fatalf("readFile returned %d foreign records: %+v", len(got), got)
		}
		if !strings.Contains(log.joined(), "origin") {
			t.Fatalf("expected a log line explaining the rejection, got %v", log.snapshot())
		}
	})

	t.Run("the matching origin reads normally", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		if err := writeFile(path, originA, fileTestNow, records); err != nil {
			t.Fatalf("writeFile: %v", err)
		}

		log := &fileCaptureLogger{}
		got := readFile(path, originA, log)
		if len(got) != 2 || got[0].Path != "/a" || got[1].Path != "/b" {
			t.Fatalf("records = %+v, want both", got)
		}
		if n := log.count(); n != 0 {
			t.Fatalf("a matching origin logged %d lines: %v", n, log.snapshot())
		}
	})

	t.Run("an empty configured origin reads anything", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		if err := writeFile(path, originA, fileTestNow, records); err != nil {
			t.Fatalf("writeFile: %v", err)
		}
		if got := readFile(path, "", &fileCaptureLogger{}); len(got) != 2 {
			t.Fatalf("records = %+v, want both", got)
		}
	})

	t.Run("a file without an origin is still readable", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		if err := writeFile(path, "", fileTestNow, records); err != nil {
			t.Fatalf("writeFile: %v", err)
		}
		if got := readFile(path, originB, &fileCaptureLogger{}); len(got) != 2 {
			t.Fatalf("records = %+v, want both: a file with no origin predates the check", got)
		}
	})

	t.Run("a foreign backup is rejected too", func(t *testing.T) {
		path, backup := fileTestPaths(t)
		fileTestWriteRaw(t, path, fileTestCorrupt)
		if err := writeFile(backup, originA, fileTestNow, records); err != nil {
			t.Fatalf("writeFile backup: %v", err)
		}

		log := &fileCaptureLogger{}
		if got := readFile(path, originB, log); len(got) != 0 {
			t.Fatalf("readFile fell back to a foreign backup: %+v", got)
		}
		if !strings.Contains(log.joined(), "empty list") {
			t.Fatalf("expected the give-up warning, got %v", log.snapshot())
		}
	})

	t.Run("the controller refuses to restore a foreign file", func(t *testing.T) {
		rt := regNewRuntime(nil)
		h := regNewHarness(t, rt, func(cfg *Config) { cfg.Origin = originB })
		if err := writeFile(h.file, originA, time.Now(), records); err != nil {
			t.Fatalf("writeFile: %v", err)
		}

		h.c.restore()

		if n := rt.seededCount(); n != 0 {
			t.Fatalf("seeded %d URLs belonging to another origin: %v", n, rt.seededKeys())
		}
		if got := h.c.Stats().Records; got != 0 {
			t.Fatalf("Records = %d, want 0", got)
		}
	})
}

// writeFile used to rename whatever sat at the target path into .bak, so a
// misconfigured file: pointing at a directory quietly moved the directory and
// everything in it out of the way.
func TestRegression_WriteFileRefusesANonRegularTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "urls.yaml")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	inner := filepath.Join(target, "precious.txt")
	fileTestWriteRaw(t, inner, "do not move me\n")

	err := writeFile(target, regOrigin, time.Now(), []Record{{Path: "/a", LastSeen: 1, Seen: 1}})
	if err == nil {
		t.Fatal("writeFile accepted a directory as the target path")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want it to explain that the target is not a regular file", err)
	}

	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("the target directory disappeared: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("the target is no longer a directory: mode=%v", info.Mode())
	}
	if got := fileTestRead(t, inner); got != "do not move me\n" {
		t.Fatalf("directory content = %q, want it untouched", got)
	}
	if _, err := os.Stat(backupPath(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the directory was rotated to %q (stat err=%v)", backupPath(target), err)
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read dir: %v", readErr)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("directory entries = %v, want only the target directory", names)
	}
}

// When a URL starts declaring Cache-Variant, its base key stops holding a
// response and starts holding a manifest, so the plain record can never be
// seeded back. pruneSupersededLocked used to leave it behind forever.
func TestRegression_PlainRecordIsDroppedWhenTheURLStartsVarying(t *testing.T) {
	t.Run("plain base key", func(t *testing.T) {
		h := newHarness(t, nil)

		h.c.NoteStored("/x", Record{Path: "/x", DiscoveredBy: "user"})
		if !h.has("/x") {
			t.Fatal("the plain record was not stored")
		}

		child := proxy.JoinVariantCacheKey("/x", "gen1", []string{"mobile"})
		h.c.NoteStored(child, variantRecord("/x", "", "gen1", []string{"mobile"}, nil))

		if h.has("/x") {
			t.Fatalf("the plain record survived the switch to variants: %v", h.keys())
		}
		if !equalStrings(h.keys(), []string{child}) {
			t.Fatalf("keys = %v, want only %q", h.keys(), child)
		}
		if !equalStrings(h.baseChildren("/x"), []string{child}) {
			t.Fatalf("byBase[/x] = %v, want only %q", h.baseChildren("/x"), child)
		}

		h.c.flush()
		records := h.persisted(t)
		if len(records) != 1 {
			t.Fatalf("persisted %d records, want only the variant one: %+v", len(records), records)
		}
		if records[0].Variant == nil {
			t.Fatalf("persisted record = %+v, want the variant one", records[0])
		}
	})

	t.Run("query aware base key", func(t *testing.T) {
		h := newHarness(t, nil)
		base := proxy.JoinCacheKey("/x", "page=2")

		h.c.NoteStored(base, Record{Path: "/x", Query: "page=2", DiscoveredBy: "user"})
		child := proxy.JoinVariantCacheKey(base, "gen1", []string{"mobile"})
		h.c.NoteStored(child, variantRecord("/x", "page=2", "gen1", []string{"mobile"}, nil))

		if h.has(base) {
			t.Fatalf("the plain record survived: %v", h.keys())
		}
		if !equalStrings(h.baseChildren(base), []string{child}) {
			t.Fatalf("byBase[%q] = %v, want only %q", base, h.baseChildren(base), child)
		}
	})

	t.Run("a second variant generation still prunes the plain record", func(t *testing.T) {
		// The plain record and an old generation of children are dead for the
		// same reason, so one store must clear both.
		h := newHarness(t, nil)
		old := proxy.JoinVariantCacheKey("/x", "gen1", []string{"mobile"})

		h.c.NoteStored("/x", Record{Path: "/x"})
		h.c.NoteStored(old, variantRecord("/x", "", "gen1", []string{"mobile"}, nil))
		fresh := proxy.JoinVariantCacheKey("/x", "gen2", []string{"mobile"})
		h.c.NoteStored(fresh, variantRecord("/x", "", "gen2", []string{"mobile"}, nil))

		if !equalStrings(h.keys(), []string{fresh}) {
			t.Fatalf("keys = %v, want only %q", h.keys(), fresh)
		}
	})
}
