package proxy

import (
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"time"
)

type Fetcher struct {
	Client *http.Client
	Origin string
}

func (f Fetcher) FetchFromOrigin(r *http.Request) (Entry, bool, string, error) {
	originURL := f.Origin + r.URL.RequestURI()
	ctx := r.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, originURL, nil)
	if err != nil {
		return Entry{}, false, "", err
	}
	CopyHeaders(req.Header, r.Header)
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := f.Client.Do(req)
	if err != nil {
		return Entry{}, false, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Entry{}, false, "", err
	}

	now := time.Now().UTC()
	ent := Entry{
		Status:       resp.StatusCode,
		Header:       CloneHeader(resp.Header),
		Body:         body,
		StoredAt:     now.Unix(),
		Inactive:     false,
		DiscoveredBy: "user",

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

func CopyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func CloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		vv := make([]string, len(vs))
		copy(vv, vs)
		out[k] = vv
	}
	return out
}
