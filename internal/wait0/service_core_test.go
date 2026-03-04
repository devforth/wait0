package wait0

import (
	"os"
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
	rule.warmEvery = time.Millisecond
	rule.warmMax = 1

	s := newTestService(t, "http://invalid.local", []Rule{rule})
	s.cfg.Rules = []Rule{rule}

	s.startWarmupGroups()
	stopTestService(s)
	s.wg.Wait()
}
