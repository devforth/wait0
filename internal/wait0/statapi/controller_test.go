package statapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"wait0/internal/wait0/auth"
	"wait0/internal/wait0/proxy"
)

type fakeRuntime struct {
	ram     map[string]EntryMeta
	disk    map[string]EntryMeta
	dur     MetricTriplet
	rules   []RuleDefinition
	warmups map[int]WarmupLoopSnapshot
}

func (f *fakeRuntime) RAMMetaSnapshot() map[string]EntryMeta {
	out := make(map[string]EntryMeta, len(f.ram))
	for k, v := range f.ram {
		out[k] = v
	}
	return out
}

func (f *fakeRuntime) DiskMetaSnapshot() map[string]EntryMeta {
	out := make(map[string]EntryMeta, len(f.disk))
	for k, v := range f.disk {
		out[k] = v
	}
	return out
}

func (f *fakeRuntime) RefreshDurationStatsMillis() MetricTriplet {
	return f.dur
}

func (f *fakeRuntime) RuleDefinitions() []RuleDefinition {
	return append([]RuleDefinition(nil), f.rules...)
}

func (f *fakeRuntime) WarmupLoopSnapshots() map[int]WarmupLoopSnapshot {
	out := make(map[int]WarmupLoopSnapshot, len(f.warmups))
	for id, snapshot := range f.warmups {
		out[id] = snapshot
	}
	return out
}

func TestIsEndpointPath(t *testing.T) {
	if !IsEndpointPath("/wait0") {
		t.Fatal("expected /wait0 to match")
	}
	if !IsEndpointPath("/wait0/") {
		t.Fatal("expected /wait0/ to match")
	}
	if IsEndpointPath("/wait0/x") {
		t.Fatal("expected /wait0/x not to match")
	}
}

