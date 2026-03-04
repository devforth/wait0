package wait0

import (
	"net/http"
	"strings"
	"time"
)

func writeEntry(w http.ResponseWriter, ent CacheEntry, wait0 string) {
	for k, vs := range ent.Header {
		if strings.EqualFold(k, "x-wait0") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	setWait0Headers(w.Header(), wait0)
	setWait0DiscoveredHeaders(w.Header(), ent)
	if wait0 == "hit" {
		setWait0RevalidatedHeaders(w.Header(), ent)
	}
	w.WriteHeader(ent.Status)
	_, _ = w.Write(ent.Body)
}

func setWait0Headers(h http.Header, wait0 string) {
	if wait0 != "" {
		h.Set("X-Wait0", wait0)
	}
	ensureExposedHeader(h, "X-Wait0")
}

func setWait0RevalidatedHeaders(h http.Header, ent CacheEntry) {
	const (
		hAt = "X-Wait0-Revalidated-At"
		hBy = "X-Wait0-Revalidated-By"
	)

	by := strings.TrimSpace(ent.RevalidatedBy)
	if by == "" {
		by = "user"
	}

	ts := ent.RevalidatedAt
	if ts == 0 && ent.StoredAt != 0 {
		ts = time.Unix(ent.StoredAt, 0).UTC().UnixNano()
	}
	if ts != 0 {
		h.Set(hAt, time.Unix(0, ts).UTC().Format(time.RFC3339Nano))
		h.Set(hBy, by)
		ensureExposedHeader(h, hAt)
		ensureExposedHeader(h, hBy)
	}
}

func setWait0DiscoveredHeaders(h http.Header, ent CacheEntry) {
	const name = "X-Wait0-Discovered-By"
	v := strings.TrimSpace(ent.DiscoveredBy)
	if v == "" {
		return
	}
	h.Set(name, v)
	ensureExposedHeader(h, name)
}

func ensureExposedHeader(h http.Header, name string) {
	if name == "" {
		return
	}

	const expose = "Access-Control-Expose-Headers"
	cur := h.Values(expose)
	if len(cur) == 0 {
		h.Set(expose, name)
		return
	}

	merged := strings.Join(cur, ",")
	for _, part := range strings.Split(merged, ",") {
		if strings.EqualFold(strings.TrimSpace(part), name) {
			return
		}
	}

	h.Set(expose, strings.TrimSpace(merged)+", "+name)
}
