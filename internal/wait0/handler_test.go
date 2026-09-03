package wait0

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"wait0/internal/wait0/auth"
	"wait0/internal/wait0/dashboard"
	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/statapi"
)

func TestHandle_CacheMissThenHit(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})

	req1 := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	w1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w1, req1)
	if got := w1.Result().Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("first request X-Wait0 = %q, want miss", got)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if got := w2.Result().Header.Get("X-Wait0"); got != "hit" {
		t.Fatalf("second request X-Wait0 = %q, want hit", got)
	}

	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestHandle_PrivateCookieResponseIsNeverShared(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		if cookie, err := r.Cookie("session"); err == nil && cookie.Value == "alice-secret" {
			w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
			w.Header().Set("Vary", "Cookie")
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "alice-secret", Path: "/", HttpOnly: true})
			fmt.Fprint(w, "signed in as alice")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		fmt.Fprint(w, "anonymous")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})

	aliceRequest := httptest.NewRequest(http.MethodGet, "http://wait0.local/account", nil)
	aliceRequest.AddCookie(&http.Cookie{Name: "session", Value: "alice-secret"})
	aliceResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(aliceResponse, aliceRequest)
	if got := aliceResponse.Result().Header.Get("X-Wait0"); got != "bypass" {
		t.Fatalf("Alice X-Wait0 = %q, want bypass", got)
	}
	if got := aliceResponse.Result().Header.Get("X-Wait0-Reason"); got != proxy.CacheabilityCacheControl {
		t.Fatalf("Alice X-Wait0-Reason = %q, want %q", got, proxy.CacheabilityCacheControl)
	}

	malloryResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(malloryResponse, httptest.NewRequest(http.MethodGet, "http://wait0.local/account", nil))
	if got := malloryResponse.Result().Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("anonymous X-Wait0 = %q, want miss", got)
	}
	if body := malloryResponse.Body.String(); body != "anonymous" {
		t.Fatalf("anonymous body = %q", body)
	}
	if cookies := malloryResponse.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("anonymous response replayed cookies: %v", cookies)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("origin hits = %d, want 2", got)
	}
}

func TestHandle_UnsafeLegacyEntryIsEvictedBeforeServing(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "anonymous")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})
	s.ram.Put("/account", CacheEntry{
		Status: http.StatusOK,
		Header: http.Header{
			"Cache-Control": {"private"},
			"Content-Type":  {"text/html"},
			"Set-Cookie":    {"session=alice-secret; Path=/; HttpOnly"},
		},
		Body: []byte("signed in as alice"),
	}, s.disk, s.overflowLog)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://wait0.local/account", nil))
	if got := w.Result().Header.Get("X-Wait0"); got != "miss" {
		t.Fatalf("X-Wait0 = %q, want miss", got)
	}
	if body := w.Body.String(); body != "anonymous" {
		t.Fatalf("body = %q", body)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("legacy cookie was replayed: %v", cookies)
	}
}

func TestHandle_OriginRedirectIsNotFollowed(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		fmt.Fprint(w, "internal secret")
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/redirect", nil)
	req.Header.Set("Cookie", "session=victim-secret")
	req.Header.Set("Authorization", "Bearer victim-secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if got := w.Result().StatusCode; got != http.StatusFound {
		t.Fatalf("status = %d, want %d", got, http.StatusFound)
	}
	if got := w.Result().Header.Get("Location"); got != target.URL+"/internal" {
		t.Fatalf("Location = %q", got)
	}
	if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-status", got)
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0", got)
	}
}

