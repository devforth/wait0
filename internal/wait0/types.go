package wait0

import "net/http"

type CacheEntry struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt int64 // unix seconds
	Hash32   uint32
}
