package stats

import (
	"runtime"
	"time"
)

type Logger interface {
	Printf(format string, v ...any)
}

type CacheIndex interface {
	RAMKeys() []string
	DiskKeyCount() int
	DiskHasKey(key string) bool
	RAMTotalSize() uint64
	DiskTotalSize() uint64
}

func CachedPathsCount(index CacheIndex) int {
	ramKeys := index.RAMKeys()
	diskCount := index.DiskKeyCount()
	intersect := 0
	for _, k := range ramKeys {
		if index.DiskHasKey(k) {
			intersect++
		}
	}
	return len(ramKeys) + diskCount - intersect
}

type LoopConfig struct {
	Every     time.Duration
	StopCh    <-chan struct{}
	Collector *Collector
	Cache     CacheIndex
	Logger    Logger
}

func Loop(cfg LoopConfig) {
	t := time.NewTicker(cfg.Every)
	defer t.Stop()
	for {
		select {
		case <-cfg.StopCh:
			return
		case <-t.C:
			ss := cfg.Collector.Snapshot()
			cachedPaths := CachedPathsCount(cfg.Cache)
			ramTotal := cfg.Cache.RAMTotalSize()
			diskTotal := cfg.Cache.DiskTotalSize()
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)

			rssBytes, ok := ProcessRSSBytes()
			rssStr := "n/a"
			if ok {
				rssStr = FormatBytes(rssBytes)
			}

			smapsVals, smapsOK := ProcessSmapsRollupBytes()
			rssAnonStr := "n/a"
			rssFileStr := "n/a"
			rssShmemStr := "n/a"
			rssRollupStr := "n/a"
			if smapsOK {
				if v, ok := smapsVals["Anonymous"]; ok {
					rssAnonStr = FormatBytes(v)
				}
				if v, ok := smapsVals["File"]; ok {
					rssFileStr = FormatBytes(v)
				}
				if v, ok := smapsVals["Shmem"]; ok {
					rssShmemStr = FormatBytes(v)
				}
				if v, ok := smapsVals["Rss"]; ok {
					rssRollupStr = FormatBytes(v)
				}
			}
			cfg.Logger.Printf(
				"Cached: Paths: %d, RAM usage: %s, Disk usage: %s, RSS: %s, RSSRollup: %s, RSSSplit: anon=%s file=%s shmem=%s, GoAlloc: %s, Resp Min/avg/max %s/%s/%s",
				cachedPaths,
				FormatBytes(ramTotal),
				FormatBytes(diskTotal),
				rssStr,
				rssRollupStr,
				rssAnonStr,
				rssFileStr,
				rssShmemStr,
				FormatBytes(ms.Alloc),
				FormatBytes(ss.MinRespBytes),
				FormatBytes(ss.AvgRespBytes),
				FormatBytes(ss.MaxRespBytes),
			)
		}
	}
}
