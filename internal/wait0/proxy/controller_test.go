package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeRuntime struct {
	handleControl bool
	rule          *Rule

	ramEnt Entry
	ramOK  bool

	diskEnt Entry
	diskOK  bool

	originEnt       Entry
	originCacheable bool
	originStatus    string
	originErr       error

	promoted     []string
	deleted      []string
	stored       []string
	revalidated  []struct{ key, path, query string }
	writeWait0   []string
	writeReasons []string
	debugHeaders DebugHeaderSet
}

func (f *fakeRuntime) HandleControl(http.ResponseWriter, *http.Request) bool {
	return f.handleControl
}

func (f *fakeRuntime) PickRule(string) *Rule { return f.rule }

func (f *fakeRuntime) LoadRAM(string, int64) (Entry, bool) { return f.ramEnt, f.ramOK }

func (f *fakeRuntime) LoadDisk(string) (Entry, bool) { return f.diskEnt, f.diskOK }

func (f *fakeRuntime) PromoteRAM(key string, _ Entry) { f.promoted = append(f.promoted, key) }

func (f *fakeRuntime) DeleteKey(key string) { f.deleted = append(f.deleted, key) }

func (f *fakeRuntime) FetchFromOrigin(*http.Request) (Entry, bool, string, error) {
	return f.originEnt, f.originCacheable, f.originStatus, f.originErr
}

func (f *fakeRuntime) Store(key string, _ Entry) { f.stored = append(f.stored, key) }

func (f *fakeRuntime) RevalidateAsync(key, path, query string) {
	f.revalidated = append(f.revalidated, struct{ key, path, query string }{key: key, path: path, query: query})
}

func (f *fakeRuntime) DebugHeaders() DebugHeaderSet { return f.debugHeaders }

func (f *fakeRuntime) WriteEntryWithStats(w http.ResponseWriter, ent Entry, wait0, reason string) {
	f.writeWait0 = append(f.writeWait0, wait0)
	f.writeReasons = append(f.writeReasons, reason)
	if ent.Status == 0 {
		ent.Status = http.StatusOK
	}
	WriteEntry(w, ent, wait0, reason, f.debugHeaders)
}

func TestController_Handle_ShortCircuitsControl(t *testing.T) {
	rt := &fakeRuntime{handleControl: true}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/page", nil)

	c.Handle(w, r)

	if len(rt.writeWait0) != 0 {
		t.Fatalf("unexpected write calls: %v", rt.writeWait0)
	}
}

func TestController_Handle_BypassPaths(t *testing.T) {
	tests := []struct {
		name   string
		rule   *Rule
		req    *http.Request
		want   string
		reason string
	}{
		{
			name:   "rule bypass",
			rule:   &Rule{Bypass: true},
			req:    httptest.NewRequest(http.MethodGet, "http://wait0.local/a", nil),
			want:   "bypass",
			reason: "bypass-rule",
		},
		{
			name: "cookie bypass",
			rule: &Rule{BypassWhenCookies: []string{"session"}},
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://wait0.local/b", nil)
				r.AddCookie(&http.Cookie{Name: "session", Value: "1"})
				return r
			}(),
			want:   "ignore-by-cookie",
			reason: "bypass-cookie",
		},
		{
			name:   "non get bypass",
			rule:   &Rule{},
			req:    httptest.NewRequest(http.MethodPost, "http://wait0.local/c", nil),
			want:   "bypass",
			reason: "non-get-method",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRuntime{
				rule:      tc.rule,
				originEnt: Entry{Status: http.StatusAccepted, Header: http.Header{}, Body: []byte("ok")},
			}
			c := NewController(rt)
			w := httptest.NewRecorder()

			c.Handle(w, tc.req)

			if len(rt.writeWait0) != 1 || rt.writeWait0[0] != tc.want {
				t.Fatalf("wait0 calls = %v, want [%s]", rt.writeWait0, tc.want)
			}
			if got := w.Result().Header.Get("X-Wait0"); got != tc.want {
				t.Fatalf("X-Wait0 = %q, want %q", got, tc.want)
			}
			if got := w.Result().Header.Get("X-Wait0-Reason"); got != tc.reason {
				t.Fatalf("X-Wait0-Reason = %q, want %q", got, tc.reason)
			}
		})
	}
}

func TestController_Handle_RAMHitAndStaleRevalidation(t *testing.T) {
	ent := Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("cached"), StoredAt: time.Now().Add(-2 * time.Minute).Unix()}
	rt := &fakeRuntime{
		rule:   &Rule{Expiration: time.Second},
		ramEnt: ent,
		ramOK:  true,
	}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/path?q=1", nil)

	c.Handle(w, r)

	if len(rt.writeWait0) != 1 || rt.writeWait0[0] != "hit" {
		t.Fatalf("writeWait0 = %v, want [hit]", rt.writeWait0)
	}
	if len(rt.revalidated) != 1 {
		t.Fatalf("revalidate calls = %d, want 1", len(rt.revalidated))
	}
	call := rt.revalidated[0]
	if call.key != "/path" || call.path != "/path" || call.query != "" {
		t.Fatalf("revalidate call = %+v", call)
	}
}

func TestController_Handle_QueryAwareCacheKeys(t *testing.T) {
	rt := &fakeRuntime{
		rule:            &Rule{VaryByQueryParams: []string{"page"}},
		originEnt:       Entry{Status: http.StatusCreated, Header: http.Header{"Content-Type": {"text/html; charset=utf-8"}}, Body: []byte("origin")},
		originCacheable: true,
		originStatus:    "ok",
	}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/path?page=1&rand=2", nil)

	c.Handle(w, r)

	if len(rt.stored) != 1 || rt.stored[0] != "/path?page=1" {
		t.Fatalf("stored = %v, want [/path?page=1]", rt.stored)
	}
}