func TestHandle_AuthAndMethod(t *testing.T) {
	authn := auth.NewAuthenticator([]auth.TokenConfig{{ID: "reader", Token: "tok", Scopes: []string{ReadScope}}})
	ctrl := NewController(authn, &fakeRuntime{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://wait0.local/wait0", nil)
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	req.Header.Set("Authorization", "Bearer tok")
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	noScope := NewController(auth.NewAuthenticator([]auth.TokenConfig{{ID: "x", Token: "t2", Scopes: []string{"invalidation:write"}}}), &fakeRuntime{})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	req.Header.Set("Authorization", "Bearer t2")
	noScope.Handle(w, req)
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}
}

func TestHandle_MetricsPayload(t *testing.T) {
	now := time.Now().UTC()
	authn := auth.NewAuthenticator([]auth.TokenConfig{{ID: "reader", Token: "tok", Scopes: []string{ReadScope}}})
	ctrl := NewController(authn, &fakeRuntime{
		ram: map[string]EntryMeta{
			"/a": {Size: 100, StorageSize: 110, LastRefreshUnixNano: now.Add(-10 * time.Second).UnixNano(), DiscoveredBy: "sitemap", Inactive: false},
			"/b": {Size: 300, StorageSize: 310, LastRefreshUnixNano: now.Add(-20 * time.Second).UnixNano(), DiscoveredBy: "sitemap", Inactive: true},
		},
		disk: map[string]EntryMeta{
			"/b": {Size: 999, StorageSize: 1009, LastRefreshUnixNano: now.Add(-1 * time.Second).UnixNano()},
			"/c": {Size: 500, StorageSize: 510, LastRefreshUnixNano: now.Add(-30 * time.Second).UnixNano(), DiscoveredBy: "user"},
		},
		dur: MetricTriplet{Min: 19, Avg: 66, Max: 119},
		rules: []RuleDefinition{
			{ID: 0, Match: "PathPrefix(/)", Priority: 2, WarmupConfigured: true, PauseBetweenRuns: 10 * time.Second, Matches: func(path string) bool { return strings.HasPrefix(path, "/") }},
			{ID: 1, Match: "PathPrefix(/none)", Priority: 3, Matches: func(path string) bool { return strings.HasPrefix(path, "/none") }},
		},
		warmups: map[int]WarmupLoopSnapshot{
			0: {
				Duration:   1500 * time.Millisecond,
				FinishedAt: now.Add(-4 * time.Second),
				URLs:       3,
				TopSlowest: []WarmupURLMetric{{URL: "/c", Duration: 120 * time.Millisecond}, {URL: "/a", Duration: 20 * time.Millisecond}},
				TopFastest: []WarmupURLMetric{{URL: "/a", Duration: 20 * time.Millisecond}, {URL: "/c", Duration: 120 * time.Millisecond}},
			},
		},
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d", w.Result().StatusCode)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	cacheObj := resp["cache"].(map[string]any)
	if int(cacheObj["urls_total"].(float64)) != 3 {
		t.Fatalf("urls_total=%v", cacheObj["urls_total"])
	}
	if uint64(cacheObj["responses_size_bytes_total"].(float64)) != 900 {
		t.Fatalf("responses_size_bytes_total=%v", cacheObj["responses_size_bytes_total"])
	}

	sitemapObj := resp["sitemap"].(map[string]any)
	if int(sitemapObj["discovered_urls"].(float64)) != 2 {
		t.Fatalf("discovered_urls=%v", sitemapObj["discovered_urls"])
	}
	if int(sitemapObj["crawled_urls"].(float64)) != 1 {
		t.Fatalf("crawled_urls=%v", sitemapObj["crawled_urls"])
	}
	if sitemapObj["crawl_percentage"].(float64) < 49.9 || sitemapObj["crawl_percentage"].(float64) > 50.1 {
		t.Fatalf("crawl_percentage=%v", sitemapObj["crawl_percentage"])
	}

	durObj := resp["refresh_duration_ms"].(map[string]any)
	if uint64(durObj["min"].(float64)) != 19 || uint64(durObj["avg"].(float64)) != 66 || uint64(durObj["max"].(float64)) != 119 {
		t.Fatalf("refresh_duration_ms=%v", durObj)
	}

	rules := resp["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rules=%v", rules)
	}
	rootRule := rules[0].(map[string]any)
	if int(rootRule["urls"].(float64)) != 3 || int(rootRule["responses"].(float64)) != 2 {
		t.Fatalf("root rule counts=%v", rootRule)
	}
	if uint64(rootRule["ram_size_bytes"].(float64)) != 420 || uint64(rootRule["disk_size_bytes"].(float64)) != 1519 {
		t.Fatalf("root rule tier sizes=%v", rootRule)
	}
	largest := rootRule["top_largest_responses"].([]any)
	smallest := rootRule["top_smallest_responses"].([]any)
	if largest[0].(map[string]any)["url"] != "/c" || smallest[0].(map[string]any)["url"] != "/a" {
		t.Fatalf("unexpected response rankings: largest=%v smallest=%v", largest, smallest)
	}
	warmup := rootRule["warmup"].(map[string]any)
	if warmup["configured"] != true || uint64(warmup["last_loop_duration_ms"].(float64)) != 1500 {
		t.Fatalf("root rule warmup=%v", warmup)
	}
	if len(warmup["top_slowest_urls"].([]any)) != 2 || len(warmup["top_fastest_urls"].([]any)) != 2 {
		t.Fatalf("root rule warmup rankings=%v", warmup)
	}

	emptyRule := rules[1].(map[string]any)
	if int(emptyRule["urls"].(float64)) != 0 || emptyRule["warmup"].(map[string]any)["configured"] != false {
		t.Fatalf("empty rule=%v", emptyRule)
	}
	if len(emptyRule["top_largest_responses"].([]any)) != 0 {
		t.Fatalf("empty rule should have empty response rankings: %v", emptyRule)
	}
}

func TestBuildSnapshot_FoldsVariantChildrenIntoOneLogicalURL(t *testing.T) {
	childMobile := proxy.JoinVariantCacheKey("/a", "generation", []string{"mobile", "CA"})
	childDesktop := proxy.JoinVariantCacheKey("/a", "generation", []string{"desktop", "CA"})
	rt := &fakeRuntime{
		ram: map[string]EntryMeta{
			"/a":        {Size: 25, StorageSize: 50, VariantKind: "manifest"},
			childMobile: {Size: 100, StorageSize: 120, VariantKind: "response", VariantBaseKey: "/a"},
		},
		disk: map[string]EntryMeta{
			childDesktop: {Size: 200, StorageSize: 220, VariantKind: "response", VariantBaseKey: "/a"},
		},
		rules: []RuleDefinition{{
			ID:      0,
			Match:   "PathPrefix(/)",
			Matches: func(path string) bool { return strings.HasPrefix(path, "/") },
		}},
	}

	got := NewController(nil, rt).buildSnapshot(time.Now().UTC())
	if got.Cache.URLsTotal != 1 {
		t.Fatalf("urls_total = %d, want 1 logical root", got.Cache.URLsTotal)
	}
	if got.Cache.ResponsesSizeBytesTotal != 300 {
		t.Fatalf("response bytes = %d, want concrete children only", got.Cache.ResponsesSizeBytesTotal)
	}
	if got.Cache.ResponseSizeBytes != (MetricTriplet{Min: 100, Avg: 150, Max: 200}) {
		t.Fatalf("response sizes = %+v", got.Cache.ResponseSizeBytes)
	}
	if got.Rules[0].URLs != 1 || got.Rules[0].Responses != 2 {
		t.Fatalf("rule counts = urls:%d responses:%d", got.Rules[0].URLs, got.Rules[0].Responses)
	}
}

func TestHandle_UsesSnapshotCacheWithinTTL(t *testing.T) {
	authn := auth.NewAuthenticator([]auth.TokenConfig{{ID: "reader", Token: "tok", Scopes: []string{ReadScope}}})
	rt := &fakeRuntime{ram: map[string]EntryMeta{"/a": {Size: 10, LastRefreshUnixNano: time.Now().Add(-1 * time.Second).UnixNano()}}}
	ctrl := NewController(authn, rt)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	req.Header.Set("Authorization", "Bearer tok")
	ctrl.Handle(w, req)

	rt.ram["/b"] = EntryMeta{Size: 20, LastRefreshUnixNano: time.Now().Add(-1 * time.Second).UnixNano()}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	req2.Header.Set("Authorization", "Bearer tok")
	ctrl.Handle(w2, req2)

	var resp map[string]any
	if err := json.NewDecoder(w2.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cacheObj := resp["cache"].(map[string]any)
	if int(cacheObj["urls_total"].(float64)) != 1 {
		t.Fatalf("expected cached snapshot to keep urls_total=1, got %v", cacheObj["urls_total"])
	}
}

func TestTopResponseSizes_KeepsTenInOrder(t *testing.T) {
	var largest []urlSizePayload
	var smallest []urlSizePayload
	for i := 0; i < 1000; i++ {
		item := urlSizePayload{URL: fmt.Sprintf("/%02d", i), SizeBytes: uint64(i + 1)}
		largest = addResponseSize(largest, item, false)
		smallest = addResponseSize(smallest, item, true)
		if len(largest) > maxRankedURLs || cap(largest) > maxRankedURLs {
			t.Fatalf("largest retained more than %d entries: len=%d cap=%d", maxRankedURLs, len(largest), cap(largest))
		}
		if len(smallest) > maxRankedURLs || cap(smallest) > maxRankedURLs {
			t.Fatalf("smallest retained more than %d entries: len=%d cap=%d", maxRankedURLs, len(smallest), cap(smallest))
		}
	}

	if len(largest) != 10 || largest[0].SizeBytes != 1000 || largest[9].SizeBytes != 991 {
		t.Fatalf("largest = %v", largest)
	}
	if len(smallest) != 10 || smallest[0].SizeBytes != 1 || smallest[9].SizeBytes != 10 {
		t.Fatalf("smallest = %v", smallest)
	}
}
