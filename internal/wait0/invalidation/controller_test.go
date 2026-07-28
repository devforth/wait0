package invalidation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"wait0/internal/wait0/auth"
	"wait0/internal/wait0/proxy"
)

type fakeRuntime struct {
	mu          sync.Mutex
	tagsByKey   map[string][]string
	present     map[string]bool
	recrawlKind map[string]string
}

func (f *fakeRuntime) CachedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.tagsByKey))
	for k := range f.tagsByKey {
		out = append(out, k)
	}
	return out
}

func (f *fakeRuntime) KeyTags(key string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tagsByKey[key]...)
}

func (f *fakeRuntime) HasKey(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.present[key]
}

func (f *fakeRuntime) DeleteKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.present, key)
}

func (f *fakeRuntime) TargetForKey(key string) Target {
	path, query := proxy.SplitCacheKey(key)
	return Target{Key: key, Path: path, Query: query}
}

func (f *fakeRuntime) RecrawlTarget(_ context.Context, target Target) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.present[target.Key] = true
	if v, ok := f.recrawlKind[target.Key]; ok {
		return v
	}
	return "updated"
}

func TestHandle_Unauthorized(t *testing.T) {
	ctrl := NewController(Config{Enabled: true, QueueSize: 1, WorkerConcurrency: 0, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}, auth.NewAuthenticator(nil), &fakeRuntime{}, make(chan struct{}), nil)

	req := httptest.NewRequest(http.MethodPost, "http://wait0.local"+EndpointPath, strings.NewReader(`{"paths":["/a"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)

	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Result().StatusCode)
	}
}

func TestHandle_Accepts(t *testing.T) {
	authn := auth.NewAuthenticator([]auth.TokenConfig{{ID: "backoffice", Token: "secret", Scopes: []string{WriteScope}}})
	ctrl := NewController(Config{Enabled: true, QueueSize: 1, WorkerConcurrency: 0, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}, authn, &fakeRuntime{}, make(chan struct{}), nil)

	req := httptest.NewRequest(http.MethodPost, "http://wait0.local"+EndpointPath, strings.NewReader(`{"paths":["/a?x=1"],"tags":["t1"]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)

	if w.Result().StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", w.Result().StatusCode)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Fatalf("status field = %v", resp["status"])
	}
}

func TestProcessJob_ByTag(t *testing.T) {
	rt := &fakeRuntime{
		tagsByKey: map[string][]string{"/a": {"t1", "t2"}, "/b": {"t3"}},
		present:   map[string]bool{"/a": true, "/b": true},
	}
	ctrl := NewController(Config{Enabled: true, QueueSize: 1, WorkerConcurrency: 2, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}, auth.NewAuthenticator(nil), rt, make(chan struct{}), nil)
	ctrl.processJob(1, Job{RequestID: "r1", ActorID: "x", Tags: []string{"t1"}})

	if !rt.present["/a"] {
		t.Fatalf("expected /a to be recrawled and present")
	}
	if !rt.present["/b"] {
		t.Fatalf("expected /b to remain present")
	}
}

func TestProcessJob_PathInvalidatesAllQueryVariants(t *testing.T) {
	rt := &fakeRuntime{
		tagsByKey: map[string][]string{
			"/a":        {"t1"},
			"/a?page=1": {"t1"},
			"/b":        {"t2"},
		},
		present: map[string]bool{
			"/a":        true,
			"/a?page=1": true,
			"/b":        true,
		},
	}
	ctrl := NewController(Config{Enabled: true, QueueSize: 1, WorkerConcurrency: 2, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}, auth.NewAuthenticator(nil), rt, make(chan struct{}), nil)

	ctrl.processJob(1, Job{RequestID: "r2", ActorID: "x", Paths: []string{"/a"}})

	if !rt.present["/a"] {
		t.Fatalf("expected /a to be recrawled and present")
	}
	if !rt.present["/a?page=1"] {
		t.Fatalf("expected /a?page=1 to be recrawled and present")
	}
	if !rt.present["/b"] {
		t.Fatalf("expected /b to remain present")
	}
}
