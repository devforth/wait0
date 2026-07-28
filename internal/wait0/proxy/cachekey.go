package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const variantCacheKeyPrefix = "\x00wait0:variant:v1:"

func NormalizeVaryByQueryParams(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for i, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("[%d]: must not be empty", i)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func CanonicalCacheQuery(rawQuery string, varyByQueryParams []string) string {
	if rawQuery == "" || len(varyByQueryParams) == 0 {
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil && len(values) == 0 {
		return ""
	}

	parts := make([]string, 0, len(varyByQueryParams))
	for _, name := range varyByQueryParams {
		vals, ok := values[name]
		if !ok {
			continue
		}
		vals = append([]string(nil), vals...)
		sort.Strings(vals)

		escapedName := url.QueryEscape(name)
		for _, val := range vals {
			if val == "" {
				parts = append(parts, escapedName)
				continue
			}
			parts = append(parts, escapedName+"="+url.QueryEscape(val))
		}
	}

	return strings.Join(parts, "&")
}

func JoinCacheKey(path, cacheQuery string) string {
	if cacheQuery == "" {
		return path
	}
	return path + "?" + cacheQuery
}

func SplitCacheKey(key string) (path string, cacheQuery string) {
	if base, ok := SplitVariantCacheKey(key); ok {
		key = base
	}
	idx := strings.IndexByte(key, '?')
	if idx < 0 {
		return key, ""
	}
	return key[:idx], key[idx+1:]
}

func CacheKeyPath(key string) string {
	path, _ := SplitCacheKey(key)
	return path
}

func JoinVariantCacheKey(baseKey, generation string, values []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:", len(generation))
	_, _ = h.Write([]byte(generation))
	for _, value := range values {
		fmt.Fprintf(h, "%d:", len(value))
		_, _ = h.Write([]byte(value))
	}
	digest := hex.EncodeToString(h.Sum(nil))
	return variantCacheKeyPrefix + strconv.Itoa(len(baseKey)) + ":" + baseKey + ":" + digest
}

func SplitVariantCacheKey(key string) (string, bool) {
	if !strings.HasPrefix(key, variantCacheKeyPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(key, variantCacheKeyPrefix)
	colon := strings.IndexByte(rest, ':')
	if colon <= 0 {
		return "", false
	}
	n, err := strconv.Atoi(rest[:colon])
	if err != nil || n < 0 {
		return "", false
	}
	rest = rest[colon+1:]
	if len(rest) < n+1 || rest[n] != ':' {
		return "", false
	}
	return rest[:n], true
}

func BaseCacheKey(key string) string {
	if base, ok := SplitVariantCacheKey(key); ok {
		return base
	}
	return key
}
