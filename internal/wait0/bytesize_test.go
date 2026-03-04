package wait0

import "testing"

func TestParseBytes_Valid(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{in: "100", want: 100},
		{in: "1k", want: 1024},
		{in: "1kb", want: 1024},
		{in: "1.5m", want: 1572864},
		{in: "2g", want: 2147483648},
	}

	for _, tc := range tests {
		got, err := parseBytes(tc.in)
		if err != nil {
			t.Fatalf("parseBytes(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseBytes_Invalid(t *testing.T) {
	tests := []string{"", "-1", "bad", "mb"}
	for _, in := range tests {
		if _, err := parseBytes(in); err == nil {
			t.Fatalf("parseBytes(%q) expected error", in)
		}
	}
}
