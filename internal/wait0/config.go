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

	Rules []Rule `yaml:"rules"`
}

type Rule struct {
	Match             string   `yaml:"match"`
	Priority          int      `yaml:"priority"`
	Bypass            bool     `yaml:"bypass"`
	BypassWhenCookies []string `yaml:"bypassWhenCookies"`
	Expiration        string   `yaml:"expiration"`
	WormUp            string   `yaml:"wormUp"`

	// compiled
	matchers []pathPrefixMatcher
	expDur   time.Duration
	warmDur  time.Duration
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
		if r.WormUp != "" {
			d, err := time.ParseDuration(r.WormUp)
			if err != nil {
				return Config{}, fmt.Errorf("rules[%d].wormUp: %w", i, err)
			}
			r.warmDur = d
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
