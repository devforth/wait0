package revalidation

import (
	"net/http"
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

type Result struct {
	OK      bool
	Changed bool
	Dur     time.Duration

	URI  string
	Path string

	Kind string
	Err  string
}

type Target struct {
	Key              string
	Path             string
	Query            string
	Headers          http.Header
	Host             string
	PreserveExisting bool
}

type WarmRule struct {
	ID                   int
	Match                string
	PauseBetweenRuns     time.Duration
	WarmMax              int
	Matches              func(path string) bool
	PresetHeaderNames    []string
	RequestHeaderPresets map[string][]string
}

type WarmupURLMetric struct {
	URL      string
	Duration time.Duration
}

type WarmupSummary struct {
	RuleID     int
	Match      string
	URLs       int
	Took       time.Duration
	FinishedAt time.Time
	RPS        float64
	MinRT      time.Duration
	AvgRT      time.Duration
	MaxRT      time.Duration

	Unchanged           int
	Updated             int
	Deleted             int
	IgnoredStatus       int
	IgnoredCacheControl int
	IgnoredContentType  int
	Errors              int

	TopSlowest []WarmupURLMetric
	TopFastest []WarmupURLMetric
}
