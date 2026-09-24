package proxy

import (
	"net/http"
	"strconv"
	"strings"

	"wait0/internal/wait0/cachevariant"
)

const (
	CacheabilityCacheControl = "non-cacheable-cache-control"
	CacheabilitySetCookie    = "non-cacheable-set-cookie"
	CacheabilityVary         = "non-cacheable-vary"
	CacheabilityCredentials  = "non-cacheable-request-credentials"
)

var cacheabilityVariantEngine = cachevariant.NewEngine()

// ResponseCacheabilityReason returns an empty string when an origin response
// is safe to put in the shared cache. It deliberately fails closed for origin
// signals wait0 cannot honor.
func ResponseCacheabilityReason(requestHeaders, responseHeaders http.Header, allowSharedCacheWithCookies bool) string {
	return responseCacheabilityReason(requestHeaders, responseHeaders, true, allowSharedCacheWithCookies)
}

// CachedResponseCacheabilityReason also verifies that a Cache-Variant response
// was actually stored as a concrete variant. This prevents an entry created by
// an older or inconsistent cache implementation from treating the header alone
// as proof that its cache key was partitioned.
func CachedResponseCacheabilityReason(requestHeaders http.Header, ent Entry, allowSharedCacheWithCookies bool) string {
	return responseCacheabilityReason(requestHeaders, ent.Header, ent.VariantKind == "response", allowSharedCacheWithCookies)
}

func responseCacheabilityReason(requestHeaders, responseHeaders http.Header, allowCacheVariant, allowSharedCacheWithCookies bool) string {
	directives := parseCacheControl(responseHeaders.Values("Cache-Control"))
	for _, name := range []string{"private", "no-store", "no-cache"} {
		if _, ok := directives[name]; ok {
			return CacheabilityCacheControl
		}
	}
	for _, name := range []string{"max-age", "s-maxage"} {
		for _, value := range directives[name] {
			seconds, err := parseDeltaSeconds(value)
			if err != nil || seconds == 0 {
				return CacheabilityCacheControl
			}
		}
	}

	if headerPresent(responseHeaders, "Set-Cookie") {
		return CacheabilitySetCookie
	}

	varyNames, invalidVary := parseVary(responseHeaders.Values("Vary"))
	requestHasCookie := headerPresent(requestHeaders, "Cookie")
	requestHasAuthorization := headerPresent(requestHeaders, "Authorization")
	public := hasBareDirective(directives, "public")
	_, sharedMaxAge := directives["s-maxage"]
	cookieNeedsCoverage := requestHasCookie && !allowSharedCacheWithCookies && !public && !sharedMaxAge
	authorizationNeedsCoverage := requestHasAuthorization && !public && !sharedMaxAge
	credentialsNeedCoverage := cookieNeedsCoverage || authorizationNeedsCoverage
	needsVariantHeaders := invalidVary || credentialsNeedCoverage
	for _, name := range varyNames {
		if !normalizedOriginRequestHeader(name) {
			needsVariantHeaders = true
		}
	}

	covered := map[string]struct{}{}
	if needsVariantHeaders && allowCacheVariant {
		covered = cacheVariantHeaderNames(responseHeaders)
	}

	if invalidVary {
		return CacheabilityVary
	}
	for _, name := range varyNames {
		if normalizedOriginRequestHeader(name) {
			continue
		}
		if _, ok := covered[strings.ToLower(name)]; !ok {
			return CacheabilityVary
		}
	}

	// The cookie rule can opt into shared caching for pages whose HTML does not
	// depend on cookies. Authorization still requires origin opt-in or a variant.
	if cookieNeedsCoverage {
		if _, ok := covered["cookie"]; !ok {
			return CacheabilityCredentials
		}
	}
	if authorizationNeedsCoverage {
		if _, ok := covered["authorization"]; !ok {
			return CacheabilityCredentials
		}
	}

	return ""
}

func cacheVariantHeaderNames(headers http.Header) map[string]struct{} {
	covered := map[string]struct{}{}
	sources, err := cachevariant.Expressions(headers)
	if err != nil || len(sources) == 0 {
		return covered
	}
	set, err := cacheabilityVariantEngine.Compile(sources)
	if err != nil {
		return covered
	}
	for _, name := range set.HeaderNames {
		covered[strings.ToLower(name)] = struct{}{}
	}
	return covered
}

func normalizedOriginRequestHeader(name string) bool {
	// These values do not vary with the inbound request: FetchFromOrigin fixes
	// Accept-Encoding to identity and the configured origin supplies Host.
	return strings.EqualFold(name, "Accept-Encoding") || strings.EqualFold(name, "Host")
}

func parseVary(values []string) ([]string, bool) {
	var names []string
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" || name == "*" {
				return nil, true
			}
			for i := 0; i < len(name); i++ {
				if !isTokenByte(name[i]) {
					return nil, true
				}
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			names = append(names, name)
		}
	}
	return names, false
}

func parseCacheControl(values []string) map[string][]string {
	directives := make(map[string][]string)
	for _, value := range values {
		for _, part := range splitHeaderList(value) {
			name, directiveValue, hasValue := strings.Cut(part, "=")
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if hasValue {
				directives[name] = append(directives[name], strings.TrimSpace(directiveValue))
			} else {
				directives[name] = append(directives[name], "")
			}
		}
	}
	return directives
}

func splitHeaderList(value string) []string {
	var parts []string
	start := 0
	inQuotes := false
	escaped := false
	for i := 0; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case inQuotes && value[i] == '\\':
			escaped = true
		case value[i] == '"':
			inQuotes = !inQuotes
		case value[i] == ',' && !inQuotes:
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func unquoteDirectiveValue(value string) string {
	value = strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(value); err == nil {
		return unquoted
	}
	return value
}

func parseDeltaSeconds(value string) (uint64, error) {
	value = unquoteDirectiveValue(value)
	if value == "" {
		return 0, strconv.ErrSyntax
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

func hasBareDirective(directives map[string][]string, name string) bool {
	for _, value := range directives[name] {
		if value == "" {
			return true
		}
	}
	return false
}

func headerPresent(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func isTokenByte(b byte) bool {
	if b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
