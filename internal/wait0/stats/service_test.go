package stats

import (
	"sync"
	"testing"
	"time"
)

type fakeCacheIndex struct {
	ramKeys   []string
	diskKeys  []string
	ramTotal  uint64
	diskTotal uint64
}

func (f fakeCacheIndex) RAMKeys() []string     { return append([]string(nil), f.ramKeys...) }
func (f fakeCacheIndex) DiskKeys() []string    { return append([]string(nil), f.diskKeys...) }
func (f fakeCacheIndex) RAMTotalSize() uint64  { return f.ramTotal }
func (f fakeCacheIndex) DiskTotalSize() uint64 { return f.diskTotal }

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
		ramKeys:  []string{"/a", "/b", "/c"},
		diskKeys: []string{"/b", "/d", "/e", "/f"},
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
			diskKeys:  []string{"/a"},
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
