package discovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Logger interface {
	Printf(format string, v ...any)
}

type Config struct {
	Origin          string
	Sitemaps        []string
	InitialDelay    time.Duration
	RediscoverEvery time.Duration
	LogAutodiscover bool
}

type Rule struct {
	Bypass bool
}

type Entry struct {
	Status       int
	Header       http.Header
	Body         []byte
	StoredAt     int64
	Hash32       uint32
	Inactive     bool
	DiscoveredBy string
}

type Runtime interface {
	PickRule(path string) *Rule
	PeekRAM(path string) (Entry, bool)
	PeekDisk(path string) (Entry, bool)
	PutDisk(path string, ent Entry)
	Do(req *http.Request) (*http.Response, error)
}

type Controller struct {
	cfg    Config
	rt     Runtime
	stopCh <-chan struct{}
	wg     *sync.WaitGroup
	logger Logger
}

type SitemapDoc struct {
	URLs     []string `xml:"url>loc"`
	Sitemaps []string `xml:"sitemap>loc"`
}

func NewController(cfg Config, rt Runtime, stopCh <-chan struct{}, wg *sync.WaitGroup, logger Logger) *Controller {
	return &Controller{cfg: cfg, rt: rt, stopCh: stopCh, wg: wg, logger: logger}
}

func (c *Controller) Start() {
	if len(c.cfg.Sitemaps) == 0 {
		return
	}

	initDelay := c.cfg.InitialDelay
	period := c.cfg.RediscoverEvery

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()

		if initDelay > 0 {
			select {
			case <-c.stopCh:
				return
			case <-time.After(initDelay):
			}
		}

		runOnce := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			stored, ignored, err := c.DiscoverOnce(ctx)
			if err != nil {
				c.logger.Printf("urlsDiscover: error: %v", err)
				return
			}
			c.logger.Printf("urlsDiscover: stored=%d ignored=%d", stored, ignored)
		}

		runOnce()
		if period <= 0 {
			return
		}

		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-c.stopCh:
				return
			case <-t.C:
				runOnce()
			}
		}
	}()
}

func (c *Controller) DiscoverOnce(ctx context.Context) (stored int, ignored int, _ error) {
	seenSitemaps := map[string]struct{}{}
	queue := make([]string, 0, len(c.cfg.Sitemaps))
	for _, sm := range c.cfg.Sitemaps {
		sm = strings.TrimSpace(sm)
		if sm == "" {
			continue
		}
		queue = append(queue, c.NormalizeMaybeRelativeURL(sm))
	}

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return stored, ignored, ctx.Err()
		case <-c.stopCh:
			return stored, ignored, nil
		default:
		}

		smURL := queue[0]
		queue = queue[1:]
		if _, ok := seenSitemaps[smURL]; ok {
			continue
		}
		seenSitemaps[smURL] = struct{}{}

		doc, err := c.FetchAndParseSitemap(ctx, smURL)
		if err != nil {
			return stored, ignored, fmt.Errorf("fetch sitemap %q: %w", smURL, err)
		}

		for _, nested := range doc.Sitemaps {
			nested = strings.TrimSpace(nested)
			if nested == "" {
				continue
			}
			queue = append(queue, c.NormalizeMaybeRelativeURL(nested))
		}

		fit := 0
		ignoredThis := 0
		for _, loc := range doc.URLs {
			path := NormalizePathFromLoc(loc)
			if path == "" {
				ignoredThis++
				continue
			}

			rule := c.rt.PickRule(path)
			if rule == nil || rule.Bypass {
				ignoredThis++
				ignored++
				continue
			}
			fit++

			if ent, ok := c.rt.PeekRAM(path); ok && !ent.Inactive {
				continue
			}
			if ent, ok := c.rt.PeekDisk(path); ok && !ent.Inactive {
				continue
			}

			now := time.Now().UTC()
			seed := Entry{
				Status:       http.StatusOK,
				Header:       make(http.Header),
				Body:         []byte{},
				StoredAt:     now.Unix(),
				Hash32:       0,
				Inactive:     true,
				DiscoveredBy: "sitemap",
			}
			c.rt.PutDisk(path, seed)
			stored++
		}

		if c.cfg.LogAutodiscover {
			c.logger.Printf(
				"urlsDiscover sitemap=%q urls=%d fit=%d ignored=%d",
				smURL,
				len(doc.URLs),
				fit,
				ignoredThis,
			)
		}
	}

	return stored, ignored, nil
}

func (c *Controller) NormalizeMaybeRelativeURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return u
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if !strings.HasPrefix(u, "/") {
		u = "/" + u
	}
	return c.cfg.Origin + u
}

func (c *Controller) FetchAndParseSitemap(ctx context.Context, sitemapURL string) (SitemapDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return SitemapDoc{}, err
	}

	resp, err := c.rt.Do(req)
	if err != nil {
		return SitemapDoc{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return SitemapDoc{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SitemapDoc{}, err
	}

	tryGzip := strings.HasSuffix(strings.ToLower(sitemapURL), ".gz") || (len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b)
	if tryGzip {
		if gz, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			defer gz.Close()
			if unzipped, err := io.ReadAll(gz); err == nil {
				body = unzipped
			}
		}
	}

	var doc SitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return SitemapDoc{}, err
	}

	for i := range doc.URLs {
		doc.URLs[i] = strings.TrimSpace(doc.URLs[i])
	}
	for i := range doc.Sitemaps {
		doc.Sitemaps[i] = strings.TrimSpace(doc.Sitemaps[i])
	}

	return doc, nil
}

func NormalizePathFromLoc(loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		u, err := url.Parse(loc)
		if err != nil {
			return ""
		}
		if u.Path == "" {
			return "/"
		}
		if !strings.HasPrefix(u.Path, "/") {
			return "/" + u.Path
		}
		return u.Path
	}
	if !strings.HasPrefix(loc, "/") {
		loc = "/" + loc
	}
	return loc
}
