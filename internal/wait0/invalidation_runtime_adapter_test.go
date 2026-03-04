package wait0

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func waitForInvalidation(t *testing.T, cond func() bool) {
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

func TestInvalidationRuntimeAdapter_KeyOpsAndTags(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	a := newInvalidationRuntimeAdapter(s)

	s.ram.Put("/ram", CacheEntry{Header: http.Header{"X-Wait0-Tag": {"a,b", " b, c "}}}, s.disk, s.overflowLog)
	s.disk.PutAsync("/disk", CacheEntry{})

	waitForInvalidation(t, func() bool { return len(a.CachedKeys()) >= 2 })
	keys := a.CachedKeys()
	if len(keys) < 2 {
		t.Fatalf("cached keys = %v", keys)
	}
	tags := a.KeyTags("/ram")
	want := []string{"a", "b", "c"}
	if len(tags) != len(want) {
		t.Fatalf("tags = %v", tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tags[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
	if !a.HasKey("/ram") || !a.HasKey("/disk") {
		t.Fatalf("expected HasKey true for cached keys")
	}
	if a.HasKey("/missing") {
		t.Fatalf("expected HasKey false")
	}

	a.DeleteKey("/ram")
	if a.HasKey("/ram") {
		t.Fatalf("expected key deleted")
	}
}

func TestInvalidationRuntimeAdapter_RecrawlKey(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fresh"))
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, nil)
	a := newInvalidationRuntimeAdapter(s)

	kind := a.RecrawlKey(context.Background(), "/page")
	if kind != "updated" {
		t.Fatalf("RecrawlKey kind = %q, want updated", kind)
	}

	s.reval = nil
	kind = a.RecrawlKey(context.Background(), "/page")
	if kind != "error" {
		t.Fatalf("RecrawlKey nil reval kind = %q, want error", kind)
	}
}
