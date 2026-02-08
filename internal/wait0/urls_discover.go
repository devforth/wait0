package wait0

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type sitemapDoc struct {
	URLs     []string `xml:"url>loc"`
	Sitemaps []string `xml:"sitemap>loc"`
}

func (s *Service) startURLsDiscover() {
	if len(s.cfg.URLsDiscover.Sitemaps) == 0 {
		return
	}

	initDelay := s.cfg.URLsDiscover.initialDelayDur
	period := s.cfg.URLsDiscover.rediscoverEveryDur

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		if initDelay > 0 {
			select {
			case <-s.stopCh:
				return
			case <-time.After(initDelay):
			}
		}

		runOnce := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			stored, ignored, err := s.discoverURLsOnce(ctx)
			if err != nil {
				log.Printf("urlsDiscover: error: %v", err)
				return
			}
			log.Printf("urlsDiscover: stored=%d ignored=%d", stored, ignored)
		}

		runOnce()
		if period <= 0 {
			return
		}

		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				runOnce()
			}
		}
	}()
}

func (s *Service) discoverURLsOnce(ctx context.Context) (stored int, ignored int, _ error) {
	seenSitemaps := map[string]struct{}{}
	queue := make([]string, 0, len(s.cfg.URLsDiscover.Sitemaps))
	for _, sm := range s.cfg.URLsDiscover.Sitemaps {
		sm = strings.TrimSpace(sm)
		if sm == "" {
			continue
		}
		queue = append(queue, s.normalizeMaybeRelativeURL(sm))
	}

	for len(queue) > 0 {
		select {
		case <-ctx.Done():
			return stored, ignored, ctx.Err()
		case <-s.stopCh:
			return stored, ignored, nil
		default:
		}

		smURL := queue[0]
		queue = queue[1:]
		if _, ok := seenSitemaps[smURL]; ok {
			continue
		}
		seenSitemaps[smURL] = struct{}{}

		doc, err := s.fetchAndParseSitemap(ctx, smURL)
		if err != nil {
			return stored, ignored, fmt.Errorf("fetch sitemap %q: %w", smURL, err)
		}

		for _, nested := range doc.Sitemaps {
			nested = strings.TrimSpace(nested)
			if nested == "" {
				continue
			}
			queue = append(queue, s.normalizeMaybeRelativeURL(nested))
		}

		fit := 0
		ignoredThis := 0
		for _, loc := range doc.URLs {
			path := normalizePathFromLoc(loc)
			if path == "" {
				ignoredThis++
				continue
			}

			rule := s.pickRule(path)
			if rule == nil || rule.Bypass {
				ignoredThis++
				ignored++
				continue
			}
			fit++

			// Don't overwrite active content; only seed if missing or inactive.
			if ent, ok := s.ram.Peek(path); ok && !ent.Inactive {
				continue
			}
			if ent, ok := s.disk.Peek(path); ok && !ent.Inactive {
				continue
			}

			now := time.Now().UTC()
			seed := CacheEntry{
				Status:       http.StatusOK,
				Header:       make(http.Header),
				Body:         []byte{},
				StoredAt:     now.Unix(),
				Hash32:       0,
				Inactive:     true,
				DiscoveredBy: "sitemap",
				// Leave RevalidatedAt/RevalidatedBy empty; warmup/user fill will set them.
			}
			s.disk.PutAsync(path, seed)
			stored++
		}

		if s.cfg.Logging.LogURLAutodiscover {
			log.Printf(
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

func (s *Service) normalizeMaybeRelativeURL(u string) string {
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
	return s.cfg.Server.Origin + u
}

func (s *Service) fetchAndParseSitemap(ctx context.Context, sitemapURL string) (sitemapDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sitemapURL, nil)
	if err != nil {
		return sitemapDoc{}, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return sitemapDoc{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return sitemapDoc{}, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return sitemapDoc{}, err
	}

	// Handle .gz or gzip magic header.
	// Be tolerant: some servers may serve a .gz URL but also apply Content-Encoding gzip,
	// in which case Go may already decompress the body.
	tryGzip := strings.HasSuffix(strings.ToLower(sitemapURL), ".gz") || (len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b)
	if tryGzip {
		if gz, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			defer gz.Close()
			if unzipped, err := io.ReadAll(gz); err == nil {
				body = unzipped
			}
		}
	}

	var doc sitemapDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return sitemapDoc{}, err
	}

	// Some sitemaps may include whitespace/newlines.
	for i := range doc.URLs {
		doc.URLs[i] = strings.TrimSpace(doc.URLs[i])
	}
	for i := range doc.Sitemaps {
		doc.Sitemaps[i] = strings.TrimSpace(doc.Sitemaps[i])
	}

	return doc, nil
}

func normalizePathFromLoc(loc string) string {
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
	// Relative locs: treat as a path.
	if !strings.HasPrefix(loc, "/") {
		loc = "/" + loc
	}
	return loc
}
