package wait0

import (
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
	wstats "wait0/internal/wait0/stats"
)

var stopOnceByService sync.Map // map[*Service]*sync.Once

func stopTestService(s *Service) {
	if s == nil || s.stopCh == nil {
		return
	}
	if v, ok := stopOnceByService.Load(s); ok {
		v.(*sync.Once).Do(func() { close(s.stopCh) })
		return
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

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
		overflowLog:           wstats.NewRateLimitedLogger(time.Hour),
		unchangedLog:          wstats.NewRateLimitedLogger(time.Hour),
		errorLog:              wstats.NewRateLimitedLogger(time.Hour),
		sendRevalidateMarkers: true,
		stats:                 wstats.NewCollector(),
	}
	s.reval = revalidation.NewController(
		newRevalidationRuntimeAdapter(s),
		s.bgSem,
		s.stopCh,
		&s.wg,
		s.cfg.Logging.LogWarmUp,
		log.Default(),
		s.unchangedLog,
		s.errorLog,
	)
	s.reval.SetDurationObserver(s.stats.ObserveRefreshDuration)
	s.proxy = proxy.NewController(newProxyRuntimeAdapter(s))

	stopOnceByService.Store(s, &sync.Once{})
	t.Cleanup(func() {
		stopTestService(s)
		s.wg.Wait()
		s.disk.close()
		stopOnceByService.Delete(s)
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
