package wait0

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchFromOrigin_NoStoreIsNotCacheable(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, nil)
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/x", nil)
	ent, cacheable, statusKind, err := s.fetchFromOrigin(req)
	if err != nil {
		t.Fatalf("fetchFromOrigin error: %v", err)
	}
	if cacheable {
		t.Fatalf("expected non-cacheable response")
	}
	if statusKind != "ok" {
		t.Fatalf("statusKind = %q, want ok", statusKind)
	}
	if ent.Hash32 == 0 {
		t.Fatalf("expected hash to be set")
	}
}

func TestFetchFromOrigin_Non2xxIsIgnoredByStatus(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusBadGateway)
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, nil)
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/x", nil)
	_, cacheable, statusKind, err := s.fetchFromOrigin(req)
	if err != nil {
		t.Fatalf("fetchFromOrigin error: %v", err)
	}
	if cacheable {
		t.Fatalf("expected non-cacheable response")
	}
	if statusKind != "ignore-by-status" {
		t.Fatalf("statusKind = %q, want ignore-by-status", statusKind)
	}
}
