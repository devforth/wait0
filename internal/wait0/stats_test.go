package wait0

import "testing"

func TestStatsCollector_Snapshot(t *testing.T) {
	s := newStatsCollector()
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

func TestFormatBytes_AndTrimFloat(t *testing.T) {
	if got := formatBytes(1023); got != "1023b" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(1024); got != "1kb" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(1536); got != "1.5kb" {
		t.Fatalf("got %q", got)
	}
	if got := formatBytes(2 * 1024 * 1024); got != "2mb" {
		t.Fatalf("got %q", got)
	}
	if got := trimFloat(" 2.0 "); got != "2" {
		t.Fatalf("got %q", got)
	}
}
