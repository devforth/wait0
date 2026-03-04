package wait0

import (
	"os"
	"path/filepath"
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
    expiration: "30s"
    warmUp:
      runEvery: "1m"
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
	if cfg.Rules[0].warmEvery != time.Minute || cfg.Rules[0].warmMax != 3 {
		t.Fatalf("warmup compiled fields not set")
	}
}

func TestLoadConfig_Errors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "missing origin", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  port: 8080\nrules: []\n"},
		{name: "bad match", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"BadExpr(/)\"\n"},
		{name: "bad warmup", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nrules:\n  - match: \"PathPrefix(/)\"\n    warmUp:\n      runEvery: \"\"\n      maxRequestsAtATime: 1\n"},
		{name: "bad log stats", yaml: "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\nlogging:\n  log_stats_every: \"bad\"\nrules: []\n"},
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
