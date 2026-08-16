package wait0

import (
	"net/http"
	"sync"
	"time"

	"wait0/internal/wait0/cachevariant"
	"wait0/internal/wait0/proxy"
)

const (
	variantKindManifest = "manifest"
	variantKindResponse = "response"
	variantLockCount    = 64
)

type variantState struct {
	engine *cachevariant.Engine

	mu       sync.RWMutex
	families map[string]map[string]struct{}
	locks    [variantLockCount]sync.Mutex
}

func newVariantState() *variantState {
	return &variantState{
		engine:   cachevariant.NewEngine(),
		families: make(map[string]map[string]struct{}),
	}
}

func (s *Service) rebuildVariantFamilies() {
	if s.variants == nil {
		s.variants = newVariantState()
	}
	seen := make(map[string]struct{})
	for _, key := range s.ram.Keys() {
		seen[key] = struct{}{}
	}
	for _, key := range s.disk.Keys() {
		seen[key] = struct{}{}
	}
	s.variants.mu.Lock()
	defer s.variants.mu.Unlock()
	for key := range seen {
		base, ok := proxy.SplitVariantCacheKey(key)
		if !ok {
			continue
		}
		children := s.variants.families[base]
		if children == nil {
			children = make(map[string]struct{})
			s.variants.families[base] = children
		}
		children[key] = struct{}{}
	}
}

func (s *Service) variantLock(baseKey string) *sync.Mutex {
	var hash uint32 = 2166136261
	for i := 0; i < len(baseKey); i++ {
		hash ^= uint32(baseKey[i])
		hash *= 16777619
	}
	return &s.variants.locks[int(hash%variantLockCount)]
}

func (s *Service) variantChildren(baseKey string) []string {
	s.variants.mu.RLock()
	children := s.variants.families[baseKey]
	out := make([]string, 0, len(children))
	for key := range children {
		out = append(out, key)
	}
	s.variants.mu.RUnlock()
	return out
}

func (s *Service) addVariantChild(baseKey, childKey string) {
	s.variants.mu.Lock()
	children := s.variants.families[baseKey]
	if children == nil {
		children = make(map[string]struct{})
		s.variants.families[baseKey] = children
	}
	children[childKey] = struct{}{}
	s.variants.mu.Unlock()
}

func (s *Service) replaceVariantChildren(baseKey, childKey string) []string {
	s.variants.mu.Lock()
	old := s.variants.families[baseKey]
	out := make([]string, 0, len(old))
	for key := range old {
		if key != childKey {
			out = append(out, key)
		}
	}
	if childKey == "" {
		delete(s.variants.families, baseKey)
	} else {
		s.variants.families[baseKey] = map[string]struct{}{childKey: {}}
	}
	s.variants.mu.Unlock()
	return out
}

func (s *Service) removeVariantChild(baseKey, childKey string) int {
	s.variants.mu.Lock()
	children := s.variants.families[baseKey]
	delete(children, childKey)
	n := len(children)
	if n == 0 {
		delete(s.variants.families, baseKey)
	}
	s.variants.mu.Unlock()
	return n
}

func (s *Service) putRawCacheEntry(key string, ent CacheEntry) {
	s.ram.Put(key, ent, s.disk, s.overflowLog)
	s.disk.PutAsync(key, ent)
}

func (s *Service) deleteRawCacheEntry(key string) {
	s.ram.Delete(key)
	s.disk.Delete(key)
}

func (s *Service) peekCacheEntry(key string) (CacheEntry, bool) {
	if ent, ok := s.ram.Peek(key); ok {
		return ent, true
	}
	return s.disk.Peek(key)
}

func (s *Service) resolveVariant(manifest CacheEntry, headers http.Header, host string) (string, []string, http.Header, error) {
	set, err := s.variants.engine.Compile(manifest.VariantExpressions)
	if err != nil {
		return "", nil, nil, err
	}
	result, err := s.variants.engine.Evaluate(set, headers, host)
	if err != nil {
		return "", nil, nil, err
	}
	baseKey := manifest.VariantBaseKey
	if baseKey == "" {
		return "", nil, nil, nil
	}
	key := proxy.JoinVariantCacheKey(baseKey, set.Fingerprint, result.Values)
	return key, result.Values, result.RequestHeaders, nil
}

