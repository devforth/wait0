package wait0

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wait0/internal/wait0/urlpersist"

	"gopkg.in/yaml.v3"
)

// waitForURLPersist polls a condition instead of sleeping, because every cache
// write the persister makes goes through the disk writer goroutine.
func waitForURLPersist(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("urlPersister condition not met in time")
}

type urlPersistLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *urlPersistLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

// urlPersistedRecord mirrors the persisted YAML document so a test can read the
// file the controller wrote without reaching into the package's unexported
// decoder.
type urlPersistedRecord struct {
	Path         string               `yaml:"path"`
	Query        string               `yaml:"query"`
	LastSeen     int64                `yaml:"lastSeen"`
	Seen         int64                `yaml:"seen"`
	DiscoveredBy string               `yaml:"discoveredBy"`
	Failures     int                  `yaml:"failures"`
	Variant      *urlPersistedVariant `yaml:"variant"`
}

type urlPersistedVariant struct {
	Fingerprint string              `yaml:"fingerprint"`
	Values      []string            `yaml:"values"`
	Headers     map[string][]string `yaml:"headers"`
}

func (r urlPersistedRecord) toRecord() urlpersist.Record {
	out := urlpersist.Record{
		Path:         r.Path,
		Query:        r.Query,
		LastSeen:     r.LastSeen,
		Seen:         r.Seen,
		DiscoveredBy: r.DiscoveredBy,
		Failures:     r.Failures,
	}
	if r.Variant != nil {
		out.Variant = &urlpersist.Variant{
			Fingerprint: r.Variant.Fingerprint,
			Values:      append([]string(nil), r.Variant.Values...),
			Headers:     r.Variant.Headers,
		}
	}
	return out
}

func readURLPersistFile(t *testing.T, path string) []urlPersistedRecord {
	t.Helper()

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	var header struct {
		Version int    `yaml:"version"`
		Origin  string `yaml:"origin"`
		Count   int    `yaml:"count"`
	}
	if err := dec.Decode(&header); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		t.Fatalf("decode header: %v", err)
	}
	if header.Version != urlpersist.FileVersion {
		t.Fatalf("file version = %d, want %d", header.Version, urlpersist.FileVersion)
	}

	out := make([]urlPersistedRecord, 0, header.Count)
	for {
		var rec urlPersistedRecord
		err := dec.Decode(&rec)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode record: %v", err)
		}
		out = append(out, rec)
	}
	if header.Count != len(out) {
		t.Fatalf("header count = %d, but %d records follow", header.Count, len(out))
	}
	return out
}

// urlPersistHarness wires a live urlpersist.Controller into a test service,
// which newTestService deliberately leaves unset. The controller runs on its own
// stop channel so a test can stop it — and force its final flush — while the
// service and its disk cache are still open.
type urlPersistHarness struct {
	s    *Service
	ctrl *urlpersist.Controller
	file string

	logger   *urlPersistLogger
	stopCh   chan struct{}
	wg       sync.WaitGroup
	stopOnce sync.Once
}

func newURLPersistHarness(t *testing.T, origin string, rules []Rule, forgetAfterFailures int) *urlPersistHarness {
	t.Helper()

	s := newTestService(t, origin, rules)
	h := &urlPersistHarness{
		s:      s,
		file:   filepath.Join(t.TempDir(), "urls.yaml"),
		logger: &urlPersistLogger{},
		stopCh: make(chan struct{}),
	}
	h.ctrl = urlpersist.NewController(
		urlpersist.Config{
			Enabled: true,
			File:    h.file,
			Origin:  origin,
			// Long enough that only the stop-triggered flush ever fires, which
			// keeps the file deterministic.
			FlushEvery:          time.Hour,
			RestoreOnStart:      false,
			MaxURLs:             100,
			ForgetAfterFailures: forgetAfterFailures,
		},
		newURLPersistRuntimeAdapter(s),
		h.stopCh,
		&h.wg,
		h.logger,
	)
	s.urlp = h.ctrl
	h.ctrl.Start()
	t.Cleanup(h.stop)
	return h
}

func (h *urlPersistHarness) stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
	h.wg.Wait()
}

