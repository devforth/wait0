package discovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRuntime struct {
	mu sync.Mutex

	rules map[string]*Rule
	ram   map[string]Entry
	disk  map[string]Entry

	putDisk []string
	doMap   map[string]*http.Response
	doErr   map[string]error
	doCalls []string
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		rules:  map[string]*Rule{},
		ram:    map[string]Entry{},
		disk:   map[string]Entry{},
		doMap:  map[string]*http.Response{},
		doErr:  map[string]error{},
		putDisk: []string{},
	}
}

func (f *fakeRuntime) PickRule(path string) *Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rules[path]; ok {
		return r
	}
	return nil
}

func (f *fakeRuntime) PeekRAM(path string) (Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ent, ok := f.ram[path]
	return ent, ok
}

func (f *fakeRuntime) PeekDisk(path string) (Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ent, ok := f.disk[path]
	return ent, ok
}

func (f *fakeRuntime) PutDisk(path string, ent Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putDisk = append(f.putDisk, path)
	f.disk[path] = ent
}

func (f *fakeRuntime) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url := req.URL.String()
	f.doCalls = append(f.doCalls, url)
	if err, ok := f.doErr[url]; ok {
		return nil, err
	}
	if resp, ok := f.doMap[url]; ok {
		clone := *resp
		if resp.Body == nil {
			clone.Body = io.NopCloser(strings.NewReader(""))
		}
		return &clone, nil
	}
	return nil, fmt.Errorf("unexpected URL: %s", url)
}

