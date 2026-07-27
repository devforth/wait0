package wait0

import (
	"time"

	"wait0/internal/wait0/cache"
	"wait0/internal/wait0/revalidation"
	"wait0/internal/wait0/statapi"
)

type statsRuntimeAdapter struct {
	s *Service
}

func newStatsRuntimeAdapter(s *Service) statapi.Runtime {
	return &statsRuntimeAdapter{s: s}
}

func (a *statsRuntimeAdapter) RAMMetaSnapshot() map[string]statapi.EntryMeta {
	in := a.s.ram.MetaSnapshot()
	return toStatMeta(in)
}

func (a *statsRuntimeAdapter) DiskMetaSnapshot() map[string]statapi.EntryMeta {
	in := a.s.disk.MetaSnapshot()
	return toStatMeta(in)
}

func (a *statsRuntimeAdapter) RefreshDurationStatsMillis() statapi.MetricTriplet {
	if a.s.stats == nil {
		return statapi.MetricTriplet{}
	}
	ss := a.s.stats.Snapshot()
	return statapi.MetricTriplet{
		Min: uint64(time.Duration(ss.MinRefreshDurNs) / time.Millisecond),
		Avg: uint64(time.Duration(ss.AvgRefreshDurNs) / time.Millisecond),
		Max: uint64(time.Duration(ss.MaxRefreshDurNs) / time.Millisecond),
	}
}

func (a *statsRuntimeAdapter) RuleDefinitions() []statapi.RuleDefinition {
	out := make([]statapi.RuleDefinition, 0, len(a.s.cfg.Rules))
	for i := range a.s.cfg.Rules {
		rule := &a.s.cfg.Rules[i]
		out = append(out, statapi.RuleDefinition{
			ID:               i,
			Match:            rule.Match,
			Priority:         rule.Priority,
			WarmupConfigured: rule.warmPause > 0 && rule.warmMax > 0,
			PauseBetweenRuns: rule.warmPause,
			Matches:          rule.Matches,
		})
	}
	return out
}

func (a *statsRuntimeAdapter) WarmupLoopSnapshots() map[int]statapi.WarmupLoopSnapshot {
	if a.s.reval == nil {
		return map[int]statapi.WarmupLoopSnapshot{}
	}
	in := a.s.reval.WarmupSummaries()
	out := make(map[int]statapi.WarmupLoopSnapshot, len(in))
	for ruleID, summary := range in {
		out[ruleID] = statapi.WarmupLoopSnapshot{
			Duration:   summary.Took,
			FinishedAt: summary.FinishedAt,
			URLs:       summary.URLs,
			TopSlowest: toStatWarmupURLMetrics(summary.TopSlowest),
			TopFastest: toStatWarmupURLMetrics(summary.TopFastest),
		}
	}
	return out
}

func toStatWarmupURLMetrics(in []revalidation.WarmupURLMetric) []statapi.WarmupURLMetric {
	out := make([]statapi.WarmupURLMetric, 0, len(in))
	for _, item := range in {
		out = append(out, statapi.WarmupURLMetric{
			URL:      item.URL,
			Duration: item.Duration,
		})
	}
	return out
}

func toStatMeta(in map[string]cache.EntryMeta) map[string]statapi.EntryMeta {
	out := make(map[string]statapi.EntryMeta, len(in))
	for k, v := range in {
		out[k] = statapi.EntryMeta{
			Size:                v.Size,
			StorageSize:         v.StorageSize,
			Inactive:            v.Inactive,
			DiscoveredBy:        v.DiscoveredBy,
			LastRefreshUnixNano: v.LastRefreshUnixNano,
		}
	}
	return out
}