func TestController_Handle_QueryAwareRevalidationUsesCanonicalQuery(t *testing.T) {
	ent := Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("cached"), StoredAt: time.Now().Add(-2 * time.Minute).Unix()}
	rt := &fakeRuntime{
		rule:   &Rule{Expiration: time.Second, VaryByQueryParams: []string{"page"}},
		ramEnt: ent,
		ramOK:  true,
	}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/path?page=1&rand=2", nil)

	c.Handle(w, r)

	if len(rt.revalidated) != 1 {
		t.Fatalf("revalidate calls = %d, want 1", len(rt.revalidated))
	}
	call := rt.revalidated[0]
	if call.key != "/path?page=1" || call.path != "/path" || call.query != "page=1" {
		t.Fatalf("revalidate call = %+v", call)
	}
}

func TestController_Handle_DiskHitPromotesRAM(t *testing.T) {
	ent := Entry{Status: http.StatusOK, Header: http.Header{}, Body: []byte("disk")}
	rt := &fakeRuntime{
		diskEnt: ent,
		diskOK:  true,
	}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/disk", nil)

	c.Handle(w, r)

	if len(rt.promoted) != 1 || rt.promoted[0] != "/disk" {
		t.Fatalf("promoted = %v", rt.promoted)
	}
	if len(rt.writeWait0) != 1 || rt.writeWait0[0] != "hit" {
		t.Fatalf("writeWait0 = %v, want [hit]", rt.writeWait0)
	}
}

func TestController_Handle_OriginBranches(t *testing.T) {
	tests := []struct {
		name          string
		cacheable     bool
		statusKind    string
		err           error
		wantCode      int
		wantWait0     string
		wantReason    string
		wantDelete    bool
		wantStore     bool
		wantBodyMatch string
		contentType   string
	}{
		{
			name:          "origin error",
			err:           errors.New("boom"),
			wantCode:      http.StatusBadGateway,
			wantWait0:     "bad-gateway",
			wantReason:    "origin-error",
			wantBodyMatch: "bad gateway",
		},
		{
			name:       "ignore by status",
			statusKind: "ignore-by-status",
			wantCode:   http.StatusNotFound,
			wantWait0:  "ignore-by-status",
			wantReason: "non-cacheable-status",
			wantDelete: true,
		},
		{
			name:       "non cacheable by cache control",
			cacheable:  false,
			statusKind: "ok",
			wantCode:   http.StatusCreated,
			wantWait0:  "bypass",
			wantReason: "non-cacheable-cache-control",
		},
		{
			name:        "non cacheable content type",
			cacheable:   true,
			statusKind:  "ok",
			contentType: "application/json",
			wantCode:    http.StatusCreated,
			wantWait0:   "bypass",
			wantReason:  "non-cacheable-content-type",
		},
		{
			name:        "cacheable miss",
			cacheable:   true,
			statusKind:  "ok",
			contentType: "text/html; charset=utf-8",
			wantCode:    http.StatusCreated,
			wantWait0:   "miss",
			wantStore:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.contentType == "" {
				tc.contentType = "text/html"
			}
			rt := &fakeRuntime{
				originEnt:       Entry{Status: http.StatusCreated, Header: http.Header{"Content-Type": {tc.contentType}}, Body: []byte("origin")},
				originCacheable: tc.cacheable,
				originStatus:    tc.statusKind,
				originErr:       tc.err,
			}
			if tc.statusKind == "ignore-by-status" {
				rt.originEnt.Status = http.StatusNotFound
			}
			c := NewController(rt)
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "http://wait0.local/origin", nil)

			c.Handle(w, r)

			if got := w.Result().StatusCode; got != tc.wantCode {
				t.Fatalf("status = %d, want %d", got, tc.wantCode)
			}
			if tc.wantWait0 != "" {
				if got := w.Result().Header.Get("X-Wait0"); got != tc.wantWait0 {
					t.Fatalf("X-Wait0 = %q, want %q", got, tc.wantWait0)
				}
			}
			if got := w.Result().Header.Get("X-Wait0-Reason"); got != tc.wantReason {
				t.Fatalf("X-Wait0-Reason = %q, want %q", got, tc.wantReason)
			}
			if tc.wantDelete != (len(rt.deleted) == 1) {
				t.Fatalf("deleted = %v", rt.deleted)
			}
			if tc.wantStore != (len(rt.stored) == 1) {
				t.Fatalf("stored = %v", rt.stored)
			}
			if tc.wantBodyMatch != "" && !strings.Contains(w.Body.String(), tc.wantBodyMatch) {
				t.Fatalf("body %q does not contain %q", w.Body.String(), tc.wantBodyMatch)
			}
		})
	}
}

func TestController_Handle_OriginErrorWithDebugHeadersDisabled(t *testing.T) {
	rt := &fakeRuntime{
		originErr:    errors.New("boom"),
		debugHeaders: NewDebugHeaderSet([]string{}),
	}
	c := NewController(rt)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://wait0.local/origin", nil)

	c.Handle(w, r)

	if got := w.Result().StatusCode; got != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", got, http.StatusBadGateway)
	}
	for _, name := range DefaultDebugHeaders() {
		if got := w.Result().Header.Get(name); got != "" {
			t.Fatalf("disabled header %s = %q", name, got)
		}
	}
}
