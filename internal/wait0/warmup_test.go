package wait0

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWarmupHelpers_OrderAndUnion(t *testing.T) {
	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, "http://origin.local", []Rule{rule})

	s.ram.Put("/a", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("a")}, s.disk, s.overflowLog)
	s.ram.Put("/b", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("b")}, s.disk, s.overflowLog)
	s.disk.PutAsync("/c", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("c")})
	waitFor(t, time.Second, func() bool { return s.disk.HasKey("/c") })

	s.ram.mu.Lock()
	s.ram.items["/a"].lastAccess = 10
	s.ram.items["/b"].lastAccess = 20
	s.ram.mu.Unlock()

	keys := s.keysByLastAccessDesc(&rule)
	if len(keys) < 3 {
		t.Fatalf("unexpected order: %#v", keys)
	}
	idx := map[string]int{}
	for i, k := range keys {
		idx[k] = i
	}
	if idx["/b"] > idx["/a"] {
		t.Fatalf("expected /b before /a, got %#v", keys)
	}

	all := s.allKeysSnapshot()
	if len(all) != 3 {
		t.Fatalf("allKeysSnapshot len = %d", len(all))
	}
}

func TestWarmupGroupLoop_DispatchAndStop(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=1")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.warmEvery = 2 * time.Millisecond
	rule.warmMax = 1

	s := newTestService(t, origin.URL, []Rule{rule})
	s.cfg.Logging.LogWarmUp = true
	s.ram.Put("/x", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("old")}, s.disk, s.overflowLog)

	done := make(chan struct{})
	go func() {
		s.warmupGroupLoop(&rule)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	stopTestService(s)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("warmupGroupLoop did not stop")
	}
}
