package revalidation

import (
	"context"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRuntime struct {
	mu sync.Mutex

	peekMap map[string]Entry
	access  map[string]int64
	allKeys []string
	origin  string

	sendMarkers bool
	random      string
	doFunc      func(req *http.Request) (*http.Response, error)

	putCalls    map[string]Entry
	deleteCalls []string
	requests    []*http.Request
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		peekMap:  map[string]Entry{},
		access:   map[string]int64{},
		origin:   "http://origin.local",
		random:   "entropy",
		putCalls: map[string]Entry{},
	}
}

func (f *fakeRuntime) Peek(key string) (Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ent, ok := f.peekMap[key]
	return ent, ok
}

func (f *fakeRuntime) Put(key string, ent Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls[key] = ent
}

func (f *fakeRuntime) Delete(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, key)
}

func (f *fakeRuntime) SnapshotAccessTimes() map[string]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int64, len(f.access))
	for k, v := range f.access {
		out[k] = v
	}
	return out
}

func (f *fakeRuntime) AllKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.allKeys...)
}

func (f *fakeRuntime) Origin() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.origin
}

func (f *fakeRuntime) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req.Clone(req.Context()))
	do := f.doFunc
	f.mu.Unlock()

	if do == nil {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}
	return do(req)
}

func (f *fakeRuntime) SendRevalidateMarkers() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendMarkers
}

func (f *fakeRuntime) RandomString(int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.random
}

type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, format)
}

func (l *captureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

type readErrBody struct{}

func (readErrBody) Read([]byte) (int, error) { return 0, errors.New("read fail") }
func (readErrBody) Close() error             { return nil }

func TestController_Async_DropsWhenQueueIsFull(t *testing.T) {
	rt := newFakeRuntime()
	bgSem := make(chan struct{}, 1)
	bgSem <- struct{}{}
	var wg sync.WaitGroup
	c := NewController(rt, bgSem, make(chan struct{}), &wg, false, nil, nil, nil)

	c.Async("/x", "/x", "", "user")

	wg.Wait()
	if len(rt.requests) != 0 {
		t.Fatalf("expected no request, got %d", len(rt.requests))
	}
}

func TestController_Async_ExecutesOnce(t *testing.T) {
	rt := newFakeRuntime()
	rt.doFunc = func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("body"))}, nil
	}
	bgSem := make(chan struct{}, 1)
	var wg sync.WaitGroup
	c := NewController(rt, bgSem, make(chan struct{}), &wg, false, nil, nil, nil)

	c.Async("/p", "/p", "q=1", "user")
	wg.Wait()

	if len(rt.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(rt.requests))
	}
}

