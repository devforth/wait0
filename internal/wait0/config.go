package wait0

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Storage struct {
		RAM struct {
			Max string `yaml:"max"`
		} `yaml:"ram"`
		Disk struct {
			Max string `yaml:"max"`
		} `yaml:"disk"`
	} `yaml:"storage"`

	Server struct {
		Port   int    `yaml:"port"`
		Origin string `yaml:"origin"`
	} `yaml:"server"`

	URLsDiscover struct {
		// NOTE: historically this was misspelled as "initalDelay" in configs.
		InitialDelay    string   `yaml:"initialDelay"`
		InitalDelay     string   `yaml:"initalDelay"`
		RediscoverEvery string   `yaml:"rediscoverEvery"`
		Sitemaps        []string `yaml:"sitemaps"`

		// compiled
		initialDelayDur    time.Duration `yaml:"-"`
		rediscoverEveryDur time.Duration `yaml:"-"`
	} `yaml:"urlsDiscover"`

	Logging struct {
		LogStatsEvery    string        `yaml:"log_stats_every"`
		logStatsEveryDur time.Duration `yaml:"-"`
		// LogWarmUp prints a summary after each warmup batch drains.
		LogWarmUp bool `yaml:"log_warmup"`

		// Deprecated: use log_warmup instead.
		// If provided, warmup logging is enabled (the duration is validated but ignored).
		LogRevalidationEvery string `yaml:"log_revalidation_every"`
		LogURLAutodiscover   bool   `yaml:"log_url_autodiscover"`
	} `yaml:"logging"`

	Rules []Rule `yaml:"rules"`
}

type WarmUpConfig struct {
	RunEvery           string `yaml:"runEvery"`
	MaxRequestsAtATime int    `yaml:"maxRequestsAtATime"`

	// compiled
	runEveryDur time.Duration `yaml:"-"`
}

type Rule struct {
	Match             string        `yaml:"match"`
	Priority          int           `yaml:"priority"`
	Bypass            bool          `yaml:"bypass"`
	BypassWhenCookies []string      `yaml:"bypassWhenCookies"`
	Expiration        string        `yaml:"expiration"`
	WarmUp            *WarmUpConfig `yaml:"warmUp"`

	// compiled
	matchers  []pathPrefixMatcher
	expDur    time.Duration
	warmEvery time.Duration
	warmMax   int
}

type pathPrefixMatcher struct{ Prefix string }

func (m pathPrefixMatcher) Match(path string) bool { return strings.HasPrefix(path, m.Prefix) }

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Origin == "" {
		return Config{}, fmt.Errorf("server.origin is required")
	}
	cfg.Server.Origin = strings.TrimRight(cfg.Server.Origin, "/")

	// urlsDiscover (optional)
	if len(cfg.URLsDiscover.Sitemaps) > 0 {
		initDelay := strings.TrimSpace(cfg.URLsDiscover.InitialDelay)
		if initDelay == "" {
			initDelay = strings.TrimSpace(cfg.URLsDiscover.InitalDelay)
		}
		if initDelay != "" {
			d, err := time.ParseDuration(initDelay)
			if err != nil {
				return Config{}, fmt.Errorf("urlsDiscover.initialDelay: %w", err)
			}
			if d < 0 {
				return Config{}, fmt.Errorf("urlsDiscover.initialDelay: must be >= 0")
			}
			cfg.URLsDiscover.initialDelayDur = d
		}

		if strings.TrimSpace(cfg.URLsDiscover.RediscoverEvery) != "" {
			d, err := time.ParseDuration(cfg.URLsDiscover.RediscoverEvery)
			if err != nil {
				return Config{}, fmt.Errorf("urlsDiscover.rediscoverEvery: %w", err)
			}
			if d <= 0 {
				return Config{}, fmt.Errorf("urlsDiscover.rediscoverEvery: must be > 0")
			}
			cfg.URLsDiscover.rediscoverEveryDur = d
		}
	}

	if cfg.Logging.LogStatsEvery != "" {
		d, err := time.ParseDuration(cfg.Logging.LogStatsEvery)
		if err != nil {
			return Config{}, fmt.Errorf("logging.log_stats_every: %w", err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("logging.log_stats_every: must be > 0")
		}
		cfg.Logging.logStatsEveryDur = d
	}

	if strings.TrimSpace(cfg.Logging.LogRevalidationEvery) != "" {
		// Backward compatible alias for the previous warmup logging setting.
		_, err := time.ParseDuration(cfg.Logging.LogRevalidationEvery)
		if err != nil {
			return Config{}, fmt.Errorf("logging.log_revalidation_every: %w", err)
		}
		cfg.Logging.LogWarmUp = true
	}

	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		ms, err := parseMatch(r.Match)
		if err != nil {
			return Config{}, fmt.Errorf("rules[%d].match: %w", i, err)
		}
		r.matchers = ms
		if r.Expiration != "" {
			d, err := time.ParseDuration(r.Expiration)
			if err != nil {
				return Config{}, fmt.Errorf("rules[%d].expiration: %w", i, err)
			}
			r.expDur = d
		}
		if r.WarmUp != nil {
			if strings.TrimSpace(r.WarmUp.RunEvery) == "" {
				return Config{}, fmt.Errorf("rules[%d].warmUp.runEvery: is required", i)
			}
			d, err := time.ParseDuration(r.WarmUp.RunEvery)
			if err != nil {
				return Config{}, fmt.Errorf("rules[%d].warmUp.runEvery: %w", i, err)
			}
			if d <= 0 {
				return Config{}, fmt.Errorf("rules[%d].warmUp.runEvery: must be > 0", i)
			}
			if r.WarmUp.MaxRequestsAtATime <= 0 {
				return Config{}, fmt.Errorf("rules[%d].warmUp.maxRequestsAtATime: must be > 0", i)
			}
			r.WarmUp.runEveryDur = d
			r.warmEvery = d
			r.warmMax = r.WarmUp.MaxRequestsAtATime
		}
	}

	sort.Slice(cfg.Rules, func(i, j int) bool {
		return cfg.Rules[i].Priority < cfg.Rules[j].Priority
	})

	return cfg, nil
}

func parseMatch(expr string) ([]pathPrefixMatcher, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty match")
	}

	parts := strings.Split(expr, "|")
	out := make([]pathPrefixMatcher, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "PathPrefix(") || !strings.HasSuffix(p, ")") {
			return nil, fmt.Errorf("only PathPrefix(...) supported, got %q", p)
		}
		inside := strings.TrimSuffix(strings.TrimPrefix(p, "PathPrefix("), ")")
		inside = strings.TrimSpace(inside)
		if inside == "" || !strings.HasPrefix(inside, "/") {
			return nil, fmt.Errorf("invalid prefix %q", inside)
		}
		out = append(out, pathPrefixMatcher{Prefix: inside})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid matchers")
	}
	return out, nil
}

func (r *Rule) Matches(path string) bool {
	for _, m := range r.matchers {
		if m.Match(path) {
			return true
		}
	}
	return false
}
