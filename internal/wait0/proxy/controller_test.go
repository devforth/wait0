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

	promoted    []string
	deleted     []string
	stored      []string
	revalidated []struct{ key, path, query string }
	writeWait0  []string
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

func (f *fakeRuntime) WriteEntryWithStats(w http.ResponseWriter, ent Entry, wait0 string) {
	f.writeWait0 = append(f.writeWait0, wait0)
	if ent.Status == 0 {
		ent.Status = http.StatusOK
	}
	WriteEntry(w, ent, wait0)
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
		name string
		rule *Rule
		req  *http.Request
		want string
	}{
		{
			name: "rule bypass",
			rule: &Rule{Bypass: true},
			req:  httptest.NewRequest(http.MethodGet, "http://wait0.local/a", nil),
			want: "bypass",
		},
		{
			name: "cookie bypass",
			rule: &Rule{BypassWhenCookies: []string{"session"}},
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "http://wait0.local/b", nil)
				r.AddCookie(&http.Cookie{Name: "session", Value: "1"})
				return r
			}(),
			want: "ignore-by-cookie",
		},
		{
			name: "non get bypass",
			rule: &Rule{},
			req:  httptest.NewRequest(http.MethodPost, "http://wait0.local/c", nil),
			want: "bypass",
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
	if call.key != "/path" || call.path != "/path" || call.query != "q=1" {
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
		wantDelete    bool
		wantStore     bool
		wantBodyMatch string
	}{
		{
			name:          "origin error",
			err:           errors.New("boom"),
			wantCode:      http.StatusBadGateway,
			wantWait0:     "bad-gateway",
			wantBodyMatch: "bad gateway",
		},
		{
			name:       "ignore by status",
			statusKind: "ignore-by-status",
			wantCode:   http.StatusNotFound,
			wantWait0:  "ignore-by-status",
			wantDelete: true,
		},
		{
			name:       "non cacheable",
			cacheable:  false,
			statusKind: "ok",
			wantCode:   http.StatusCreated,
			wantWait0:  "bypass",
		},
		{
			name:       "cacheable miss",
			cacheable:  true,
			statusKind: "ok",
			wantCode:   http.StatusCreated,
			wantWait0:  "miss",
			wantStore:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := &fakeRuntime{
				originEnt:       Entry{Status: http.StatusCreated, Header: http.Header{}, Body: []byte("origin")},
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
