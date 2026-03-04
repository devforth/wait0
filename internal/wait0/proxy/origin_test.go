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
