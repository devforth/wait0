package statapi

import (
	"encoding/json"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"wait0/internal/wait0/auth"
	"wait0/internal/wait0/proxy"
	wstats "wait0/internal/wait0/stats"
)

const ReadScope = "stats:read"
const EndpointPath = "/wait0"

const snapshotTTL = 5 * time.Second
const maxRankedURLs = 10

type EntryMeta struct {
	Size                int64
	StorageSize         int64
	Inactive            bool
	DiscoveredBy        string
	LastRefreshUnixNano int64
}

type RuleDefinition struct {
	ID               int
	Match            string
	Priority         int
	WarmupConfigured bool
	PauseBetweenRuns time.Duration
	Matches          func(path string) bool
}

type WarmupURLMetric struct {
	URL      string
	Duration time.Duration
}

type WarmupLoopSnapshot struct {
	Duration   time.Duration
	FinishedAt time.Time
	URLs       int
	TopSlowest []WarmupURLMetric
	TopFastest []WarmupURLMetric
}

type Runtime interface {
	RAMMetaSnapshot() map[string]EntryMeta
	DiskMetaSnapshot() map[string]EntryMeta
	RefreshDurationStatsMillis() MetricTriplet
	RuleDefinitions() []RuleDefinition
	WarmupLoopSnapshots() map[int]WarmupLoopSnapshot
}

type Controller struct {
	authn *auth.Authenticator
	rt    Runtime

	mu       sync.Mutex
	snapshot response
	at       time.Time
}

type response struct {
	GeneratedAt        string         `json:"generated_at"`
	SnapshotTTLSeconds int            `json:"snapshot_ttl_seconds"`
	Cache              cachePayload   `json:"cache"`
	Memory             memoryPayload  `json:"memory"`
	RefreshDurationMS  MetricTriplet  `json:"refresh_duration_ms"`
	Sitemap            sitemapPayload `json:"sitemap"`
	Rules              []rulePayload  `json:"rules"`
}

type cachePayload struct {
	URLsTotal               int           `json:"urls_total"`
	ResponsesSizeBytesTotal uint64        `json:"responses_size_bytes_total"`
	ResponseSizeBytes       MetricTriplet `json:"response_size_bytes"`
}

type memoryPayload struct {
	RSSBytes     uint64 `json:"rss_bytes"`
	GoAllocBytes uint64 `json:"go_alloc_bytes"`
}

type sitemapPayload struct {
	DiscoveredURLs  int     `json:"discovered_urls"`
	CrawledURLs     int     `json:"crawled_urls"`
	CrawlPercentage float64 `json:"crawl_percentage"`
}

type MetricTriplet struct {
	Min uint64 `json:"min"`
	Avg uint64 `json:"avg"`
	Max uint64 `json:"max"`
}

type rulePayload struct {
	ID            int              `json:"id"`
	Match         string           `json:"match"`
	Priority      int              `json:"priority"`
	URLs          int              `json:"urls"`
	Responses     int              `json:"responses"`
	RAMSizeBytes  uint64           `json:"ram_size_bytes"`
	DiskSizeBytes uint64           `json:"disk_size_bytes"`
	Warmup        warmupPayload    `json:"warmup"`
	TopLargest    []urlSizePayload `json:"top_largest_responses"`
	TopSmallest   []urlSizePayload `json:"top_smallest_responses"`
}

type warmupPayload struct {
	Configured             bool                 `json:"configured"`
	PauseBetweenRunsMS     uint64               `json:"pause_between_runs_ms"`
	LastLoopDurationMS     *uint64              `json:"last_loop_duration_ms"`
	LastFinishedAt         *string              `json:"last_finished_at"`
	LastFinishedSecondsAgo *uint64              `json:"last_finished_seconds_ago"`
	URLs                   int                  `json:"urls"`
	TopSlowest             []urlDurationPayload `json:"top_slowest_urls"`
	TopFastest             []urlDurationPayload `json:"top_fastest_urls"`
}

type urlDurationPayload struct {
	URL        string  `json:"url"`
	DurationMS float64 `json:"duration_ms"`
}

type urlSizePayload struct {
	URL       string `json:"url"`
	SizeBytes uint64 `json:"size_bytes"`
}

type ruleAggregation struct {
	ramSizeBytes  uint64
	diskSizeBytes uint64
	urlCount      int
	responseCount int
	topLargest    []urlSizePayload
	topSmallest   []urlSizePayload
}

