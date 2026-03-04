package wait0

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func newTestService(t *testing.T, origin string, rules []Rule) *Service {
	t.Helper()

	disk, err := newDiskCache(filepath.Join(t.TempDir(), "leveldb"), 8*1024*1024, true)
	if err != nil {
		t.Fatalf("newDiskCache: %v", err)
	}

	cfg := Config{}
	cfg.Server.Origin = origin
	cfg.Rules = rules

	s := &Service{
		cfg:                   cfg,
		httpClient:            &http.Client{Timeout: 2 * time.Second},
		ram:                   newRAMCache(8 * 1024 * 1024),
		disk:                  disk,
		bgSem:                 make(chan struct{}, 8),
		stopCh:                make(chan struct{}),
		overflowLog:           newRateLimitedLogger(time.Hour),
		hashLog:               newRateLimitedLogger(time.Hour),
		unchangedLog:          newRateLimitedLogger(time.Hour),
		errorLog:              newRateLimitedLogger(time.Hour),
		sendRevalidateMarkers: true,
	}

	t.Cleanup(func() {
		close(s.stopCh)
		s.wg.Wait()
		s.disk.close()
	})

	return s
}

func mustRule(t *testing.T, match string) Rule {
	t.Helper()
	ms, err := parseMatch(match)
	if err != nil {
		t.Fatalf("parseMatch(%q): %v", match, err)
	}
	return Rule{Match: match, matchers: ms}
}
