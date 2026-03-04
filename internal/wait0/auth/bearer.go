package auth

import (
	"crypto/subtle"
	"strings"
)

type TokenConfig struct {
	ID     string
	Token  string
	Scopes []string
}

type Principal struct {
	ID     string
	Scopes map[string]struct{}
}

type token struct {
	id     string
	token  string
	scopes map[string]struct{}
}

type Authenticator struct {
	tokens []token
}

func NewAuthenticator(configs []TokenConfig) *Authenticator {
	out := make([]token, 0, len(configs))
	for _, c := range configs {
		scopes := make(map[string]struct{}, len(c.Scopes))
		for _, s := range c.Scopes {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			scopes[s] = struct{}{}
		}
		out = append(out, token{id: c.ID, token: c.Token, scopes: scopes})
	}
	return &Authenticator{tokens: out}
}

func (a *Authenticator) AuthenticateBearer(authHeader string) (Principal, bool) {
	if a == nil {
		return Principal{}, false
	}
	tok, ok := parseBearerToken(authHeader)
	if !ok {
		return Principal{}, false
	}
	for _, t := range a.tokens {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(t.token)) == 1 {
			return Principal{ID: t.id, Scopes: t.scopes}, true
		}
	}
	return Principal{}, false
}

func AuthorizedForScope(p Principal, requiredScope string) bool {
	req := strings.TrimSpace(requiredScope)
	if req == "" {
		return true
	}
	if len(p.Scopes) == 0 {
		return false
	}
	_, ok := p.Scopes[req]
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