// flushAndRead stops the controller, which runs the final flush synchronously,
// and returns whatever it wrote.
func (h *urlPersistHarness) flushAndRead(t *testing.T) []urlPersistedRecord {
	t.Helper()
	h.stop()
	return readURLPersistFile(t, h.file)
}

func (h *urlPersistHarness) get(t *testing.T, path string) *http.Response {
	t.Helper()
	return h.request(t, http.MethodGet, path, nil)
}

func (h *urlPersistHarness) request(t *testing.T, method, path string, headers http.Header) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "http://wait0.local"+path, nil)
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	w := httptest.NewRecorder()
	h.s.proxy.Handle(w, req)
	return w.Result()
}

func TestURLPersistRuntimeAdapter_SeedSkipsUnmatchedAndBypassedPaths(t *testing.T) {
	bypass := mustRule(t, "PathPrefix(/private)")
	bypass.Bypass = true
	s := newTestService(t, "http://example.com", []Rule{bypass, mustRule(t, "PathPrefix(/ok)")})
	a := newURLPersistRuntimeAdapter(s)

	if a.Seed(urlpersist.Record{Path: "/nothing/matches"}) {
		t.Fatalf("Seed of an unmatched path = true, want false")
	}
	if a.Seed(urlpersist.Record{Path: "/private/page"}) {
		t.Fatalf("Seed of a bypassed path = true, want false")
	}
	// Seeding a matched path last drains the disk writer past the two calls
	// above, so their absence below is a real assertion rather than a race.
	if !a.Seed(urlpersist.Record{Path: "/ok/page"}) {
		t.Fatalf("Seed of a matched path = false, want true")
	}

	waitForURLPersist(t, func() bool { return s.disk.HasKey("/ok/page") })
	for _, key := range []string{"/nothing/matches", "/private/page"} {
		if s.disk.HasKey(key) {
			t.Fatalf("rejected record %q was written to the cache anyway", key)
		}
	}
}

func TestURLPersistRuntimeAdapter_SeedPlainRecord(t *testing.T) {
	s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/)")})
	a := newURLPersistRuntimeAdapter(s)

	rec := urlpersist.Record{
		Path:         "/plain",
		Query:        "page=2",
		LastSeen:     1700000000,
		DiscoveredBy: "sitemap",
	}
	if !a.Seed(rec) {
		t.Fatalf("Seed = false, want true")
	}

	key := rec.BaseCacheKey()
	if key != "/plain?page=2" {
		t.Fatalf("base cache key = %q, want /plain?page=2", key)
	}
	waitForURLPersist(t, func() bool { _, ok := s.disk.Peek(key); return ok })

	ent, ok := s.disk.Peek(key)
	if !ok {
		t.Fatalf("seeded entry missing at %q", key)
	}
	if !ent.Inactive {
		t.Fatalf("seeded entry is active; warmup would never fill it")
	}
	if ent.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", ent.Status)
	}
	if len(ent.Body) != 0 {
		t.Fatalf("body = %q, want empty placeholder", ent.Body)
	}
	if ent.DiscoveredBy != "sitemap" {
		t.Fatalf("discoveredBy = %q, want the persisted sitemap", ent.DiscoveredBy)
	}
	if ent.VariantKind != "" {
		t.Fatalf("variantKind = %q, want empty for a plain record", ent.VariantKind)
	}
	// The remembered access time is what keeps warmup ordering across restarts.
	if got := s.disk.SnapshotAccessTimes()[key]; got != rec.LastSeen {
		t.Fatalf("last access = %d, want the persisted lastSeen %d", got, rec.LastSeen)
	}
}

func TestURLPersistRuntimeAdapter_SeedDefaultsDiscoveredByToUser(t *testing.T) {
	s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/)")})
	a := newURLPersistRuntimeAdapter(s)

	if !a.Seed(urlpersist.Record{Path: "/anon"}) {
		t.Fatalf("Seed = false, want true")
	}
	waitForURLPersist(t, func() bool { _, ok := s.disk.Peek("/anon"); return ok })

	ent, _ := s.disk.Peek("/anon")
	if ent.DiscoveredBy != "user" {
		t.Fatalf("discoveredBy = %q, want user", ent.DiscoveredBy)
	}
}

