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
)

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
}

func NewController(rt Runtime, bgSem chan struct{}, stopCh <-chan struct{}, wg *sync.WaitGroup, logWarmUp bool, summaryLog Logger, unchangedLog Logger, errorLog Logger) *Controller {
	return &Controller{
		rt:           rt,
		bgSem:        bgSem,
		stopCh:       stopCh,
		wg:           wg,
		logWarmUp:    logWarmUp,
		summaryLog:   summaryLog,
		unchangedLog: unchangedLog,
		errorLog:     errorLog,
	}
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

	res := Result{OK: true, Changed: false, Dur: time.Since(start), URI: uri, Path: path}

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
	if rule.WarmMax <= 0 {
		return
	}

	sem := make(chan struct{}, rule.WarmMax)
	results := make(chan Result, rule.WarmMax*4)

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

	makeSummary := func() WarmupSummary {
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
		return WarmupSummary{
			Match:               rule.Match,
			URLs:                urls,
			Took:                took,
			RPS:                 rps,
			MinRT:               minRT,
			AvgRT:               avg,
			MaxRT:               maxRT,
			Unchanged:           unchanged,
			Updated:             updated,
			Deleted:             deleted,
			IgnoredStatus:       ignoredStatus,
			IgnoredCacheControl: ignoredCacheControl,
			Errors:              errors,
		}
	}

	maybeFinish := func() {
		if batchStart.IsZero() {
			return
		}
		if inflight != 0 || len(queue) != 0 {
			return
		}
		if c.logWarmUp && c.summaryLog != nil {
			sum := makeSummary()
			c.summaryLog.Printf(
				"Revalidated for match %q: %d URLs (unchanged=%d updated=%d deleted=%d ignoredStatus=%d ignoredCC=%d errors=%d updated+errors=%d), Took: %s, RPS: %.2f, resp time min/avg/max - %s/%s/%s",
				sum.Match, sum.URLs,
				sum.Unchanged, sum.Updated, sum.Deleted, sum.IgnoredStatus, sum.IgnoredCacheControl, sum.Errors, sum.Updated+sum.Errors,
				sum.Took.Truncate(time.Millisecond), sum.RPS,
				sum.MinRT.Truncate(time.Millisecond), sum.AvgRT.Truncate(time.Millisecond), sum.MaxRT.Truncate(time.Millisecond),
			)
		}
		resetBatch()
	}

	dispatch := func() {
		for inflight < rule.WarmMax && len(queue) > 0 {
			key := queue[0]
			queue = queue[1:]
			delete(queued, key)

			inflight++
			sem <- struct{}{}
			c.wg.Add(1)
			go func(k string) {
				defer c.wg.Done()
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				results <- c.Once(ctx, k, k, "", "warmup")
			}(key)
		}
	}

	refresh := func() {
		keys := c.KeysByLastAccessDesc(rule)
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

	t := time.NewTicker(rule.WarmEvery)
	defer t.Stop()

	stopping := false
	stopCh := c.stopCh

	for {
		if stopping && inflight == 0 {
			if !batchStart.IsZero() && c.logWarmUp && c.summaryLog != nil {
				sum := makeSummary()
				c.summaryLog.Printf(
					"Revalidated for match %q: %d URLs (unchanged=%d updated=%d deleted=%d ignoredStatus=%d ignoredCC=%d errors=%d updated+errors=%d), Took: %s, RPS: %.2f, resp time min/avg/max - %s/%s/%s",
					sum.Match, sum.URLs,
					sum.Unchanged, sum.Updated, sum.Deleted, sum.IgnoredStatus, sum.IgnoredCacheControl, sum.Errors, sum.Updated+sum.Errors,
					sum.Took.Truncate(time.Millisecond), sum.RPS,
					sum.MinRT.Truncate(time.Millisecond), sum.AvgRT.Truncate(time.Millisecond), sum.MaxRT.Truncate(time.Millisecond),
				)
			}
			return
		}

		select {
		case <-stopCh:
			stopping = true
			stopCh = nil
			t.Stop()
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
				sumRT += res.Dur
				if minRT == 0 || res.Dur < minRT {
					minRT = res.Dur
				}
				if res.Dur > maxRT {
					maxRT = res.Dur
				}
				switch res.Kind {
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
					if c.errorLog != nil {
						c.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.Path, res.URI, res.Err)
					}
				default:
					errors++
					if c.errorLog != nil {
						c.errorLog.Printf("Revalidate error: path=%q uri=%q err=%q", res.Path, res.URI, "unknown-kind")
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
		if rule.Matches != nil && !rule.Matches(k) {
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
