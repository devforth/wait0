package proxy

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestWriteEntryAndHeaders(t *testing.T) {
	ent := Entry{
		Status:        http.StatusAccepted,
		Header:        http.Header{"Cache-Control": {"public"}, "X-Wait0": {"old"}, "X-Wait0-Reason": {"origin-spoof"}},
		Body:          []byte("ok"),
		DiscoveredBy:  "sitemap",
		StoredAt:      1,
		RevalidatedAt: 1,
		RevalidatedBy: "warmup",
	}
	w := httptest.NewRecorder()
	WriteEntry(w, ent, "hit", "")
	res := w.Result()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if got := res.Header.Get("X-Wait0"); got != "hit" {
		t.Fatalf("X-Wait0 = %q", got)
	}
	if got := res.Header.Get("X-Wait0-Reason"); got != "" {
		t.Fatalf("X-Wait0-Reason = %q, want empty", got)
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
	SetWait0Headers(h, "miss", "non-cacheable-content-type")
	SetWait0Headers(h, "hit", "")

	if got := h.Get("X-Wait0"); got != "hit" {
		t.Fatalf("X-Wait0 = %q", got)
	}
	if got := h.Get("Access-Control-Expose-Headers"); got != "X-Wait0, X-Other, X-Wait0-Reason" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want reason header to remain exposed without duplication", got)
	}
}

func TestWriteEntry_DebugHeadersCanBeSelectedOrDisabled(t *testing.T) {
	ent := Entry{
		Status: http.StatusOK,
		Header: http.Header{
			DebugHeaderCacheStatus:       {"origin-spoof"},
			DebugHeaderReason:            {"origin-spoof"},
			DebugHeaderRevalidatedAt:     {"origin-spoof"},
			DebugHeaderRevalidatedBy:     {"origin-spoof"},
			DebugHeaderDiscoveredBy:      {"origin-spoof"},
			DebugHeaderRevalidateAt:      {"origin-spoof"},
			DebugHeaderRevalidateEntropy: {"origin-spoof"},
		},
		Body:          []byte("ok"),
		DiscoveredBy:  "sitemap",
		RevalidatedAt: 1,
		RevalidatedBy: "warmup",
	}

	t.Run("selected subset", func(t *testing.T) {
		w := httptest.NewRecorder()
		enabled := NewDebugHeaderSet([]string{DebugHeaderReason, DebugHeaderRevalidatedBy})
		WriteEntry(w, ent, "hit", "test-reason", enabled)

		got := w.Result().Header
		if got.Get(DebugHeaderReason) != "test-reason" {
			t.Fatalf("%s = %q", DebugHeaderReason, got.Get(DebugHeaderReason))
		}
		if got.Get(DebugHeaderRevalidatedBy) != "warmup" {
			t.Fatalf("%s = %q", DebugHeaderRevalidatedBy, got.Get(DebugHeaderRevalidatedBy))
		}
		for _, name := range []string{DebugHeaderCacheStatus, DebugHeaderRevalidatedAt, DebugHeaderDiscoveredBy} {
			if value := got.Get(name); value != "" {
				t.Fatalf("disabled header %s = %q", name, value)
			}
		}
	})

	t.Run("explicit empty disables all", func(t *testing.T) {
		w := httptest.NewRecorder()
		WriteEntry(w, ent, "hit", "test-reason", NewDebugHeaderSet([]string{}))

		got := w.Result().Header
		for _, name := range DefaultDebugHeaders() {
			if value := got.Get(name); value != "" {
				t.Fatalf("disabled header %s = %q", name, value)
			}
		}
		if value := got.Get("Access-Control-Expose-Headers"); value != "" {
			t.Fatalf("Access-Control-Expose-Headers = %q, want empty", value)
		}
	})
}

func TestNormalizeDebugHeaders(t *testing.T) {
	defaults, err := NormalizeDebugHeaders(nil)
	if err != nil {
		t.Fatalf("NormalizeDebugHeaders(nil): %v", err)
	}
	if !reflect.DeepEqual(defaults, DefaultDebugHeaders()) {
		t.Fatalf("defaults = %v", defaults)
	}

	disabled, err := NormalizeDebugHeaders([]string{})
	if err != nil {
		t.Fatalf("NormalizeDebugHeaders(empty): %v", err)
	}
	if disabled == nil || len(disabled) != 0 {
		t.Fatalf("disabled = %#v, want non-nil empty list", disabled)
	}

	subset, err := NormalizeDebugHeaders([]string{"x-wait0-reason", "X-Wait0", "X-WAIT0-REASON"})
	if err != nil {
		t.Fatalf("NormalizeDebugHeaders(subset): %v", err)
	}
	wantSubset := []string{DebugHeaderReason, DebugHeaderCacheStatus}
	if !reflect.DeepEqual(subset, wantSubset) {
		t.Fatalf("subset = %v, want %v", subset, wantSubset)
	}

	if _, err := NormalizeDebugHeaders([]string{"X-Wait0-Unknown"}); err == nil {
		t.Fatal("expected unsupported debug header error")
	}
}
