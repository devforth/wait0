package wait0

import "net/http"

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	if s.proxy == nil {
		http.NotFound(w, r)
		return
	}
	s.proxy.Handle(w, r)
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
