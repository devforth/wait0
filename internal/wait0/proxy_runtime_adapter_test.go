package wait0

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wait0/internal/wait0/invalidation"
	"wait0/internal/wait0/proxy"
	wstats "wait0/internal/wait0/stats"
)

func TestProxyRuntimeAdapter_HandleInvalidationAndRule(t *testing.T) {
	s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/api)")})
	a := newProxyRuntimeAdapter(s)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/other", nil)
	if got := a.HandleInvalidation(w, r); got {
		t.Fatalf("expected false for non-invalidation path")
	}

	s.inv = nil
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "http://wait0.local"+invalidation.EndpointPath, nil)
	if got := a.HandleInvalidation(w, r); !got {
		t.Fatalf("expected true for invalidation endpoint")
	}
	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Result().StatusCode)
	}

	rule := a.PickRule("/api/x")
	if rule == nil {
		t.Fatalf("expected matching rule")
	}
	rule.BypassWhenCookies = append(rule.BypassWhenCookies, "session")
	base := s.pickRule("/api/x")
	if len(base.BypassWhenCookies) != 0 {
		t.Fatalf("rule cookie list should be copied")
	}
}

func TestProxyRuntimeAdapter_CacheAndStoreOps(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	a := newProxyRuntimeAdapter(s)

	rootEnt := CacheEntry{Status: http.StatusCreated, Header: http.Header{"X-Test": {"v"}}, Body: []byte("ram"), StoredAt: time.Now().Unix()}
	s.ram.Put("/k", rootEnt, s.disk, s.overflowLog)

	ramEnt, ok := a.LoadRAM("/k", time.Now().Unix())
	if !ok || ramEnt.Status != http.StatusCreated {
		t.Fatalf("LoadRAM ok=%v ent=%+v", ok, ramEnt)
	}

	a.Store("/disk", proxy.Entry{Status: http.StatusAccepted, Header: http.Header{"X-S": {"1"}}, Body: []byte("body")})
	if _, ok := s.ram.Peek("/disk"); !ok {
		t.Fatalf("expected Store to populate RAM")
	}
	waitFor(t, 500*time.Millisecond, func() bool { _, ok := s.disk.Get("/disk"); return ok })

	a.PromoteRAM("/promote", proxy.Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("p")})
	if _, ok := s.ram.Peek("/promote"); !ok {
		t.Fatalf("expected PromoteRAM to populate RAM")
	}

	a.DeleteKey("/promote")
	if _, ok := s.ram.Peek("/promote"); ok {
		t.Fatalf("expected deleted key in RAM")
	}
}

func TestProxyRuntimeAdapter_RevalidateAndWriteStats(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	a := newProxyRuntimeAdapter(s)
	s.reval = nil
	a.RevalidateAsync("/x", "/x", "")

	s.stats = wstats.NewCollector()
	w := httptest.NewRecorder()
	a.WriteEntryWithStats(w, proxy.Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("12345")}, "hit")
	a.WriteEntryWithStats(w, proxy.Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("123")}, "bypass")

	snap := s.stats.Snapshot()
	if snap.TotalResponses != 1 {
		t.Fatalf("stats responses = %d, want 1", snap.TotalResponses)
	}
}

func TestProxyEntryConverters_CopyData(t *testing.T) {
	src := CacheEntry{Status: 201, Header: http.Header{"X": {"1"}}, Body: []byte("abc")}
	ent := toProxyEntry(src)
	ent.Header.Set("X", "2")
	ent.Body[0] = 'z'
	if src.Header.Get("X") != "1" {
		t.Fatalf("header should be copied")
	}
	if string(src.Body) != "abc" {
		t.Fatalf("body should be copied")
	}

	back := fromProxyEntry(ent)
	back.Header.Set("X", "3")
	if ent.Header.Get("X") != "2" {
		t.Fatalf("header should be copied back")
	}
}
