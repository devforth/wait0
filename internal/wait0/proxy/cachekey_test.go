package proxy

import "testing"

func TestNormalizeVaryByQueryParams(t *testing.T) {
	params, err := NormalizeVaryByQueryParams([]string{" page ", "lang", "page"})
	if err != nil {
		t.Fatalf("NormalizeVaryByQueryParams error: %v", err)
	}
	want := []string{"lang", "page"}
	if len(params) != len(want) {
		t.Fatalf("params = %v", params)
	}
	for i := range want {
		if params[i] != want[i] {
			t.Fatalf("params[%d] = %q, want %q", i, params[i], want[i])
		}
	}
}

func TestNormalizeVaryByQueryParams_RejectsEmpty(t *testing.T) {
	if _, err := NormalizeVaryByQueryParams([]string{"page", " "}); err == nil {
		t.Fatalf("expected error for empty param name")
	}
}

func TestCanonicalCacheQuery(t *testing.T) {
	got := CanonicalCacheQuery("rand=2&page=1&page&lang=en&lang=de", []string{"lang", "page"})
	if got != "lang=de&lang=en&page&page=1" {
		t.Fatalf("CanonicalCacheQuery() = %q", got)
	}
}

func TestJoinAndSplitCacheKey(t *testing.T) {
	key := JoinCacheKey("/path", "page&page=1")
	if key != "/path?page&page=1" {
		t.Fatalf("JoinCacheKey() = %q", key)
	}
	path, query := SplitCacheKey(key)
	if path != "/path" || query != "page&page=1" {
		t.Fatalf("SplitCacheKey() = (%q, %q)", path, query)
	}
	if got := CacheKeyPath(key); got != "/path" {
		t.Fatalf("CacheKeyPath() = %q", got)
	}
}

func TestVariantCacheKeyRoundTrip(t *testing.T) {
	base := "/path?page=1"
	key := JoinVariantCacheKey(base, "generation", []string{"mobile|tablet", "CA-ON", ""})
	if key == base {
		t.Fatal("variant key must differ from base")
	}
	if got, ok := SplitVariantCacheKey(key); !ok || got != base {
		t.Fatalf("SplitVariantCacheKey() = (%q, %v), want (%q, true)", got, ok, base)
	}
	if got := CacheKeyPath(key); got != "/path" {
		t.Fatalf("CacheKeyPath() = %q", got)
	}
	if got := BaseCacheKey(key); got != base {
		t.Fatalf("BaseCacheKey() = %q", got)
	}
}
