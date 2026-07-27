package revalidation

import (
	"context"
	"hash/crc32"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"wait0/internal/wait0/proxy"
)

const maxRankedURLs = 10

type Logger interface {
	Printf(format string, v ...any)
}

type Runtime interface {
	Peek(key string) (Entry, bool)
	Put(key string, ent Entry)
	Delete(key string)
	SnapshotAccessTimes() map[string]int64
	AllKeys() []string
	Origin() string
	Do(req *http.Request) (*http.Response, error)
	CachableContentTypes(path string) []string
	SendRevalidateMarkers() bool
	RandomString(n int) string
}

type Controller struct {
	rt Runtime

	bgSem  chan struct{}
	stopCh <-chan struct{}
	wg     *sync.WaitGroup

	logWarmUp bool

	summaryLog   Logger
	unchangedLog Logger
	errorLog     Logger

	observeDuration func(time.Duration)

	warmupMu        sync.RWMutex
	warmupSummaries map[int]WarmupSummary
}

func NewController(rt Runtime, bgSem chan struct{}, stopCh <-chan struct{}, wg *sync.WaitGroup, logWarmUp bool, summaryLog Logger, unchangedLog Logger, errorLog Logger) *Controller {
	return &Controller{
		rt:              rt,
		bgSem:           bgSem,
		stopCh:          stopCh,
		wg:              wg,
		logWarmUp:       logWarmUp,
		summaryLog:      summaryLog,
		unchangedLog:    unchangedLog,
		errorLog:        errorLog,
		warmupSummaries: make(map[int]WarmupSummary),
	}
}

func (c *Controller) SetDurationObserver(fn func(time.Duration)) {
	c.observeDuration = fn
}

func (c *Controller) Async(key, path, query, by string) {
	select {
	case c.bgSem <- struct{}{}:
	default:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.bgSem }()
		defer cancel()
		_ = c.Once(ctx, key, path, query, by)
	}()
}

func (c *Controller) Once(ctx context.Context, key, path, query, by string) Result {
	start := time.Now()
	defer func() {
		if c.observeDuration != nil {
			c.observeDuration(time.Since(start))
		}
	}()
	cur, hasCur := c.rt.Peek(key)

	discoveredBy := "user"
	if hasCur {
		if v := strings.TrimSpace(cur.DiscoveredBy); v != "" {
			discoveredBy = v
		}
	}

	uri := path
	if query != "" {
		uri += "?" + query
	}
	originURL := c.rt.Origin() + uri

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return Result{OK: false, Changed: false, Dur: time.Since(start), URI: uri, Path: path, Kind: "error", Err: err.Error()}
	}

	if c.rt.SendRevalidateMarkers() {
		req.Header.Set("X-Wait0-Revalidate-At", time.Now().UTC().Format(time.RFC3339Nano))
		req.Header.Set("X-Wait0-Revalidate-Entropy", c.rt.RandomString(8))
	}
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := c.rt.Do(req)
	if err != nil {
		return Result{OK: false, Changed: false, Dur: time.Since(start), URI: uri, Path: path, Kind: "error", Err: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{OK: false, Changed: false, Dur: time.Since(start), URI: uri, Path: path, Kind: "error", Err: err.Error()}
	}

	res := Result{
		OK:      true,
		Changed: false,
		Dur:     time.Since(start),
		URI:     uri,
		Path:    path,
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if hasCur {
			c.rt.Delete(key)
			res.Changed = true
			res.Kind = "deleted"
		} else {
			res.Kind = "ignored-status"
		}
		return res
	}

	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		if hasCur {
			c.rt.Delete(key)
			res.Changed = true
			res.Kind = "deleted"
		} else {
			res.Kind = "ignored-cache-control"
		}
		return res
	}

	if !proxy.IsCachableContentType(resp.Header.Get("Content-Type"), c.rt.CachableContentTypes(path)) {
		if hasCur {
			c.rt.Delete(key)
			res.Changed = true
			res.Kind = "deleted"
		} else {
			res.Kind = "ignored-content-type"
		}
		return res
	}

	now := time.Now().UTC()
	newEnt := Entry{
		Status:        resp.StatusCode,
		Header:        cloneHeader(resp.Header),
		Body:          body,
		StoredAt:      now.Unix(),
		Hash32:        crc32.ChecksumIEEE(body),
		Inactive:      false,
		DiscoveredBy:  discoveredBy,
		RevalidatedAt: now.UnixNano(),
		RevalidatedBy: by,
	}
	newEnt.Header.Del("Content-Length")

	if hasCur && cur.Hash32 == newEnt.Hash32 {
		res.Kind = "unchanged"
		if c.unchangedLog != nil {
			c.unchangedLog.Printf("Revalidate unchanged: path=%q uri=%q", path, uri)
		}
		c.rt.Put(key, newEnt)
		return res
	}

	c.rt.Put(key, newEnt)
	res.Changed = true
	res.Kind = "updated"
	return res
}

