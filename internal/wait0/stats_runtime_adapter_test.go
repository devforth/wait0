package wait0

import (
	"testing"
	"time"
)

func TestStatsRuntimeAdapter_MetaSnapshots(t *testing.T) {
	s := newTestService(t, "http://example.com", nil)
	now := time.Now().UTC()
	s.ram.Put("/a", CacheEntry{Body: []byte("ram"), StoredAt: now.Unix(), RevalidatedAt: now.Add(-time.Second).UnixNano(), DiscoveredBy: "sitemap"}, s.disk, s.overflowLog)
	s.disk.PutAsync("/b", CacheEntry{Body: []byte("disk"), StoredAt: now.Unix(), RevalidatedAt: now.Add(-2 * time.Second).UnixNano(), DiscoveredBy: "user"})
	waitFor(t, 700*time.Millisecond, func() bool { return s.disk.HasKey("/b") })

	a := newStatsRuntimeAdapter(s)
	ram := a.RAMMetaSnapshot()
	disk := a.DiskMetaSnapshot()
	if len(ram) == 0 {
		t.Fatal("expected non-empty RAM snapshot")
	}
	if len(disk) == 0 {
		t.Fatal("expected non-empty disk snapshot")
	}
	if ram["/a"].Size == 0 {
		t.Fatal("expected non-zero RAM size")
	}
	if disk["/b"].Size == 0 {
		t.Fatal("expected non-zero disk size")
	}

	s.stats.ObserveRefreshDuration(19 * time.Millisecond)
	s.stats.ObserveRefreshDuration(119 * time.Millisecond)
	dur := a.RefreshDurationStatsMillis()
	if dur.Min != 19 || dur.Max != 119 {
		t.Fatalf("unexpected duration stats: %+v", dur)
	}
}
