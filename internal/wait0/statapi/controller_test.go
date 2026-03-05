package statapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"wait0/internal/wait0/auth"
)

type fakeRuntime struct {
	ram  map[string]EntryMeta
	disk map[string]EntryMeta
	dur  MetricTriplet
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
			"/a": {Size: 100, LastRefreshUnixNano: now.Add(-10 * time.Second).UnixNano(), DiscoveredBy: "sitemap", Inactive: false},
			"/b": {Size: 300, LastRefreshUnixNano: now.Add(-20 * time.Second).UnixNano(), DiscoveredBy: "sitemap", Inactive: true},
		},
		disk: map[string]EntryMeta{
			"/b": {Size: 999, LastRefreshUnixNano: now.Add(-1 * time.Second).UnixNano()},
			"/c": {Size: 500, LastRefreshUnixNano: now.Add(-30 * time.Second).UnixNano(), DiscoveredBy: "user"},
		},
		dur: MetricTriplet{Min: 19, Avg: 66, Max: 119},
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
