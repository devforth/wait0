package wait0

import "net/http"

type CacheEntry struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt int64 // unix seconds
	Hash32   uint32

	// RevalidatedAt is the last time this entry was fetched/validated against the
	// origin (including warmups). Stored as unix nanoseconds in UTC.
	RevalidatedAt int64

	// RevalidatedBy indicates what triggered the last revalidation.
	// Expected values: "user" | "warmup".
	RevalidatedBy string
}
