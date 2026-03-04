package cache

import (
	"net/http"
	"testing"
)

func TestCodec_RoundTrip(t *testing.T) {
	in := Entry{Status: 201, Header: http.Header{"X": {"1"}}, Body: []byte("abc"), Inactive: true, DiscoveredBy: "sitemap"}
	b, err := encodeGob(in)
	if err != nil {
		t.Fatalf("encodeGob error: %v", err)
	}
	var out Entry
	if err := decodeGob(b, &out); err != nil {
		t.Fatalf("decodeGob error: %v", err)
	}
	if out.Status != in.Status || out.Header.Get("X") != "1" || string(out.Body) != "abc" || !out.Inactive {
		t.Fatalf("decoded mismatch: %+v", out)
	}
}

func TestCodec_DecodeError(t *testing.T) {
	var out Entry
	if err := decodeGob([]byte("not-gob"), &out); err == nil {
		t.Fatalf("expected decode error")
	}
}
