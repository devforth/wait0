package wait0

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met in %s", timeout)
}

func TestDiskCache_BasicIOAndIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	d, err := newDiskCache(path, 10*1024*1024, true)
	if err != nil {
		t.Fatalf("newDiskCache: %v", err)
	}
	defer d.close()

	ent := CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("abc")}
	d.PutAsync("/a", ent)
	waitFor(t, time.Second, func() bool { return d.HasKey("/a") })

	if d.KeyCount() != 1 {
		t.Fatalf("KeyCount = %d", d.KeyCount())
	}
	if len(d.Keys()) != 1 {
		t.Fatalf("Keys len = %d", len(d.Keys()))
	}
	if d.TotalSize() <= 0 {
		t.Fatalf("TotalSize should be > 0")
	}

	got, ok := d.Get("/a")
	if !ok || string(got.Body) != "abc" {
		t.Fatalf("Get failed: ok=%v body=%q", ok, string(got.Body))
	}

	d.Delete("/a")
	waitFor(t, time.Second, func() bool { return !d.HasKey("/a") })
}

func TestDiskCache_LoadIndexAndEvictSome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	d, err := newDiskCache(path, 10*1024*1024, true)
	if err != nil {
		t.Fatalf("newDiskCache: %v", err)
	}

	for i := 0; i < 6; i++ {
		d.PutAsync(string(rune('a'+i)), CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("payload-payload-payload")})
	}
	waitFor(t, time.Second, func() bool { return d.KeyCount() > 0 })
	before := d.KeyCount()
	if before == 0 {
		t.Fatalf("unexpected key count: %d", before)
	}

	d.evictSome()
	after := d.KeyCount()
	if after >= before {
		t.Fatalf("evictSome did not reduce key count: before=%d after=%d", before, after)
	}

	d.close()

	d2, err := newDiskCache(path, 300, false)
	if err != nil {
		t.Fatalf("reopen newDiskCache: %v", err)
	}
	defer d2.close()
	if d2.KeyCount() == 0 {
		t.Fatalf("expected persisted index entries")
	}
}
