package wait0

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
)

type statsCollector struct {
	totalResponses atomic.Uint64
	totalRespBytes atomic.Uint64
	minRespBytes   atomic.Uint64
	maxRespBytes   atomic.Uint64
}

func newStatsCollector() *statsCollector {
	s := &statsCollector{}
	s.minRespBytes.Store(math.MaxUint64)
	return s
}

func (s *statsCollector) Observe(respBytes int) {
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

type statsSnapshot struct {
	TotalResponses uint64
	TotalRespBytes uint64
	MinRespBytes   uint64
	MaxRespBytes   uint64
	AvgRespBytes   uint64
}

func (s *statsCollector) Snapshot() statsSnapshot {
	count := s.totalResponses.Load()
	total := s.totalRespBytes.Load()
	minv := s.minRespBytes.Load()
	maxv := s.maxRespBytes.Load()
	if count == 0 {
		return statsSnapshot{}
	}
	if minv == math.MaxUint64 {
		minv = 0
	}
	return statsSnapshot{
		TotalResponses: count,
		TotalRespBytes: total,
		MinRespBytes:   minv,
		MaxRespBytes:   maxv,
		AvgRespBytes:   total / count,
	}
}

func formatBytes(b uint64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	if b < kb {
		return fmt.Sprintf("%db", b)
	}
	if b < mb {
		return trimFloat(fmt.Sprintf("%.1f", float64(b)/kb)) + "kb"
	}
	if b < gb {
		return trimFloat(fmt.Sprintf("%.1f", float64(b)/mb)) + "mb"
	}
	return trimFloat(fmt.Sprintf("%.1f", float64(b)/gb)) + "gb"
}

func trimFloat(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".0")
	return s
}