func (c *Controller) WarmupGroupLoop(rule WarmRule) {
	if rule.WarmMax <= 0 || rule.PauseBetweenRuns <= 0 {
		return
	}

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		summary, completed := c.runWarmupBatch(rule)
		if !completed {
			return
		}
		c.storeWarmupSummary(summary)
		c.logWarmupSummary(summary)

		timer := time.NewTimer(rule.PauseBetweenRuns)
		select {
		case <-c.stopCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (c *Controller) runWarmupBatch(rule WarmRule) (WarmupSummary, bool) {
	start := time.Now()
	summary := WarmupSummary{RuleID: rule.ID, Match: rule.Match}
	keys := c.KeysByLastAccessDesc(rule)
	if len(keys) == 0 {
		summary.FinishedAt = time.Now().UTC()
		summary.Took = summary.FinishedAt.Sub(start)
		return summary, true
	}

	batchCtx, cancelBatch := context.WithCancel(context.Background())
	defer cancelBatch()

	results := make(chan Result, rule.WarmMax)
	next := 0
	inflight := 0
	stopping := false
	stopCh := c.stopCh
	sumRT := time.Duration(0)

	dispatch := func() {
		for !stopping && inflight < rule.WarmMax && next < len(keys) {
			key := keys[next]
			next++
			inflight++
			go func(k string) {
				ctx, cancel := context.WithTimeout(batchCtx, 30*time.Second)
				defer cancel()
				path, query := proxy.SplitCacheKey(k)
				results <- c.Once(ctx, k, path, query, "warmup")
			}(key)
		}
	}

	dispatch()
	for inflight > 0 {
		select {
		case <-stopCh:
			stopping = true
			stopCh = nil
			cancelBatch()
		case res := <-results:
			inflight--
			summary.URLs++
			sumRT += res.Dur
			if summary.MinRT == 0 || res.Dur < summary.MinRT {
				summary.MinRT = res.Dur
			}
			if res.Dur > summary.MaxRT {
				summary.MaxRT = res.Dur
			}
			metric := WarmupURLMetric{URL: res.URI, Duration: res.Dur}
			summary.TopSlowest = addWarmupURLMetric(summary.TopSlowest, metric, false)
			summary.TopFastest = addWarmupURLMetric(summary.TopFastest, metric, true)
			c.countWarmupResult(&summary, res)
			dispatch()
		}
	}

	if stopping || next < len(keys) {
		return WarmupSummary{}, false
	}

	summary.FinishedAt = time.Now().UTC()
	summary.Took = summary.FinishedAt.Sub(start)
	if summary.URLs > 0 {
		summary.AvgRT = sumRT / time.Duration(summary.URLs)
	}
	if summary.Took > 0 {
		summary.RPS = float64(summary.URLs) / summary.Took.Seconds()
	}
	return summary, true
}

func (c *Controller) countWarmupResult(summary *WarmupSummary, res Result) {
	switch res.Kind {
	case "unchanged":
		summary.Unchanged++
	case "updated":
		summary.Updated++
	case "deleted":
		summary.Deleted++
	case "ignored-status":
		summary.IgnoredStatus++
	case "ignored-cache-control":
		summary.IgnoredCacheControl++
	case "ignored-content-type":
		summary.IgnoredContentType++
	case "error":
		summary.Errors++
		if c.errorLog != nil {
			c.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.Path, res.URI, res.Err)
		}
	default:
		summary.Errors++
		if c.errorLog != nil {
			c.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.Path, res.URI, "unknown-kind")
		}
	}
}

