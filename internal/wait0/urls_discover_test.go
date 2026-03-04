package wait0

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizePathFromLoc(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "absolute", in: "https://example.com/a/b?q=1", want: "/a/b"},
		{name: "relative with slash", in: "/x", want: "/x"},
		{name: "relative no slash", in: "x", want: "/x"},
		{name: "root", in: "https://example.com", want: "/"},
	}

	for _, tc := range tests {
		got := normalizePathFromLoc(tc.in)
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeMaybeRelativeURL(t *testing.T) {
	s := newTestService(t, "https://origin.example", nil)

	if got := s.normalizeMaybeRelativeURL("/sitemap.xml"); got != "https://origin.example/sitemap.xml" {
		t.Fatalf("got %q", got)
	}
	if got := s.normalizeMaybeRelativeURL("https://other.example/sitemap.xml"); got != "https://other.example/sitemap.xml" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchAndParseSitemap_PlainAndGzip(t *testing.T) {
	const xmlBody = `<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>https://site.test/a</loc></url>
  <url><loc>/b</loc></url>
</urlset>`

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, xmlBody)
	}))
	defer plain.Close()

	s := newTestService(t, plain.URL, nil)
	doc, err := s.fetchAndParseSitemap(context.Background(), plain.URL+"/sitemap.xml")
	if err != nil {
		t.Fatalf("fetchAndParseSitemap plain: %v", err)
	}
	if len(doc.URLs) != 2 {
		t.Fatalf("URLs = %d", len(doc.URLs))
	}

	gzSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(xmlBody))
		_ = gz.Close()
		_, _ = w.Write(buf.Bytes())
	}))
	defer gzSrv.Close()

	doc, err = s.fetchAndParseSitemap(context.Background(), gzSrv.URL+"/sitemap.xml.gz")
	if err != nil {
		t.Fatalf("fetchAndParseSitemap gzip: %v", err)
	}
	if len(doc.URLs) != 2 {
		t.Fatalf("URLs gzip = %d", len(doc.URLs))
	}
}

func TestDiscoverURLsOnce_SeedsInactiveEntries(t *testing.T) {
	main := httptest.NewServer(nil)
	defer main.Close()
	nested := httptest.NewServer(nil)
	defer nested.Close()

	main.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?>
<sitemapindex>
  <sitemap><loc>%s/nested.xml</loc></sitemap>
</sitemapindex>`, nested.URL)
	})
	nested.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?>
<urlset>
  <url><loc>https://example.test/a</loc></url>
  <url><loc>/admin/skip</loc></url>
</urlset>`)
	})

	r1 := mustRule(t, "PathPrefix(/admin)")
	r1.Bypass = true
	r2 := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, "https://origin.example", []Rule{r1, r2})
	s.cfg.URLsDiscover.Sitemaps = []string{main.URL + "/root.xml"}

	stored, ignored, err := s.discoverURLsOnce(context.Background())
	if err != nil {
		t.Fatalf("discoverURLsOnce: %v", err)
	}
	if stored != 1 || ignored != 1 {
		t.Fatalf("stored=%d ignored=%d", stored, ignored)
	}
	waitFor(t, time.Second, func() bool { return s.disk.HasKey("/a") })
	ent, ok := s.disk.Peek("/a")
	if !ok || !ent.Inactive || ent.DiscoveredBy != "sitemap" {
		t.Fatalf("seed entry invalid: ok=%v inactive=%v discoveredBy=%q", ok, ent.Inactive, ent.DiscoveredBy)
	}
}

func TestStartURLsDiscover_RunOnceAndStop(t *testing.T) {
	sitemap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><urlset><url><loc>/x</loc></url></urlset>`)
	}))
	defer sitemap.Close()

	r := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, "https://origin.example", []Rule{r})
	s.cfg.URLsDiscover.Sitemaps = []string{sitemap.URL + "/s.xml"}
	s.cfg.URLsDiscover.initialDelayDur = 1 * time.Millisecond
	s.cfg.URLsDiscover.rediscoverEveryDur = 0

	s.startURLsDiscover()
	waitFor(t, time.Second, func() bool { return s.disk.HasKey("/x") })
	stopTestService(s)
	s.wg.Wait()
}
