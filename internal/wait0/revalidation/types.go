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

type WarmRule struct {
	Match     string
	WarmEvery time.Duration
	WarmMax   int
	Matches   func(path string) bool
}

type WarmupSummary struct {
	Match string
	URLs  int
	Took  time.Duration
	RPS   float64
	MinRT time.Duration
	AvgRT time.Duration
	MaxRT time.Duration

	Unchanged           int
	Updated             int
	Deleted             int
	IgnoredStatus       int
	IgnoredCacheControl int
	Errors              int
}
