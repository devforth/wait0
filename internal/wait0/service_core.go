package wait0

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Service struct {
	cfg Config

	httpClient *http.Client

	ram  *ramCache
	disk *diskCache

	bgSem chan struct{}

	stopCh chan struct{}
	wg     sync.WaitGroup

	overflowLog  *rateLimitedLogger
	hashLog      *rateLimitedLogger
	unchangedLog *rateLimitedLogger
	errorLog     *rateLimitedLogger

	sendRevalidateMarkers bool

	stats *statsCollector

	invCfg   InvalidationConfig
	invQueue chan invalidateJob

	invTokens []authToken
}

func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func NewService(cfg Config) (*Service, error) {
	ramMax, err := parseBytes(cfg.Storage.RAM.Max)
	if err != nil {
		return nil, err
	}
	diskMax, err := parseBytes(cfg.Storage.Disk.Max)
	if err != nil {
		return nil, err
	}
	// Disk cache is explicitly invalidated on every restart.
	// This is done efficiently by deleting the LevelDB directory before opening.
	invalidateDiskOnStart := envBool("WAIT0_INVALIDATE_DISK_CACHE_ON_START", true)
	disk, err := newDiskCache("./data/leveldb", diskMax, invalidateDiskOnStart)
	if err != nil {
		return nil, err
	}

	s := &Service{
		cfg:                   cfg,
		httpClient:            &http.Client{Timeout: 30 * time.Second},
		ram:                   newRAMCache(ramMax),
		disk:                  disk,
		bgSem:                 make(chan struct{}, 32),
		stopCh:                make(chan struct{}),
		overflowLog:           newRateLimitedLogger(1 * time.Minute),
		hashLog:               newRateLimitedLogger(1 * time.Minute),
		unchangedLog:          newRateLimitedLogger(10 * time.Second),
		errorLog:              newRateLimitedLogger(10 * time.Second),
		sendRevalidateMarkers: envBool("WAIT0_SEND_REVALIDATE_MARKERS", true),
		invCfg:                cfg.Server.Invalidation,
	}

	s.invTokens = make([]authToken, 0, len(cfg.Auth.Tokens))
	for _, t := range cfg.Auth.Tokens {
		scopes := make(map[string]struct{}, len(t.Scopes))
		for _, s := range t.Scopes {
			scopes[s] = struct{}{}
		}
		s.invTokens = append(s.invTokens, authToken{
			ID:     t.ID,
			Token:  t.Token,
			Scopes: scopes,
		})
	}
	if cfg.Server.Invalidation.Enabled {
		s.invQueue = make(chan invalidateJob, cfg.Server.Invalidation.QueueSize)
		log.Printf(
			"invalidation API enabled: queueSize=%d workers=%d maxBodyBytes=%d maxPaths=%d maxTags=%d hardLimits=%t",
			cfg.Server.Invalidation.QueueSize,
			cfg.Server.Invalidation.WorkerConcurrency,
			cfg.Server.Invalidation.MaxBodyBytes,
			cfg.Server.Invalidation.MaxPaths,
			cfg.Server.Invalidation.MaxTags,
			cfg.Server.Invalidation.HardLimits,
		)
	}

	if cfg.Logging.logStatsEveryDur > 0 {
		s.stats = newStatsCollector()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.statsLoop(cfg.Logging.logStatsEveryDur)
		}()
	}

	s.startWarmupGroups()
	s.startURLsDiscover()
	s.startInvalidationWorkers()

	return s, nil
}

func (s *Service) Close() {
	close(s.stopCh)
	s.wg.Wait()
	s.disk.close()
}

func (s *Service) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

func (s *Service) startWarmupGroups() {
	for i := range s.cfg.Rules {
		r := &s.cfg.Rules[i]
		if r.warmEvery <= 0 || r.warmMax <= 0 {
			continue
		}
		log.Printf("warmup group start: match=%q, runEvery=%s, maxRequestsAtATime=%d", r.Match, r.warmEvery, r.warmMax)
		s.wg.Add(1)
		go func(rule *Rule) {
			defer s.wg.Done()
			s.warmupGroupLoop(rule)
		}(r)
	}
}
