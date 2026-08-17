package wait0

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
)

// stableOrigin serves a constant body while varying everything a real origin
// varies per response, so a store is redundant in content but never in bytes.
func stableOrigin(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// End to end over the real revalidation path: repeated warmup of an unchanged
// response must stop rewriting the body to LevelDB, and the entry it leaves
// behind must still read as fresh. Those two have to hold together — a gate that
// saved the write but left the entry looking stale would turn every disk-tier
// request into a background revalidation, which is worse than the write.
func TestWarmupLoops_StopRewritingDiskWithoutGoingStale(t *testing.T) {
	const body = "<html>stable page</html>"
	const expiration = time.Minute

	origin, hits := stableOrigin(t, body)

	rule := warmableRule(t, "PathPrefix(/)", 1)
	rule.expDur = expiration
	s := newTestService(t, origin.URL, []Rule{rule})

	target := revalidation.Target{Key: "/", Path: "/"}

	// First loop populates the cache.
	if res := s.reval.Once(context.Background(), target, "warmup"); !res.OK {
		t.Fatalf("first revalidation failed: %+v", res)
	}
	waitForAdapter(t, func() bool { return s.disk.HasKey("/") })
	skipsAfterFirst := s.disk.SkippedBodyWrites()

	// Nine more loops over the same unchanged response.
	const loops = 9
	for i := range loops {
		res := s.reval.Once(context.Background(), target, "warmup")
		if !res.OK {
			t.Fatalf("loop %d failed: %+v", i, res)
		}
		if res.Kind != "unchanged" {
			t.Fatalf("loop %d kind = %q, want %q", i, res.Kind, "unchanged")
		}
	}
	waitForAdapter(t, func() bool {
		return s.disk.SkippedBodyWrites()-skipsAfterFirst == loops
	})

	if got := hits.Load(); got != loops+1 {
		t.Fatalf("origin hits = %d, want %d", got, loops+1)
	}

	// The entry the disk tier hands back must be fresh, intact, and not stale.
	ent, ok := s.disk.Peek("/")
	if !ok {
		t.Fatal("expected disk hit")
	}
	if string(ent.Body) != body {
		t.Fatalf("disk body = %q, want %q", ent.Body, body)
	}
	if proxy.IsStale(toProxyEntry(ent), expiration) {
		t.Fatalf("disk entry is stale after warmup: StoredAt=%d now=%d", ent.StoredAt, time.Now().Unix())
	}
	if ent.RevalidatedBy != "warmup" {
		t.Fatalf("RevalidatedBy = %q, want %q", ent.RevalidatedBy, "warmup")
	}
}

// The disk tier is only read once RAM no longer holds the key, so that is where a
// stale stamp would actually surface: as a background revalidation on a request
// that should have been a clean hit.
func TestRequestServedFromDisk_DoesNotRevalidateAfterSkippedWrites(t *testing.T) {
	const body = "<html>served from disk</html>"

	origin, hits := stableOrigin(t, body)

	rule := warmableRule(t, "PathPrefix(/)", 1)
	rule.expDur = time.Minute
	s := newTestService(t, origin.URL, []Rule{rule})

	target := revalidation.Target{Key: "/", Path: "/"}
	for range 5 {
		if res := s.reval.Once(context.Background(), target, "warmup"); !res.OK {
			t.Fatalf("warmup failed: %+v", res)
		}
	}
	waitForAdapter(t, func() bool { return s.disk.SkippedBodyWrites() >= 4 })

	// Force the request path onto the disk tier.
	s.ram.Delete("/")
	warmupHits := hits.Load()

	rec := httptest.NewRecorder()
	s.proxy.Handle(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if got := rec.Header().Get(proxy.DebugHeaderCacheStatus); got != "hit" {
		t.Fatalf("%s = %q, want %q", proxy.DebugHeaderCacheStatus, got, "hit")
	}

	// A stale stamp would have queued a background revalidation here.
	s.wg.Wait()
	if got := hits.Load(); got != warmupHits {
		t.Fatalf("disk-tier hit triggered %d extra origin requests, want 0", got-warmupHits)
	}
}

// A genuine change still has to reach disk after any number of redundant stores,
// otherwise the gate would be silently serving stale content.
func TestWarmupLoops_PickUpRealChangeAfterRedundantStores(t *testing.T) {
	var hits atomic.Int64
	body := "<html>v1</html>"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Date", time.Now().UTC().Format(http.TimeFormat))
		_, _ = w.Write([]byte(body))
	}))
	defer origin.Close()

	rule := warmableRule(t, "PathPrefix(/)", 1)
	rule.expDur = time.Minute
	s := newTestService(t, origin.URL, []Rule{rule})

	target := revalidation.Target{Key: "/", Path: "/"}
	for range 5 {
		if res := s.reval.Once(context.Background(), target, "warmup"); !res.OK {
			t.Fatalf("warmup failed: %+v", res)
		}
	}
	waitForAdapter(t, func() bool { return s.disk.SkippedBodyWrites() >= 4 })

	body = "<html>v2 after deploy</html>"
	res := s.reval.Once(context.Background(), target, "warmup")
	if res.Kind != "updated" {
		t.Fatalf("kind = %q, want %q", res.Kind, "updated")
	}

	waitForAdapter(t, func() bool {
		ent, ok := s.disk.Peek("/")
		return ok && string(ent.Body) == body
	})

	ent, ok := s.disk.Peek("/")
	if !ok || string(ent.Body) != body {
		t.Fatalf("disk body = %q, want %q", ent.Body, body)
	}
	if ent.Hash32 == 0 {
		t.Fatal("expected the refreshed entry to carry a body hash")
	}
}
