package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestIsEndpointPath(t *testing.T) {
	if !IsEndpointPath(EndpointPath) {
		t.Fatal("expected dashboard endpoint")
	}
	if !IsEndpointPath(EndpointPath + "/") {
		t.Fatal("expected dashboard trailing slash endpoint")
	}
	if !IsEndpointPath(StatsEndpointPath) {
		t.Fatal("expected stats endpoint")
	}
	if !IsEndpointPath(InvalidateEndpointPath) {
		t.Fatal("expected invalidate endpoint")
	}
	if IsEndpointPath("/wait0/dashboard/other") {
		t.Fatal("unexpected match for other route")
	}
}

func TestHandle_RequiresBasicAuth(t *testing.T) {
	ctrl := NewController(Config{Username: "ops", Password: "secret", StatsBearerToken: "stats-token"}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local"+EndpointPath, nil)
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusUnauthorized)
	}
	if got := w.Result().Header.Get("WWW-Authenticate"); !strings.Contains(got, "Basic") {
		t.Fatalf("WWW-Authenticate=%q", got)
	}
}

func TestHandle_StatsBridge(t *testing.T) {
	var gotAuth string
	ctrl := NewController(Config{Username: "ops", Password: "secret", StatsBearerToken: "stats-token"}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"cache":{"urls_total":7}}`))
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local"+StatsEndpointPath, nil)
	req.SetBasicAuth("ops", "secret")
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusOK)
	}
	if gotAuth != "Bearer stats-token" {
		t.Fatalf("authorization header=%q", gotAuth)
	}

	var payload map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cacheObj := payload["cache"].(map[string]any)
	if int(cacheObj["urls_total"].(float64)) != 7 {
		t.Fatalf("urls_total=%v", cacheObj["urls_total"])
	}
	if got := w.Result().Header.Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("cache-control=%q", got)
	}
	if got := w.Result().Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("pragma=%q", got)
	}
	if got := w.Result().Header.Get("Expires"); got != "0" {
		t.Fatalf("expires=%q", got)
	}
}

func TestHandle_InvalidateBridgeAndDisabledMode(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		var gotAuth string
		var gotCT string
		ctrl := NewController(Config{
			Username:                "ops",
			Password:                "secret",
			StatsBearerToken:        "stats-token",
			InvalidationBearerToken: "inv-token",
		}, Runtime{
			StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
			InvalidationHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotCT = r.Header.Get("Content-Type")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"accepted"}`))
			}),
		})

		req := httptest.NewRequest(http.MethodPost, "http://wait0.local"+InvalidateEndpointPath, strings.NewReader(`{"paths":["/a"]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://wait0.local")
		req.Header.Set(csrfHeaderName, "token-ok")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-ok"})
		req.SetBasicAuth("ops", "secret")
		req.RemoteAddr = "127.0.0.1:9000"
		w := httptest.NewRecorder()
		ctrl.Handle(w, req)

		if w.Result().StatusCode != http.StatusAccepted {
			t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusAccepted)
		}
		if gotAuth != "Bearer inv-token" {
			t.Fatalf("authorization header=%q", gotAuth)
		}
		if gotCT != "application/json" {
			t.Fatalf("content-type=%q", gotCT)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		ctrl := NewController(Config{Username: "ops", Password: "secret", StatsBearerToken: "stats-token"}, Runtime{
			StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		})
		req := httptest.NewRequest(http.MethodPost, "http://wait0.local"+InvalidateEndpointPath, strings.NewReader(`{"paths":["/a"]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://wait0.local")
		req.Header.Set(csrfHeaderName, "token-ok")
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-ok"})
		req.SetBasicAuth("ops", "secret")
		req.RemoteAddr = "127.0.0.1:9000"
		w := httptest.NewRecorder()
		ctrl.Handle(w, req)
		if w.Result().StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusServiceUnavailable)
		}
	})
}

func TestHandle_IndexRendersPage(t *testing.T) {
	ctrl := NewController(Config{Username: "ops", Password: "secret", StatsBearerToken: "stats-token"}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local"+EndpointPath, nil)
	req.SetBasicAuth("ops", "secret")
	req.RemoteAddr = "127.0.0.1:9000"
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "wait0 dashboard") {
		t.Fatalf("expected dashboard title in page")
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatalf("expected csrf cookie in response")
	}
}