type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (l *captureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func mkResp(status int, body string, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func gzipBytes(t *testing.T, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	if _, err := gz.Write([]byte(body)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return b.Bytes()
}

func TestController_Start_RunOnceAndStopBeforeDelay(t *testing.T) {
	t.Run("run once", func(t *testing.T) {
		rt := newFakeRuntime()
		rt.rules["/a"] = &Rule{}
		rt.doMap["http://origin.local/sitemap.xml"] = mkResp(http.StatusOK, `<?xml version="1.0"?><urlset><url><loc>/a</loc></url></urlset>`, nil)

		stopCh := make(chan struct{})
		var wg sync.WaitGroup
		log := &captureLogger{}
		c := NewController(Config{Origin: "http://origin.local", Sitemaps: []string{"/sitemap.xml"}, RediscoverEvery: 0}, rt, stopCh, &wg, log)

		c.Start()
		waitWG(t, &wg)
		if log.count() == 0 {
			t.Fatalf("expected start log line")
		}
	})

	t.Run("stop before initial delay", func(t *testing.T) {
		rt := newFakeRuntime()
		stopCh := make(chan struct{})
		var wg sync.WaitGroup
		c := NewController(Config{Origin: "http://origin.local", Sitemaps: []string{"/sitemap.xml"}, InitialDelay: 200 * time.Millisecond}, rt, stopCh, &wg, &captureLogger{})

		c.Start()
		close(stopCh)
		waitWG(t, &wg)

		if len(rt.doCalls) != 0 {
			t.Fatalf("unexpected Do calls: %v", rt.doCalls)
		}
	})
}

func TestController_DiscoverOnce_StoresAndIgnores(t *testing.T) {
	rt := newFakeRuntime()
	rt.rules["/a"] = &Rule{}
	rt.rules["/c"] = &Rule{Bypass: true}
	rt.doMap["http://origin.local/sitemap.xml"] = mkResp(http.StatusOK, `<sitemapindex><sitemap><loc>/nested.xml</loc></sitemap></sitemapindex>`, nil)
	rt.doMap["http://origin.local/nested.xml"] = mkResp(http.StatusOK, `
		<urlset>
			<url><loc>https://example.com/a</loc></url>
			<url><loc>/b</loc></url>
			<url><loc>/c</loc></url>
			<url><loc> </loc></url>
		</urlset>`, nil)

	var wg sync.WaitGroup
	log := &captureLogger{}
	c := NewController(Config{Origin: "http://origin.local", Sitemaps: []string{"/sitemap.xml"}, LogAutodiscover: true}, rt, make(chan struct{}), &wg, log)

	stored, ignored, err := c.DiscoverOnce(context.Background())
	if err != nil {
		t.Fatalf("DiscoverOnce error: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored = %d, want 1", stored)
	}
	if ignored != 2 {
		t.Fatalf("ignored = %d, want 2", ignored)
	}
	if len(rt.putDisk) != 1 || rt.putDisk[0] != "/a" {
		t.Fatalf("putDisk = %v", rt.putDisk)
	}
	if log.count() == 0 {
		t.Fatalf("expected autodiscover logs")
	}
}

func TestController_DiscoverOnce_StopEarly(t *testing.T) {
	rt := newFakeRuntime()
	stopCh := make(chan struct{})
	close(stopCh)
	var wg sync.WaitGroup
	c := NewController(Config{Origin: "http://origin.local", Sitemaps: []string{"/sitemap.xml"}}, rt, stopCh, &wg, &captureLogger{})

	stored, ignored, err := c.DiscoverOnce(context.Background())
	if err != nil {
		t.Fatalf("DiscoverOnce error: %v", err)
	}
	if stored != 0 || ignored != 0 {
		t.Fatalf("stored=%d ignored=%d, want 0/0", stored, ignored)
	}
}

func TestController_NormalizeMaybeRelativeURL(t *testing.T) {
	c := NewController(Config{Origin: "http://origin.local"}, newFakeRuntime(), make(chan struct{}), &sync.WaitGroup{}, &captureLogger{})
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "  /a ", want: "http://origin.local/a"},
		{in: "a", want: "http://origin.local/a"},
		{in: "https://x.test/sm.xml", want: "https://x.test/sm.xml"},
	}
	for _, tc := range tests {
		if got := c.NormalizeMaybeRelativeURL(tc.in); got != tc.want {
			t.Fatalf("NormalizeMaybeRelativeURL(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestController_FetchAndParseSitemap_PlainAndGzip(t *testing.T) {
	xmlBody := `<?xml version="1.0"?><urlset><url><loc>/a</loc></url></urlset>`

	t.Run("plain", func(t *testing.T) {
		rt := newFakeRuntime()
		rt.doMap["http://origin.local/sitemap.xml"] = mkResp(http.StatusOK, xmlBody, nil)
		c := NewController(Config{Origin: "http://origin.local"}, rt, make(chan struct{}), &sync.WaitGroup{}, &captureLogger{})

		doc, err := c.FetchAndParseSitemap(context.Background(), "http://origin.local/sitemap.xml")
		if err != nil {
			t.Fatalf("FetchAndParseSitemap error: %v", err)
		}
		if len(doc.URLs) != 1 || doc.URLs[0] != "/a" {
			t.Fatalf("URLs = %v", doc.URLs)
		}
		if len(doc.Sitemaps) != 0 {
			t.Fatalf("Sitemaps = %v", doc.Sitemaps)
		}
	})

	t.Run("gzip by suffix", func(t *testing.T) {
		rt := newFakeRuntime()
		gz := gzipBytes(t, xmlBody)
		rt.doMap["http://origin.local/sitemap.xml.gz"] = &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(gz))}
		c := NewController(Config{Origin: "http://origin.local"}, rt, make(chan struct{}), &sync.WaitGroup{}, &captureLogger{})

		doc, err := c.FetchAndParseSitemap(context.Background(), "http://origin.local/sitemap.xml.gz")
		if err != nil {
			t.Fatalf("FetchAndParseSitemap error: %v", err)
		}
		if len(doc.URLs) != 1 || doc.URLs[0] != "/a" {
			t.Fatalf("URLs = %v", doc.URLs)
		}
	})

	t.Run("bad status", func(t *testing.T) {
		rt := newFakeRuntime()
		rt.doMap["http://origin.local/sitemap.xml"] = mkResp(http.StatusBadGateway, "upstream fail", nil)
		c := NewController(Config{Origin: "http://origin.local"}, rt, make(chan struct{}), &sync.WaitGroup{}, &captureLogger{})

		_, err := c.FetchAndParseSitemap(context.Background(), "http://origin.local/sitemap.xml")
		if err == nil || !strings.Contains(err.Error(), "unexpected status") {
			t.Fatalf("expected status error, got %v", err)
		}
	})
}

func TestNormalizePathFromLoc(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "https://example.com/a/b?q=1", want: "/a/b"},
		{in: "/x", want: "/x"},
		{in: "x", want: "/x"},
		{in: "https://example.com", want: "/"},
		{in: "", want: ""},
	}

	for _, tc := range tests {
		if got := NormalizePathFromLoc(tc.in); got != tc.want {
			t.Fatalf("NormalizePathFromLoc(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func waitWG(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitgroup timeout")
	}
}