func TestURLPersistRuntimeAdapter_SeedVariantRecordSurvivesCacheRoundTrip(t *testing.T) {
	s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/)")})
	a := newURLPersistRuntimeAdapter(s)

	rec := urlpersist.Record{
		Path:         "/vary",
		LastSeen:     1690000123,
		DiscoveredBy: "user",
		Variant: &urlpersist.Variant{
			Fingerprint: "fingerprint-1",
			Values:      []string{"mobile", "CA"},
			Headers: map[string][]string{
				"X-Device":     {"mobile"},
				"Cf-Ipcountry": {"CA"},
			},
		},
	}
	if !a.Seed(rec) {
		t.Fatalf("Seed = false, want true")
	}

	baseKey := rec.BaseCacheKey()
	childKey := rec.CacheKey()
	if childKey == baseKey {
		t.Fatalf("variant record cache key = base key %q", baseKey)
	}
	waitForURLPersist(t, func() bool { _, ok := s.disk.Peek(childKey); return ok })

	ent, ok := s.disk.Peek(childKey)
	if !ok {
		t.Fatalf("seeded variant missing at %q", childKey)
	}
	// VariantKind carries the whole variant sub-struct through the codec: if it
	// were empty the request headers below would silently vanish.
	if ent.VariantKind != variantKindResponse {
		t.Fatalf("variantKind = %q, want %q", ent.VariantKind, variantKindResponse)
	}
	if ent.VariantFingerprint != "fingerprint-1" {
		t.Fatalf("fingerprint = %q", ent.VariantFingerprint)
	}
	if ent.VariantBaseKey != baseKey {
		t.Fatalf("variantBaseKey = %q, want %q", ent.VariantBaseKey, baseKey)
	}
	if len(ent.VariantValues) != 2 || ent.VariantValues[0] != "mobile" || ent.VariantValues[1] != "CA" {
		t.Fatalf("variantValues = %v", ent.VariantValues)
	}
	if len(ent.VariantRequestHeaders) != 2 {
		t.Fatalf("variantRequestHeaders = %v, want exactly the two persisted headers", ent.VariantRequestHeaders)
	}
	if got := ent.VariantRequestHeaders.Get("X-Device"); got != "mobile" {
		t.Fatalf("replayed X-Device = %q, want mobile", got)
	}
	if got := ent.VariantRequestHeaders.Get("CF-IPCountry"); got != "CA" {
		t.Fatalf("replayed CF-IPCountry = %q, want CA", got)
	}
	if !ent.Inactive {
		t.Fatalf("seeded variant is active")
	}

	children := s.variantChildren(baseKey)
	if len(children) != 1 || children[0] != childKey {
		t.Fatalf("variant family = %v, want [%s]", children, childKey)
	}
	if got := s.disk.SnapshotAccessTimes()[childKey]; got != rec.LastSeen {
		t.Fatalf("last access = %d, want the persisted lastSeen %d", got, rec.LastSeen)
	}
}

func TestURLPersistRuntimeAdapter_SnapshotAccessTimesMergesTiers(t *testing.T) {
	s := newTestService(t, "http://example.com", []Rule{mustRule(t, "PathPrefix(/)")})
	a := newURLPersistRuntimeAdapter(s)

	a.Seed(urlpersist.Record{Path: "/disk-only", LastSeen: 1000})
	a.Seed(urlpersist.Record{Path: "/both", LastSeen: 1000})
	waitForURLPersist(t, func() bool {
		return s.disk.HasKey("/disk-only") && s.disk.HasKey("/both")
	})

	s.ram.Put("/ram-only", CacheEntry{Status: 200, Header: make(http.Header)}, s.disk, s.overflowLog)
	s.ram.Put("/both", CacheEntry{Status: 200, Header: make(http.Header)}, s.disk, s.overflowLog)
	if !s.ram.setLastAccessForTest("/ram-only", 5000) {
		t.Fatalf("setLastAccessForTest(/ram-only) = false")
	}
	if !s.ram.setLastAccessForTest("/both", 9000) {
		t.Fatalf("setLastAccessForTest(/both) = false")
	}

	access := a.SnapshotAccessTimes()
	if got := access["/disk-only"]; got != 1000 {
		t.Fatalf("disk-only access = %d, want 1000", got)
	}
	if got := access["/ram-only"]; got != 5000 {
		t.Fatalf("ram-only access = %d, want 5000", got)
	}
	// The newer RAM stamp wins over the older disk one.
	if got := access["/both"]; got != 9000 {
		t.Fatalf("merged access = %d, want the newer RAM stamp 9000", got)
	}
}

