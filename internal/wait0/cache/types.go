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
