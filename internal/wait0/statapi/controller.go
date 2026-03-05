package statapi

import (
	"encoding/json"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"wait0/internal/wait0/auth"
	wstats "wait0/internal/wait0/stats"
)

const ReadScope = "stats:read"
const EndpointPath = "/wait0"

const snapshotTTL = 5 * time.Second

type EntryMeta struct {
	Size                int64
	Inactive            bool
	DiscoveredBy        string
	LastRefreshUnixNano int64
}

type Runtime interface {
	RAMMetaSnapshot() map[string]EntryMeta
	DiskMetaSnapshot() map[string]EntryMeta
	RefreshDurationStatsMillis() MetricTriplet
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
	}
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