func TestURLPersistHooks_RecordsOnlyCacheableStores(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/boom":
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		case "/nostore":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cache-Control", "no-store")
		case "/json":
			w.Header().Set("Content-Type", "application/json")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	bypass := mustRule(t, "PathPrefix(/private)")
	bypass.Bypass = true
	rules := []Rule{bypass, mustRule(t, "PathPrefix(/)")}

	tests := []struct {
		name        string
		method      string
		path        string
		wantWait0   string
		wantRecords int
	}{
		{name: "cacheable html get", method: http.MethodGet, path: "/page", wantWait0: "miss", wantRecords: 1},
		{name: "bypass rule", method: http.MethodGet, path: "/private/page", wantWait0: "bypass", wantRecords: 0},
		{name: "non get method", method: http.MethodPost, path: "/page", wantWait0: "bypass", wantRecords: 0},
		{name: "error status", method: http.MethodGet, path: "/boom", wantWait0: "ignore-by-status", wantRecords: 0},
		{name: "no-store response", method: http.MethodGet, path: "/nostore", wantWait0: "bypass", wantRecords: 0},
		{name: "non cachable content type", method: http.MethodGet, path: "/json", wantWait0: "bypass", wantRecords: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newURLPersistHarness(t, origin.URL, rules, 3)

			resp := h.request(t, tc.method, tc.path, nil)
			if got := resp.Header.Get("X-Wait0"); got != tc.wantWait0 {
				t.Fatalf("X-Wait0 = %q, want %q", got, tc.wantWait0)
			}
			if got := h.ctrl.Stats().Records; got != tc.wantRecords {
				t.Fatalf("records = %d, want %d", got, tc.wantRecords)
			}

			records := h.flushAndRead(t)
			if len(records) != tc.wantRecords {
				t.Fatalf("persisted records = %d, want %d", len(records), tc.wantRecords)
			}
			if tc.wantRecords == 0 {
				return
			}
			rec := records[0]
			if rec.Path != tc.path || rec.Query != "" {
				t.Fatalf("record identity = %q/%q, want %q", rec.Path, rec.Query, tc.path)
			}
			if rec.DiscoveredBy != "user" {
				t.Fatalf("discoveredBy = %q, want user", rec.DiscoveredBy)
			}
			if rec.Variant != nil {
				t.Fatalf("variant = %+v, want nil for an unvaried response", rec.Variant)
			}
			if rec.Seen < 1 || rec.LastSeen <= 0 {
				t.Fatalf("seen/lastSeen = %d/%d, want both set", rec.Seen, rec.LastSeen)
			}
			if rec.Failures != 0 {
				t.Fatalf("failures = %d, want 0", rec.Failures)
			}
		})
	}
}

func TestURLPersistHooks_RecordsQueryAwareKey(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.VaryByQueryParams = []string{"page"}
	h := newURLPersistHarness(t, origin.URL, []Rule{rule}, 3)

	if got := h.get(t, "/list?page=2&utm=ignored").Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("X-Wait0 = %q, want miss", got)
	}

	records := h.flushAndRead(t)
	if len(records) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(records))
	}
	if records[0].Path != "/list" || records[0].Query != "page=2" {
		t.Fatalf("record identity = %q/%q, want /list and page=2", records[0].Path, records[0].Query)
	}
	if got := records[0].toRecord().CacheKey(); got != "/list?page=2" {
		t.Fatalf("record cache key = %q, want /list?page=2", got)
	}
}

