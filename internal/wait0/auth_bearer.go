package wait0

import (
	"crypto/subtle"
	"strings"
)

type authToken struct {
	ID     string
	Token  string
	Scopes map[string]struct{}
}

func authenticateBearerToken(authHeader string, tokens []authToken) (authToken, bool) {
	token, ok := parseBearerToken(authHeader)
	if !ok {
		return authToken{}, false
	}
	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(t.Token)) == 1 {
			return t, true
		}
	}
	return authToken{}, false
}

func isAuthorizedForScope(token authToken, requiredScope string) bool {
	req := strings.TrimSpace(requiredScope)
	if req == "" {
		return true
	}
	if len(token.Scopes) == 0 {
		return false
	}
	_, ok := token.Scopes[req]
	return ok
}

func parseBearerToken(authHeader string) (token string, ok bool) {
	h := strings.TrimSpace(authHeader)
	if h == "" {
		return "", false
	}
	parts := strings.Fields(h)
	if len(parts) != 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	t := strings.TrimSpace(parts[1])
	if t == "" {
		return "", false
	}
	return t, true
}
