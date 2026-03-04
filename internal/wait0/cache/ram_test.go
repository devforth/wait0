package cache

import (
	"path/filepath"
	"testing"
	"time"
)

type fakeLogger struct{ n int }

func (l *fakeLogger) Printf(string, ...any) { l.n++ }

func waitForRAM(t *testing.T, cond func() bool) {
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

func TestRAM_BasicOps(t *testing.T) {
	ram := NewRAM(1024)
	if ram.TotalSize() != 0 {
		t.Fatalf("total size = %d", ram.TotalSize())
	}
	ent := Entry{Status: 200, Body: []byte("ok")}
	ram.Put("/a", ent, nil, nil)
	if _, ok := ram.Peek("/a"); !ok {
		t.Fatalf("expected key in RAM")
	}
	if got, ok := ram.Get("/a", 10); !ok || got.Status != 200 {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}
	if !ram.SetLastAccessForTest("/a", 123) {
		t.Fatalf("expected SetLastAccessForTest true")
	}
	snap := ram.SnapshotAccessTimes()
	if snap["/a"] != 123 {
		t.Fatalf("snapshot ts = %d", snap["/a"])
	}
	ram.Delete("/a")
	if _, ok := ram.Peek("/a"); ok {
		t.Fatalf("expected delete")
	}
}

func TestRAM_InactiveAndOversizePaths(t *testing.T) {
	disk, err := NewDisk(filepath.Join(t.TempDir(), "disk"), 10*1024*1024, true)
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	defer disk.Close()

	ram := NewRAM(16)
	inactive := Entry{Inactive: true, Body: []byte("i")}
	ram.Put("/inactive", inactive, disk, nil)
	if _, ok := ram.Get("/inactive", time.Now().Unix()); ok {
		t.Fatalf("inactive get should miss")
	}

	big := Entry{Body: make([]byte, 1024)}
	ram.Put("/big", big, disk, nil)
	if _, ok := ram.Peek("/big"); ok {
		t.Fatalf("oversize entry should skip RAM")
	}
	waitForRAM(t, func() bool { return disk.HasKey("/big") })
}

func TestRAM_EvictsToDisk(t *testing.T) {
	disk, err := NewDisk(filepath.Join(t.TempDir(), "disk"), 10*1024*1024, true)
	if err != nil {
		t.Fatalf("NewDisk: %v", err)
	}
	defer disk.Close()

	log := &fakeLogger{}
	ram := NewRAM(80)
	for i := 0; i < 8; i++ {
		ram.Put(string(rune('a'+i)), Entry{Body: make([]byte, 30)}, disk, log)
	}
	waitForRAM(t, func() bool { return disk.KeyCount() > 0 })
	if ram.TotalSize() > 80 {
		t.Fatalf("ram total exceeded max: %d", ram.TotalSize())
	}
	_ = ram.Keys()
}
