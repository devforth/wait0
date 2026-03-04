package wait0

import (
	"context"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"
)

type revalResult struct {
	ok      bool
	changed bool
	dur     time.Duration

	uri  string
	path string

	kind string // "unchanged" | "updated" | "deleted" | "ignored-status" | "ignored-cache-control" | "error"
	err  string
}

func (s *Service) revalidateAsync(key string, r *http.Request, rule *Rule) {
	select {
	case s.bgSem <- struct{}{}:
		// ok
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	path := r.URL.Path
	query := r.URL.RawQuery

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.bgSem }()
		defer cancel()

		_ = s.revalidateOnce(ctx, key, path, query, "user")
	}()
}

func (s *Service) revalidateOnce(ctx context.Context, key, path, query, by string) revalResult {
	start := time.Now()
	cur, hasCur := s.ram.Peek(key)
	if !hasCur {
		cur, hasCur = s.disk.Peek(key)
	}

	discoveredBy := "user"
	if hasCur {
		if v := strings.TrimSpace(cur.DiscoveredBy); v != "" {
			discoveredBy = v
		}
	}

	uri := path
	if query != "" {
		uri = uri + "?" + query
	}
	originURL := s.cfg.Server.Origin + uri

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return revalResult{ok: false, changed: false, dur: time.Since(start), uri: uri, path: path, kind: "error", err: err.Error()}
	}

	if s.sendRevalidateMarkers {
		req.Header.Set("X-Wait0-Revalidate-At", time.Now().UTC().Format(time.RFC3339Nano))
		req.Header.Set("X-Wait0-Revalidate-Entropy", randomString(8))
	}
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return revalResult{ok: false, changed: false, dur: time.Since(start), uri: uri, path: path, kind: "error", err: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return revalResult{ok: false, changed: false, dur: time.Since(start), uri: uri, path: path, kind: "error", err: err.Error()}
	}

	res := revalResult{
		ok:      true,
		changed: false,
		dur:     time.Since(start),
		uri:     uri,
		path:    path,
		kind:    "",
		err:     "",
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if hasCur {
			s.ram.Delete(key)
			s.disk.Delete(key)
			res.changed = true
			res.kind = "deleted"
		} else {
			res.kind = "ignored-status"
		}
		return res
	}

	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	cacheable := true
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		cacheable = false
	}
	if !cacheable {
		if hasCur {
			s.ram.Delete(key)
			s.disk.Delete(key)
			res.changed = true
			res.kind = "deleted"
		} else {
			res.kind = "ignored-cache-control"
		}
		return res
	}

	now := time.Now().UTC()
	newEnt := CacheEntry{
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
		res.kind = "unchanged"
		if s.unchangedLog != nil {
			s.unchangedLog.Printf("Revalidate unchanged: path=%q uri=%q", path, uri)
		}
		// Content is identical, but we still refresh StoredAt and revalidation
		// metadata so the entry doesn't remain stale forever.
		s.ram.Put(key, newEnt, s.disk, s.overflowLog)
		s.disk.PutAsync(key, newEnt)
		return res
	}

	s.ram.Put(key, newEnt, s.disk, s.overflowLog)
	s.disk.PutAsync(key, newEnt)
	res.changed = true
	res.kind = "updated"
	return res
}
