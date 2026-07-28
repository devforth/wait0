package cachevariant

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const deviceExpression = `"header('User-Agent') matches '(?i)(Android.*Mobile|iPhone|iPod|IEMobile|Windows Phone|Opera Mini)' ? 'mobile' : 'desktop'"`
const countryExpression = `"let c = header('CF-IPCountry', 'XX'); c == 'CA' && header('CF-Region-Code') == 'ON' ? 'CA-ON' : c"`

func TestEngine_UserExamplesAsWritten(t *testing.T) {
	headers := http.Header{}
	headers.Add(HeaderName, deviceExpression)
	headers.Add(HeaderName, countryExpression)

	sources, err := Expressions(headers)
	if err != nil {
		t.Fatalf("Expressions: %v", err)
	}
	engine := NewEngine()
	set, err := engine.Compile(sources)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	tests := []struct {
		name    string
		headers http.Header
		want    []string
	}{
		{
			name: "mobile Ontario",
			headers: http.Header{
				"User-Agent":     {"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0)"},
				"Cf-Ipcountry":   {"CA"},
				"Cf-Region-Code": {"ON"},
			},
			want: []string{"mobile", "CA-ON"},
		},
		{
			name: "desktop Canada",
			headers: http.Header{
				"User-Agent":     {"Mozilla/5.0 (X11; Linux x86_64)"},
				"Cf-Ipcountry":   {"CA"},
				"Cf-Region-Code": {"QC"},
			},
			want: []string{"desktop", "CA"},
		},
		{
			name:    "missing country uses default",
			headers: http.Header{"User-Agent": {"Android Mobile"}},
			want:    []string{"mobile", "XX"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Evaluate(set, tc.headers, "")
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if !reflect.DeepEqual(result.Values, tc.want) {
				t.Fatalf("values = %v, want %v", result.Values, tc.want)
			}
		})
	}
}

func TestEngine_MissingRequiredHeaderFails(t *testing.T) {
	engine := NewEngine()
	set, err := engine.Compile([]string{`header('CF-IPCountry')`})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := engine.Evaluate(set, http.Header{}, ""); err == nil || !strings.Contains(err.Error(), "required request header") {
		t.Fatalf("Evaluate error = %v", err)
	}
}

func TestEngine_ProjectsSensitiveReferencedHeaders(t *testing.T) {
	engine := NewEngine()
	set, err := engine.Compile([]string{`header('Cookie') contains 'plan=pro' ? 'pro' : 'free'`})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	result, err := engine.Evaluate(set, http.Header{"Cookie": {"plan=pro; session=secret"}, "X-Irrelevant": {"drop-me"}}, "")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got := result.RequestHeaders.Get("Cookie"); got != "plan=pro; session=secret" {
		t.Fatalf("projected Cookie = %q", got)
	}
	if got := result.RequestHeaders.Get("X-Irrelevant"); got != "" {
		t.Fatalf("projected irrelevant header = %q", got)
	}
}

func TestEngine_RequiresLiteralHeaderArgumentsAndStringResult(t *testing.T) {
	engine := NewEngine()
	for _, source := range []string{
		`header(lower('X-Test'))`,
		`header('X-Test', lower('fallback'))`,
		`42`,
	} {
		if _, err := engine.Compile([]string{source}); err == nil {
			t.Fatalf("Compile(%q) succeeded, want error", source)
		}
	}
}

func TestExpressions_PreservesHeaderOrder(t *testing.T) {
	h := http.Header{}
	h.Add(HeaderName, `"first"`)
	h.Add(HeaderName, `"second"`)

	got, err := Expressions(h)
	if err != nil {
		t.Fatalf("Expressions: %v", err)
	}
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expressions = %v, want %v", got, want)
	}
}