func TestHandle_CacheVariantsSplitAndHit(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		device := "desktop"
		if strings.Contains(r.UserAgent(), "iPhone") || strings.Contains(r.UserAgent(), "Android Mobile") {
			device = "mobile"
		}
		country := r.Header.Get("CF-IPCountry")
		if country == "" {
			country = "XX"
		}
		if country == "CA" && r.Header.Get("CF-Region-Code") == "ON" {
			country = "CA-ON"
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Add("Cache-Variant", `"header('User-Agent') matches '(?i)(Android.*Mobile|iPhone|iPod|IEMobile|Windows Phone|Opera Mini)' ? 'mobile' : 'desktop'"`)
		w.Header().Add("Cache-Variant", `"let c = header('CF-IPCountry', 'XX'); c == 'CA' && header('CF-Region-Code') == 'ON' ? 'CA-ON' : c"`)
		fmt.Fprintf(w, "%s|%s", device, country)
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")})

	type requestCase struct {
		ua      string
		country string
		region  string
		wait0   string
		key     string
	}
	cases := []requestCase{
		{ua: "Mozilla/5.0 (iPhone)", country: "CA", region: "ON", wait0: "miss", key: "mobile|CA-ON"},
		{ua: "Mozilla/5.0 (X11; Linux)", country: "CA", region: "ON", wait0: "miss", key: "desktop|CA-ON"},
		{ua: "Android Mobile", country: "US", wait0: "miss", key: "mobile|US"},
		{ua: "Mozilla/5.0 (iPhone)", country: "CA", region: "ON", wait0: "hit", key: "mobile|CA-ON"},
	}
	for i, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/a", nil)
		req.Header.Set("User-Agent", tc.ua)
		req.Header.Set("CF-IPCountry", tc.country)
		if tc.region != "" {
			req.Header.Set("CF-Region-Code", tc.region)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if got := w.Result().Header.Get("X-Wait0"); got != tc.wait0 {
			t.Fatalf("request %d X-Wait0 = %q, want %q", i+1, got, tc.wait0)
		}
		if got := w.Result().Header.Get("X-Wait0-Cache-Variant-Key"); got != tc.key {
			t.Fatalf("request %d variant key = %q, want %q", i+1, got, tc.key)
		}
		if got := strings.TrimSpace(w.Body.String()); got != tc.key {
			t.Fatalf("request %d body = %q, want %q", i+1, got, tc.key)
		}
		if got := len(w.Result().Header.Values("Cache-Variant")); got != 2 {
			t.Fatalf("request %d Cache-Variant values = %d, want 2", i+1, got)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("origin hits = %d, want 3", got)
	}
	if got := len(s.variantChildren("/a")); got != 3 {
		t.Fatalf("variant children = %d, want 3", got)
	}
}

func TestHandle_CacheVariantExpressionErrorBypasses(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Variant", `header('X-Required')`)
		fmt.Fprint(w, "uncached")
	}))
	defer origin.Close()

	s := newTestService(t, origin.URL, []Rule{mustRule(t, "PathPrefix(/)")})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://wait0.local/a", nil))
		if got := w.Result().Header.Get("X-Wait0"); got != "bypass" {
			t.Fatalf("request %d X-Wait0 = %q", i+1, got)
		}
		if got := w.Result().Header.Get("X-Wait0-Reason"); got != "cache-variant-expression-error" {
			t.Fatalf("request %d reason = %q", i+1, got)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("origin hits = %d, want 2", hits.Load())
	}
	if _, ok := s.peekCacheEntry("/a"); ok {
		t.Fatal("invalid expression response was cached")
	}
}

func TestHandle_ExplicitEmptyDebugHeadersEmitsNone(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		for _, name := range proxy.DefaultDebugHeaders() {
			w.Header().Set(name, "origin-spoof")
		}
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})
	s.debugHeaders = proxy.NewDebugHeaderSet([]string{})

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil))

	if got := w.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	for _, name := range proxy.DefaultDebugHeaders() {
		if got := w.Result().Header.Get(name); got != "" {
			t.Fatalf("disabled debug header %s = %q", name, got)
		}
	}
}

