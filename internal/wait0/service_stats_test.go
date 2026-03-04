package wait0

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedPathsCount(t *testing.T) {
	s := newTestService(t, "http://origin.local", nil)
	s.ram.Put("/a", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("a")}, s.disk, s.overflowLog)
	s.ram.Put("/b", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("b")}, s.disk, s.overflowLog)
	s.disk.PutAsync("/b", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("b")})
	s.disk.PutAsync("/c", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("c")})
	waitFor(t, time.Second, func() bool { return s.disk.KeyCount() >= 2 })

	if got := s.cachedPathsCount(); got != 3 {
		t.Fatalf("cachedPathsCount = %d, want 3", got)
	}
}

func TestStatsLoop_StopsOnSignal(t *testing.T) {
	disk, err := newDiskCache(filepath.Join(t.TempDir(), "db"), 8*1024*1024, true)
	if err != nil {
		t.Fatalf("newDiskCache: %v", err)
	}
	defer disk.close()

	s := &Service{
		ram:    newRAMCache(1024),
		disk:   disk,
		stopCh: make(chan struct{}),
		stats:  newStatsCollector(),
	}
	s.stats.Observe(10)
	done := make(chan struct{})
	go func() {
		s.statsLoop(2 * time.Millisecond)
		close(done)
	}()
	time.Sleep(6 * time.Millisecond)
	stopTestService(s)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("statsLoop did not stop")
	}
}
