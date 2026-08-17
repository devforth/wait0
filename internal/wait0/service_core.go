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
	"wait0/internal/wait0/dashboard"
	"wait0/internal/wait0/discovery"
	"wait0/internal/wait0/invalidation"
	"wait0/internal/wait0/proxy"
	"wait0/internal/wait0/revalidation"
	"wait0/internal/wait0/statapi"
	wstats "wait0/internal/wait0/stats"
	"wait0/internal/wait0/urlpersist"
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
	unchangedLog *wstats.RateLimitedLogger
	errorLog     *wstats.RateLimitedLogger

	sendRevalidateMarkers bool
	debugHeaders          proxy.DebugHeaderSet
	variants              *variantState

	stats *wstats.Collector

	invAuth *auth.Authenticator
	inv     *invalidation.Controller
	stat    *statapi.Controller
	dash    *dashboard.Controller
	proxy   *proxy.Controller
	reval   *revalidation.Controller
	disco   *discovery.Controller
	urlp    *urlpersist.Controller
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

func envInt(name string, def int) int {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return i
}

func envCSV(name string) []string {
	v, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
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
	disk, err := newDiskCache(diskCacheDir, diskMax, invalidateDiskOnStart)
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
		unchangedLog:          wstats.NewRateLimitedLogger(10 * time.Second),
		errorLog:              wstats.NewRateLimitedLogger(10 * time.Second),
		sendRevalidateMarkers: envBool("WAIT0_SEND_REVALIDATE_MARKERS", true),
		debugHeaders:          proxy.NewDebugHeaderSet(cfg.Logging.DebugHeaders),
		stats:                 wstats.NewCollector(),
		variants:              newVariantState(),
	}
	s.rebuildVariantFamilies()

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
	s.stat = statapi.NewController(s.invAuth, newStatsRuntimeAdapter(s))
	s.configureDashboard()
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
	s.reval.SetDurationObserver(s.stats.ObserveRefreshDuration)
	s.proxy = proxy.NewController(newProxyRuntimeAdapter(s))
	if cfg.URLPersister.Enabled {
		s.urlp = urlpersist.NewController(
			urlpersist.Config{
				Enabled:             true,
				File:                cfg.URLPersister.File,
				Origin:              cfg.Server.Origin,
				FlushEvery:          cfg.URLPersister.flushEveryDur,
				RestoreOnStart:      cfg.URLPersister.RestoreOnStart == nil || *cfg.URLPersister.RestoreOnStart,
				MaxURLs:             cfg.URLPersister.MaxURLs,
				ForgetAfterFailures: *cfg.URLPersister.ForgetAfterFailures,
			},
			newURLPersistRuntimeAdapter(s),
			s.stopCh,
			&s.wg,
			log.Default(),
		)
		log.Printf(
			"urlPersister enabled: file=%q flushEvery=%s maxUrls=%d restoreOnStart=%t forgetAfterFailures=%d",
			cfg.URLPersister.File, cfg.URLPersister.flushEveryDur, cfg.URLPersister.MaxURLs,
			cfg.URLPersister.RestoreOnStart == nil || *cfg.URLPersister.RestoreOnStart,
			*cfg.URLPersister.ForgetAfterFailures,
		)
	}
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
	s.urlp.Start()

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

func (s *Service) pickRule(path string) *Rule {
	if i := s.pickRuleIndex(path); i >= 0 {
		return &s.cfg.Rules[i]
	}
	return nil
}

// pickRuleIndex returns the index of the rule that governs path, or -1. Rules
// are sorted by priority, so the first match is the most specific one the
// configuration declares.
func (s *Service) pickRuleIndex(path string) int {
	for i := range s.cfg.Rules {
		if s.cfg.Rules[i].Matches(path) {
			return i
		}
	}
	return -1
}

// ownsPath reports whether the rule at index is the rule a request for path
// would actually be served under.
//
// Warmup needs this rather than the rule's own matcher. A matcher answers "could
// this rule apply", which for a catch-all like PathPrefix(/) is true of every
// path in the cache, including the ones a higher-priority rule governs. Warming
// by matcher therefore ran a second loop over every path already covered by a
// more specific rule, doubling origin requests and cache writes for those paths.
func (s *Service) ownsPath(index int) func(path string) bool {
	return func(path string) bool {
		return s.pickRuleIndex(path) == index
	}
}

