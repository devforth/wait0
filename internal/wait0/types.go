package wait0

import "net/http"

type CacheEntry struct {
	Status   int
	Header   http.Header
	Body     []byte
	StoredAt int64 // unix seconds
	Hash32   uint32

	// Inactive entries are never served from cache, but are kept so warmup can
	// revalidate and fill them without needing a prior user request.
	Inactive bool

	// DiscoveredBy indicates how this URL entered the system.
	// Expected values: "user" | "sitemap".
	DiscoveredBy string

	// RevalidatedAt is the last time this entry was fetched/validated against the
	// origin (including warmups). Stored as unix nanoseconds in UTC.
	RevalidatedAt int64

	// RevalidatedBy indicates what triggered the last revalidation.
	// Expected values: "user" | "warmup" | "invalidate".
	RevalidatedBy string

	// VariantKind is empty for ordinary responses, "manifest" for a base-key
	// routing record, and "response" for a concrete variant child.
	VariantKind        string
	VariantExpressions []string
	VariantFingerprint string
	VariantHeaderNames []string

	VariantBaseKey        string
	VariantValues         []string
	VariantRequestHeaders http.Header
}
