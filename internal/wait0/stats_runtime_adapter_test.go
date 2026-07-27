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
	if ram["/a"].StorageSize == 0 {
		t.Fatal("expected non-zero RAM storage size")
	}
	if disk["/b"].Size == 0 {
		t.Fatal("expected non-zero disk size")
	}
	if disk["/b"].StorageSize == 0 {
		t.Fatal("expected non-zero disk storage size")
	}

	s.stats.ObserveRefreshDuration(19 * time.Millisecond)
	s.stats.ObserveRefreshDuration(119 * time.Millisecond)
	dur := a.RefreshDurationStatsMillis()
	if dur.Min != 19 || dur.Max != 119 {
		t.Fatalf("unexpected duration stats: %+v", dur)
	}
}

func TestStatsRuntimeAdapter_RuleDefinitionsIncludeEmptyRules(t *testing.T) {
	warmRule := mustRule(t, "PathPrefix(/blog)")
	warmRule.Priority = 1
	warmRule.warmPause = 10 * time.Second
	warmRule.warmMax = 4
	emptyRule := mustRule(t, "PathPrefix(/empty)")
	emptyRule.Priority = 2

	s := newTestService(t, "http://example.com", []Rule{warmRule, emptyRule})
	a := newStatsRuntimeAdapter(s)

	definitions := a.RuleDefinitions()
	if len(definitions) != 2 {
		t.Fatalf("rule definitions = %v", definitions)
	}
	if definitions[0].Match != "PathPrefix(/blog)" || !definitions[0].WarmupConfigured || definitions[0].PauseBetweenRuns != 10*time.Second {
		t.Fatalf("warm rule definition = %+v", definitions[0])
	}
	if definitions[1].Match != "PathPrefix(/empty)" || definitions[1].WarmupConfigured {
		t.Fatalf("empty rule definition = %+v", definitions[1])
	}
	if !definitions[0].Matches("/blog/post") || definitions[0].Matches("/other") {
		t.Fatal("rule matcher was not preserved")
	}
	if got := a.WarmupLoopSnapshots(); len(got) != 0 {
		t.Fatalf("unexpected warmup snapshots: %v", got)
	}
}