func TestURLPersistHooks_RecordsVariantHeaderProjection(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Variant", `header('X-Device')`)
		fmt.Fprint(w, "variant body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 3)

	resp := h.request(t, http.MethodGet, "/vary", http.Header{
		"X-Device": {"mobile"},
		"X-Noise":  {"not-referenced"},
	})
	if got := resp.Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("X-Wait0 = %q, want miss", got)
	}
	if got := h.ctrl.Stats().Records; got != 1 {
		t.Fatalf("records = %d, want 1", got)
	}

	records := h.flushAndRead(t)
	if len(records) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.Path != "/vary" {
		t.Fatalf("path = %q, want /vary", rec.Path)
	}
	if rec.Variant == nil {
		t.Fatalf("variant = nil, want the projected variant")
	}
	if rec.Variant.Fingerprint == "" {
		t.Fatalf("variant fingerprint is empty")
	}
	if len(rec.Variant.Values) != 1 || rec.Variant.Values[0] != "mobile" {
		t.Fatalf("variant values = %v, want [mobile]", rec.Variant.Values)
	}
	// Only the header the expression references is persisted, which is what
	// makes a re-fetch replayable without storing the whole request.
	if len(rec.Variant.Headers) != 1 {
		t.Fatalf("variant headers = %v, want only the referenced header", rec.Variant.Headers)
	}
	if got := rec.Variant.Headers["X-Device"]; len(got) != 1 || got[0] != "mobile" {
		t.Fatalf("variant header X-Device = %v, want [mobile]", got)
	}

	// The persisted identity must resolve back to the live variant child key.
	children := h.s.variantChildren("/vary")
	if len(children) != 1 {
		t.Fatalf("variant family = %v, want one child", children)
	}
	if got := rec.toRecord().CacheKey(); got != children[0] {
		t.Fatalf("record cache key = %q, want the cached child key %q", got, children[0])
	}
}

// An origin that stops emitting Cache-Variant collapses the whole family in the
// cache, so the persisted children of that family are dead identities too.
func TestURLPersistHooks_PlainStoreDropsSupersededVariantRecords(t *testing.T) {

	var varied atomic.Bool
	varied.Store(true)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if varied.Load() {
			w.Header().Set("Cache-Variant", `header('X-Device')`)
		}
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 3)

	h.request(t, http.MethodGet, "/vary", http.Header{"X-Device": {"mobile"}})
	children := h.s.variantChildren("/vary")
	if len(children) != 1 {
		t.Fatalf("variant family = %v, want one child", children)
	}

	// Evicting the child forces the next request back to the origin, which by
	// then no longer varies the response.
	dropURLPersistCacheEntry(t, h.s, children[0])
	varied.Store(false)
	if got := h.request(t, http.MethodGet, "/vary", http.Header{"X-Device": {"mobile"}}).Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("X-Wait0 = %q, want miss", got)
	}
	if got := h.s.variantChildren("/vary"); len(got) != 0 {
		t.Fatalf("variant family = %v, want it torn down", got)
	}

	if got := h.ctrl.Stats().Records; got != 1 {
		t.Fatalf("records = %d, want only the unvaried record", got)
	}
	records := h.flushAndRead(t)
	if len(records) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(records))
	}
	if records[0].Variant != nil {
		t.Fatalf("surviving record = %+v, want the unvaried one", records[0].Variant)
	}
}

func TestURLPersistHooks_DeleteIncrementsFailures(t *testing.T) {
	var fail atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 3)

	if got := h.get(t, "/page").Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("first X-Wait0 = %q, want miss", got)
	}
	if got := h.ctrl.Stats().Records; got != 1 {
		t.Fatalf("records after store = %d, want 1", got)
	}

	// Drop the cached copies without touching the persister so the next request
	// reaches the origin instead of being served as a hit.
	dropURLPersistCacheEntry(t, h.s, "/page")

	fail.Store(true)
	if got := h.get(t, "/page").Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("second X-Wait0 = %q, want ignore-by-status", got)
	}
	if got := h.ctrl.Stats().Records; got != 1 {
		t.Fatalf("records after one failure = %d, want the record kept", got)
	}

	records := h.flushAndRead(t)
	if len(records) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(records))
	}
	if records[0].Failures != 1 {
		t.Fatalf("failures = %d, want 1", records[0].Failures)
	}
}

