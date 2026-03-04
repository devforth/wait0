package wait0

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wait0/internal/wait0/discovery"
)

func waitForDiscovery(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestDiscoveryRuntimeAdapter_RulesAndCacheViews(t *testing.T) {
	rule := mustRule(t, "PathPrefix(/ok)")
	s := newTestService(t, "http://example.com", []Rule{rule})
	a := newDiscoveryRuntimeAdapter(s)

	if got := a.PickRule("/missing"); got != nil {
		t.Fatalf("expected nil rule for non-match")
	}
	if got := a.PickRule("/ok/path"); got == nil {
		t.Fatalf("expected non-nil rule")
	}

	s.ram.Put("/ram", CacheEntry{Inactive: true}, s.disk, s.overflowLog)
	if ent, ok := a.PeekRAM("/ram"); !ok || !ent.Inactive {
		t.Fatalf("PeekRAM ok=%v ent=%+v", ok, ent)
	}

	a.PutDisk("/disk", discovery.Entry{Status: http.StatusOK, Header: http.Header{"X": {"1"}}, Body: []byte("d"), StoredAt: time.Now().Unix(), Inactive: true, DiscoveredBy: "sitemap"})
	waitForDiscovery(t, func() bool { ent, ok := s.disk.Peek("/disk"); return ok && ent.Inactive })
	if ent, ok := a.PeekDisk("/disk"); !ok || !ent.Inactive {
		t.Fatalf("PeekDisk ok=%v ent=%+v", ok, ent)
	}
}

func TestDiscoveryRuntimeAdapter_Do(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, nil)
	a := newDiscoveryRuntimeAdapter(s)
	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/sitemap.xml", nil)
	resp, err := a.Do(req)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
