package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchFromOrigin_PreservesMethodAndBody(t *testing.T) {
	type receivedRequest struct {
		method        string
		body          string
		contentLength int64
		contentType   string
	}
	received := make(chan receivedRequest, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		received <- receivedRequest{
			method:        r.Method,
			body:          string(body),
			contentLength: r.ContentLength,
			contentType:   r.Header.Get("Content-Type"),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	f := Fetcher{Client: &http.Client{Timeout: 2 * time.Second}, Origin: origin.URL}
	const payload = `{"name":"wait0"}`
	req := httptest.NewRequest(http.MethodPost, "http://wait0.local/api/submit", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")

	if _, _, _, err := f.FetchFromOrigin(req); err != nil {
		t.Fatalf("FetchFromOrigin error: %v", err)
	}
	got := <-received
	if got.method != http.MethodPost {
		t.Fatalf("origin method = %q, want POST", got.method)
	}
	if got.body != payload {
		t.Fatalf("origin body = %q, want %q", got.body, payload)
	}
	if got.contentLength != int64(len(payload)) {
		t.Fatalf("origin content length = %d, want %d", got.contentLength, len(payload))
	}
	if got.contentType != "application/json" {
		t.Fatalf("origin content type = %q, want application/json", got.contentType)
	}
}

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
	if statusKind != CacheabilityCacheControl {
		t.Fatalf("statusKind = %q, want %q", statusKind, CacheabilityCacheControl)
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
