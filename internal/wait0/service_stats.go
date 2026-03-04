package wait0

import (
	"log"
	"runtime"
	"time"
)

func (s *Service) statsLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			ss := s.stats.Snapshot()
			cachedPaths := s.cachedPathsCount()
			ramTotal := uint64(s.ram.TotalSize())
			diskTotal := uint64(s.disk.TotalSize())
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			rssBytes, ok := processRSSBytes()
			rssStr := "n/a"
			if ok {
				rssStr = formatBytes(rssBytes)
			}

			smapsVals, smapsOK := processSmapsRollupBytes()
			rssAnonStr := "n/a"
			rssFileStr := "n/a"
			rssShmemStr := "n/a"
			rssRollupStr := "n/a"
			if smapsOK {
				if v, ok := smapsVals["Anonymous"]; ok {
					rssAnonStr = formatBytes(v)
				}
				if v, ok := smapsVals["File"]; ok {
					rssFileStr = formatBytes(v)
				}
				if v, ok := smapsVals["Shmem"]; ok {
					rssShmemStr = formatBytes(v)
				}
				if v, ok := smapsVals["Rss"]; ok {
					rssRollupStr = formatBytes(v)
				}
			}
			log.Printf(
				"Cached: Paths: %d, RAM usage: %s, Disk usage: %s, RSS: %s, RSSRollup: %s, RSSSplit: anon=%s file=%s shmem=%s, GoAlloc: %s, Resp Min/avg/max %s/%s/%s",
				cachedPaths,
				formatBytes(ramTotal),
				formatBytes(diskTotal),
				rssStr,
				rssRollupStr,
				rssAnonStr,
				rssFileStr,
				rssShmemStr,
				formatBytes(ms.Alloc),
				formatBytes(ss.MinRespBytes),
				formatBytes(ss.AvgRespBytes),
				formatBytes(ss.MaxRespBytes),
			)
		}
	}
}

func (s *Service) cachedPathsCount() int {
	// Union count without building a combined map.
	ramKeys := s.ram.Keys()
	diskCount := s.disk.KeyCount()
	intersect := 0
	for _, k := range ramKeys {
		if s.disk.HasKey(k) {
			intersect++
		}
	}
	return len(ramKeys) + diskCount - intersect
}
