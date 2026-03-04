package stats

import (
	"sync"
	"testing"
	"time"
)

type fakeCacheIndex struct {
	ramKeys   []string
	diskCount int
	diskSet   map[string]bool
	ramTotal  uint64
	diskTotal uint64
}

func (f fakeCacheIndex) RAMKeys() []string       { return append([]string(nil), f.ramKeys...) }
func (f fakeCacheIndex) DiskKeyCount() int       { return f.diskCount }
func (f fakeCacheIndex) DiskHasKey(key string) bool { return f.diskSet[key] }
func (f fakeCacheIndex) RAMTotalSize() uint64    { return f.ramTotal }
func (f fakeCacheIndex) DiskTotalSize() uint64   { return f.diskTotal }

type captureLogger struct {
	mu    sync.Mutex
	lines int
}

func (l *captureLogger) Printf(string, ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines++
}

func (l *captureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lines
}

func TestCachedPathsCount(t *testing.T) {
	idx := fakeCacheIndex{
		ramKeys:   []string{"/a", "/b", "/c"},
		diskCount: 4,
		diskSet: map[string]bool{
			"/b": true,
			"/x": true,
		},
	}
	if got := CachedPathsCount(idx); got != 6 {
		t.Fatalf("CachedPathsCount = %d, want 6", got)
	}
}

func TestLoop_LogsAndStops(t *testing.T) {
	collector := NewCollector()
	collector.Observe(128)

	stopCh := make(chan struct{})
	logger := &captureLogger{}
	cfg := LoopConfig{
		Every:     10 * time.Millisecond,
		StopCh:    stopCh,
		Collector: collector,
		Cache: fakeCacheIndex{
			ramKeys:   []string{"/a"},
			diskCount: 1,
			diskSet:   map[string]bool{"/a": true},
			ramTotal:  1024,
			diskTotal: 2048,
		},
		Logger: logger,
	}

	done := make(chan struct{})
	go func() {
		Loop(cfg)
		close(done)
	}()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if logger.count() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if logger.count() == 0 {
		t.Fatal("expected at least one stats log")
	}

	close(stopCh)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Loop did not stop")
	}
}
