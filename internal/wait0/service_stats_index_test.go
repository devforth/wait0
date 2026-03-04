package wait0

import (
	"testing"
	"time"
)

func TestStatsCacheIndex_Methods(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	s.ram.Put("/a", CacheEntry{Body: []byte("a")}, s.disk, s.overflowLog)
	s.disk.PutAsync("/b", CacheEntry{Body: []byte("b")})
	waitFor(t, 700*time.Millisecond, func() bool { return s.disk.HasKey("/b") })

	idx := statsCacheIndex{s: s}
	if len(idx.RAMKeys()) == 0 {
		t.Fatalf("expected RAM keys")
	}
	if idx.DiskKeyCount() == 0 {
		t.Fatalf("expected disk key count")
	}
	if !idx.DiskHasKey("/b") {
		t.Fatalf("expected disk key")
	}
	if idx.RAMTotalSize() == 0 {
		t.Fatalf("expected ram total size")
	}
	if idx.DiskTotalSize() == 0 {
		t.Fatalf("expected disk total size")
	}
}
