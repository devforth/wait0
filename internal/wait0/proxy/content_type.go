package proxy

import (
	"fmt"
	"mime"
	"strings"
)

var defaultCachableContentTypes = []string{
	"text/html",
	"application/xhtml+xml",
}

func DefaultCachableContentTypes() []string {
	return append([]string(nil), defaultCachableContentTypes...)
}

func NormalizeCachableContentTypes(values []string) ([]string, error) {
	if len(values) == 0 {
		return DefaultCachableContentTypes(), nil
	}

	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("entry %d is empty", i)
		}
		mediaType, _, err := mime.ParseMediaType(value)
		if err != nil {
			return nil, fmt.Errorf("entry %d %q: %w", i, value, err)
		}
		mediaType = strings.ToLower(mediaType)
		if _, ok := seen[mediaType]; ok {
			continue
		}
		seen[mediaType] = struct{}{}
		out = append(out, mediaType)
	}
	return out, nil
}

func IsCachableContentType(value string, allowed []string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)

	if len(allowed) == 0 {
		allowed = defaultCachableContentTypes
	}
	for _, candidate := range allowed {
		candidateType, _, err := mime.ParseMediaType(strings.TrimSpace(candidate))
		if err == nil && strings.EqualFold(candidateType, mediaType) {
			return true
		}
	}
	return false
}
