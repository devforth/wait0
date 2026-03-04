package cache

import (
	"path/filepath"
	"testing"
	"time"
)

func waitForDisk(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestDisk_BasicOpsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leveldb")
	d, err := NewDisk(path, 10*1024*1024, true)
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}

	d.PutAsync("/a", Entry{Status: 200, Body: []byte("ok")})
	waitForDisk(t, func() bool { return d.HasKey("/a") })
	if d.KeyCount() == 0 || d.TotalSize() == 0 {
		t.Fatalf("expected non-empty disk index")
	}
	if _, ok := d.Peek("/a"); !ok {
		t.Fatalf("expected Peek hit")
	}
	if _, ok := d.Get("/a"); !ok {
		t.Fatalf("expected Get hit")
	}
	snap := d.SnapshotAccessTimes()
	if len(snap) == 0 {
		t.Fatalf("expected snapshot data")
	}
	if len(d.Keys()) == 0 {
		t.Fatalf("expected Keys")
	}

	d.PutAsync("/inactive", Entry{Inactive: true})
	waitForDisk(t, func() bool { return d.HasKey("/inactive") })
	if _, ok := d.Get("/inactive"); ok {
		t.Fatalf("inactive entry should not be returned from Get")
	}

	d.Delete("/a")
	waitForDisk(t, func() bool { return !d.HasKey("/a") })

	d.Close()

	// Reopen without invalidation to exercise index load.
	d2, err := NewDisk(path, 10*1024*1024, false)
	if err != nil {
		t.Fatalf("NewDisk reopen: %v", err)
	}
	defer d2.Close()
	if !d2.HasKey("/inactive") {
		t.Fatalf("expected persisted key after reopen")
	}
}

func TestDisk_Eviction(t *testing.T) {
	d, err := NewDisk(filepath.Join(t.TempDir(), "leveldb"), 256, true)
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	defer d.Close()

	for i := 0; i < 20; i++ {
		d.PutAsync(string(rune('a'+(i%26)))+"-x", Entry{Body: make([]byte, 64)})
	}
	waitForDisk(t, func() bool { return d.KeyCount() > 0 })
	d.EvictSomeForTest()
}