func TestHandle_InvalidateRequiresOriginAndCSRF(t *testing.T) {
	ctrl := NewController(Config{
		Username:                "ops",
		Password:                "secret",
		StatsBearerToken:        "stats-token",
		InvalidationBearerToken: "inv-token",
	}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		InvalidationHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
	})

	req := httptest.NewRequest(http.MethodPost, "http://wait0.local"+InvalidateEndpointPath, strings.NewReader(`{"paths":["/a"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeaderName, "token-ok")
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-ok"})
	req.SetBasicAuth("ops", "secret")
	req.RemoteAddr = "127.0.0.1:9000"
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusForbidden)
	}

	req2 := httptest.NewRequest(http.MethodPost, "http://wait0.local"+InvalidateEndpointPath, strings.NewReader(`{"paths":["/a"]}`))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", "http://wait0.local")
	req2.Header.Set(csrfHeaderName, "bad")
	req2.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "token-ok"})
	req2.SetBasicAuth("ops", "secret")
	req2.RemoteAddr = "127.0.0.1:9000"
	w2 := httptest.NewRecorder()
	ctrl.Handle(w2, req2)
	if w2.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want=%d", w2.Result().StatusCode, http.StatusForbidden)
	}
}

func TestHandle_RateLimit(t *testing.T) {
	ctrl := NewController(Config{
		Username:           "ops",
		Password:           "secret",
		StatsBearerToken:   "stats-token",
		RateLimitPerMinute: 1,
	}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	})

	req1 := httptest.NewRequest(http.MethodGet, "http://wait0.local"+StatsEndpointPath, nil)
	req1.SetBasicAuth("ops", "secret")
	req1.RemoteAddr = "127.0.0.1:9000"
	w1 := httptest.NewRecorder()
	ctrl.Handle(w1, req1)
	if w1.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=%d", w1.Result().StatusCode, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://wait0.local"+StatsEndpointPath, nil)
	req2.SetBasicAuth("ops", "secret")
	req2.RemoteAddr = "127.0.0.1:9000"
	w2 := httptest.NewRecorder()
	ctrl.Handle(w2, req2)
	if w2.Result().StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d want=%d", w2.Result().StatusCode, http.StatusTooManyRequests)
	}
}

func TestHandle_RateLimitKeyIncludesPrincipal(t *testing.T) {
	ctrl := NewController(Config{
		Username:           "ops",
		Password:           "secret",
		StatsBearerToken:   "stats-token",
		RateLimitPerMinute: 1,
	}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	})

	req1 := httptest.NewRequest(http.MethodGet, "http://wait0.local"+StatsEndpointPath, nil)
	req1.SetBasicAuth("ops", "secret")
	req1.RemoteAddr = "127.0.0.1:9000"
	w1 := httptest.NewRecorder()
	ctrl.Handle(w1, req1)
	if w1.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=%d", w1.Result().StatusCode, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodGet, "http://wait0.local"+StatsEndpointPath, nil)
	req2.SetBasicAuth("ops2", "secret")
	req2.RemoteAddr = "127.0.0.1:9000"
	w2 := httptest.NewRecorder()
	ctrl.Handle(w2, req2)
	if w2.Result().StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status=%d want=%d", w2.Result().StatusCode, http.StatusTooManyRequests)
	}
}

func TestClientIP_TrustProxyHeaders(t *testing.T) {
	t.Run("trusted proxy", func(t *testing.T) {
		ctrl := NewController(Config{
			Username:           "ops",
			Password:           "secret",
			StatsBearerToken:   "stats-token",
			TrustProxyHeaders:  true,
			TrustedProxyCIDRs:  []string{"10.0.0.0/24"},
			RateLimitPerMinute: 100,
		}, Runtime{})
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0/dashboard", nil)
		req.RemoteAddr = "10.0.0.2:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.2")
		if got := ctrl.clientIP(req); got != "203.0.113.9" {
			t.Fatalf("clientIP with trusted proxy = %q", got)
		}
	})
	t.Run("untrusted proxy", func(t *testing.T) {
		ctrl := NewController(Config{
			Username:           "ops",
			Password:           "secret",
			StatsBearerToken:   "stats-token",
			TrustProxyHeaders:  true,
			TrustedProxyCIDRs:  []string{"10.0.0.0/24"},
			RateLimitPerMinute: 100,
		}, Runtime{})
		req := httptest.NewRequest(http.MethodGet, "http://wait0.local/wait0/dashboard", nil)
		req.RemoteAddr = "192.0.2.55:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113.9, 192.0.2.55")
		if got := ctrl.clientIP(req); got != "192.0.2.55" {
			t.Fatalf("clientIP with untrusted proxy = %q", got)
		}
	})
}

func TestDashboardPageContainsCSRFToken(t *testing.T) {
	ctrl := NewController(Config{
		Username:         "ops",
		Password:         "secret",
		StatsBearerToken: "stats-token",
	}, Runtime{
		StatsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "http://wait0.local"+EndpointPath, nil)
	req.SetBasicAuth("ops", "secret")
	req.RemoteAddr = "127.0.0.1:9000"
	w := httptest.NewRecorder()
	ctrl.Handle(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("status=%d want=%d", w.Result().StatusCode, http.StatusOK)
	}
	re := regexp.MustCompile(`csrfToken:\s*'([0-9a-f]+)'`)
	m := re.FindStringSubmatch(w.Body.String())
	if len(m) != 2 {
		t.Fatalf("csrf token not found in dashboard page")
	}
}
