package wait0

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestRAMCache_BasicOperations(t *testing.T) {
	c := newRAMCache(1024)
	ent := CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("a")}
	c.Put("/a", ent, nil, newRateLimitedLogger(time.Hour))

	if c.TotalSize() == 0 {
		t.Fatalf("expected non-zero total size")
	}
	if len(c.Keys()) != 1 {
		t.Fatalf("expected one key")
	}
	if _, ok := c.Get("/a", time.Now().Unix()); !ok {
		t.Fatalf("expected key /a")
	}
	if len(c.SnapshotAccessTimes()) != 1 {
		t.Fatalf("expected snapshot access times")
	}

	c.Delete("/a")
	if _, ok := c.Peek("/a"); ok {
		t.Fatalf("expected key deleted")
	}
}

func TestRAMCache_EvictToDisk(t *testing.T) {
	disk, err := newDiskCache(filepath.Join(t.TempDir(), "db"), 10*1024*1024, true)
	if err != nil {
		t.Fatalf("newDiskCache: %v", err)
	}
	defer disk.close()

	c := newRAMCache(200)
	logr := newRateLimitedLogger(time.Hour)
	for i := 0; i < 10; i++ {
		k := string(rune('a' + i))
		c.Put(k, CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")}, disk, logr)
	}

	waitFor(t, time.Second, func() bool { return disk.KeyCount() > 0 })
	if disk.KeyCount() == 0 {
		t.Fatalf("expected evicted entries in disk")
	}
}
