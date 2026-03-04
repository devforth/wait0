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

func TestSetWait0Headers_ExposeHeaderNoDuplicate(t *testing.T) {
	h := http.Header{}
	h.Set("Access-Control-Expose-Headers", "X-Wait0, X-Other")
	SetWait0Headers(h, "miss")
	SetWait0Headers(h, "hit")

	if got := h.Get("X-Wait0"); got != "hit" {
		t.Fatalf("X-Wait0 = %q", got)
	}
	if got := h.Get("Access-Control-Expose-Headers"); got != "X-Wait0, X-Other" {
		t.Fatalf("Access-Control-Expose-Headers = %q", got)
	}
}