func NewController(authn *auth.Authenticator, rt Runtime) *Controller {
	return &Controller{authn: authn, rt: rt}
}

func IsEndpointPath(path string) bool {
	return path == EndpointPath || path == EndpointPath+"/"
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	if !IsEndpointPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	actor, ok := c.authn.AuthenticateBearer(r.Header.Get("Authorization"))
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	if !auth.AuthorizedForScope(actor, ReadScope) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
		return
	}

	resp := c.getSnapshot()
	writeAnyJSON(w, http.StatusOK, resp)
}

func (c *Controller) getSnapshot() response {
	now := time.Now().UTC()
	c.mu.Lock()
	if !c.at.IsZero() && now.Sub(c.at) < snapshotTTL {
		snap := c.snapshot
		c.mu.Unlock()
		return snap
	}
	resp := c.buildSnapshot(now)
	c.snapshot = resp
	c.at = now
	c.mu.Unlock()
	return resp
}

func (c *Controller) buildSnapshot(now time.Time) response {
	ram := c.rt.RAMMetaSnapshot()
	disk := c.rt.DiskMetaSnapshot()
	ruleDefinitions := c.rt.RuleDefinitions()
	warmupLoops := c.rt.WarmupLoopSnapshots()

	keys := make(map[string]struct{}, len(ram)+len(disk))
	for k := range ram {
		keys[k] = struct{}{}
	}
	for k := range disk {
		keys[k] = struct{}{}
	}

	totalSize := uint64(0)
	respMin := uint64(math.MaxUint64)
	respMax := uint64(0)
	respCount := uint64(0)

	sitemapDiscovered := 0
	sitemapCrawled := 0

	for key := range keys {
		meta, ok := ram[key]
		if !ok {
			meta = disk[key]
		}

		sz := uint64(0)
		if meta.Size > 0 {
			sz = uint64(meta.Size)
		}
		totalSize += sz
		respCount++
		if sz < respMin {
			respMin = sz
		}
		if sz > respMax {
			respMax = sz
		}

		if strings.EqualFold(strings.TrimSpace(meta.DiscoveredBy), "sitemap") {
			sitemapDiscovered++
			if !meta.Inactive {
				sitemapCrawled++
			}
		}

	}
	rules := buildRulePayloads(now, ruleDefinitions, warmupLoops, ram, disk, keys)

	respStats := MetricTriplet{}
	if respCount > 0 {
		if respMin == math.MaxUint64 {
			respMin = 0
		}
		respStats = MetricTriplet{
			Min: respMin,
			Avg: totalSize / respCount,
			Max: respMax,
		}
	}

	rssBytes := uint64(0)
	if rss, ok := wstats.ProcessRSSBytes(); ok {
		rssBytes = rss
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	crawlPct := 0.0
	if sitemapDiscovered > 0 {
		crawlPct = float64(sitemapCrawled) * 100 / float64(sitemapDiscovered)
	}

	return response{
		GeneratedAt:        now.Format(time.RFC3339Nano),
		SnapshotTTLSeconds: int(snapshotTTL / time.Second),
		Cache: cachePayload{
			URLsTotal:               len(keys),
			ResponsesSizeBytesTotal: totalSize,
			ResponseSizeBytes:       respStats,
		},
		Memory: memoryPayload{
			RSSBytes:     rssBytes,
			GoAllocBytes: ms.Alloc,
		},
		RefreshDurationMS: c.rt.RefreshDurationStatsMillis(),
		Sitemap: sitemapPayload{
			DiscoveredURLs:  sitemapDiscovered,
			CrawledURLs:     sitemapCrawled,
			CrawlPercentage: crawlPct,
		},
		Rules: rules,
	}
}

func buildRulePayloads(
	now time.Time,
	definitions []RuleDefinition,
	warmupLoops map[int]WarmupLoopSnapshot,
	ram map[string]EntryMeta,
	disk map[string]EntryMeta,
	keys map[string]struct{},
) []rulePayload {
	aggregations := make([]ruleAggregation, len(definitions))

	for key, meta := range ram {
		if idx := matchingRuleIndex(definitions, proxy.CacheKeyPath(key)); idx >= 0 {
			aggregations[idx].ramSizeBytes += metaStorageSize(meta)
		}
	}
	for key, meta := range disk {
		if idx := matchingRuleIndex(definitions, proxy.CacheKeyPath(key)); idx >= 0 {
			aggregations[idx].diskSizeBytes += metaStorageSize(meta)
		}
	}
	for key := range keys {
		idx := matchingRuleIndex(definitions, proxy.CacheKeyPath(key))
		if idx < 0 {
			continue
		}
		aggregations[idx].urlCount++
		meta, ok := ram[key]
		if !ok {
			meta = disk[key]
		}
		if meta.Inactive {
			continue
		}
		item := urlSizePayload{
			URL:       key,
			SizeBytes: nonNegativeSize(meta.Size),
		}
		aggregations[idx].responseCount++
		aggregations[idx].topLargest = addResponseSize(aggregations[idx].topLargest, item, false)
		aggregations[idx].topSmallest = addResponseSize(aggregations[idx].topSmallest, item, true)
	}

	out := make([]rulePayload, 0, len(definitions))
	for i, definition := range definitions {
		aggregation := aggregations[i]
		payload := rulePayload{
			ID:            definition.ID,
			Match:         definition.Match,
			Priority:      definition.Priority,
			URLs:          aggregation.urlCount,
			Responses:     aggregation.responseCount,
			RAMSizeBytes:  aggregation.ramSizeBytes,
			DiskSizeBytes: aggregation.diskSizeBytes,
			TopLargest:    nonNilResponseSizes(aggregation.topLargest),
			TopSmallest:   nonNilResponseSizes(aggregation.topSmallest),
			Warmup: warmupPayload{
				Configured:         definition.WarmupConfigured,
				PauseBetweenRunsMS: uint64(definition.PauseBetweenRuns / time.Millisecond),
				TopSlowest:         []urlDurationPayload{},
				TopFastest:         []urlDurationPayload{},
			},
		}

		if definition.WarmupConfigured {
			if loop, ok := warmupLoops[definition.ID]; ok && !loop.FinishedAt.IsZero() {
				durationMS := uint64(loop.Duration / time.Millisecond)
				finishedAt := loop.FinishedAt.UTC().Format(time.RFC3339Nano)
				secondsAgo := uint64(0)
				if now.After(loop.FinishedAt) {
					secondsAgo = uint64(now.Sub(loop.FinishedAt) / time.Second)
				}
				payload.Warmup.LastLoopDurationMS = &durationMS
				payload.Warmup.LastFinishedAt = &finishedAt
				payload.Warmup.LastFinishedSecondsAgo = &secondsAgo
				payload.Warmup.URLs = loop.URLs
				payload.Warmup.TopSlowest = toURLDurationPayloads(loop.TopSlowest)
				payload.Warmup.TopFastest = toURLDurationPayloads(loop.TopFastest)
			}
		}
		out = append(out, payload)
	}
	return out
}

func matchingRuleIndex(definitions []RuleDefinition, path string) int {
	for i, definition := range definitions {
		if definition.Matches != nil && definition.Matches(path) {
			return i
		}
	}
	return -1
}

func metaStorageSize(meta EntryMeta) uint64 {
	if meta.StorageSize > 0 {
		return uint64(meta.StorageSize)
	}
	return nonNegativeSize(meta.Size)
}

func nonNegativeSize(size int64) uint64 {
	if size <= 0 {
		return 0
	}
	return uint64(size)
}

func addResponseSize(items []urlSizePayload, item urlSizePayload, smallest bool) []urlSizePayload {
	if len(items) == maxRankedURLs && !responseSizeRanksBefore(item, items[len(items)-1], smallest) {
		return items
	}
	if len(items) < maxRankedURLs {
		items = append(items, item)
	} else {
		// Replace the previous last-place entry so an eleventh URL is never retained.
		items[len(items)-1] = item
	}
	sort.Slice(items, func(i, j int) bool {
		return responseSizeRanksBefore(items[i], items[j], smallest)
	})
	return items[:len(items):len(items)]
}

func responseSizeRanksBefore(a, b urlSizePayload, smallest bool) bool {
	if a.SizeBytes == b.SizeBytes {
		return a.URL < b.URL
	}
	if smallest {
		return a.SizeBytes < b.SizeBytes
	}
	return a.SizeBytes > b.SizeBytes
}

func nonNilResponseSizes(items []urlSizePayload) []urlSizePayload {
	if items == nil {
		return []urlSizePayload{}
	}
	return items
}

func toURLDurationPayloads(items []WarmupURLMetric) []urlDurationPayload {
	out := make([]urlDurationPayload, 0, len(items))
	for _, item := range items {
		out = append(out, urlDurationPayload{
			URL:        item.URL,
			DurationMS: float64(item.Duration) / float64(time.Millisecond),
		})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAnyJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
