package cache

import (
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// volatileHeaders name response headers that carry a different value on every
// origin response even when the response itself is identical. They are left out
// of the shape fingerprint: including them would make every warmup store look
// like a change, which is exactly what the fingerprint exists to detect.
//
// Both are per-response freshness metadata rather than content. The cost of
// excluding them is that a blob whose body stops changing keeps the Date it was
// stored with, which only surfaces on a disk read, and reads only happen after
// the RAM tier has evicted the key or the process restarted.
var volatileHeaders = map[string]struct{}{
	"date": {},
	"age":  {},
}

// entryShapeHash fingerprints everything a stored entry must reproduce except
// its body, which Entry.Hash32 and the body length cover, and the freshness
// stamps, which diskMeta mirrors so they can be refreshed without rewriting the
// body.
//
// Fields are length-prefixed and header names sorted so the result depends only
// on content. Hashing an encoded entry instead would not work: gob walks maps in
// randomised order, so http.Header would produce a different encoding on every
// call and no store would ever be recognised as redundant.
func entryShapeHash(ent Entry) uint32 {
	var b strings.Builder
	writeHashField(&b, strconv.Itoa(ent.Status))
	writeHashField(&b, strconv.FormatBool(ent.Inactive))
	writeHashField(&b, ent.DiscoveredBy)
	writeHashHeader(&b, ent.Header)
	if ent.Variant != nil {
		writeHashField(&b, ent.Variant.Kind)
		writeHashField(&b, ent.Variant.Fingerprint)
		writeHashField(&b, ent.Variant.BaseKey)
		writeHashSlice(&b, ent.Variant.Expressions)
		writeHashSlice(&b, ent.Variant.HeaderNames)
		writeHashSlice(&b, ent.Variant.Values)
		writeHashHeader(&b, ent.Variant.RequestHeaders)
	}
	return crc32.ChecksumIEEE([]byte(b.String()))
}

// blobUpToDate reports whether the "e:" record already stored under a key holds
// exactly the response the incoming entry describes, so the store only has to
// refresh the "m:" metadata record.
//
// Warmup re-stores every cached URL on every loop, and virtually all of those
// stores carry a byte-identical response, so without this the whole cache is
// rewritten to LevelDB every loop for nothing.
//
// A zero body hash never matches. Variant manifests and URL-persister seeds
// carry no body, and CRC32 of an empty body is zero, so accepting that as a
// match would pin their first written blob in place permanently.
func blobUpToDate(old diskMeta, bodyHash uint32, bodyLen int64, shape uint32) bool {
	if old.Size <= 0 || bodyHash == 0 {
		return false
	}
	return old.BodyHash == bodyHash && old.BodyLen == bodyLen && old.ShapeHash == shape
}

func writeHashField(b *strings.Builder, value string) {
	b.WriteString(strconv.Itoa(len(value)))
	b.WriteByte(':')
	b.WriteString(value)
}

func writeHashSlice(b *strings.Builder, values []string) {
	writeHashField(b, strconv.Itoa(len(values)))
	for _, value := range values {
		writeHashField(b, value)
	}
}

// writeHashHeader writes a canonical form of h. The name count and per-name
// value counts are included so that no regrouping of the same characters across
// different names can produce the same bytes.
func writeHashHeader(b *strings.Builder, h http.Header) {
	names := make([]string, 0, len(h))
	for name := range h {
		if _, skip := volatileHeaders[strings.ToLower(name)]; skip {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	writeHashField(b, strconv.Itoa(len(names)))
	for _, name := range names {
		writeHashField(b, name)
		writeHashSlice(b, h[name])
	}
}
