package wait0

import (
	"time"

	"wait0/internal/wait0/cache"
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

func toStatMeta(in map[string]cache.EntryMeta) map[string]statapi.EntryMeta {
	out := make(map[string]statapi.EntryMeta, len(in))
	for k, v := range in {
		out[k] = statapi.EntryMeta{
			Size:                v.Size,
			Inactive:            v.Inactive,
			DiscoveredBy:        v.DiscoveredBy,
			LastRefreshUnixNano: v.LastRefreshUnixNano,
		}
	}
	return out
}
