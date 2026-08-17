package wait0

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"wait0/internal/wait0/revalidation"
)

func warmupTargetPaths(t *testing.T, s *Service, ruleIndex int) []string {
	t.Helper()
	rule := &s.cfg.Rules[ruleIndex]
	targets := s.reval.WarmupTargets(revalidation.WarmRule{
		ID:               ruleIndex,
		Match:            rule.Match,
		PauseBetweenRuns: rule.warmPause,
		WarmMax:          rule.warmMax,
		Matches:          s.ownsPath(ruleIndex),
	})
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	sort.Strings(paths)
	return paths
}

func cacheOne(t *testing.T, s *Service, path string) {
	t.Helper()
	s.ram.Put(path, CacheEntry{
		Status:   200,
		Header:   http.Header{"Content-Type": {"text/html"}},
		Body:     []byte("<html>" + path + "</html>"),
		StoredAt: time.Now().Unix(),
	}, s.disk, s.overflowLog)
}

// A catch-all rule must not warm the paths a more specific rule governs. Warming
// by the rule's own matcher instead of by ownership ran a second loop over every
// path already covered by the specific rule, doubling origin requests and cache
// writes for those paths.
func TestWarmupTargets_PartitionedByRuleOwnership(t *testing.T) {
	insights := warmableRule(t, "PathPrefix(/insights)", 2)
	catchAll := warmableRule(t, "PathPrefix(/)", 3)

	s := newTestService(t, "http://example.com", []Rule{insights, catchAll})

	cacheOne(t, s, "/")
	cacheOne(t, s, "/about")
	cacheOne(t, s, "/insights")
	cacheOne(t, s, "/insights/some-article/")

	insightsPaths := warmupTargetPaths(t, s, 0)
	catchAllPaths := warmupTargetPaths(t, s, 1)

	wantInsights := []string{"/insights", "/insights/some-article/"}
	if !equalStrings(insightsPaths, wantInsights) {
		t.Fatalf("/insights loop targets = %v, want %v", insightsPaths, wantInsights)
	}

	wantCatchAll := []string{"/", "/about"}
	if !equalStrings(catchAllPaths, wantCatchAll) {
		t.Fatalf("catch-all loop targets = %v, want %v", catchAllPaths, wantCatchAll)
	}

	// Every cached path is warmed exactly once across all loops.
	seen := map[string]int{}
	for _, path := range append(insightsPaths, catchAllPaths...) {
		seen[path]++
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct warmed paths, got %v", seen)
	}
	for path, count := range seen {
		if count != 1 {
			t.Fatalf("path %q warmed %d times, want exactly 1", path, count)
		}
	}
}

// Ownership follows the priority order the request path already uses, not prefix
// length, so a lower priority number keeps winning even when its prefix is
// shorter.
func TestWarmupTargets_FollowsPriorityNotPrefixLength(t *testing.T) {
	catchAll := warmableRule(t, "PathPrefix(/)", 1)
	insights := warmableRule(t, "PathPrefix(/insights)", 2)

	s := newTestService(t, "http://example.com", []Rule{catchAll, insights})

	cacheOne(t, s, "/")
	cacheOne(t, s, "/insights/article/")

	if got, want := warmupTargetPaths(t, s, 0), []string{"/", "/insights/article/"}; !equalStrings(got, want) {
		t.Fatalf("priority-1 catch-all targets = %v, want %v", got, want)
	}
	if got := warmupTargetPaths(t, s, 1); len(got) != 0 {
		t.Fatalf("outranked rule should own nothing, got %v", got)
	}
}

// A rule that governs paths but declares no warmUp leaves them cold. That is
// deliberate, since inheriting a broader rule's schedule is what caused the
// duplicate warmup, so the behaviour is pinned here.
func TestWarmupTargets_RuleWithoutWarmupLeavesItsPathsCold(t *testing.T) {
	insights := mustRule(t, "PathPrefix(/insights)")
	insights.Priority = 2
	catchAll := warmableRule(t, "PathPrefix(/)", 3)

	s := newTestService(t, "http://example.com", []Rule{insights, catchAll})

	cacheOne(t, s, "/")
	cacheOne(t, s, "/insights/article/")

	if got, want := warmupTargetPaths(t, s, 1), []string{"/"}; !equalStrings(got, want) {
		t.Fatalf("catch-all targets = %v, want %v", got, want)
	}
}

