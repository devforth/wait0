package wait0

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleInvalidateAPI_Unauthorized(t *testing.T) {
	s := newTestService(t, "http://origin.local", nil)
	s.invCfg = InvalidationConfig{Enabled: true, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}
	s.invQueue = make(chan invalidateJob, 1)
	s.invTokens = []authToken{{ID: "backoffice", Token: "secret", Scopes: map[string]struct{}{invalidationWriteScope: {}}}}

	req := httptest.NewRequest(http.MethodPost, "http://wait0.local/wait0/invalidate", http.NoBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleInvalidateAPI(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Result().StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleInvalidateAPI_AcceptsAndQueuesJob(t *testing.T) {
	s := newTestService(t, "http://origin.local", nil)
	s.invCfg = InvalidationConfig{Enabled: true, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}
	s.invQueue = make(chan invalidateJob, 1)
	s.invTokens = []authToken{{ID: "backoffice", Token: "secret", Scopes: map[string]struct{}{invalidationWriteScope: {}}}}

	body := `{"paths":["/a?x=1","https://site.local/b#f"," /a "],"tags":["t1"," t1 ","t2"]}`
	req := httptest.NewRequest(http.MethodPost, "http://wait0.local/wait0/invalidate", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleInvalidateAPI(w, req)
	if w.Result().StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Result().StatusCode, http.StatusAccepted)
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Fatalf("status field = %v", resp["status"])
	}

	select {
	case job := <-s.invQueue:
		if len(job.Paths) != 2 {
			t.Fatalf("queued paths = %d, want 2", len(job.Paths))
		}
		if len(job.Tags) != 2 {
			t.Fatalf("queued tags = %d, want 2", len(job.Tags))
		}
		if job.Paths[0] != "/a" || job.Paths[1] != "/b" {
			t.Fatalf("queued paths = %#v", job.Paths)
		}
	default:
		t.Fatalf("expected queued invalidation job")
	}
}

func TestHandleInvalidateAPI_QueueFull(t *testing.T) {
	s := newTestService(t, "http://origin.local", nil)
	s.invCfg = InvalidationConfig{Enabled: true, MaxBodyBytes: 4096, MaxPaths: 10, MaxTags: 10}
	s.invQueue = make(chan invalidateJob, 1)
	s.invQueue <- invalidateJob{RequestID: "existing"}
	s.invTokens = []authToken{{ID: "backoffice", Token: "secret", Scopes: map[string]struct{}{invalidationWriteScope: {}}}}

	req := httptest.NewRequest(http.MethodPost, "http://wait0.local/wait0/invalidate", strings.NewReader(`{"paths":["/a"]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.handleInvalidateAPI(w, req)
	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Result().StatusCode, http.StatusServiceUnavailable)
	}
}
