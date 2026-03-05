package stats

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"
)

type Collector struct {
	totalResponses atomic.Uint64
	totalRespBytes atomic.Uint64
	minRespBytes   atomic.Uint64
	maxRespBytes   atomic.Uint64

	totalRefreshDurNs atomic.Uint64
	refreshCount      atomic.Uint64
	minRefreshDurNs   atomic.Uint64
	maxRefreshDurNs   atomic.Uint64
}

func NewCollector() *Collector {
	s := &Collector{}
	s.minRespBytes.Store(math.MaxUint64)
	s.minRefreshDurNs.Store(math.MaxUint64)
	return s
}

func (s *Collector) Observe(respBytes int) {
	if respBytes < 0 {
		respBytes = 0
	}
	n := uint64(respBytes)

	s.totalResponses.Add(1)
	s.totalRespBytes.Add(n)

	for {
		cur := s.minRespBytes.Load()
		if n >= cur {
			break
		}
		if s.minRespBytes.CompareAndSwap(cur, n) {
			break
		}
	}
	for {
		cur := s.maxRespBytes.Load()
		if n <= cur {
			break
		}
		if s.maxRespBytes.CompareAndSwap(cur, n) {
			break
		}
	}
}

func (s *Collector) ObserveRefreshDuration(d time.Duration) {
	if d < 0 {
		d = 0
	}
	n := uint64(d)
	s.refreshCount.Add(1)
	s.totalRefreshDurNs.Add(n)

	for {
		cur := s.minRefreshDurNs.Load()
		if n >= cur {
			break
		}
		if s.minRefreshDurNs.CompareAndSwap(cur, n) {
			break
		}
	}
	for {
		cur := s.maxRefreshDurNs.Load()
		if n <= cur {
			break
		}
		if s.maxRefreshDurNs.CompareAndSwap(cur, n) {
			break
		}
	}
}

type Snapshot struct {
	TotalResponses uint64
	TotalRespBytes uint64
	MinRespBytes   uint64
	MaxRespBytes   uint64
	AvgRespBytes   uint64

	RefreshCount      uint64
	TotalRefreshDurNs uint64
	MinRefreshDurNs   uint64
	MaxRefreshDurNs   uint64
	AvgRefreshDurNs   uint64
}

func (s *Collector) Snapshot() Snapshot {
	count := s.totalResponses.Load()
	total := s.totalRespBytes.Load()
	minv := s.minRespBytes.Load()
	maxv := s.maxRespBytes.Load()
	refreshCount := s.refreshCount.Load()
	totalRefresh := s.totalRefreshDurNs.Load()
	minRefresh := s.minRefreshDurNs.Load()
	maxRefresh := s.maxRefreshDurNs.Load()
	if minRefresh == math.MaxUint64 {
		minRefresh = 0
	}

	if count == 0 {
		return Snapshot{
			RefreshCount:      refreshCount,
			TotalRefreshDurNs: totalRefresh,
			MinRefreshDurNs:   minRefresh,
			MaxRefreshDurNs:   maxRefresh,
			AvgRefreshDurNs:   avgDiv(totalRefresh, refreshCount),
		}
	}
	if minv == math.MaxUint64 {
		minv = 0
	}
	return Snapshot{
		TotalResponses:    count,
		TotalRespBytes:    total,
		MinRespBytes:      minv,
		MaxRespBytes:      maxv,
		AvgRespBytes:      total / count,
		RefreshCount:      refreshCount,
		TotalRefreshDurNs: totalRefresh,
		MinRefreshDurNs:   minRefresh,
		MaxRefreshDurNs:   maxRefresh,
		AvgRefreshDurNs:   avgDiv(totalRefresh, refreshCount),
	}
}

func avgDiv(total uint64, count uint64) uint64 {
	if count == 0 {
		return 0
	}
	return total / count
}

func FormatBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	if b < kb {
		return fmt.Sprintf("%db", b)
	}
	if b < mb {
		return TrimFloat(fmt.Sprintf("%.1f", float64(b)/kb)) + "kb"
	}
	if b < gb {
		return TrimFloat(fmt.Sprintf("%.1f", float64(b)/mb)) + "mb"
	}
	return TrimFloat(fmt.Sprintf("%.1f", float64(b)/gb)) + "gb"
}

func TrimFloat(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".0")
	return s
}
