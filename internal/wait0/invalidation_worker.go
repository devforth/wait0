package wait0

import (
	"context"
	"log"
	"sync"
	"time"
)

func (s *Service) startInvalidationWorkers() {
	if !s.invCfg.Enabled || s.invQueue == nil {
		return
	}
	for i := 0; i < s.invCfg.WorkerConcurrency; i++ {
		s.wg.Add(1)
		go func(workerID int) {
			defer s.wg.Done()
			s.invalidationWorkerLoop(workerID)
		}(i + 1)
	}
}

func (s *Service) invalidationWorkerLoop(workerID int) {
	for {
		select {
		case <-s.stopCh:
			return
		case job := <-s.invQueue:
			s.processInvalidationJob(workerID, job)
		}
	}
}

func (s *Service) processInvalidationJob(workerID int, job invalidateJob) {
	start := time.Now()
	keys := make(map[string]struct{}, len(job.Paths))
	for _, p := range job.Paths {
		keys[p] = struct{}{}
	}

	if len(job.Tags) > 0 {
		tagSet := make(map[string]struct{}, len(job.Tags))
		for _, tag := range job.Tags {
			tagSet[tag] = struct{}{}
		}
		for _, k := range s.resolveKeysByTags(tagSet) {
			keys[k] = struct{}{}
		}
	}

	resolvedKeys := make([]string, 0, len(keys))
	for k := range keys {
		resolvedKeys = append(resolvedKeys, k)
	}

	invalidated := 0
	for _, k := range resolvedKeys {
		had := false
		if _, ok := s.ram.Peek(k); ok {
			had = true
		}
		if _, ok := s.disk.Peek(k); ok {
			had = true
		}
		s.ram.Delete(k)
		s.disk.Delete(k)
		if had {
			invalidated++
		}
	}

	type recrawlStats struct {
		updated int
		errs    int
	}
	stats := recrawlStats{}
	var statsMu sync.Mutex

	semSize := s.invCfg.WorkerConcurrency
	if semSize <= 0 {
		semSize = 1
	}
	sem := make(chan struct{}, semSize)
	var wg sync.WaitGroup

	for _, key := range resolvedKeys {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			res := s.revalidateOnce(ctx, k, k, "", "invalidate")
			cancel()

			statsMu.Lock()
			if res.kind == "updated" || res.kind == "unchanged" {
				stats.updated++
			}
			if res.kind == "error" {
				stats.errs++
			}
			statsMu.Unlock()
		}(key)
	}
	wg.Wait()

	log.Printf(
		"invalidation completed: request_id=%q actor=%q worker=%d requested_paths=%d requested_tags=%d resolved_keys=%d invalidated=%d recrawled=%d recrawl_errors=%d took=%s",
		job.RequestID,
		job.ActorID,
		workerID,
		len(job.Paths),
		len(job.Tags),
		len(resolvedKeys),
		invalidated,
		stats.updated,
		stats.errs,
		time.Since(start).Truncate(time.Millisecond),
	)
}

func (s *Service) resolveKeysByTags(tags map[string]struct{}) []string {
	if len(tags) == 0 {
		return nil
	}
	keys := make(map[string]struct{})
	for _, k := range s.cachedKeysSnapshot() {
		ent, ok := s.peekCacheEntry(k)
		if !ok {
			continue
		}
		entTags := entryTagsNormalized(ent.Header)
		if len(entTags) == 0 {
			continue
		}
		for t := range entTags {
			if _, match := tags[t]; match {
				keys[k] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	return out
}

func (s *Service) cachedKeysSnapshot() []string {
	ram := s.ram.Keys()
	disk := s.disk.Keys()
	seen := make(map[string]struct{}, len(ram)+len(disk))
	out := make([]string, 0, len(ram)+len(disk))
	for _, k := range ram {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, k := range disk {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func (s *Service) peekCacheEntry(key string) (CacheEntry, bool) {
	if ent, ok := s.ram.Peek(key); ok {
		return ent, true
	}
	if ent, ok := s.disk.Peek(key); ok {
		return ent, true
	}
	return CacheEntry{}, false
}
