package wait0

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"wait0/internal/wait0/proxy"
)

func TestHandle_CacheMissThenHit(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})

	req1 := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	w1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w1, req1)
	if got := w1.Result().Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("first request X-Wait0 = %q, want miss", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if got := w2.Result().Header.Get("X-Wait0"); got != "hit" {
		t.Fatalf("second request X-Wait0 = %q, want hit", got)
	}

	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestHandle_BypassWhenCookiePresent(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.BypassWhenCookies = []string{"sessionid"}
	s := newTestService(t, origin.URL, []Rule{rule})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	req.AddCookie(&http.Cookie{Name: "sessionid", Value: "abc"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-cookie" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-cookie", got)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestHandle_IgnoreByStatusInvalidatesRAM(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})
	s.ram.Put("/broken", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("cached"), Inactive: true}, s.disk, s.overflowLog)

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/broken", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-status", got)
	}
	if _, ok := s.ram.Peek("/broken"); ok {
		t.Fatalf("expected RAM cache entry to be deleted")
	}
}

func TestHasAnyCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "1"})

	if !proxy.HasAnyCookie(req, []string{"session"}) {
		t.Fatalf("expected cookie match")
	}
	if proxy.HasAnyCookie(req, []string{"other"}) {
		t.Fatalf("did not expect cookie match")
	}
}
