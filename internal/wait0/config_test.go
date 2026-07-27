package wait0

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig_ValidAndCompiledFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "wait0.yaml")
	yaml := `storage:
  ram:
    max: "64m"
  disk:
    max: "1g"
server:
  port: 8082
  origin: "http://localhost:3000/"
urlsDiscover:
  initalDelay: "2s"
  rediscoverEvery: "1m"
  sitemaps:
    - "/sitemap.xml"
logging:
  log_stats_every: "10s"
rules:
  - match: "PathPrefix(/admin)"
    priority: 2
    bypass: true
  - match: "PathPrefix(/)"
    priority: 1
    cachableContentType: [" Application/JSON; Charset=UTF-8 ", "application/json"]
    varyByQueryParams: [" page ", "lang", "page"]
    expiration: "30s"
    warmUp:
      pauseBetweenRuns: "1m"
      maxRequestsAtATime: 3
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Server.Origin != "http://localhost:3000" {
		t.Fatalf("origin = %q", cfg.Server.Origin)
	}
	if cfg.Logging.logStatsEveryDur != 10*time.Second {
		t.Fatalf("logStatsEveryDur = %s", cfg.Logging.logStatsEveryDur)
	}
	if cfg.URLsDiscover.initialDelayDur != 2*time.Second {
		t.Fatalf("initialDelayDur = %s", cfg.URLsDiscover.initialDelayDur)
	}
	if cfg.URLsDiscover.rediscoverEveryDur != time.Minute {
		t.Fatalf("rediscoverEveryDur = %s", cfg.URLsDiscover.rediscoverEveryDur)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("rules = %d", len(cfg.Rules))
	}
	if cfg.Rules[0].Priority != 1 {
		t.Fatalf("rules not sorted by priority")
	}
	if cfg.Rules[0].expDur != 30*time.Second {
		t.Fatalf("expiration = %s", cfg.Rules[0].expDur)
	}
	wantQueryParams := []string{"lang", "page"}
	if len(cfg.Rules[0].VaryByQueryParams) != len(wantQueryParams) {
		t.Fatalf("varyByQueryParams = %v", cfg.Rules[0].VaryByQueryParams)
	}
	for i := range wantQueryParams {
		if cfg.Rules[0].VaryByQueryParams[i] != wantQueryParams[i] {
			t.Fatalf("varyByQueryParams[%d] = %q, want %q", i, cfg.Rules[0].VaryByQueryParams[i], wantQueryParams[i])
		}
	}
	if got := cfg.Rules[0].CachableContentTypes; len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("cachableContentType = %v, want [application/json]", got)
	}
	wantDefaultContentTypes := []string{"text/html", "application/xhtml+xml"}
	if got := cfg.Rules[1].CachableContentTypes; len(got) != len(wantDefaultContentTypes) {
		t.Fatalf("default cachableContentType = %v, want %v", got, wantDefaultContentTypes)
	}
	for i, want := range wantDefaultContentTypes {
		if got := cfg.Rules[1].CachableContentTypes[i]; got != want {
			t.Fatalf("default cachableContentType[%d] = %q, want %q", i, got, want)
		}
	}
	if cfg.Rules[0].warmPause != time.Minute || cfg.Rules[0].warmMax != 3 {
		t.Fatalf("warmup compiled fields not set")
	}
}

func TestLoadConfig_LegacyRunEveryWarnsAndMapsToPause(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
	yaml := `storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
rules:
  - match: "PathPrefix(/)"
    warmUp:
      runEvery: "10s"
      maxRequestsAtATime: 2
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var warning bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&warning)
	defer log.SetOutput(previousOutput)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Rules[0].warmPause; got != 10*time.Second {
		t.Fatalf("warmPause = %s, want 10s", got)
	}
	if got := cfg.Rules[0].WarmUp.PauseBetweenRuns; got != "10s" {
		t.Fatalf("PauseBetweenRuns = %q, want 10s", got)
	}
	logged := warning.String()
	if !strings.Contains(logged, "runEvery is deprecated") || !strings.Contains(logged, "waits this duration after a full loop finishes") {
		t.Fatalf("warning = %q", logged)
	}
}

