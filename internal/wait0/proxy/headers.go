package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	DebugHeaderCacheStatus       = "X-Wait0"
	DebugHeaderReason            = "X-Wait0-Reason"
	DebugHeaderRevalidatedAt     = "X-Wait0-Revalidated-At"
	DebugHeaderRevalidatedBy     = "X-Wait0-Revalidated-By"
	DebugHeaderDiscoveredBy      = "X-Wait0-Discovered-By"
	DebugHeaderRevalidateAt      = "X-Wait0-Revalidate-At"
	DebugHeaderRevalidateEntropy = "X-Wait0-Revalidate-Entropy"
)

var supportedDebugHeaders = []string{
	DebugHeaderCacheStatus,
	DebugHeaderReason,
	DebugHeaderRevalidatedAt,
	DebugHeaderRevalidatedBy,
	DebugHeaderDiscoveredBy,
	DebugHeaderRevalidateAt,
	DebugHeaderRevalidateEntropy,
}

var managedDebugHeaders = map[string]struct{}{
	strings.ToLower(DebugHeaderCacheStatus):       {},
	strings.ToLower(DebugHeaderReason):            {},
	strings.ToLower(DebugHeaderRevalidatedAt):     {},
	strings.ToLower(DebugHeaderRevalidatedBy):     {},
	strings.ToLower(DebugHeaderDiscoveredBy):      {},
	strings.ToLower(DebugHeaderRevalidateAt):      {},
	strings.ToLower(DebugHeaderRevalidateEntropy): {},
}

// DebugHeaderSet is immutable after service construction. A nil set enables all
// supported headers, matching an omitted logging.debug_headers configuration.
type DebugHeaderSet map[string]struct{}

func DefaultDebugHeaders() []string {
	return append([]string(nil), supportedDebugHeaders...)
}

func NormalizeDebugHeaders(names []string) ([]string, error) {
	if names == nil {
		return DefaultDebugHeaders(), nil
	}

	supported := make(map[string]string, len(supportedDebugHeaders))
	for _, name := range supportedDebugHeaders {
		supported[strings.ToLower(name)] = name
	}

	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for i, raw := range names {
		key := strings.ToLower(strings.TrimSpace(raw))
		name, ok := supported[key]
		if !ok {
			return nil, fmt.Errorf("item %d: unsupported debug header %q", i, raw)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

func NewDebugHeaderSet(names []string) DebugHeaderSet {
	if names == nil {
		return nil
	}
	out := make(DebugHeaderSet, len(names))
	for _, name := range names {
		out[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	return out
}

func (s DebugHeaderSet) Enabled(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if s == nil {
		for _, supported := range supportedDebugHeaders {
			if strings.EqualFold(supported, key) {
				return true
			}
		}
		return false
	}
	_, ok := s[key]
	return ok
}

func WriteEntry(w http.ResponseWriter, ent Entry, wait0, reason string, configured ...DebugHeaderSet) {
	debugHeaders := selectedDebugHeaders(configured)
	for k, vs := range ent.Header {
		if isManagedDebugHeader(k) {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	SetWait0Headers(w.Header(), wait0, reason, debugHeaders)
	setWait0DiscoveredHeaders(w.Header(), ent, debugHeaders)
	if wait0 == "hit" {
		setWait0RevalidatedHeaders(w.Header(), ent, debugHeaders)
	}
	w.WriteHeader(ent.Status)
	_, _ = w.Write(ent.Body)
}

func SetWait0Headers(h http.Header, wait0, reason string, configured ...DebugHeaderSet) {
	debugHeaders := selectedDebugHeaders(configured)
	h.Del(DebugHeaderCacheStatus)
	if wait0 != "" && debugHeaders.Enabled(DebugHeaderCacheStatus) {
		h.Set(DebugHeaderCacheStatus, wait0)
		ensureExposedHeader(h, DebugHeaderCacheStatus)
	}

	h.Del(DebugHeaderReason)
	if reason != "" && debugHeaders.Enabled(DebugHeaderReason) {
		h.Set(DebugHeaderReason, reason)
		ensureExposedHeader(h, DebugHeaderReason)
	}
}

func setWait0RevalidatedHeaders(h http.Header, ent Entry, debugHeaders DebugHeaderSet) {
	by := strings.TrimSpace(ent.RevalidatedBy)
	if by == "" {
		by = "user"
	}

	ts := ent.RevalidatedAt
	if ts == 0 && ent.StoredAt != 0 {
		ts = time.Unix(ent.StoredAt, 0).UTC().UnixNano()
	}
	if ts != 0 {
		if debugHeaders.Enabled(DebugHeaderRevalidatedAt) {
			h.Set(DebugHeaderRevalidatedAt, time.Unix(0, ts).UTC().Format(time.RFC3339Nano))
			ensureExposedHeader(h, DebugHeaderRevalidatedAt)
		}
		if debugHeaders.Enabled(DebugHeaderRevalidatedBy) {
			h.Set(DebugHeaderRevalidatedBy, by)
			ensureExposedHeader(h, DebugHeaderRevalidatedBy)
		}
	}
}

func setWait0DiscoveredHeaders(h http.Header, ent Entry, debugHeaders DebugHeaderSet) {
	v := strings.TrimSpace(ent.DiscoveredBy)
	if v == "" || !debugHeaders.Enabled(DebugHeaderDiscoveredBy) {
		return
	}
	h.Set(DebugHeaderDiscoveredBy, v)
	ensureExposedHeader(h, DebugHeaderDiscoveredBy)
}

func selectedDebugHeaders(configured []DebugHeaderSet) DebugHeaderSet {
	if len(configured) == 0 {
		return nil
	}
	return configured[0]
}

func isManagedDebugHeader(name string) bool {
	_, ok := managedDebugHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok
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