func (s *Service) configureDashboard() {
	user := strings.TrimSpace(os.Getenv("WAIT0_DASHBOARD_USERNAME"))
	pass := strings.TrimSpace(os.Getenv("WAIT0_DASHBOARD_PASSWORD"))
	if user == "" || pass == "" {
		log.Printf("dashboard disabled: missing WAIT0_DASHBOARD_USERNAME or WAIT0_DASHBOARD_PASSWORD")
		return
	}

	_, statsToken, ok := resolveAuthTokenByScope(s.cfg.Auth.Tokens, statapi.ReadScope)
	if !ok {
		log.Printf("dashboard disabled: no auth token with scope %q", statapi.ReadScope)
		return
	}
	_, invToken, invOK := resolveAuthTokenByScope(s.cfg.Auth.Tokens, invalidation.WriteScope)

	s.dash = dashboard.NewController(
		dashboard.Config{
			Username:                user,
			Password:                pass,
			StatsBearerToken:        statsToken,
			InvalidationBearerToken: invToken,
			RateLimitPerMinute:      envInt("WAIT0_DASHBOARD_RATE_LIMIT_RPM", 120),
			TrustProxyHeaders:       envBool("WAIT0_DASHBOARD_TRUST_PROXY_HEADERS", false),
			TrustedProxyCIDRs:       envCSV("WAIT0_DASHBOARD_TRUSTED_PROXY_CIDRS"),
		},
		dashboard.Runtime{
			StatsHandler:        http.HandlerFunc(s.stat.Handle),
			InvalidationHandler: http.HandlerFunc(s.inv.Handle),
		},
	)
	if invOK {
		log.Printf("dashboard enabled: stats and invalidation routes active")
		return
	}
	log.Printf("dashboard enabled (stats-only): no auth token with scope %q", invalidation.WriteScope)
}

func resolveAuthTokenByScope(tokens []AuthTokenConfig, scope string) (id, token string, ok bool) {
	need := strings.TrimSpace(scope)
	if need == "" {
		return "", "", false
	}
	for _, t := range tokens {
		for _, s := range t.Scopes {
			if strings.TrimSpace(s) != need {
				continue
			}
			if strings.TrimSpace(t.Token) == "" {
				continue
			}
			return strings.TrimSpace(t.ID), strings.TrimSpace(t.Token), true
		}
	}
	return "", "", false
}

func (s *Service) startWarmupGroups() {
	s.warnWarmupGaps()
	for i := range s.cfg.Rules {
		r := &s.cfg.Rules[i]
		if r.warmPause <= 0 || r.warmMax <= 0 {
			continue
		}
		log.Printf("warmup group start: match=%q, pauseBetweenRuns=%s, maxRequestsAtATime=%d", r.Match, r.warmPause, r.warmMax)
		s.wg.Add(1)
		go func(ruleID int, rule *Rule) {
			defer s.wg.Done()
			s.reval.WarmupGroupLoop(revalidation.WarmRule{
				ID:                   ruleID,
				Match:                rule.Match,
				PauseBetweenRuns:     rule.warmPause,
				WarmMax:              rule.warmMax,
				Matches:              s.ownsPath(ruleID),
				PresetHeaderNames:    append([]string(nil), rule.warmPresetHeaderNames...),
				RequestHeaderPresets: cloneStringSliceMap(rule.WarmupRequestHeaderPresets),
			})
		}(i, r)
	}
}

// warnWarmupGaps reports rules that shadow a warmup loop without declaring one.
//
// Because warmup covers only the paths a rule actually governs, a rule with no
// warmUp block leaves its paths cold even when a broader rule below it warms
// everything else. That is the right default, since inheriting a less specific
// rule's schedule is what produced duplicate warmup in the first place, but it is
// invisible in the config, so say it out loud at startup.
func (s *Service) warnWarmupGaps() {
	for i := range s.cfg.Rules {
		shadowing := &s.cfg.Rules[i]
		if shadowing.Bypass || (shadowing.warmPause > 0 && shadowing.warmMax > 0) {
			continue
		}
		for j := i + 1; j < len(s.cfg.Rules); j++ {
			broader := &s.cfg.Rules[j]
			if broader.warmPause <= 0 || broader.warmMax <= 0 {
				continue
			}
			if !rulesOverlap(shadowing, broader) {
				continue
			}
			log.Printf(
				"warmup gap: rule %q (priority %d) declares no warmUp and takes precedence over %q (priority %d), so paths under %q are never warmed",
				shadowing.Match, shadowing.Priority, broader.Match, broader.Priority, shadowing.Match,
			)
			break
		}
	}
}

// rulesOverlap reports whether broader would match paths that shadowing governs,
// which is true exactly when it matches one of shadowing's own prefixes.
func rulesOverlap(shadowing, broader *Rule) bool {
	for _, m := range shadowing.matchers {
		if broader.Matches(m.Prefix) {
			return true
		}
	}
	return false
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if in == nil {
		return nil
	}
	out := make(map[string][]string, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

type statsCacheIndex struct {
	s *Service
}

func (i statsCacheIndex) RAMKeys() []string {
	return i.s.ram.Keys()
}

func (i statsCacheIndex) DiskKeys() []string {
	return i.s.disk.Keys()
}

func (i statsCacheIndex) DiskKeyCount() int {
	return i.s.disk.KeyCount()
}

func (i statsCacheIndex) DiskHasKey(key string) bool {
	return i.s.disk.HasKey(key)
}

func (i statsCacheIndex) RAMTotalSize() uint64 {
	return uint64(i.s.ram.TotalSize())
}

func (i statsCacheIndex) DiskTotalSize() uint64 {
	return uint64(i.s.disk.TotalSize())
}
