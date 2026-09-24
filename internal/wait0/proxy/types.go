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

	VariantKind        string
	VariantExpressions []string
	VariantFingerprint string
	VariantHeaderNames []string

	VariantBaseKey        string
	VariantValues         []string
	VariantRequestHeaders http.Header
}

type Rule struct {
	Bypass                      bool
	BypassWhenCookies           []string
	AllowSharedCacheWithCookies bool
	BypassWhenRequestHeaders    []string
	CachableContentTypes        []string
	VaryByQueryParams           []string
	Expiration                  time.Duration
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

func HasAnyRequestHeader(r *http.Request, names []string) bool {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, "Host") {
			if r.Host != "" {
				return true
			}
			continue
		}
		for requestName := range r.Header {
			if strings.EqualFold(requestName, name) {
				return true
			}
		}
	}
	return false
}
