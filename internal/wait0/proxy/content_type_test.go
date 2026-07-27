package proxy

import "testing"

func TestNormalizeCachableContentTypes(t *testing.T) {
	got, err := NormalizeCachableContentTypes([]string{
		" Text/HTML; Charset=UTF-8 ",
		"text/html",
		"application/json",
	})
	if err != nil {
		t.Fatalf("NormalizeCachableContentTypes: %v", err)
	}
	want := []string{"text/html", "application/json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	defaults, err := NormalizeCachableContentTypes(nil)
	if err != nil {
		t.Fatalf("default NormalizeCachableContentTypes: %v", err)
	}
	if len(defaults) != 2 || defaults[0] != "text/html" || defaults[1] != "application/xhtml+xml" {
		t.Fatalf("defaults = %v", defaults)
	}
}

func TestIsCachableContentType(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		allowed []string
		want    bool
	}{
		{name: "default html with charset", value: "text/html; charset=utf-8", want: true},
		{name: "default xhtml", value: "APPLICATION/XHTML+XML", want: true},
		{name: "default rejects json", value: "application/json", want: false},
		{name: "custom allows json", value: "application/json; charset=utf-8", allowed: []string{"Application/JSON; profile=test"}, want: true},
		{name: "missing content type", value: "", want: false},
		{name: "invalid content type", value: "not a content type", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCachableContentType(tc.value, tc.allowed); got != tc.want {
				t.Fatalf("IsCachableContentType(%q, %v) = %v, want %v", tc.value, tc.allowed, got, tc.want)
			}
		})
	}
}