func addWarmupURLMetric(items []WarmupURLMetric, item WarmupURLMetric, fastest bool) []WarmupURLMetric {
	if len(items) == maxRankedURLs && !warmupMetricRanksBefore(item, items[len(items)-1], fastest) {
		return items
	}
	if len(items) < maxRankedURLs {
		items = append(items, item)
	} else {
		// Replace the previous last-place entry so an eleventh URL is never retained.
		items[len(items)-1] = item
	}
	sort.Slice(items, func(i, j int) bool {
		return warmupMetricRanksBefore(items[i], items[j], fastest)
	})
	return items[:len(items):len(items)]
}

func warmupMetricRanksBefore(a, b WarmupURLMetric, fastest bool) bool {
	if a.Duration == b.Duration {
		return a.URL < b.URL
	}
	if fastest {
		return a.Duration < b.Duration
	}
	return a.Duration > b.Duration
}

func (c *Controller) storeWarmupSummary(summary WarmupSummary) {
	latest := cloneWarmupSummary(summary)
	c.warmupMu.Lock()
	// Keep one slot per rule. Deleting first eagerly releases the previous loop's
	// ranked URL strings before installing the latest completed loop.
	delete(c.warmupSummaries, summary.RuleID)
	c.warmupSummaries[summary.RuleID] = latest
	c.warmupMu.Unlock()
}

func (c *Controller) WarmupSummaries() map[int]WarmupSummary {
	c.warmupMu.RLock()
	defer c.warmupMu.RUnlock()
	out := make(map[int]WarmupSummary, len(c.warmupSummaries))
	for id, summary := range c.warmupSummaries {
		out[id] = cloneWarmupSummary(summary)
	}
	return out
}

func cloneWarmupSummary(summary WarmupSummary) WarmupSummary {
	summary.TopSlowest = cloneWarmupURLMetrics(summary.TopSlowest)
	summary.TopFastest = cloneWarmupURLMetrics(summary.TopFastest)
	return summary
}

func cloneWarmupURLMetrics(items []WarmupURLMetric) []WarmupURLMetric {
	if len(items) > maxRankedURLs {
		items = items[:maxRankedURLs]
	}
	if len(items) == 0 {
		return nil
	}
	out := make([]WarmupURLMetric, len(items))
	copy(out, items)
	return out
}

func (c *Controller) logWarmupSummary(sum WarmupSummary) {
	if !c.logWarmUp || c.summaryLog == nil {
		return
	}
	c.summaryLog.Printf(
		"Revalidated for match %q: %d URLs (unchanged=%d updated=%d deleted=%d ignoredStatus=%d ignoredCC=%d ignoredContentType=%d errors=%d updated+errors=%d), Took: %s, RPS: %.2f, resp time min/avg/max - %s/%s/%s",
		sum.Match, sum.URLs,
		sum.Unchanged, sum.Updated, sum.Deleted, sum.IgnoredStatus, sum.IgnoredCacheControl, sum.IgnoredContentType, sum.Errors, sum.Updated+sum.Errors,
		sum.Took.Truncate(time.Millisecond), sum.RPS,
		sum.MinRT.Truncate(time.Millisecond), sum.AvgRT.Truncate(time.Millisecond), sum.MaxRT.Truncate(time.Millisecond),
	)
}

func (c *Controller) KeysByLastAccessDesc(rule WarmRule) []string {
	access := c.rt.SnapshotAccessTimes()
	if len(access) == 0 {
		return nil
	}
	items := make([]struct {
		k  string
		ts int64
	}, 0, len(access))
	for k, ts := range access {
		path := proxy.CacheKeyPath(k)
		if rule.Matches != nil && !rule.Matches(path) {
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

func (c *Controller) AllKeysSnapshot() []string {
	out := make([]string, 0)
	seen := map[string]struct{}{}
	for _, k := range c.rt.AllKeys() {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		vv := make([]string, len(vs))
		copy(vv, vs)
		out[k] = vv
	}
	return out
}
