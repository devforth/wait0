package wait0

import (
	"context"
	"net/http"
	"time"
)

type revalResult struct {
	ok      bool
	changed bool
	dur     time.Duration

	uri  string
	path string

	kind string
	err  string
}

func (s *Service) revalidateAsync(key string, r *http.Request, rule *Rule) {
	if s.reval == nil {
		return
	}
	s.reval.Async(key, r.URL.Path, r.URL.RawQuery, "user")
}

func (s *Service) revalidateOnce(ctx context.Context, key, path, query, by string) revalResult {
	if s.reval == nil {
		return revalResult{ok: false, kind: "error", err: "revalidation controller is not initialized"}
	}
	res := s.reval.Once(ctx, key, path, query, by)
	return revalResult{
		ok:      res.OK,
		changed: res.Changed,
		dur:     res.Dur,
		uri:     res.URI,
		path:    res.Path,
		kind:    res.Kind,
		err:     res.Err,
	}
}
