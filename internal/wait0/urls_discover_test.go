package wait0

import "testing"

func TestNormalizePathFromLoc(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "absolute", in: "https://example.com/a/b?q=1", want: "/a/b"},
		{name: "relative with slash", in: "/x", want: "/x"},
		{name: "relative no slash", in: "x", want: "/x"},
		{name: "root", in: "https://example.com", want: "/"},
	}

	for _, tc := range tests {
		got := normalizePathFromLoc(tc.in)
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeMaybeRelativeURL(t *testing.T) {
	s := newTestService(t, "https://origin.example", nil)

	if got := s.normalizeMaybeRelativeURL("/sitemap.xml"); got != "https://origin.example/sitemap.xml" {
		t.Fatalf("got %q", got)
	}
	if got := s.normalizeMaybeRelativeURL("https://other.example/sitemap.xml"); got != "https://other.example/sitemap.xml" {
		t.Fatalf("got %q", got)
	}
}
