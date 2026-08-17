package cache

import (
	"bytes"
	"hash/crc32"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func openDisk(t *testing.T, path string, maxBytes int64, invalidate bool) *Disk {
	t.Helper()
	d, err := NewDisk(path, maxBytes, invalidate)
	if err != nil {
		t.Fatalf("NewDisk(%q): %v", path, err)
	}
	return d
}

func awaitDisk(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func rawBlob(t *testing.T, d *Disk, key string) []byte {
	t.Helper()
	b, err := d.db.Get([]byte("e:"+key), nil)
	if err != nil {
		t.Fatalf("raw blob for %q: %v", key, err)
	}
	return append([]byte(nil), b...)
}

// warmupStore builds the entry a warmup loop produces for an unchanged
// response: identical body, fresh stamps, and the Date the origin just sent.
func warmupStore(body string, at int64) Entry {
	return Entry{
		Status: 200,
		Header: http.Header{
			"Content-Type": {"text/html; charset=utf-8"},
			"Date":         {time.Unix(at, 0).UTC().Format(http.TimeFormat)},
		},
		Body:          []byte(body),
		StoredAt:      at,
		Hash32:        crc32.ChecksumIEEE([]byte(body)),
		DiscoveredBy:  "sitemap",
		RevalidatedAt: at * int64(time.Second),
		RevalidatedBy: "warmup",
	}
}

// This is the whole point of the change: warmup re-stores every cached URL on
// every loop, and an unchanged response must not rewrite the body to LevelDB.
func TestDisk_UnchangedResponseDoesNotRewriteBlob(t *testing.T) {
	d := openDisk(t, filepath.Join(t.TempDir(), "leveldb"), 10*1024*1024, true)
	defer d.Close()

	const key = "/insights/"
	const body = "<html>unchanged</html>"

	d.PutAsync(key, warmupStore(body, 1000))
	awaitDisk(t, "first store", func() bool { return d.HasKey(key) })

	firstBlob := rawBlob(t, d, key)
	sizeAfterFirst := d.TotalSize()

	// Twenty warmup loops over the same unchanged response.
	const loops = 20
	for i := 1; i <= loops; i++ {
		d.PutAsync(key, warmupStore(body, 1000+int64(i)*120))
	}
	awaitDisk(t, "warmup loops to drain", func() bool {
		return d.SkippedBodyWrites() == loops
	})

	if got := rawBlob(t, d, key); !bytes.Equal(got, firstBlob) {
		t.Fatalf("blob was rewritten: %d bytes before, %d after", len(firstBlob), len(got))
	}
	if got := d.TotalSize(); got != sizeAfterFirst {
		t.Fatalf("accounted size drifted: want %d, got %d", sizeAfterFirst, got)
	}
	if got := d.KeyCount(); got != 1 {
		t.Fatalf("KeyCount = %d, want 1", got)
	}
}

// The regression that would make the change worse than the problem: a skipped
// body write leaves the blob's own StoredAt behind, so if the stamp were not
// restored from metadata the entry would look permanently stale to
// proxy.IsStale and every request reaching disk would trigger a revalidation.
func TestDisk_SkippedWriteStillRefreshesFreshness(t *testing.T) {
	d := openDisk(t, filepath.Join(t.TempDir(), "leveldb"), 10*1024*1024, true)
	defer d.Close()

	const key = "/"
	const body = "<html>stable</html>"

	d.PutAsync(key, warmupStore(body, 1000))
	awaitDisk(t, "first store", func() bool { return d.HasKey(key) })

	const latest = int64(100000)
	d.PutAsync(key, warmupStore(body, latest))
	awaitDisk(t, "redundant store", func() bool { return d.SkippedBodyWrites() == 1 })

	// The blob on disk still carries the original stamps.
	var onDisk Entry
	if err := decodeGob(rawBlob(t, d, key), &onDisk); err != nil {
		t.Fatalf("decode raw blob: %v", err)
	}
	if onDisk.StoredAt != 1000 {
		t.Fatalf("raw blob StoredAt = %d, want the original 1000", onDisk.StoredAt)
	}

	// Everything that reads the tier must see the refreshed ones.
	peeked, ok := d.Peek(key)
	if !ok {
		t.Fatal("expected Peek hit")
	}
	if peeked.StoredAt != latest {
		t.Fatalf("Peek StoredAt = %d, want %d", peeked.StoredAt, latest)
	}
	if peeked.RevalidatedAt != latest*int64(time.Second) {
		t.Fatalf("Peek RevalidatedAt = %d, want %d", peeked.RevalidatedAt, latest*int64(time.Second))
	}
	if peeked.RevalidatedBy != "warmup" {
		t.Fatalf("Peek RevalidatedBy = %q, want %q", peeked.RevalidatedBy, "warmup")
	}

	got, ok := d.Get(key)
	if !ok {
		t.Fatal("expected Get hit")
	}
	if got.StoredAt != latest {
		t.Fatalf("Get StoredAt = %d, want %d", got.StoredAt, latest)
	}

	// Body is served from the reused blob and must be intact.
	if string(got.Body) != body {
		t.Fatalf("Get Body = %q, want %q", got.Body, body)
	}
}

// A restart is the case where every entry is read from disk at once. If stamps
// did not survive it, the first request for each URL would queue a background
// revalidation and stampede the origin.
func TestDisk_RefreshedFreshnessSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leveldb")
	d := openDisk(t, path, 10*1024*1024, true)

	const key = "/asset.js"
	const body = "console.log(1)"
	const latest = int64(555000)

	d.PutAsync(key, warmupStore(body, 1000))
	awaitDisk(t, "first store", func() bool { return d.HasKey(key) })
	d.PutAsync(key, warmupStore(body, latest))
	awaitDisk(t, "redundant store", func() bool { return d.SkippedBodyWrites() == 1 })
	d.Close()

	reopened := openDisk(t, path, 10*1024*1024, false)
	defer reopened.Close()

	ent, ok := reopened.Peek(key)
	if !ok {
		t.Fatal("expected key to survive reopen")
	}
	if ent.StoredAt != latest {
		t.Fatalf("StoredAt after reopen = %d, want %d", ent.StoredAt, latest)
	}
	if string(ent.Body) != body {
		t.Fatalf("Body after reopen = %q, want %q", ent.Body, body)
	}
}

func TestDisk_ChangedResponseRewritesBlob(t *testing.T) {
	tests := map[string]Entry{
		"changed body": func() Entry {
			e := warmupStore("<html>v2</html>", 2000)
			return e
		}(),
		"changed header": func() Entry {
			e := warmupStore("<html>v1</html>", 2000)
			e.Header.Set("Cache-Control", "public, max-age=60")
			return e
		}(),
		"changed cache variant declaration": func() Entry {
			e := warmupStore("<html>v1</html>", 2000)
			e.Header.Set("Cache-Variant", `header('Cookie')`)
			return e
		}(),
		"changed status": func() Entry {
			e := warmupStore("<html>v1</html>", 2000)
			e.Status = 404
			return e
		}(),
		"became inactive": func() Entry {
			e := warmupStore("<html>v1</html>", 2000)
			e.Inactive = true
			return e
		}(),
	}

	for name, updated := range tests {
		t.Run(name, func(t *testing.T) {
			d := openDisk(t, filepath.Join(t.TempDir(), "leveldb"), 10*1024*1024, true)
			defer d.Close()

			const key = "/page"
			d.PutAsync(key, warmupStore("<html>v1</html>", 1000))
			awaitDisk(t, "first store", func() bool { return d.HasKey(key) })
			firstBlob := rawBlob(t, d, key)

			d.PutAsync(key, updated)
			awaitDisk(t, "updated store", func() bool {
				return !bytes.Equal(rawBlob(t, d, key), firstBlob)
			})

			if d.SkippedBodyWrites() != 0 {
				t.Fatalf("%s must not be treated as redundant", name)
			}

			stored, ok := d.Peek(key)
			if !ok {
				t.Fatal("expected Peek hit")
			}
			if string(stored.Body) != string(updated.Body) {
				t.Fatalf("Body = %q, want %q", stored.Body, updated.Body)
			}
			if stored.Status != updated.Status {
				t.Fatalf("Status = %d, want %d", stored.Status, updated.Status)
			}
			if stored.Header.Get("Cache-Control") != updated.Header.Get("Cache-Control") {
				t.Fatalf("Cache-Control = %q, want %q",
					stored.Header.Get("Cache-Control"), updated.Header.Get("Cache-Control"))
			}
			if stored.Header.Get("Cache-Variant") != updated.Header.Get("Cache-Variant") {
				t.Fatalf("Cache-Variant = %q, want %q",
					stored.Header.Get("Cache-Variant"), updated.Header.Get("Cache-Variant"))
			}
		})
	}
}

// Bodyless entries hash to zero, so they must never qualify as redundant: a
// manifest's declarations and a seed's promotion to a real response both have to
// reach disk.
func TestDisk_BodylessEntriesAlwaysWriteBlob(t *testing.T) {
	d := openDisk(t, filepath.Join(t.TempDir(), "leveldb"), 10*1024*1024, true)
	defer d.Close()

	manifest := func(fingerprint string) Entry {
		return Entry{
			StoredAt: 1000,
			Variant: &VariantData{
				Kind:        "manifest",
				Expressions: []string{`header('Cookie')`},
				Fingerprint: fingerprint,
				BaseKey:     "/variant",
			},
		}
	}

	d.PutAsync("/variant", manifest("fp1"))
	awaitDisk(t, "manifest store", func() bool { return d.HasKey("/variant") })

	d.PutAsync("/variant", manifest("fp2"))
	awaitDisk(t, "manifest update", func() bool {
		ent, ok := d.Peek("/variant")
		return ok && ent.Variant != nil && ent.Variant.Fingerprint == "fp2"
	})

	// A seed carries no body either, and warmup replaces it with a real
	// response under the same key.
	d.PutAsync("/seed", Entry{Inactive: true, DiscoveredBy: "sitemap", StoredAt: 1000})
	awaitDisk(t, "seed store", func() bool { return d.HasKey("/seed") })
	d.PutAsync("/seed", warmupStore("<html>filled</html>", 1200))
	awaitDisk(t, "seed fill", func() bool {
		ent, ok := d.Peek("/seed")
		return ok && !ent.Inactive && len(ent.Body) > 0
	})

	if d.SkippedBodyWrites() != 0 {
		t.Fatalf("bodyless entries were treated as redundant: %d skips", d.SkippedBodyWrites())
	}
}

// Metadata written before the fingerprint fields existed reads back as zeroes,
// which must force a full write rather than pin whatever blob is there.
func TestDisk_LegacyMetadataForcesRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leveldb")
	d := openDisk(t, path, 10*1024*1024, true)

	const key = "/legacy"
	const body = "<html>legacy</html>"
	d.PutAsync(key, warmupStore(body, 1000))
	awaitDisk(t, "first store", func() bool { return d.HasKey(key) })
	firstBlob := rawBlob(t, d, key)

	// Rewrite the metadata record the way an older build would have left it.
	d.mu.Lock()
	legacy := d.index[key]
	d.mu.Unlock()
	legacy.BodyHash = 0
	legacy.BodyLen = 0
	legacy.ShapeHash = 0
	legacy.StoredAt = 0
	legacy.RevalidatedBy = ""
	mb, err := encodeGob(legacy)
	if err != nil {
		t.Fatalf("encodeGob: %v", err)
	}
	if err := d.db.Put([]byte("m:"+key), mb, nil); err != nil {
		t.Fatalf("Put legacy metadata: %v", err)
	}
	d.Close()

	reopened := openDisk(t, path, 10*1024*1024, false)
	defer reopened.Close()

	// The stamp overlay must not lower a blob's own stamps on legacy metadata.
	ent, ok := reopened.Peek(key)
	if !ok {
		t.Fatal("expected legacy key to load")
	}
	if ent.StoredAt != 1000 {
		t.Fatalf("legacy StoredAt = %d, want 1000 from the blob", ent.StoredAt)
	}

	reopened.PutAsync(key, warmupStore(body, 4000))
	awaitDisk(t, "rewrite over legacy metadata", func() bool {
		return !bytes.Equal(rawBlob(t, reopened, key), firstBlob)
	})
	if reopened.SkippedBodyWrites() != 0 {
		t.Fatal("legacy metadata must not be trusted as a fingerprint match")
	}
}

