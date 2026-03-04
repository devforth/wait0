package proxy

import (
	"net/http"
	"strings"
	"time"
)

type Entry struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt int64
	Hash32   uint32

	Inactive bool

	DiscoveredBy string

	RevalidatedAt int64
	RevalidatedBy string
}

type Rule struct {
	Bypass            bool
	BypassWhenCookies []string
	Expiration        time.Duration
}

func IsStale(ent Entry, exp time.Duration) bool {
	stored := time.Unix(ent.StoredAt, 0)
	return time.Since(stored) > exp
}

func HasAnyCookie(r *http.Request, names []string) bool {
	if len(names) == 0 {
		return false
	}
	need := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			need[n] = struct{}{}
		}
	}
	for _, c := range r.Cookies() {
		if _, ok := need[c.Name]; ok {
			return true
		}
	}
	return false
}
