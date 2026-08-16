package wait0

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"wait0/internal/wait0/urlpersist"
)

// This file holds regression tests for bugs an adversarial review found in the
// urlPersister feature. Helpers are prefixed with reg so nothing here collides
// with urlpersist_runtime_adapter_test.go or urlpersist_config_test.go, whose
// helpers are reused wherever they fit.

// regLiveEntry is a real cached response. Its status, body and Inactive flag all
// differ from the placeholder Seed writes, so an overwrite is unmistakable.
func regLiveEntry() CacheEntry {
	return CacheEntry{
		Status:       http.StatusNonAuthoritativeInfo,
		Header:       http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:         []byte("<html>live content</html>"),
		StoredAt:     1700000000,
		Hash32:       0xDEADBEEF,
		Inactive:     false,
		DiscoveredBy: "user",
	}
}

func regAssertLive(t *testing.T, where string, ent CacheEntry) {
	t.Helper()
	want := regLiveEntry()
	if ent.Status != want.Status {
		t.Fatalf("%s status = %d, want %d: the seed placeholder overwrote a live entry", where, ent.Status, want.Status)
	}
	if string(ent.Body) != string(want.Body) {
		t.Fatalf("%s body = %q, want %q: the seed placeholder overwrote a live entry", where, ent.Body, want.Body)
	}
	if ent.Inactive {
		t.Fatalf("%s was marked inactive, so it would no longer be served", where)
	}
	if ent.Hash32 != want.Hash32 {
		t.Fatalf("%s hash = %#x, want %#x", where, ent.Hash32, want.Hash32)
	}
}

// Seed writes an empty inactive placeholder, so it must never land on a key that
// already holds real content. It used to call PutAsyncWithAccess
// unconditionally, which blanked out any URL that had been served during boot
// before restore reached its record.
func TestRegression_SeedNeverOverwritesALiveCacheEntry(t *testing.T) {
	variantRec := urlpersist.Record{
		Path:         "/live",
		Query:        "page=2",
		LastSeen:     1690000000,
		DiscoveredBy: "sitemap",
		Variant: &urlpersist.Variant{
			Fingerprint: "fp-generation-1",
			Values:      []string{"mobile"},
			Headers:     map[string][]string{"X-Device": {"mobile"}},
		},
	}

	tests := []struct {
		name string
		rec  urlpersist.Record
		tier string
	}{
		{
			name: "plain record already in RAM",
			rec:  urlpersist.Record{Path: "/live", Query: "page=2", LastSeen: 1690000000, DiscoveredBy: "sitemap"},
			tier: "ram",
		},
		{
			name: "plain record already on disk",
			rec:  urlpersist.Record{Path: "/live", Query: "page=2", LastSeen: 1690000000, DiscoveredBy: "sitemap"},
			tier: "disk",
		},
		{
			name: "variant record already in RAM",
			rec:  variantRec,
			tier: "ram",
		},
		{
			name: "variant record already on disk",
			rec:  variantRec,
			tier: "disk",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/)")})
			a := newURLPersistRuntimeAdapter(s)

			key := tc.rec.CacheKey()
			live := regLiveEntry()
			switch tc.tier {
			case "ram":
				s.ram.Put(key, live, s.disk, s.overflowLog)
				if _, ok := s.ram.Peek(key); !ok {
					t.Fatalf("setup: %q is not in RAM", key)
				}
			case "disk":
				s.disk.PutAsyncWithAccess(key, live, live.StoredAt)
				waitForURLPersist(t, func() bool { return s.disk.HasKey(key) })
			}

			// The record still matches a rule, so Seed reports success; it just
			// must not touch the cache.
			if !a.Seed(tc.rec) {
				t.Fatalf("Seed = false, want true for a rule-matched record")
			}

			// Seeding a fresh URL and waiting for it drains the disk writer past
			// anything the call above might have queued, so the assertions below
			// are real rather than a race with the writer goroutine.
			sentinel := urlpersist.Record{Path: "/sentinel"}
			if !a.Seed(sentinel) {
				t.Fatalf("Seed of a fresh URL = false, want true")
			}
			waitForURLPersist(t, func() bool { return s.disk.HasKey(sentinel.CacheKey()) })

			switch tc.tier {
			case "ram":
				ent, ok := s.ram.Peek(key)
				if !ok {
					t.Fatalf("the live RAM entry for %q disappeared", key)
				}
				regAssertLive(t, "the RAM entry", ent)
				// A blank copy on disk would resurface the moment the RAM entry
				// is evicted, so the placeholder must not be written there either.
				if s.disk.HasKey(key) {
					if ent, ok := s.disk.Peek(key); ok && ent.Inactive {
						t.Fatalf("Seed wrote an inactive placeholder to disk for %q, which is live in RAM", key)
					}
				}
			case "disk":
				ent, ok := s.disk.Peek(key)
				if !ok {
					t.Fatalf("the live disk entry for %q disappeared", key)
				}
				regAssertLive(t, "the disk entry", ent)
			}

			// The fresh URL really was seeded, proving the early return is not a
			// blanket refusal to write.
			seeded, ok := s.disk.Peek(sentinel.CacheKey())
			if !ok {
				t.Fatalf("the fresh URL was not seeded at all")
			}
			if !seeded.Inactive || len(seeded.Body) != 0 {
				t.Fatalf("the fresh seed = %+v, want an empty inactive placeholder", seeded)
			}
		})
	}
}