func TestURLPersistHooks_DeleteForgetsRecordAtThreshold(t *testing.T) {
	var fail atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 1)

	h.get(t, "/page")
	if got := h.ctrl.Stats().Records; got != 1 {
		t.Fatalf("records after store = %d, want 1", got)
	}
	dropURLPersistCacheEntry(t, h.s, "/page")

	fail.Store(true)
	if got := h.get(t, "/page").Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-status", got)
	}
	if got := h.ctrl.Stats().Records; got != 0 {
		t.Fatalf("records after reaching the threshold = %d, want 0", got)
	}
	if records := h.flushAndRead(t); len(records) != 0 {
		t.Fatalf("persisted records = %d, want 0", len(records))
	}
}

// Deleting a base key tears down a whole variant family, so every child record
// under it must be failed.
func TestURLPersistHooks_DeleteOfBaseKeyFailsVariantFamily(t *testing.T) {
	var fail atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Variant", `header('X-Device')`)
		fmt.Fprint(w, "variant body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 1)

	for _, device := range []string{"mobile", "desktop"} {
		resp := h.request(t, http.MethodGet, "/vary", http.Header{"X-Device": {device}})
		if got := resp.Header.Get("X-Wait0"); got != "miss" {
			t.Fatalf("%s X-Wait0 = %q, want miss", device, got)
		}
	}
	if got := h.ctrl.Stats().Records; got != 2 {
		t.Fatalf("records = %d, want one per variant", got)
	}

	// Evicting only the manifest makes the next request a plain miss on the base
	// key, so the failure arrives addressed to the base rather than a child.
	dropURLPersistCacheEntry(t, h.s, "/vary")

	fail.Store(true)
	if got := h.get(t, "/vary").Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-status", got)
	}
	if got := h.ctrl.Stats().Records; got != 0 {
		t.Fatalf("records after the family failed = %d, want 0", got)
	}
}

func TestURLPersistHooks_StoreResetsFailures(t *testing.T) {
	var fail atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	h := newURLPersistHarness(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")}, 3)

	h.get(t, "/page")
	dropURLPersistCacheEntry(t, h.s, "/page")

	fail.Store(true)
	h.get(t, "/page")

	fail.Store(false)
	if got := h.get(t, "/page").Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("recovery X-Wait0 = %q, want miss", got)
	}

	records := h.flushAndRead(t)
	if len(records) != 1 {
		t.Fatalf("persisted records = %d, want 1", len(records))
	}
	if records[0].Failures != 0 {
		t.Fatalf("failures = %d, want the successful store to reset the count", records[0].Failures)
	}
}

func TestURLPersistHooks_NoopWhenPersisterDisabled(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Variant", `header('X-Device')`)
		fmt.Fprint(w, "body")
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")})
	if s.urlp != nil {
		t.Fatalf("newTestService configured a persister; this test covers the nil case")
	}

	// Direct hook calls, including the variant shape, must be inert.
	s.notePersistedStore("/a", "/a", CacheEntry{DiscoveredBy: "user"})
	s.notePersistedStore("child", "/a", CacheEntry{
		VariantKind:           variantKindResponse,
		VariantFingerprint:    "fp",
		VariantValues:         []string{"mobile"},
		VariantRequestHeaders: http.Header{"X-Device": {"mobile"}},
	})
	s.notePersistedDelete("/a")
	s.notePersistedDelete("")

	// And so must the same hooks reached through a real request.
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	req.Header.Set("X-Device", "mobile")
	w := httptest.NewRecorder()
	s.proxy.Handle(w, req)
	if got := w.Result().Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("X-Wait0 = %q, want miss", got)
	}
	s.deleteCacheKey("/page")
}

// dropURLPersistCacheEntry removes a key from both cache tiers directly, which
// is the one path that does not notify the persister.
func dropURLPersistCacheEntry(t *testing.T, s *Service, key string) {
	t.Helper()
	s.ram.Delete(key)
	s.disk.Delete(key)
	waitForURLPersist(t, func() bool { return !s.disk.HasKey(key) })
}
