package wait0

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnvBool(t *testing.T) {
	const key = "WAIT0_TEST_BOOL"
	defer os.Unsetenv(key)

	if got := envBool(key, true); !got {
		t.Fatalf("expected default true")
	}
	if err := os.Setenv(key, "false"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if got := envBool(key, true); got {
		t.Fatalf("expected false")
	}
	if err := os.Setenv(key, "not-bool"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	if got := envBool(key, true); !got {
		t.Fatalf("expected fallback default true")
	}
}

func TestOriginHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
	}))
	defer origin.Close()

	req, err := http.NewRequest(http.MethodGet, origin.URL+"/redirect", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Cookie", "session=victim-secret")
	req.Header.Set("Authorization", "Bearer victim-secret")
	resp, err := newOriginHTTPClient(2 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
	if got := resp.Header.Get("Location"); got != target.URL+"/internal" {
		t.Fatalf("Location = %q", got)
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0", got)
	}
}

func TestNewService_Close_Handler(t *testing.T) {
	cfg := Config{}
	cfg.Storage.RAM.Max = "2m"
	cfg.Storage.Disk.Max = "8m"
	cfg.Server.Origin = "http://localhost:3000"

	s, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if s.Handler() == nil {
		t.Fatalf("expected non-nil handler")
	}
	s.Close()
}

func TestStartWarmupGroups_StopsOnClose(t *testing.T) {
	rule := mustRule(t, "PathPrefix(/)")
	rule.warmPause = time.Millisecond
	rule.warmMax = 1

	s := newTestService(t, "http://invalid.local", []Rule{rule})
	s.cfg.Rules = []Rule{rule}

	s.startWarmupGroups()
	stopTestService(s)
	s.wg.Wait()
}

func TestResolveAuthTokenByScope(t *testing.T) {
	tokens := []AuthTokenConfig{
		{ID: "read", Token: "tok-read", Scopes: []string{"stats:read"}},
		{ID: "write", Token: "tok-write", Scopes: []string{"invalidation:write"}},
		{ID: "both", Token: "tok-both", Scopes: []string{"stats:read", "invalidation:write"}},
	}

	id, tok, ok := resolveAuthTokenByScope(tokens, "stats:read")
	if !ok {
		t.Fatal("expected stats scope token")
	}
	if id != "read" || tok != "tok-read" {
		t.Fatalf("got id=%q tok=%q", id, tok)
	}

	id, tok, ok = resolveAuthTokenByScope(tokens, "invalidation:write")
	if !ok {
		t.Fatal("expected invalidation scope token")
	}
	if id != "write" || tok != "tok-write" {
		t.Fatalf("got id=%q tok=%q", id, tok)
	}

	if _, _, ok := resolveAuthTokenByScope(tokens, "unknown:scope"); ok {
		t.Fatal("did not expect token for unknown scope")
	}
}
