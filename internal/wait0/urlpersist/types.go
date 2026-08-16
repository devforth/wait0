// Package urlpersist keeps a durable list of URLs that were successfully
// cached, so a wiped cache can be repopulated from the URLs traffic actually
// used instead of relying on a sitemap.
package urlpersist

import (
	"time"

	"wait0/internal/wait0/proxy"
)

// FileVersion is written to the persisted file and rejected when it does not
// match on load.
const FileVersion = 1

type Logger interface {
	Printf(format string, v ...any)
}

type Config struct {
	Enabled bool
	File    string
	Origin  string

	FlushEvery     time.Duration
	RestoreOnStart bool
	MaxURLs        int

	// ForgetAfterFailures is how many failures a record tolerates before it is
	// dropped. Warmup contributes at most one per process, because a deleted
	// entry never re-enters the access-time index it draws targets from; client
	// requests to a dead URL contribute one each. Counting rather than dropping
	// on the first failure is what stops a brief origin outage from erasing the
	// file mid-warmup. Zero drops the record on the first failure.
	ForgetAfterFailures int
}

// Variant carries everything needed to replay one cache variant. The headers
// are the projection cachevariant.Evaluate produced, which already contains
// exactly the headers the expressions reference and nothing else.
type Variant struct {
	Fingerprint string              `yaml:"fingerprint"`
	Values      []string            `yaml:"values"`
	Headers     map[string][]string `yaml:"headers,omitempty"`
}

// Record is one persisted URL identity. It holds no response body: the file
// remembers which URLs exist, never what they returned.
type Record struct {
	Path  string `yaml:"path"`
	Query string `yaml:"query,omitempty"`

	LastSeen int64 `yaml:"lastSeen"`
	Seen     int64 `yaml:"seen"`

	DiscoveredBy string `yaml:"discoveredBy,omitempty"`
	Failures     int    `yaml:"failures,omitempty"`

	Variant *Variant `yaml:"variant,omitempty"`
}

// BaseCacheKey is the key of the URL ignoring variants.
func (r Record) BaseCacheKey() string {
	return proxy.JoinCacheKey(r.Path, r.Query)
}

// CacheKey is the key this record occupies in the cache: the variant child key
// when the record describes a variant, the base key otherwise.
func (r Record) CacheKey() string {
	base := r.BaseCacheKey()
	if r.Variant == nil {
		return base
	}
	return proxy.JoinVariantCacheKey(base, r.Variant.Fingerprint, r.Variant.Values)
}

func (r Record) clone() Record {
	out := r
	if r.Variant != nil {
		v := &Variant{
			Fingerprint: r.Variant.Fingerprint,
			Values:      append([]string(nil), r.Variant.Values...),
		}
		if r.Variant.Headers != nil {
			v.Headers = make(map[string][]string, len(r.Variant.Headers))
			for name, values := range r.Variant.Headers {
				v.Headers[name] = append([]string(nil), values...)
			}
		}
		out.Variant = v
	}
	return out
}

// Runtime is the slice of the service urlpersist needs.
type Runtime interface {
	// Seed materializes an inactive placeholder entry so the existing warmup
	// machinery discovers the URL. It reports false when no rule matches the
	// path or the matching rule bypasses the cache.
	Seed(rec Record) bool

	// SnapshotAccessTimes returns the last-access time of every cached key.
	SnapshotAccessTimes() map[string]int64
}

// Stats is the snapshot exposed through the stats API.
type Stats struct {
	Records       int   `json:"records"`
	Restored      int   `json:"restored"`
	LastFlushUnix int64 `json:"last_flush_unix"`
}
