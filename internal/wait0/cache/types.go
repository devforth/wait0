package cache

import "net/http"

type Entry struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt int64
	Hash32   uint32

	Inactive bool

	DiscoveredBy  string
	RevalidatedAt int64
	RevalidatedBy string
}

type EntryMeta struct {
	// Size is logical response size in bytes (headers + body).
	Size int64

	Inactive     bool
	DiscoveredBy string

	// LastRefreshUnixNano is unix nanos timestamp of latest refresh.
	// May be zero for legacy entries.
	LastRefreshUnixNano int64

	// StoredAtUnix is unix seconds timestamp.
	StoredAtUnix int64
}

func EntryLogicalSize(ent Entry) int64 {
	total := int64(len(ent.Body))
	for k, vals := range ent.Header {
		total += int64(len(k))
		for _, v := range vals {
			total += int64(len(v))
		}
	}
	return total
}