func TestOwnsPath(t *testing.T) {
	api := mustRule(t, "PathPrefix(/api)")
	api.Priority = 1
	api.Bypass = true
	insights := warmableRule(t, "PathPrefix(/insights)", 2)
	catchAll := warmableRule(t, "PathPrefix(/)", 3)

	s := newTestService(t, "http://example.com", []Rule{api, insights, catchAll})

	tests := map[string]int{
		"/api/orders":  0,
		"/insights":    1,
		"/insights/x/": 1,
		"/":            2,
		"/about":       2,
		// Matching is on the raw prefix, not on path segments, so a path that
		// merely starts with the prefix is owned by that rule too.
		"/insightsomething": 1,
		"/apifoo":           0,
		// A path that diverges inside the prefix falls through to the catch-all.
		"/insightful": 2,
	}

	for path, wantIndex := range tests {
		if got := s.pickRuleIndex(path); got != wantIndex {
			t.Fatalf("pickRuleIndex(%q) = %d, want %d", path, got, wantIndex)
		}
		for index := range s.cfg.Rules {
			owns := s.ownsPath(index)(path)
			if want := index == wantIndex; owns != want {
				t.Fatalf("ownsPath(%d)(%q) = %v, want %v", index, path, owns, want)
			}
		}
	}
}

// rulesOverlap is deliberately asymmetric: it answers "would the broader rule
// have warmed the paths this rule takes precedence over", which is the only
// direction the warmup-gap warning cares about.
func TestRulesOverlap(t *testing.T) {
	catchAll := mustRule(t, "PathPrefix(/)")
	insights := mustRule(t, "PathPrefix(/insights)")
	api := mustRule(t, "PathPrefix(/api)")
	multi := mustRule(t, "PathPrefix(/docs)|PathPrefix(/insights)")

	if !rulesOverlap(&insights, &catchAll) {
		t.Fatal("the catch-all would have warmed everything under /insights")
	}
	if rulesOverlap(&insights, &api) {
		t.Fatal("/api and /insights are disjoint")
	}
	if rulesOverlap(&catchAll, &insights) {
		t.Fatal("/insights cannot cover the paths a catch-all takes precedence over")
	}
	if !rulesOverlap(&multi, &catchAll) {
		t.Fatal("a rule with several prefixes overlaps if any of them does")
	}
	if !rulesOverlap(&multi, &insights) {
		t.Fatal("/insights covers one of the multi-prefix rule's own prefixes")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The gap warning is the only signal that a rule without warmUp now leaves its
// paths cold, so it has to fire exactly when that is true.
func TestWarnWarmupGaps(t *testing.T) {
	tests := map[string]struct {
		rules    func(*testing.T) []Rule
		wantWarn bool
	}{
		"specific rule without warmup shadows a warming catch-all": {
			rules: func(t *testing.T) []Rule {
				insights := mustRule(t, "PathPrefix(/insights)")
				insights.Priority = 2
				return []Rule{insights, warmableRule(t, "PathPrefix(/)", 3)}
			},
			wantWarn: true,
		},
		"specific rule declares its own warmup": {
			rules: func(t *testing.T) []Rule {
				return []Rule{
					warmableRule(t, "PathPrefix(/insights)", 2),
					warmableRule(t, "PathPrefix(/)", 3),
				}
			},
			wantWarn: false,
		},
		"bypass rules are never warmed anyway": {
			rules: func(t *testing.T) []Rule {
				api := mustRule(t, "PathPrefix(/api)")
				api.Priority = 1
				api.Bypass = true
				return []Rule{api, warmableRule(t, "PathPrefix(/)", 3)}
			},
			wantWarn: false,
		},
		"disjoint prefixes do not shadow each other": {
			rules: func(t *testing.T) []Rule {
				docs := mustRule(t, "PathPrefix(/docs)")
				docs.Priority = 1
				return []Rule{docs, warmableRule(t, "PathPrefix(/insights)", 2)}
			},
			wantWarn: false,
		},
		"no warming rule at all": {
			rules: func(t *testing.T) []Rule {
				insights := mustRule(t, "PathPrefix(/insights)")
				insights.Priority = 2
				catchAll := mustRule(t, "PathPrefix(/)")
				catchAll.Priority = 3
				return []Rule{insights, catchAll}
			},
			wantWarn: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t, "http://example.com", tc.rules(t))

			var buf bytes.Buffer
			flags := log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() {
				log.SetOutput(os.Stderr)
				log.SetFlags(flags)
			})

			s.warnWarmupGaps()

			if got := strings.Contains(buf.String(), "warmup gap:"); got != tc.wantWarn {
				t.Fatalf("warned = %v, want %v (log: %q)", got, tc.wantWarn, buf.String())
			}
		})
	}
}
