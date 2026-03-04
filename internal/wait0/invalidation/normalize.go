package invalidation

import (
	"fmt"
	"net/url"
	"strings"
)

func NormalizePaths(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for i, raw := range in {
		norm, err := NormalizePath(raw)
		if err != nil {
			return nil, fmt.Errorf("paths[%d]: %w", i, err)
		}
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out, nil
}

func NormalizePath(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}

	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		u, err := url.Parse(v)
		if err != nil {
			return "", fmt.Errorf("invalid URL")
		}
		if u.Path == "" {
			return "/", nil
		}
		if !strings.HasPrefix(u.Path, "/") {
			return "/" + u.Path, nil
		}
		return u.Path, nil
	}

	u, err := url.ParseRequestURI(v)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	p := u.Path
	if p == "" {
		if u.RawQuery != "" || u.Fragment != "" || strings.HasPrefix(v, "?") || strings.HasPrefix(v, "#") {
			return "", fmt.Errorf("query-only or fragment-only path is not allowed")
		}
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p, nil
}

func NormalizeTags(in []string) ([]string, error) {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for i, raw := range in {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, "\r\n") {
			return nil, fmt.Errorf("tags[%d]: contains invalid control characters", i)
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}
