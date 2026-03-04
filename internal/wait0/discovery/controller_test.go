package discovery

import "testing"

func TestNormalizePathFromLoc(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "https://example.com/a/b?q=1", want: "/a/b"},
		{in: "/x", want: "/x"},
		{in: "x", want: "/x"},
		{in: "https://example.com", want: "/"},
	}

	for _, tc := range tests {
		if got := NormalizePathFromLoc(tc.in); got != tc.want {
			t.Fatalf("NormalizePathFromLoc(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
