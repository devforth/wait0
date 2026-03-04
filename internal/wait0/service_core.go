package wait0

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"wait0/internal/wait0/auth"
	"wait0/internal/wait0/discovery"
	"wait0/internal/wait0/invalidation"
	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
	wstats "wait0/internal/wait0/stats"
)

type Service struct {
	cfg Config

	httpClient *http.Client

	ram  *ramCache
	disk *diskCache

	bgSem chan struct{}

	stopCh chan struct{}
	wg     sync.WaitGroup

	overflowLog  *wstats.RateLimitedLogger
	hashLog      *wstats.RateLimitedLogger
	unchangedLog *wstats.RateLimitedLogger
	errorLog     *wstats.RateLimitedLogger

	sendRevalidateMarkers bool

	stats *wstats.Collector

	invAuth *auth.Authenticator
	inv     *invalidation.Controller
	proxy   *proxy.Controller
	reval   *revalidation.Controller
	disco   *discovery.Controller
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
		overflowLog:           wstats.NewRateLimitedLogger(1 * time.Minute),
		hashLog:               wstats.NewRateLimitedLogger(1 * time.Minute),
		unchangedLog:          wstats.NewRateLimitedLogger(10 * time.Second),
		errorLog:              wstats.NewRateLimitedLogger(10 * time.Second),
		sendRevalidateMarkers: envBool("WAIT0_SEND_REVALIDATE_MARKERS", true),
	}

	authCfgs := make([]auth.TokenConfig, 0, len(cfg.Auth.Tokens))
	for _, t := range cfg.Auth.Tokens {
		authCfgs = append(authCfgs, auth.TokenConfig{
			ID:     t.ID,
			Token:  t.Token,
			Scopes: t.Scopes,
		})
	}
	s.invAuth = auth.NewAuthenticator(authCfgs)
	s.inv = invalidation.NewController(
		invalidation.Config{
			Enabled:           cfg.Server.Invalidation.Enabled,
			QueueSize:         cfg.Server.Invalidation.QueueSize,
			WorkerConcurrency: cfg.Server.Invalidation.WorkerConcurrency,
			MaxBodyBytes:      cfg.Server.Invalidation.MaxBodyBytes,
			MaxPaths:          cfg.Server.Invalidation.MaxPaths,
			MaxTags:           cfg.Server.Invalidation.MaxTags,
			HardLimits:        cfg.Server.Invalidation.HardLimits,
		},
		s.invAuth,
		newInvalidationRuntimeAdapter(s),
		s.stopCh,
		&s.wg,
	)
	s.reval = revalidation.NewController(
		newRevalidationRuntimeAdapter(s),
		s.bgSem,
		s.stopCh,
		&s.wg,
		cfg.Logging.LogWarmUp,
		log.Default(),
		s.unchangedLog,
		s.errorLog,
	)
	s.proxy = proxy.NewController(newProxyRuntimeAdapter(s))
	s.disco = discovery.NewController(
		discovery.Config{
			Origin:          cfg.Server.Origin,
			Sitemaps:        append([]string(nil), cfg.URLsDiscover.Sitemaps...),
			InitialDelay:    cfg.URLsDiscover.initialDelayDur,
			RediscoverEvery: cfg.URLsDiscover.rediscoverEveryDur,
			LogAutodiscover: cfg.Logging.LogURLAutodiscover,
		},
		newDiscoveryRuntimeAdapter(s),
		s.stopCh,
		&s.wg,
		log.Default(),
	)
	if cfg.Server.Invalidation.Enabled {
		log.Printf("invalidation API enabled: queueSize=%d workers=%d maxBodyBytes=%d maxPaths=%d maxTags=%d hardLimits=%t", cfg.Server.Invalidation.QueueSize, cfg.Server.Invalidation.WorkerConcurrency, cfg.Server.Invalidation.MaxBodyBytes, cfg.Server.Invalidation.MaxPaths, cfg.Server.Invalidation.MaxTags, cfg.Server.Invalidation.HardLimits)
	}

	if cfg.Logging.logStatsEveryDur > 0 {
		s.stats = wstats.NewCollector()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			wstats.Loop(wstats.LoopConfig{
				Every:     cfg.Logging.logStatsEveryDur,
				StopCh:    s.stopCh,
				Collector: s.stats,
				Cache:     statsCacheIndex{s: s},
				Logger:    log.Default(),
			})
		}()
	}

	s.startWarmupGroups()
	if s.disco != nil {
		s.disco.Start()
	}

	return s, nil
}

func (s *Service) Close() {
	close(s.stopCh)
	s.wg.Wait()
	s.disk.close()
}

func (s *Service) Handler() http.Handler {
	if s.proxy == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		})
	}
	return http.HandlerFunc(s.proxy.Handle)
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
			s.reval.WarmupGroupLoop(revalidation.WarmRule{
				Match:     rule.Match,
				WarmEvery: rule.warmEvery,
				WarmMax:   rule.warmMax,
				Matches:   rule.Matches,
			})
		}(r)
	}
}
