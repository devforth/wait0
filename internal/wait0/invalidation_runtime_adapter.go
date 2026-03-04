package wait0

import (
	"context"
	"strings"

	"wait0/internal/wait0/invalidation"
)

type invalidationRuntimeAdapter struct {
	s *Service
}

func newInvalidationRuntimeAdapter(s *Service) invalidation.Runtime {
	return &invalidationRuntimeAdapter{s: s}
}

func (a *invalidationRuntimeAdapter) CachedKeys() []string {
	ram := a.s.ram.Keys()
	disk := a.s.disk.Keys()
	seen := make(map[string]struct{}, len(ram)+len(disk))
	out := make([]string, 0, len(ram)+len(disk))
	for _, k := range ram {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	for _, k := range disk {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func (a *invalidationRuntimeAdapter) KeyTags(key string) []string {
	ent, ok := a.peekCacheEntry(key)
	if !ok {
		return nil
	}
	vals := ent.Header.Values("X-Wait0-Tag")
	if len(vals) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(vals))
	for _, raw := range vals {
		for _, part := range strings.Split(raw, ",") {
			t := strings.TrimSpace(part)
			if t == "" {
				continue
			}
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	return out
}

func (a *invalidationRuntimeAdapter) HasKey(key string) bool {
	if _, ok := a.s.ram.Peek(key); ok {
		return true
	}
	if _, ok := a.s.disk.Peek(key); ok {
		return true
	}
	return false
}

func (a *invalidationRuntimeAdapter) DeleteKey(key string) {
	a.s.ram.Delete(key)
	a.s.disk.Delete(key)
}

func (a *invalidationRuntimeAdapter) RecrawlKey(ctx context.Context, key string) string {
	if a.s.reval == nil {
		return "error"
	}
	return a.s.reval.Once(ctx, key, key, "", "invalidate").Kind
}

func (a *invalidationRuntimeAdapter) peekCacheEntry(key string) (CacheEntry, bool) {
	if ent, ok := a.s.ram.Peek(key); ok {
		return ent, true
	}
	if ent, ok := a.s.disk.Peek(key); ok {
		return ent, true
	}
	return CacheEntry{}, false
}
