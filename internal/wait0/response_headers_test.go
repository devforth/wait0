package wait0

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteEntryAndHeaders(t *testing.T) {
	ent := CacheEntry{
		Status:        http.StatusAccepted,
		Header:        http.Header{"Cache-Control": {"public"}, "X-Wait0": {"old"}},
		Body:          []byte("ok"),
		DiscoveredBy:  "sitemap",
		StoredAt:      1,
		RevalidatedAt: 1,
		RevalidatedBy: "warmup",
	}
	w := httptest.NewRecorder()
	writeEntry(w, ent, "hit")
	res := w.Result()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("X-Wait0"); got != "hit" {
		t.Fatalf("X-Wait0 = %q", got)
	}
	if got := res.Header.Get("X-Wait0-Discovered-By"); got != "sitemap" {
		t.Fatalf("X-Wait0-Discovered-By = %q", got)
	}
	if got := res.Header.Get("Access-Control-Expose-Headers"); got == "" {
		t.Fatalf("expected expose headers")
	}
}

func TestEnsureExposedHeader_Deduplicates(t *testing.T) {
	h := make(http.Header)
	h.Add("Access-Control-Expose-Headers", "X-A")
	h.Add("Access-Control-Expose-Headers", "X-B")
	ensureExposedHeader(h, "X-A")
	got := h.Values("Access-Control-Expose-Headers")
	if len(got) != 2 || got[0] != "X-A" || got[1] != "X-B" {
		t.Fatalf("got %#v", got)
	}
}
