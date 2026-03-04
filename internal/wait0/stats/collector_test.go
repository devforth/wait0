package stats

import "testing"

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
