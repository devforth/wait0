package cache

import (
	"net/http"
	"testing"
)

func htmlEntry() Entry {
	return Entry{
		Status: 200,
		Header: http.Header{
			"Content-Type": {"text/html; charset=utf-8"},
			"Etag":         {`W/"abc"`},
			"Date":         {"Mon, 17 Aug 2026 12:47:42 GMT"},
		},
		Body:          []byte("<html>hello</html>"),
		StoredAt:      1000,
		Hash32:        0xDEADBEEF,
		DiscoveredBy:  "sitemap",
		RevalidatedAt: 1000 * int64(1e9),
		RevalidatedBy: "warmup",
	}
}

// The fingerprint has to be stable across calls or the gate never engages. gob
// walks maps in randomised order, so this is the property that forced a
// hand-written canonical form instead of hashing an encoded entry.
func TestEntryShapeHash_StableAcrossCalls(t *testing.T) {
	ent := htmlEntry()
	for i := range 200 {
		ent.Header.Set("X-Many", "value")
		ent.Header.Add("X-Multi", "a")
		ent.Header.Add("X-Multi", "b")
		if got, want := entryShapeHash(ent), entryShapeHash(ent); got != want {
			t.Fatalf("iteration %d: unstable hash %d != %d", i, got, want)
		}
	}
}

func TestEntryShapeHash_IgnoresVolatileHeadersAndStamps(t *testing.T) {
	base := htmlEntry()
	want := entryShapeHash(base)

	// Everything a warmup loop legitimately changes on an unchanged response.
	drifted := htmlEntry()
	drifted.Header.Set("Date", "Mon, 17 Aug 2026 13:51:03 GMT")
	drifted.Header.Set("Age", "7")
	drifted.StoredAt = 9999
	drifted.RevalidatedAt = 9999 * int64(1e9)
	drifted.RevalidatedBy = "invalidation"

	if got := entryShapeHash(drifted); got != want {
		t.Fatalf("volatile drift changed shape hash: %d != %d", got, want)
	}
}

func TestEntryShapeHash_DetectsMeaningfulChanges(t *testing.T) {
	base := htmlEntry()
	baseHash := entryShapeHash(base)

	tests := []struct {
		name   string
		mutate func(*Entry)
	}{
		{"status", func(e *Entry) { e.Status = 301 }},
		{"inactive", func(e *Entry) { e.Inactive = true }},
		{"discovered by", func(e *Entry) { e.DiscoveredBy = "user" }},
		{"header value", func(e *Entry) { e.Header.Set("Etag", `W/"xyz"`) }},
		{"header added", func(e *Entry) { e.Header.Set("Cache-Control", "public") }},
		{"header removed", func(e *Entry) { e.Header.Del("Etag") }},
		{"header extra value", func(e *Entry) { e.Header.Add("Etag", `W/"second"`) }},
		{"cache variant declaration", func(e *Entry) {
			e.Header.Set("Cache-Variant", `header('Cookie')`)
		}},
		{"variant attached", func(e *Entry) {
			e.Variant = &VariantData{Kind: "response", Fingerprint: "fp", BaseKey: "/"}
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ent := htmlEntry()
			tc.mutate(&ent)
			if got := entryShapeHash(ent); got == baseHash {
				t.Fatalf("%s did not change the shape hash", tc.name)
			}
		})
	}
}

func TestEntryShapeHash_DetectsVariantChanges(t *testing.T) {
	withVariant := func(mutate func(*VariantData)) Entry {
		ent := htmlEntry()
		ent.Variant = &VariantData{
			Kind:           "response",
			Expressions:    []string{`header('Cookie')`},
			Fingerprint:    "fp1",
			HeaderNames:    []string{"Cookie"},
			BaseKey:        "/",
			Values:         []string{"pro"},
			RequestHeaders: http.Header{"Cookie": {"plan=pro"}},
		}
		mutate(ent.Variant)
		return ent
	}

	baseHash := entryShapeHash(withVariant(func(*VariantData) {}))

	tests := map[string]func(*VariantData){
		"kind":            func(v *VariantData) { v.Kind = "manifest" },
		"expressions":     func(v *VariantData) { v.Expressions = []string{`header('Accept')`} },
		"fingerprint":     func(v *VariantData) { v.Fingerprint = "fp2" },
		"header names":    func(v *VariantData) { v.HeaderNames = []string{"Accept"} },
		"values":          func(v *VariantData) { v.Values = []string{"free"} },
		"request headers": func(v *VariantData) { v.RequestHeaders = http.Header{"Cookie": {"plan=free"}} },
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if got := entryShapeHash(withVariant(mutate)); got == baseHash {
				t.Fatalf("variant %s did not change the shape hash", name)
			}
		})
	}
}

// Header names and values are length-prefixed so that regrouping the same
// characters cannot collide.
func TestEntryShapeHash_NoHeaderBoundaryCollision(t *testing.T) {
	a := Entry{Status: 200, Hash32: 1, Header: http.Header{"X-Ab": {"c"}}}
	b := Entry{Status: 200, Hash32: 1, Header: http.Header{"X-A": {"bc"}}}
	if entryShapeHash(a) == entryShapeHash(b) {
		t.Fatal("header name/value boundary collides")
	}

	multi := Entry{Status: 200, Hash32: 1, Header: http.Header{"X-A": {"b", "c"}}}
	single := Entry{Status: 200, Hash32: 1, Header: http.Header{"X-A": {"bc"}}}
	if entryShapeHash(multi) == entryShapeHash(single) {
		t.Fatal("multi-value and joined-value headers collide")
	}
}

func TestBlobUpToDate(t *testing.T) {
	const (
		hash    = uint32(0xABCD)
		bodyLen = int64(18)
		shape   = uint32(0x1234)
	)
	fresh := diskMeta{Size: 400, BodyHash: hash, BodyLen: bodyLen, ShapeHash: shape}

	if !blobUpToDate(fresh, hash, bodyLen, shape) {
		t.Fatal("identical response should reuse the stored blob")
	}

	tests := map[string]struct {
		meta     diskMeta
		bodyHash uint32
		bodyLen  int64
		shape    uint32
	}{
		"body hash differs":   {fresh, 0x1111, bodyLen, shape},
		"body length differs": {fresh, hash, bodyLen + 1, shape},
		"shape differs":       {fresh, hash, bodyLen, 0x9999},
		// Manifests and seeds carry no body, and CRC32 of nothing is zero.
		// Accepting that as a match would freeze their first blob forever.
		"empty body":      {fresh, 0, 0, shape},
		"unknown key":     {diskMeta{}, hash, bodyLen, shape},
		"legacy metadata": {diskMeta{Size: 400}, hash, bodyLen, shape},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if blobUpToDate(tc.meta, tc.bodyHash, tc.bodyLen, tc.shape) {
				t.Fatalf("%s should force a full write", name)
			}
		})
	}
}
