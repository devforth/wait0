package wait0

import (
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Service) fetchFromOrigin(r *http.Request) (CacheEntry, bool, string, error) {
	originURL := s.cfg.Server.Origin + r.URL.RequestURI()
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return CacheEntry{}, false, "", err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return CacheEntry{}, false, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CacheEntry{}, false, "", err
	}

	now := time.Now().UTC()
	ent := CacheEntry{
		Status:       resp.StatusCode,
		Header:       cloneHeader(resp.Header),
		Body:         body,
		StoredAt:     now.Unix(),
		Inactive:     false,
		DiscoveredBy: "user",
		// Initial fill is considered a user-triggered revalidation.
		RevalidatedAt: now.UnixNano(),
		RevalidatedBy: "user",
	}
	ent.Header.Del("Content-Length")
	ent.Hash32 = crc32.ChecksumIEEE(body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ent, false, "ignore-by-status", nil
	}

	cc := strings.ToLower(resp.Header.Get("Cache-Control"))
	cacheable := true
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		cacheable = false
	}
	return ent, cacheable, "ok", nil
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
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

func (s *Service) store(key string, ent CacheEntry) {
	s.ram.Put(key, ent, s.disk, s.overflowLog)
	// also persist to disk for durability (async)
	s.disk.PutAsync(key, ent)
}
