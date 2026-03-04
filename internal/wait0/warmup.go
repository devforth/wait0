package wait0

import "wait0/internal/wait0/revalidation"

type warmupSummary struct {
	match string
	urls  int
}

func (s *Service) warmupGroupLoop(rule *Rule) {
	if s.reval == nil || rule == nil {
		return
	}
	s.reval.WarmupGroupLoop(revalidation.WarmRule{
		Match:     rule.Match,
		WarmEvery: rule.warmEvery,
		WarmMax:   rule.warmMax,
		Matches:   rule.Matches,
	})
}

func (s *Service) keysByLastAccessDesc(rule *Rule) []string {
	if s.reval == nil || rule == nil {
		return nil
	}
	return s.reval.KeysByLastAccessDesc(revalidation.WarmRule{
		Match:   rule.Match,
		Matches: rule.Matches,
	})
}

func (s *Service) snapshotAccessTimes() map[string]int64 {
	if s.reval == nil {
		return nil
	}
	return newRevalidationRuntimeAdapter(s).SnapshotAccessTimes()
}

func (s *Service) allKeysSnapshot() []string {
	if s.reval == nil {
		return nil
	}
	return s.reval.AllKeysSnapshot()
}
