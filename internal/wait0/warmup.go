package wait0

import (
	"context"
	"log"
	"sort"
	"time"
)

type warmupSummary struct {
	match string
	urls  int
	took  time.Duration
	rps   float64
	minRT time.Duration
	avgRT time.Duration
	maxRT time.Duration

	unchanged           int
	updated             int
	deleted             int
	ignoredStatus       int
	ignoredCacheControl int
	errors              int
}

func (s *Service) warmupGroupLoop(rule *Rule) {
	sem := make(chan struct{}, rule.warmMax)
	results := make(chan revalResult, rule.warmMax*4)

	queued := make(map[string]struct{})
	queue := make([]string, 0, 1024)

	var inflight int
	var batchStart time.Time
	var urls int
	var minRT, maxRT, sumRT time.Duration

	var unchanged, updated, deleted, ignoredStatus, ignoredCacheControl, errors int

	resetBatch := func() {
		batchStart = time.Time{}
		urls = 0
		minRT, maxRT, sumRT = 0, 0, 0
		unchanged, updated, deleted, ignoredStatus, ignoredCacheControl, errors = 0, 0, 0, 0, 0, 0
	}

	makeSummary := func() warmupSummary {
		took := time.Since(batchStart)
		if batchStart.IsZero() {
			took = 0
		}
		var rps float64
		if took > 0 {
			rps = float64(urls) / took.Seconds()
		}
		avg := time.Duration(0)
		if urls > 0 {
			avg = sumRT / time.Duration(urls)
		}
		return warmupSummary{
			match:               rule.Match,
			urls:                urls,
			took:                took,
			rps:                 rps,
			minRT:               minRT,
			avgRT:               avg,
			maxRT:               maxRT,
			unchanged:           unchanged,
			updated:             updated,
			deleted:             deleted,
			ignoredStatus:       ignoredStatus,
			ignoredCacheControl: ignoredCacheControl,
			errors:              errors,
		}
	}

	maybeFinish := func() {
		if batchStart.IsZero() {
			return
		}
		if inflight != 0 || len(queue) != 0 {
			return
		}

		// drained
		if s.cfg.Logging.LogWarmUp {
			sum := makeSummary()
			log.Printf(
				"Revalidated for match %q: %d URLs (unchanged=%d updated=%d deleted=%d ignoredStatus=%d ignoredCC=%d errors=%d updated+errors=%d), Took: %s, RPS: %.2f, resp time min/avg/max - %s/%s/%s",
				sum.match, sum.urls,
				sum.unchanged, sum.updated, sum.deleted, sum.ignoredStatus, sum.ignoredCacheControl, sum.errors, sum.updated+sum.errors,
				sum.took.Truncate(time.Millisecond), sum.rps,
				sum.minRT.Truncate(time.Millisecond), sum.avgRT.Truncate(time.Millisecond), sum.maxRT.Truncate(time.Millisecond),
			)
		}
		resetBatch()
	}

	dispatch := func() {
		for inflight < rule.warmMax && len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			delete(queued, key)

			inflight++
			sem <- struct{}{}
			s.wg.Add(1)
			go func(k string) {
				defer s.wg.Done()
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				results <- s.revalidateOnce(ctx, k, k, "", "warmup")
			}(key)
		}
	}

	refresh := func() {
		keys := s.keysByLastAccessDesc(rule)
		if len(keys) == 0 {
			return
		}
		if batchStart.IsZero() {
			batchStart = time.Now()
		}
		for _, k := range keys {
			if _, ok := queued[k]; ok {
				continue
			}
			queued[k] = struct{}{}
			queue = append(queue, k)
		}
	}

	t := time.NewTicker(rule.warmEvery)
	defer t.Stop()

	stopping := false
	stopCh := s.stopCh // local so we can nil it after first signal (avoid select starvation)

	for {
		// If shutting down and nothing is in-flight, exit (safe: no worker can still be sending).
		if stopping && inflight == 0 {
			// Optional: flush a final summary immediately (no delayed timer) if a batch was active.
			if !batchStart.IsZero() && s.cfg.Logging.LogWarmUp {
				sum := makeSummary()
				log.Printf(
					"Revalidated for match %q: %d URLs (unchanged=%d updated=%d deleted=%d ignoredStatus=%d ignoredCC=%d errors=%d updated+errors=%d), Took: %s, RPS: %.2f, resp time min/avg/max - %s/%s/%s",
					sum.match, sum.urls,
					sum.unchanged, sum.updated, sum.deleted, sum.ignoredStatus, sum.ignoredCacheControl, sum.errors, sum.updated+sum.errors,
					sum.took.Truncate(time.Millisecond), sum.rps,
					sum.minRT.Truncate(time.Millisecond), sum.avgRT.Truncate(time.Millisecond), sum.maxRT.Truncate(time.Millisecond),
				)
			}
			return
		}

		select {
		case <-stopCh:
			// Stop dispatching new work, but keep draining results until inflight == 0.
			stopping = true
			stopCh = nil // critical: prevent this always-ready case from starving results
			t.Stop()

			// Drop any queued (not-yet-dispatched) warmups.
			for k := range queued {
				delete(queued, k)
			}
			queue = queue[:0]

		case <-t.C:
			if stopping {
				continue
			}
			refresh()
			dispatch()

		case res := <-results:
			inflight--
			if !batchStart.IsZero() {
				urls++
				sumRT += res.dur
				if minRT == 0 || res.dur < minRT {
					minRT = res.dur
				}
				if res.dur > maxRT {
					maxRT = res.dur
				}

				switch res.kind {
				case "unchanged":
					unchanged++
				case "updated":
					updated++
				case "deleted":
					deleted++
				case "ignored-status":
					ignoredStatus++
				case "ignored-cache-control":
					ignoredCacheControl++
				case "error":
					errors++
					if s.errorLog != nil {
						s.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.path, res.uri, res.err)
					}
				default:
					// keep buckets stable even if kind is empty/unknown
					errors++
					if s.errorLog != nil {
						s.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.path, res.uri, "unknown-kind")
					}
				}
			}
			if !stopping {
				dispatch()
				maybeFinish()
			}

		}
	}
}

func (s *Service) keysByLastAccessDesc(rule *Rule) []string {
	access := s.snapshotAccessTimes()
	if len(access) == 0 {
		return nil
	}
	items := make([]struct {
		k  string
		ts int64
	}, 0, len(access))
	for k, ts := range access {
		if !rule.Matches(k) {
			continue
		}
		items = append(items, struct {
			k  string
			ts int64
		}{k: k, ts: ts})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ts == items[j].ts {
			return items[i].k < items[j].k
		}
		return items[i].ts > items[j].ts
	})
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.k)
	}
	return out
}

func (s *Service) snapshotAccessTimes() map[string]int64 {
	// merge RAM and disk access times; keep the latest timestamp.
	ram := s.ram.SnapshotAccessTimes()
	disk := s.disk.SnapshotAccessTimes()
	out := make(map[string]int64, len(ram)+len(disk))
	for k, ts := range disk {
		out[k] = ts
	}
	for k, ts := range ram {
		if cur, ok := out[k]; !ok || ts > cur {
			out[k] = ts
		}
	}
	return out
}

func (s *Service) allKeysSnapshot() []string {
	m := map[string]struct{}{}
	for _, k := range s.ram.Keys() {
		m[k] = struct{}{}
	}
	for _, k := range s.disk.Keys() {
		m[k] = struct{}{}
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
