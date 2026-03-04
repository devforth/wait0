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
