package proxy

import (
	"net/http"
	"testing"
)

func TestResponseCacheabilityReason(t *testing.T) {
	tests := []struct {
		name                        string
		request                     http.Header
		response                    http.Header
		allowSharedCacheWithCookies bool
		want                        string
	}{
		{
			name:     "anonymous public response",
			response: http.Header{"Cache-Control": {"public, max-age=60"}},
		},
		{
			name:     "private in a second field value",
			response: http.Header{"Cache-Control": {"max-age=60", "private"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "no cache",
			response: http.Header{"Cache-Control": {"no-cache"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "no store",
			response: http.Header{"Cache-Control": {"no-store"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "zero max age",
			response: http.Header{"Cache-Control": {"public, max-age=\"0\""}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "zero shared max age in any field value",
			response: http.Header{"Cache-Control": {"s-maxage=60", "s-maxage=0"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "invalid max age fails closed",
			response: http.Header{"Cache-Control": {"max-age=soon"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "directive names are not substring matched",
			response: http.Header{"Cache-Control": {"x-private-data=yes, max-age=60"}},
		},
		{
			name:     "set cookie",
			response: http.Header{"Cache-Control": {"public"}, "Set-Cookie": {"session=secret"}},
			want:     CacheabilitySetCookie,
		},
		{
			name:     "vary star",
			response: http.Header{"Vary": {"*"}},
			want:     CacheabilityVary,
		},
		{
			name:     "unkeyed vary header",
			response: http.Header{"Vary": {"Accept-Language"}},
			want:     CacheabilityVary,
		},
		{
			name:     "normalized accept encoding vary",
			response: http.Header{"Vary": {"Accept-Encoding"}},
		},
		{
			name:    "cookie request without opt in",
			request: http.Header{"Cookie": {"session=secret"}},
			want:    CacheabilityCredentials,
		},
		{
			name:                        "cookie request with shared cache opt in",
			request:                     http.Header{"Cookie": {"_ga=analytics"}},
			allowSharedCacheWithCookies: true,
		},
		{
			name:                        "authorization still requires coverage with cookie opt in",
			request:                     http.Header{"Cookie": {"_ga=analytics"}, "Authorization": {"Bearer secret"}},
			allowSharedCacheWithCookies: true,
			want:                        CacheabilityCredentials,
		},
		{
			name:                        "cookie opt in does not override private",
			request:                     http.Header{"Cookie": {"_ga=analytics"}},
			response:                    http.Header{"Cache-Control": {"private"}},
			allowSharedCacheWithCookies: true,
			want:                        CacheabilityCacheControl,
		},
		{
			name:                        "cookie opt in does not override set cookie",
			request:                     http.Header{"Cookie": {"_ga=analytics"}},
			response:                    http.Header{"Set-Cookie": {"session=secret"}},
			allowSharedCacheWithCookies: true,
			want:                        CacheabilitySetCookie,
		},
		{
			name:                        "cookie opt in does not override vary cookie",
			request:                     http.Header{"Cookie": {"_ga=analytics"}},
			response:                    http.Header{"Vary": {"Cookie"}},
			allowSharedCacheWithCookies: true,
			want:                        CacheabilityVary,
		},
		{
			name:     "cookie request explicitly public",
			request:  http.Header{"Cookie": {"session=secret"}},
			response: http.Header{"Cache-Control": {"public, max-age=60"}},
		},
		{
			name:     "public with a value is not an opt in",
			request:  http.Header{"Cookie": {"session=secret"}},
			response: http.Header{"Cache-Control": {"public=yes, max-age=60"}},
			want:     CacheabilityCredentials,
		},
		{
			name:     "cookie request partitioned by cache variant",
			request:  http.Header{"Cookie": {"plan=pro"}},
			response: http.Header{"Vary": {"Cookie"}, "Cache-Variant": {`header('Cookie')`}},
		},
		{
			name:    "authorization request without opt in",
			request: http.Header{"Authorization": {"Bearer secret"}},
			want:    CacheabilityCredentials,
		},
		{
			name:     "authorization request with positive shared max age",
			request:  http.Header{"Authorization": {"Bearer secret"}},
			response: http.Header{"Cache-Control": {"s-maxage=60"}},
		},
		{
			name:     "malformed shared max age is rejected",
			request:  http.Header{"Authorization": {"Bearer secret"}},
			response: http.Header{"Cache-Control": {"s-maxage=+60"}},
			want:     CacheabilityCacheControl,
		},
		{
			name:     "authorization request partitioned by cache variant",
			request:  http.Header{"Authorization": {"Bearer secret"}},
			response: http.Header{"Cache-Variant": {`header('Authorization')`}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResponseCacheabilityReason(tc.request, tc.response, tc.allowSharedCacheWithCookies); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCachedResponseCacheabilityReasonRequiresConcreteVariantMetadata(t *testing.T) {
	request := http.Header{"Cookie": {"session=secret"}}
	header := http.Header{"Cache-Variant": {`header('Cookie')`}}

	legacy := Entry{Header: header}
	if got := CachedResponseCacheabilityReason(request, legacy, false); got != CacheabilityCredentials {
		t.Fatalf("legacy entry reason = %q, want %q", got, CacheabilityCredentials)
	}

	variant := Entry{Header: header, VariantKind: "response"}
	if got := CachedResponseCacheabilityReason(request, variant, false); got != "" {
		t.Fatalf("concrete variant reason = %q, want cacheable", got)
	}
}