func TestLoadConfig_Errors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "missing origin", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  port: 8080\nrules: []\n"},
		{name: "bad match", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"BadExpr(/)\"\n"},
		{name: "bad varyByQueryParams", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"PathPrefix(/)\"\n    varyByQueryParams: [\"page\", \" \" ]\n"},
		{name: "bad cachableContentType", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"PathPrefix(/)\"\n    cachableContentType: [\"not a content type\"]\n"},
		{name: "bad warmup", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"PathPrefix(/)\"\n    warmUp:\n      pauseBetweenRuns: \"\"\n      maxRequestsAtATime: 1\n"},
		{name: "both warmup intervals", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"PathPrefix(/)\"\n    warmUp:\n      pauseBetweenRuns: \"10s\"\n      runEvery: \"10s\"\n      maxRequestsAtATime: 1\n"},
		{name: "bad log stats", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nlogging:\n  log_stats_every: \"bad\"\nrules: []\n"},
		{name: "duplicate auth token ids", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\n  invalidation:\n    enabled: true\nauth:\n  tokens:\n    - id: \"dup\"\n      token: \"a\"\n      scopes: [\"invalidation:write\"]\n    - id: \"dup\"\n      token: \"b\"\n      scopes: [\"invalidation:write\"]\nrules: []\n"},
		{name: "invalidation enabled without auth scope", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\n  invalidation:\n    enabled: true\nauth:\n  tokens:\n    - id: \"x\"\n      token: \"t\"\n      scopes: [\"other:scope\"]\nrules: []\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
			if err := os.WriteFile(cfgPath, []byte(tc.yaml), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := LoadConfig(cfgPath); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestLoadConfig_AuthTokenEnvOverride(t *testing.T) {
	t.Setenv("WAIT0_INV_TOKEN", "from-env-token")

	cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
	yaml := strings.TrimSpace(`
storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
  invalidation:
    enabled: true
    queue_size: 4
    worker_concurrency: 2
    max_body_bytes: 2048
    max_paths_per_request: 10
    max_tags_per_request: 10
auth:
  tokens:
    - id: "backoffice"
      token: "from-file"
      token_env: "WAIT0_INV_TOKEN"
      scopes: ["invalidation:write"]
rules: []
`) + "\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Server.Invalidation.Enabled {
		t.Fatalf("expected invalidation enabled")
	}
	if got := cfg.Auth.Tokens[0].Token; got != "from-env-token" {
		t.Fatalf("token = %q, want from-env-token", got)
	}
}

func TestLoadConfig_DebugConfigHasFixedLocalAuthToken(t *testing.T) {
	t.Setenv("WAIT0_API_AUTH_TOKEN", "must-not-override-debug-config")

	cfgPath := filepath.Join("..", "..", "debug", "wait0.yaml")
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig(%q): %v", cfgPath, err)
	}
	if len(cfg.Auth.Tokens) != 1 {
		t.Fatalf("auth token count = %d, want 1", len(cfg.Auth.Tokens))
	}
	if got := cfg.Auth.Tokens[0].Token; got != "demotoken" {
		t.Fatalf("debug auth token = %q, want demotoken", got)
	}
}

func TestLoadConfig_LegacyInvalidationTokensStillSupported(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
	yaml := strings.TrimSpace(`
storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
  invalidation:
    enabled: true
    tokens:
      - id: "legacy-backoffice"
        token: "legacy-token"
        role: "invalidate_all"
rules: []
`) + "\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Auth.Tokens) != 1 {
		t.Fatalf("auth token count = %d, want 1", len(cfg.Auth.Tokens))
	}
	if got := cfg.Auth.Tokens[0].ID; got != "legacy-backoffice" {
		t.Fatalf("legacy mapped id = %q", got)
	}
	if len(cfg.Auth.Tokens[0].Scopes) != 1 || cfg.Auth.Tokens[0].Scopes[0] != "invalidation:write" {
		t.Fatalf("legacy scopes = %#v", cfg.Auth.Tokens[0].Scopes)
	}
}