func TestController_Once_Branches(t *testing.T) {
	tests := []struct {
		name        string
		hasCur      bool
		cur         Entry
		respStatus  int
		cacheCtl    string
		body        string
		sendMarkers bool
		doErr       error
		readErr     bool
		by          string
		wantKind    string
		wantChanged bool
		wantDeleted bool
		wantPut     bool
		wantReqHdr  bool
	}{
		{
			name:        "updated",
			hasCur:      true,
			cur:         Entry{Hash32: 1, DiscoveredBy: "sitemap"},
			respStatus:  http.StatusOK,
			body:        "new",
			sendMarkers: true,
			by:          "warmup",
			wantKind:    "updated",
			wantChanged: true,
			wantPut:     true,
			wantReqHdr:  true,
		},
		{
			name:        "unchanged",
			hasCur:      true,
			cur:         Entry{Hash32: crc32.ChecksumIEEE([]byte("same"))},
			respStatus:  http.StatusOK,
			body:        "same",
			wantKind:    "unchanged",
			wantPut:     true,
			wantChanged: false,
		},
		{
			name:        "delete by status",
			hasCur:      true,
			cur:         Entry{Hash32: 1},
			respStatus:  http.StatusNotFound,
			body:        "missing",
			wantKind:    "deleted",
			wantChanged: true,
			wantDeleted: true,
		},
		{
			name:        "ignored status without current",
			respStatus:  http.StatusNotFound,
			body:        "missing",
			wantKind:    "ignored-status",
			wantChanged: false,
		},
		{
			name:        "delete by cache control",
			hasCur:      true,
			cur:         Entry{Hash32: 1},
			respStatus:  http.StatusOK,
			cacheCtl:    "no-store",
			body:        "x",
			wantKind:    "deleted",
			wantChanged: true,
			wantDeleted: true,
		},
		{
			name:       "origin error",
			doErr:      errors.New("origin down"),
			wantKind:   "error",
			wantChanged:false,
		},
		{
			name:       "read error",
			respStatus: http.StatusOK,
			readErr:    true,
			wantKind:   "error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := newFakeRuntime()
			rt.sendMarkers = tc.sendMarkers
			if tc.hasCur {
				rt.peekMap["/page"] = tc.cur
			}
			rt.doFunc = func(req *http.Request) (*http.Response, error) {
				if tc.doErr != nil {
					return nil, tc.doErr
				}
				h := http.Header{}
				if tc.cacheCtl != "" {
					h.Set("Cache-Control", tc.cacheCtl)
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.readErr {
					body = readErrBody{}
				}
				return &http.Response{StatusCode: tc.respStatus, Header: h, Body: body}, nil
			}
			var wg sync.WaitGroup
			unchangedLog := &captureLogger{}
			c := NewController(rt, make(chan struct{}, 1), make(chan struct{}), &wg, false, nil, unchangedLog, nil)

			res := c.Once(context.Background(), "/page", "/page", "a=1", tc.by)

			if res.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", res.Kind, tc.wantKind)
			}
			if res.Changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", res.Changed, tc.wantChanged)
			}
			if tc.wantDeleted != (len(rt.deleteCalls) == 1) {
				t.Fatalf("deleteCalls = %v", rt.deleteCalls)
			}
			if tc.wantPut != (len(rt.putCalls) == 1) {
				t.Fatalf("putCalls = %d", len(rt.putCalls))
			}
			if tc.wantReqHdr {
				if len(rt.requests) != 1 {
					t.Fatalf("requests = %d, want 1", len(rt.requests))
				}
				req := rt.requests[0]
				if got := req.Header.Get("X-Wait0-Revalidate-Entropy"); got == "" {
					t.Fatalf("expected revalidate entropy header")
				}
			}
			if tc.wantKind == "unchanged" && unchangedLog.count() != 1 {
				t.Fatalf("unchanged log count = %d, want 1", unchangedLog.count())
			}
		})
	}
}

func TestController_KeysAndAllKeysSnapshot(t *testing.T) {
	rt := newFakeRuntime()
	rt.access = map[string]int64{
		"/b": 2,
		"/a": 2,
		"/c": 1,
	}
	rt.allKeys = []string{"/z", "/a", "/z", "/b"}

	var wg sync.WaitGroup
	c := NewController(rt, make(chan struct{}, 1), make(chan struct{}), &wg, false, nil, nil, nil)

	keys := c.KeysByLastAccessDesc(WarmRule{Matches: func(path string) bool { return path != "/c" }})
	want := []string{"/a", "/b"}
	if len(keys) != len(want) {
		t.Fatalf("keys len = %d, want %d", len(keys), len(want))
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}

	all := c.AllKeysSnapshot()
	wantAll := []string{"/a", "/b", "/z"}
	if len(all) != len(wantAll) {
		t.Fatalf("all len = %d, want %d", len(all), len(wantAll))
	}
	for i := range wantAll {
		if all[i] != wantAll[i] {
			t.Fatalf("all[%d] = %q, want %q", i, all[i], wantAll[i])
		}
	}
}

func TestController_WarmupGroupLoop_StopAndLogs(t *testing.T) {
	rt := newFakeRuntime()
	rt.access = map[string]int64{"/x": 10, "/y": 9}
	rt.peekMap["/x"] = Entry{Hash32: 1}
	rt.peekMap["/y"] = Entry{Hash32: 2}
	rt.doFunc = func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/y" {
			return nil, errors.New("origin failed")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("updated"))}, nil
	}

	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	summaryLog := &captureLogger{}
	errorLog := &captureLogger{}
	c := NewController(rt, make(chan struct{}, 2), stopCh, &wg, true, summaryLog, nil, errorLog)

	done := make(chan struct{})
	go func() {
		c.WarmupGroupLoop(WarmRule{Match: "/", WarmEvery: 10 * time.Millisecond, WarmMax: 2, Matches: func(string) bool { return true }})
		close(done)
	}()

	time.Sleep(60 * time.Millisecond)
	close(stopCh)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("warmup loop did not stop")
	}
	wg.Wait()

	if summaryLog.count() == 0 {
		t.Fatalf("expected warmup summary log")
	}
	if errorLog.count() == 0 {
		t.Fatalf("expected warmup error log")
	}
}
