package wait0

import (
	"net/http"
	"testing"
)

func TestRootCodec_RoundTrip(t *testing.T) {
	in := CacheEntry{Status: 202, Header: http.Header{"X": {"1"}}, Body: []byte("abc"), Inactive: true}
	b, err := encodeGob(in)
	if err != nil {
		t.Fatalf("encodeGob error: %v", err)
	}
	var out CacheEntry
	if err := decodeGob(b, &out); err != nil {
		t.Fatalf("decodeGob error: %v", err)
	}
	if out.Status != in.Status || out.Header.Get("X") != "1" || string(out.Body) != "abc" || !out.Inactive {
		t.Fatalf("decoded mismatch: %+v", out)
	}
}

func TestRandomString(t *testing.T) {
	if got := randomString(0); got != "" {
		t.Fatalf("randomString(0) = %q", got)
	}
	if got := randomString(16); len(got) != 16 {
		t.Fatalf("len(randomString(16)) = %d", len(got))
	}
}