// Stores reuse the blob a metadata record describes, so a metadata record with
// no blob would answer every read with a miss forever instead of being repaired
// by the next store. Open is where that pairing is checked.
func TestDisk_OrphanedMetadataDroppedOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leveldb")
	d := openDisk(t, path, 10*1024*1024, true)

	const orphan = "/torn"
	const intact = "/intact"
	d.PutAsync(orphan, warmupStore("<html>torn</html>", 1000))
	d.PutAsync(intact, warmupStore("<html>intact</html>", 1000))
	awaitDisk(t, "stores", func() bool { return d.HasKey(orphan) && d.HasKey(intact) })

	// Simulate a lost journal tail: the metadata record survived, the blob did not.
	if err := d.db.Delete([]byte("e:"+orphan), nil); err != nil {
		t.Fatalf("Delete blob: %v", err)
	}
	d.Close()

	reopened := openDisk(t, path, 10*1024*1024, false)
	defer reopened.Close()

	if reopened.HasKey(orphan) {
		t.Fatal("orphaned metadata should not be indexed")
	}
	if !reopened.HasKey(intact) {
		t.Fatal("intact key should survive")
	}
	if ok, err := reopened.db.Has([]byte("m:"+orphan), nil); err != nil || ok {
		t.Fatalf("orphaned metadata record should be deleted (has=%v, err=%v)", ok, err)
	}

	// The key is storable again, which is what "repaired" means here.
	reopened.PutAsync(orphan, warmupStore("<html>restored</html>", 2000))
	awaitDisk(t, "restore after orphan drop", func() bool {
		ent, ok := reopened.Peek(orphan)
		return ok && string(ent.Body) == "<html>restored</html>"
	})
}

