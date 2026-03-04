package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteEntryAndHeaders(t *testing.T) {
	ent := Entry{
		Status:        http.StatusAccepted,
		Header:        http.Header{"Cache-Control": {"public"}, "X-Wait0": {"old"}},
		Body:          []byte("ok"),
		DiscoveredBy:  "sitemap",
		StoredAt:      1,
		RevalidatedAt: 1,
		RevalidatedBy: "warmup",
	}
	w := httptest.NewRecorder()
	WriteEntry(w, ent, "hit")
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
