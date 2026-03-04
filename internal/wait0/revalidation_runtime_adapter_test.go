package wait0

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wait0/internal/wait0/revalidation"
)

func waitForAdapter(t *testing.T, cond func() bool) {
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

func TestRevalidationRuntimeAdapter_PeekPutDelete(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	a := newRevalidationRuntimeAdapter(s)

	s.ram.Put("/ram", CacheEntry{Status: 200, Header: http.Header{"X": {"1"}}, Body: []byte("r")}, s.disk, s.overflowLog)
	if ent, ok := a.Peek("/ram"); !ok || ent.Status != 200 {
		t.Fatalf("Peek RAM ok=%v ent=%+v", ok, ent)
	}

	a.Put("/disk", revalidation.Entry{Status: 201, Header: http.Header{"Y": {"2"}}, Body: []byte("d"), DiscoveredBy: "sitemap"})
	if _, ok := s.ram.Peek("/disk"); !ok {
		t.Fatalf("expected put to RAM")
	}
	waitForAdapter(t, func() bool { _, ok := s.disk.Get("/disk"); return ok })

	a.Delete("/disk")
	if _, ok := s.ram.Peek("/disk"); ok {
		t.Fatalf("expected deleted key from RAM")
	}
}

func TestRevalidationRuntimeAdapter_SnapshotsAndHelpers(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	a := newRevalidationRuntimeAdapter(s)

	s.ram.Put("/a", CacheEntry{Status: 200}, s.disk, s.overflowLog)
	s.ram.setLastAccessForTest("/a", 10)
	s.disk.PutAsync("/a", CacheEntry{Status: 200})
	s.disk.PutAsync("/b", CacheEntry{Status: 200})

	m := a.SnapshotAccessTimes()
	if len(m) == 0 {
		t.Fatalf("expected snapshot map")
	}
	waitForAdapter(t, func() bool { return len(a.AllKeys()) >= 2 })
	keys := a.AllKeys()
	if len(keys) < 2 {
		t.Fatalf("expected deduped keys, got %v", keys)
	}

	if a.Origin() != "http://example.com" {
		t.Fatalf("origin = %q", a.Origin())
	}
	if !a.SendRevalidateMarkers() {
		t.Fatalf("expected send markers true")
	}
	if got := a.RandomString(8); len(got) != 8 {
		t.Fatalf("random string length = %d", len(got))
	}
}

func TestRevalidationRuntimeAdapter_Do(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, nil)
	a := newRevalidationRuntimeAdapter(s)

	req, _ := http.NewRequest(http.MethodGet, origin.URL+"/x", nil)
	resp, err := a.Do(req)
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRevalidationConverters_CopyData(t *testing.T) {
	src := CacheEntry{Status: 200, Header: http.Header{"X": {"1"}}, Body: []byte("abc"), StoredAt: time.Now().Unix()}
	rent := toRevalEntry(src)
	rent.Header.Set("X", "2")
	rent.Body[0] = 'z'
	if src.Header.Get("X") != "1" || string(src.Body) != "abc" {
		t.Fatalf("expected deep copy in toRevalEntry")
	}

	back := fromRevalEntry(rent)
	back.Header.Set("X", "3")
	if rent.Header.Get("X") != "2" {
		t.Fatalf("expected deep copy in fromRevalEntry")
	}
}