func TestHandle_NonCachableContentTypeByDefault(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/data", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if got := w.Result().Header.Get("X-Wait0"); got != "bypass" {
			t.Fatalf("request %d X-Wait0 = %q, want bypass", i+1, got)
		}
		if got := w.Result().Header.Get("X-Wait0-Reason"); got != "non-cacheable-content-type" {
			t.Fatalf("request %d X-Wait0-Reason = %q", i+1, got)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("origin hits = %d, want 2", got)
	}
}

func TestHandle_CustomCachableContentType(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.CachableContentTypes = []string{"application/json"}
	s := newTestService(t, origin.URL, []Rule{rule})

	for i, want := range []string{"miss", "hit"} {
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/data", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if got := w.Result().Header.Get("X-Wait0"); got != want {
			t.Fatalf("request %d X-Wait0 = %q, want %q", i+1, got, want)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestHandle_BypassWhenCookiePresent(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.BypassWhenCookies = []string{"sessionid"}
	s := newTestService(t, origin.URL, []Rule{rule})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
	req.AddCookie(&http.Cookie{Name: "sessionid", Value: "abc"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-cookie" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-cookie", got)
	}
	if got := w.Result().Header.Get("X-Wait0-Reason"); got != "bypass-cookie" {
		t.Fatalf("X-Wait0-Reason = %q, want bypass-cookie", got)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("origin hits = %d, want 1", got)
	}
}

func TestHandle_BypassWhenRequestHeaderPresent(t *testing.T) {
	var hits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("origin Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "ok")
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	rule.BypassWhenRequestHeaders = []string{"Authorization"}
	s := newTestService(t, origin.URL, []Rule{rule})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)
		req.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)

		if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-request-header" {
			t.Fatalf("request %d X-Wait0 = %q", i+1, got)
		}
		if got := w.Result().Header.Get("X-Wait0-Reason"); got != "bypass-request-header" {
			t.Fatalf("request %d X-Wait0-Reason = %q", i+1, got)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("origin hits = %d, want 2 uncached requests", got)
	}
	if _, ok := s.peekCacheEntry("/page"); ok {
		t.Fatal("header-bypassed response was cached")
	}
}

func TestHandle_IgnoreByStatusInvalidatesRAM(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	defer origin.Close()

	rule := mustRule(t, "PathPrefix(/)")
	s := newTestService(t, origin.URL, []Rule{rule})
	s.ram.Put("/broken", CacheEntry{Status: 200, Header: make(http.Header), Body: []byte("cached"), Inactive: true}, s.disk, s.overflowLog)

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/broken", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Wait0"); got != "ignore-by-status" {
		t.Fatalf("X-Wait0 = %q, want ignore-by-status", got)
	}
	if _, ok := s.ram.Peek("/broken"); ok {
		t.Fatalf("expected RAM cache entry to be deleted")
	}
}

func TestHasAnyCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "1"})

	if !proxy.HasAnyCookie(req, []string{"session"}) {
		t.Fatalf("expected cookie match")
	}
	if proxy.HasAnyCookie(req, []string{"other"}) {
		t.Fatalf("did not expect cookie match")
	}
}

func TestHandle_StatsEndpoint(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	s.invAuth = auth.NewAuthenticator([]auth.TokenConfig{{ID: "stats", Token: "secret", Scopes: []string{statapi.ReadScope}}})
	s.stat = statapi.NewController(s.invAuth, newStatsRuntimeAdapter(s))
	s.ram.Put("/page", CacheEntry{Status: 200, Header: http.Header{"X-Test": {"v"}}, Body: []byte("ok"), StoredAt: 1}, s.disk, s.overflowLog)

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0", nil)
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Result().StatusCode)
	}
}

func TestHandle_DashboardEndpoint(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	s.dash = dashboard.NewController(
		dashboard.Config{
			Username:         "ops",
			Password:         "secret",
			StatsBearerToken: "stats-token",
		},
		dashboard.Runtime{
			StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"cache":{"urls_total":1}}`))
			}),
		},
	)

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0/dashboard", nil)
	req.SetBasicAuth("ops", "secret")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Result().StatusCode)
	}
}
