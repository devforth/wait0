package wait0

import (
	"context"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRevalidateOnce_UnchangedKeepsDiscoveredBy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		fmt.Fprint(w, "same")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})

	old := CacheEntry{
		Status:       200,
		Header:       make(http.Header),
		Body:         []byte("same"),
		StoredAt:     1,
		Hash32:       crc32.ChecksumIEEE([]byte("same")),
		DiscoveredBy: "sitemap",
	}
	s.ram.Put("/p", old, s.disk, s.overflowLog)

	res := s.revalidateOnce(context.Background(), "/p", "/p", "", "warmup")
	if res.kind != "unchanged" {
		t.Fatalf("kind = %q, want unchanged", res.kind)
	}
	if res.changed {
		t.Fatalf("changed = true, want false")
	}

	got, ok := s.ram.Peek("/p")
	if !ok {
		t.Fatalf("expected cached entry")
	}
	if got.DiscoveredBy != "sitemap" {
		t.Fatalf("DiscoveredBy = %q, want sitemap", got.DiscoveredBy)
	}
	if got.RevalidatedBy != "warmup" {
		t.Fatalf("RevalidatedBy = %q, want warmup", got.RevalidatedBy)
	}
}
