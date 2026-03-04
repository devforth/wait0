package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchFromOrigin_NoStoreIsNotCacheable(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	f := Fetcher{Client: &http.Client{Timeout: 2 * time.Second}, Origin: origin.URL}
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/x", nil)
	ent, cacheable, statusKind, err := f.FetchFromOrigin(req)
	if err != nil {
		t.Fatalf("FetchFromOrigin error: %v", err)
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

func TestFetchFromOrigin_Non2xxIsIgnoreByStatus(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprint(w, "brew")
	}))
	defer origin.Close()

	f := Fetcher{Client: &http.Client{Timeout: 2 * time.Second}, Origin: origin.URL}
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/x", nil)
	ent, cacheable, statusKind, err := f.FetchFromOrigin(req)
	if err != nil {
		t.Fatalf("FetchFromOrigin error: %v", err)
	}
	if cacheable {
		t.Fatalf("expected non-cacheable response")
	}
	if statusKind != "ignore-by-status" {
		t.Fatalf("statusKind = %q, want ignore-by-status", statusKind)
	}
	if ent.Status != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", ent.Status, http.StatusTeapot)
	}
}

func TestCopyHeaders_SkipsHostAndCopiesValues(t *testing.T) {
	src := http.Header{}
	src.Add("Host", "example.com")
	src.Add("X-Test", "a")
	src.Add("X-Test", "b")
	dst := http.Header{}

	CopyHeaders(dst, src)

	if got := dst.Values("Host"); len(got) != 0 {
		t.Fatalf("Host should be skipped, got %v", got)
	}
	got := dst.Values("X-Test")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("X-Test = %v", got)
	}
}
