package invalidation

import "testing"

func TestNormalizePath_RejectsQueryOnlyOrFragmentOnly(t *testing.T) {
	bad := []string{"?a=1", "#frag", " ?a=1 ", " #x "}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			if _, err := NormalizePath(in); err == nil {
				t.Fatalf("expected error for input %q", in)
			}
		})
	}
}

func TestNormalizePath_ValidPathInputs(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/a?x=1", want: "/a"},
		{in: "https://example.com/p?q=1", want: "/p"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := NormalizePath(tc.in)
			if err != nil {
				t.Fatalf("NormalizePath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