// Reusing the blob must not desynchronise the size accounting that drives
// eviction, since a drift would either evict a cache that fits or let one grow
// past its limit.
func TestDisk_ReuseKeepsEvictionAccountingHonest(t *testing.T) {
	d := openDisk(t, filepath.Join(t.TempDir(), "leveldb"), 10*1024*1024, true)
	defer d.Close()

	keys := []string{"/a", "/b", "/c"}
	for _, key := range keys {
		d.PutAsync(key, warmupStore("<html>"+key+"</html>", 1000))
	}
	awaitDisk(t, "initial stores", func() bool { return d.KeyCount() == len(keys) })
	baseline := d.TotalSize()

	for loop := 1; loop <= 5; loop++ {
		for _, key := range keys {
			d.PutAsync(key, warmupStore("<html>"+key+"</html>", 1000+int64(loop)*120))
		}
	}
	awaitDisk(t, "redundant loops", func() bool {
		return d.SkippedBodyWrites() == int64(5*len(keys))
	})

	if got := d.TotalSize(); got != baseline {
		t.Fatalf("TotalSize drifted from %d to %d over redundant stores", baseline, got)
	}

	// Growing a body must move the accounting again.
	d.PutAsync("/a", warmupStore("<html>/a with much more content than before</html>", 3000))
	awaitDisk(t, "grown body", func() bool { return d.TotalSize() > baseline })
}
