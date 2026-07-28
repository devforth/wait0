package wait0

import (
	"net/http"
	"testing"
)

func TestStoreCacheableResponse_ReplacesChangedVariantFamily(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)

	first := CacheEntry{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type":  {"text/html"},
			"Cache-Variant": {`header('X-Device')`},
		},
		Body: []byte("first"),
	}
	key1, values, err := s.storeCacheableResponse("/a", http.Header{"X-Device": {"mobile"}}, "", first)
	if err != nil {
		t.Fatalf("store first: %v", err)
	}
	if key1 == "/a" || len(values) != 1 || values[0] != "mobile" {
		t.Fatalf("first key/values = %q %v", key1, values)
	}

	key2, _, err := s.storeCacheableResponse("/a", http.Header{"X-Device": {"desktop"}}, "", first)
	if err != nil {
		t.Fatalf("store sibling: %v", err)
	}
	if key2 == key1 || len(s.variantChildren("/a")) != 2 {
		t.Fatalf("siblings = %v", s.variantChildren("/a"))
	}

	changed := first
	changed.Header = http.Header{
		"Content-Type":  {"text/html"},
		"Cache-Variant": {`header('X-Country')`},
	}
	newKey, newValues, err := s.storeCacheableResponse("/a", http.Header{"X-Country": {"CA"}}, "", changed)
	if err != nil {
		t.Fatalf("store changed family: %v", err)
	}
	if len(newValues) != 1 || newValues[0] != "CA" {
		t.Fatalf("new values = %v", newValues)
	}
	children := s.variantChildren("/a")
	if len(children) != 1 || children[0] != newKey {
		t.Fatalf("children after replacement = %v", children)
	}
	for _, oldKey := range []string{key1, key2} {
		if _, ok := s.ram.Peek(oldKey); ok {
			t.Fatalf("old child %q remains in RAM", oldKey)
		}
	}
}

func TestStoreCacheableResponse_RemovesVariantFamilyForUnvariedResponse(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	varied := CacheEntry{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"text/html"}, "Cache-Variant": {`header('X-Test')`}},
		Body:   []byte("varied"),
	}
	childKey, _, err := s.storeCacheableResponse("/a", http.Header{"X-Test": {"one"}}, "", varied)
	if err != nil {
		t.Fatalf("store varied: %v", err)
	}

	plain := CacheEntry{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"text/html"}},
		Body:   []byte("plain"),
	}
	key, values, err := s.storeCacheableResponse("/a", nil, "", plain)
	if err != nil {
		t.Fatalf("store plain: %v", err)
	}
	if key != "/a" || len(values) != 0 {
		t.Fatalf("plain key/values = %q %v", key, values)
	}
	if len(s.variantChildren("/a")) != 0 {
		t.Fatalf("variant children remain: %v", s.variantChildren("/a"))
	}
	if _, ok := s.ram.Peek(childKey); ok {
		t.Fatalf("old child remains in RAM")
	}
	root, ok := s.ram.Peek("/a")
	if !ok || root.VariantKind != "" || string(root.Body) != "plain" {
		t.Fatalf("root = %+v, ok=%v", root, ok)
	}
}
