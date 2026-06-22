package proxy

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

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
