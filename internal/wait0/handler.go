package wait0

import (
	"net/http"
	"strings"
	"time"
)

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == invalidationEndpointPath {
		s.handleInvalidateAPI(w, r)
		return
	}
	key := path

	rule := s.pickRule(path)
	if rule != nil {
		if rule.Bypass {
			s.proxyPass(w, r, "bypass")
			return
		}
		if hasAnyCookie(r, rule.BypassWhenCookies) {
			s.proxyPass(w, r, "ignore-by-cookie")
			return
		}
	}

	if r.Method != http.MethodGet {
		s.proxyPass(w, r, "bypass")
		return
	}

	now := time.Now().Unix()
	if ent, ok := s.ram.Get(key, now); ok {
		if !ent.Inactive {
			s.writeEntryWithStats(w, ent, "hit")
			if rule != nil && rule.expDur > 0 && isStale(ent, rule.expDur) {
				s.revalidateAsync(key, r, rule)
			}
			return
		}
	}

	if ent, ok := s.disk.Get(key); ok {
		if !ent.Inactive {
			s.ram.Put(key, ent, s.disk, s.overflowLog)
			s.writeEntryWithStats(w, ent, "hit")
			if rule != nil && rule.expDur > 0 && isStale(ent, rule.expDur) {
				s.revalidateAsync(key, r, rule)
			}
			return
		}
	}

	// miss
	respEnt, cacheable, statusKind, err := s.fetchFromOrigin(r)
	if err != nil {
		setWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if statusKind == "ignore-by-status" {
		s.ram.Delete(key)
		s.disk.Delete(key)
		s.writeEntryWithStats(w, respEnt, "ignore-by-status")
		return
	}
	if !cacheable {
		s.writeEntryWithStats(w, respEnt, "bypass")
		return
	}

	s.store(key, respEnt)
	s.writeEntryWithStats(w, respEnt, "miss")
}

func (s *Service) pickRule(path string) *Rule {
	for i := range s.cfg.Rules {
		r := &s.cfg.Rules[i]
		if r.Matches(path) {
			return r
		}
	}
	return nil
}

func isStale(ent CacheEntry, exp time.Duration) bool {
	stored := time.Unix(ent.StoredAt, 0)
	return time.Since(stored) > exp
}

func hasAnyCookie(r *http.Request, names []string) bool {
	if len(names) == 0 {
		return false
	}
	need := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			need[n] = struct{}{}
		}
	}
	for _, c := range r.Cookies() {
		if _, ok := need[c.Name]; ok {
			return true
		}
	}
	return false
}

func (s *Service) proxyPass(w http.ResponseWriter, r *http.Request, wait0 string) {
	ent, _, _, err := s.fetchFromOrigin(r)
	if err != nil {
		setWait0Headers(w.Header(), "bad-gateway")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	s.writeEntryWithStats(w, ent, wait0)
}

func (s *Service) writeEntryWithStats(w http.ResponseWriter, ent CacheEntry, wait0 string) {
	writeEntry(w, ent, wait0)
	if s.stats != nil {
		switch wait0 {
		case "hit", "miss":
			s.stats.Observe(len(ent.Body))
		}
	}
}
