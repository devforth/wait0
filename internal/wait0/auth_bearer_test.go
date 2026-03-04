package wait0

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
	tokens := []authToken{{ID: "a", Token: "tok-a", Scopes: map[string]struct{}{"invalidation:write": {}}}}
	tok, ok := authenticateBearerToken("Bearer tok-a", tokens)
	if !ok {
		t.Fatalf("expected token to authenticate")
	}
	if tok.ID != "a" {
		t.Fatalf("id = %q", tok.ID)
	}

	if _, ok := authenticateBearerToken("Bearer wrong", tokens); ok {
		t.Fatalf("expected wrong token to fail")
	}
}

func TestIsAuthorizedForScope(t *testing.T) {
	tok := authToken{Scopes: map[string]struct{}{"invalidation:write": {}}}
	if !isAuthorizedForScope(tok, "invalidation:write") {
		t.Fatalf("expected scope to authorize")
	}
	if isAuthorizedForScope(tok, "other") {
		t.Fatalf("expected missing scope to fail")
	}
	if !isAuthorizedForScope(tok, "") {
		t.Fatalf("empty required role should allow")
	}
}
