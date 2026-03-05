package stats

import (
	"testing"
	"time"
)

func TestCollectorSnapshot(t *testing.T) {
	s := NewCollector()
	s.Observe(100)
	s.Observe(50)
	s.Observe(-1)

	ss := s.Snapshot()
	if ss.TotalResponses != 3 {
		t.Fatalf("TotalResponses = %d", ss.TotalResponses)
	}
	if ss.MinRespBytes != 0 {
		t.Fatalf("MinRespBytes = %d", ss.MinRespBytes)
	}
	if ss.MaxRespBytes != 100 {
		t.Fatalf("MaxRespBytes = %d", ss.MaxRespBytes)
	}
	if ss.TotalRespBytes != 150 {
		t.Fatalf("TotalRespBytes = %d", ss.TotalRespBytes)
	}
	if ss.AvgRespBytes != 50 {
		t.Fatalf("AvgRespBytes = %d", ss.AvgRespBytes)
	}
}

func TestCollectorSnapshot_RefreshDurations(t *testing.T) {
	s := NewCollector()
	s.ObserveRefreshDuration(19 * time.Millisecond)
	s.ObserveRefreshDuration(66 * time.Millisecond)
	s.ObserveRefreshDuration(119 * time.Millisecond)

	ss := s.Snapshot()
	if ss.RefreshCount != 3 {
		t.Fatalf("RefreshCount = %d", ss.RefreshCount)
	}
	if time.Duration(ss.MinRefreshDurNs) != 19*time.Millisecond {
		t.Fatalf("MinRefreshDurNs = %d", ss.MinRefreshDurNs)
	}
	if time.Duration(ss.MaxRefreshDurNs) != 119*time.Millisecond {
		t.Fatalf("MaxRefreshDurNs = %d", ss.MaxRefreshDurNs)
	}
	if time.Duration(ss.AvgRefreshDurNs) != 68*time.Millisecond {
		t.Fatalf("AvgRefreshDurNs = %d", ss.AvgRefreshDurNs)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{in: 999, want: "999b"},
		{in: 1024, want: "1kb"},
		{in: 1536, want: "1.5kb"},
		{in: 1024 * 1024, want: "1mb"},
		{in: 1024 * 1024 * 1024, want: "1gb"},
	}
	for _, tc := range tests {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Fatalf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTrimFloat(t *testing.T) {
	if got := TrimFloat(" 12.0 "); got != "12" {
		t.Fatalf("TrimFloat = %q", got)
	}
	if got := TrimFloat("12.5"); got != "12.5" {
		t.Fatalf("TrimFloat = %q", got)
	}
}