// withinDir compared the configured path against the relative "./data/leveldb"
// constant, so an absolute path naming the same directory slipped through and
// the URL list ended up inside the tree that is wiped on every start. Both sides
// are made absolute now.
func TestRegression_URLPersisterFileMustNotResolveIntoTheDiskCacheDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	const want = `file: must not be inside the disk cache directory "./data/leveldb", which is deleted on start`

	rejected := []string{
		filepath.Join(cwd, "data", "leveldb", "urls.yaml"),
		filepath.Join(cwd, "data", "leveldb", "nested", "urls.yaml"),
		filepath.Join(cwd, "data", "leveldb"),
		// An absolute path that only resolves into the directory once cleaned.
		cwd + "/data/../data/leveldb/urls.yaml",
		// The relative forms the original comparison already caught.
		"./data/leveldb/urls.yaml",
		"data/leveldb",
	}
	for _, path := range rejected {
		cfg := URLPersisterConfig{Enabled: true, File: path}
		err := cfg.applyDefaultsAndValidate()
		if err == nil {
			t.Fatalf("applyDefaultsAndValidate(%q) = nil, want %q", path, want)
		}
		if err.Error() != want {
			t.Fatalf("applyDefaultsAndValidate(%q) error = %q, want %q", path, err.Error(), want)
		}
		if !withinDir(path, diskCacheDir) {
			t.Fatalf("withinDir(%q, %q) = false, want true", path, diskCacheDir)
		}
	}

	accepted := []string{
		filepath.Join(cwd, "data", "urls.yaml"),
		filepath.Join(cwd, "data", "leveldbextra", "urls.yaml"),
		cwd + "/data/leveldb/../urls.yaml",
		"/var/lib/wait0/urls.yaml",
		"./data/urls.yaml",
	}
	for _, path := range accepted {
		cfg := URLPersisterConfig{Enabled: true, File: path}
		if err := cfg.applyDefaultsAndValidate(); err != nil {
			t.Fatalf("applyDefaultsAndValidate(%q) = %v, want it accepted", path, err)
		}
		if cfg.File != path {
			t.Fatalf("file = %q, want %q left as configured", cfg.File, path)
		}
		if withinDir(path, diskCacheDir) {
			t.Fatalf("withinDir(%q, %q) = true, want false", path, diskCacheDir)
		}
	}
}