func (s *Service) storeCacheableResponse(baseKey string, headers http.Header, host string, ent CacheEntry) (string, []string, error) {
	sources, err := cachevariant.Expressions(ent.Header)
	if err != nil {
		return "", nil, err
	}

	lock := s.variantLock(baseKey)
	lock.Lock()
	defer lock.Unlock()

	current, hasCurrent := s.peekCacheEntry(baseKey)
	if len(sources) == 0 {
		ent.VariantKind = ""
		ent.VariantExpressions = nil
		ent.VariantFingerprint = ""
		ent.VariantHeaderNames = nil
		ent.VariantBaseKey = ""
		ent.VariantValues = nil
		ent.VariantRequestHeaders = nil
		s.putRawCacheEntry(baseKey, ent)
		for _, child := range s.replaceVariantChildren(baseKey, "") {
			s.deleteRawCacheEntry(child)
		}
		s.notePersistedStore(baseKey, baseKey, ent)
		return baseKey, nil, nil
	}

	set, err := s.variants.engine.Compile(sources)
	if err != nil {
		return "", nil, err
	}
	result, err := s.variants.engine.Evaluate(set, headers, host)
	if err != nil {
		return "", nil, err
	}

	childKey := proxy.JoinVariantCacheKey(baseKey, set.Fingerprint, result.Values)
	child := ent
	child.VariantKind = variantKindResponse
	child.VariantExpressions = nil
	child.VariantFingerprint = set.Fingerprint
	child.VariantHeaderNames = nil
	child.VariantBaseKey = baseKey
	child.VariantValues = append([]string(nil), result.Values...)
	child.VariantRequestHeaders = proxy.CloneHeader(result.RequestHeaders)

	manifest := CacheEntry{
		StoredAt:           time.Now().Unix(),
		Inactive:           false,
		DiscoveredBy:       ent.DiscoveredBy,
		RevalidatedAt:      ent.RevalidatedAt,
		RevalidatedBy:      ent.RevalidatedBy,
		VariantKind:        variantKindManifest,
		VariantExpressions: append([]string(nil), set.Sources...),
		VariantFingerprint: set.Fingerprint,
		VariantHeaderNames: append([]string(nil), set.HeaderNames...),
		VariantBaseKey:     baseKey,
	}

	s.putRawCacheEntry(childKey, child)
	s.notePersistedStore(childKey, baseKey, child)
	if hasCurrent && current.VariantKind == variantKindManifest && current.VariantFingerprint == set.Fingerprint {
		s.addVariantChild(baseKey, childKey)
		return childKey, result.Values, nil
	}

	s.putRawCacheEntry(baseKey, manifest)
	for _, oldChild := range s.replaceVariantChildren(baseKey, childKey) {
		s.deleteRawCacheEntry(oldChild)
	}
	return childKey, result.Values, nil
}

func (s *Service) deleteCacheKey(key string) {
	s.notePersistedDelete(key)
	baseKey, isChild := proxy.SplitVariantCacheKey(key)
	if !isChild {
		if ent, ok := s.peekCacheEntry(key); ok && ent.VariantKind == variantKindManifest {
			s.deleteVariantFamily(key)
			return
		}
		s.deleteRawCacheEntry(key)
		return
	}

	lock := s.variantLock(baseKey)
	lock.Lock()
	defer lock.Unlock()
	s.deleteRawCacheEntry(key)
	if s.removeVariantChild(baseKey, key) == 0 {
		s.deleteRawCacheEntry(baseKey)
	}
}

func (s *Service) deleteVariantFamily(baseKey string) {
	lock := s.variantLock(baseKey)
	lock.Lock()
	defer lock.Unlock()
	s.deleteRawCacheEntry(baseKey)
	for _, child := range s.replaceVariantChildren(baseKey, "") {
		s.deleteRawCacheEntry(child)
	}
}
