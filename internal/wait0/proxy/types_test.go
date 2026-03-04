package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		ent  Entry
		exp  time.Duration
		want bool
	}{
		{
			name: "fresh",
			ent:  Entry{StoredAt: now.Add(-500 * time.Millisecond).Unix()},
			exp:  2 * time.Second,
			want: false,
		},
		{
			name: "stale",
			ent:  Entry{StoredAt: now.Add(-5 * time.Second).Unix()},
			exp:  time.Second,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStale(tc.ent, tc.exp); got != tc.want {
				t.Fatalf("IsStale() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHasAnyCookie(t *testing.T) {
	tests := []struct {
		name   string
		names  []string
		cookie [2]string
		want   bool
	}{
		{name: "empty names", names: nil, want: false},
		{name: "trim and match", names: []string{" session ", ""}, cookie: [2]string{"session", "1"}, want: true},
		{name: "no match", names: []string{"auth"}, cookie: [2]string{"session", "1"}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://wait0.local", nil)
			if tc.cookie[0] != "" {
				r.AddCookie(&http.Cookie{Name: tc.cookie[0], Value: tc.cookie[1]})
			}
			if got := HasAnyCookie(r, tc.names); got != tc.want {
				t.Fatalf("HasAnyCookie() = %v, want %v", got, tc.want)
			}
		})
	}
}
