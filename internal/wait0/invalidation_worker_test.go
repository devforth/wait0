package wait0

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestProcessInvalidationJob_ByPathRecrawlsEntry(t *testing.T) {
	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, "http://origin.local", []Rule{rule})
	s.invCfg = InvalidationConfig{Enabled: true, WorkerConcurrency: 2}
	s.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Cache-Control": {"public, max-age=60"},
				"X-Wait0-Tag":   {"new-tag"},
			},
			Body: io.NopCloser(strings.NewReader("fresh")),
		}, nil
	})}

	s.ram.Put("/p", CacheEntry{Status: 200, Header: http.Header{"X-Wait0-Tag": {"old-tag"}}, Body: []byte("old")}, s.disk, s.overflowLog)
	s.processInvalidationJob(1, invalidateJob{RequestID: "r1", ActorID: "backoffice", Paths: []string{"/p"}})

	got, ok := s.ram.Peek("/p")
	if !ok {
		t.Fatalf("expected recrawled cache entry")
	}
	if string(got.Body) != "fresh" {
		t.Fatalf("body = %q, want fresh", string(got.Body))
	}
	if got.RevalidatedBy != "invalidate" {
		t.Fatalf("RevalidatedBy = %q, want invalidate", got.RevalidatedBy)
	}
}

func TestProcessInvalidationJob_ByTagResolvesMatchingKeys(t *testing.T) {
	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, "http://origin.local", []Rule{rule})
	s.invCfg = InvalidationConfig{Enabled: true, WorkerConcurrency: 2}
	s.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "fresh-" + req.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Cache-Control": {"public, max-age=60"},
				"X-Wait0-Tag":   {"t1"},
			},
			Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	})}

	s.ram.Put("/a", CacheEntry{Status: 200, Header: http.Header{"X-Wait0-Tag": {"t1, t3"}}, Body: []byte("old-a")}, s.disk, s.overflowLog)
	s.ram.Put("/b", CacheEntry{Status: 200, Header: http.Header{"X-Wait0-Tag": {"t2"}}, Body: []byte("old-b")}, s.disk, s.overflowLog)

	s.processInvalidationJob(1, invalidateJob{RequestID: "r2", ActorID: "backoffice", Tags: []string{"t1"}})

	a, ok := s.ram.Peek("/a")
	if !ok {
		t.Fatalf("expected /a entry")
	}
	if string(a.Body) != "fresh-/a" {
		t.Fatalf("/a body = %q", string(a.Body))
	}
	b, ok := s.ram.Peek("/b")
	if !ok {
		t.Fatalf("expected /b entry")
	}
	if string(b.Body) != "old-b" {
		t.Fatalf("/b body = %q, want old-b", string(b.Body))
	}
}
