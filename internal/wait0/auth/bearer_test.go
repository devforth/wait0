package auth

import "testing"

func TestParseBearerToken(t *testing.T) {
	tok, ok := parseBearerToken("Bearer abc123")
	if !ok {
		t.Fatalf("expected bearer token to parse")
	}
	if tok != "abc123" {
		t.Fatalf("token = %q", tok)
	}

	if _, ok := parseBearerToken("Basic abc123"); ok {
		t.Fatalf("expected non-bearer header to fail")
	}
	if tok, ok := parseBearerToken("bearer zyx987"); !ok || tok != "zyx987" {
		t.Fatalf("expected case-insensitive bearer scheme parsing")
	}
}

func TestAuthenticateBearerToken(t *testing.T) {
	a := NewAuthenticator([]TokenConfig{{ID: "a", Token: "tok-a", Scopes: []string{"invalidation:write"}}})
	tok, ok := a.AuthenticateBearer("Bearer tok-a")
	if !ok {
		t.Fatalf("expected token to authenticate")
	}
	if tok.ID != "a" {
		t.Fatalf("id = %q", tok.ID)
	}

	if _, ok := a.AuthenticateBearer("Bearer wrong"); ok {
		t.Fatalf("expected wrong token to fail")
	}
}

func TestAuthorizedForScope(t *testing.T) {
	tok := Principal{Scopes: map[string]struct{}{"invalidation:write": {}}}
	if !AuthorizedForScope(tok, "invalidation:write") {
		t.Fatalf("expected scope to authorize")
	}
	if AuthorizedForScope(tok, "other") {
		t.Fatalf("expected missing scope to fail")
	}
	if !AuthorizedForScope(tok, "") {
		t.Fatalf("empty required role should allow")
	}
}
