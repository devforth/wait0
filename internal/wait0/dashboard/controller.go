package dashboard

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const EndpointPath = "/wait0/dashboard"
const StatsEndpointPath = "/wait0/dashboard/stats"
const InvalidateEndpointPath = "/wait0/dashboard/invalidate"
const csrfCookieName = "wait0_dashboard_csrf"
const csrfHeaderName = "X-Wait0-CSRF"

const pollIntervalMS = 5000

type Config struct {
	Username                string
	Password                string
	StatsBearerToken        string
	InvalidationBearerToken string
	RateLimitPerMinute      int
	TrustProxyHeaders       bool
	TrustedProxyCIDRs       []string
}

type Runtime struct {
	StatsHandler        http.Handler
	InvalidationHandler http.Handler
}

type Controller struct {
	username          string
	password          string
	statsToken        string
	invalidationToken string

	statsHandler        http.Handler
	invalidationHandler http.Handler
	preAuthLimiter      *fixedWindowLimiter
	postAuthLimiter     *fixedWindowLimiter
	trustProxyHeaders   bool
	trustedProxies      []*net.IPNet
}

type pageData struct {
	InvalidationEnabled bool
	PollIntervalMS      int
	CSRFToken           string
}

func NewController(cfg Config, rt Runtime) *Controller {
	rpm := cfg.RateLimitPerMinute
	if rpm <= 0 {
		rpm = 120
	}
	return &Controller{
		username:            strings.TrimSpace(cfg.Username),
		password:            strings.TrimSpace(cfg.Password),
		statsToken:          strings.TrimSpace(cfg.StatsBearerToken),
		invalidationToken:   strings.TrimSpace(cfg.InvalidationBearerToken),
		statsHandler:        rt.StatsHandler,
		invalidationHandler: rt.InvalidationHandler,
		preAuthLimiter:      newFixedWindowLimiter(rpm, time.Minute),
		postAuthLimiter:     newFixedWindowLimiter(rpm, time.Minute),
		trustProxyHeaders:   cfg.TrustProxyHeaders,
		trustedProxies:      parseTrustedCIDRs(cfg.TrustedProxyCIDRs),
	}
}

func IsEndpointPath(path string) bool {
	switch path {
	case EndpointPath, EndpointPath + "/", StatsEndpointPath, InvalidateEndpointPath:
		return true
	default:
		return false
	}
}

func (c *Controller) Handle(w http.ResponseWriter, r *http.Request) {
	if !IsEndpointPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	setNoCacheHeaders(w)
	clientIP := c.clientIP(r)
	if !c.allowPreAuthRate(clientIP) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}

	principal, ok := c.authenticatedPrincipal(r)
	if !ok {
		log.Printf("dashboard auth failed: remote=%q method=%s path=%s", strings.TrimSpace(r.RemoteAddr), r.Method, r.URL.Path)
		writeBasicUnauthorized(w)
		return
	}
	if !c.allowPostAuthRate(clientIP, principal) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
		return
	}

	switch r.URL.Path {
	case EndpointPath, EndpointPath + "/":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		csrfToken := randomTokenHex(32)
		http.SetCookie(w, &http.Cookie{
			Name:     csrfCookieName,
			Value:    csrfToken,
			Path:     EndpointPath,
			MaxAge:   3600,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   requestIsSecure(r),
		})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = dashboardTemplate.Execute(w, pageData{
			InvalidationEnabled: c.invalidationEnabled(),
			PollIntervalMS:      pollIntervalMS,
			CSRFToken:           csrfToken,
		})
	case StatsEndpointPath:
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if strings.TrimSpace(c.statsToken) == "" || c.statsHandler == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "dashboard stats are unavailable"})
			return
		}
		c.proxyToControl(w, r, http.MethodGet, "/wait0", http.NoBody, c.statsToken, c.statsHandler)
	case InvalidateEndpointPath:
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if !c.invalidationEnabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "dashboard invalidation is unavailable"})
			return
		}
		if !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "csrf origin check failed"})
			return
		}
		if !validCSRFToken(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "csrf token check failed"})
			return
		}
		ct := strings.TrimSpace(r.Header.Get("Content-Type"))
		c.proxyToControl(w, r, http.MethodPost, "/wait0/invalidate", r.Body, c.invalidationToken, c.invalidationHandler, withContentType(ct))
	default:
		http.NotFound(w, r)
	}
}

func (c *Controller) authenticatedPrincipal(r *http.Request) (string, bool) {
	if strings.TrimSpace(c.username) == "" || strings.TrimSpace(c.password) == "" {
		return "", false
	}
	u, p, ok := r.BasicAuth()
	if !ok {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(u), []byte(c.username)) != 1 {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(p), []byte(c.password)) != 1 {
		return "", false
	}
	return strings.TrimSpace(u), true
}

func (c *Controller) invalidationEnabled() bool {
	return strings.TrimSpace(c.invalidationToken) != "" && c.invalidationHandler != nil
}

type requestOpt func(*http.Request)

func withContentType(ct string) requestOpt {
	return func(r *http.Request) {
		if strings.TrimSpace(ct) != "" {
			r.Header.Set("Content-Type", ct)
		}
	}
}

func (c *Controller) proxyToControl(w http.ResponseWriter, r *http.Request, method, path string, body io.Reader, bearerToken string, handler http.Handler, opts ...requestOpt) {
	upReq := httptest.NewRequest(method, fmt.Sprintf("http://wait0.local%s", path), body)
	upReq = upReq.WithContext(r.Context())
	upReq.Header.Set("Authorization", "Bearer "+bearerToken)
	for _, opt := range opts {
		opt(upReq)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, upReq)
	res := rr.Result()
	defer res.Body.Close()

	for k, vals := range res.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func writeBasicUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="wait0 dashboard", charset="UTF-8"`)
	writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
}

func writeJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func setNoCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if xf == "" {
		return false
	}
	parts := strings.Split(xf, ",")
	return strings.EqualFold(strings.TrimSpace(parts[0]), "https")
}

func expectedOrigin(r *http.Request) string {
	scheme := "http"
	if requestIsSecure(r) {
		scheme = "https"
	}
	return scheme + "://" + strings.TrimSpace(r.Host)
}

func sameOrigin(r *http.Request) bool {
	want := strings.TrimSpace(expectedOrigin(r))
	if want == "" {
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return strings.EqualFold(origin, want)
	}
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref == "" {
		return false
	}
	u, err := url.Parse(ref)
	if err != nil || strings.TrimSpace(u.Host) == "" || strings.TrimSpace(u.Scheme) == "" {
		return false
	}
	got := strings.ToLower(strings.TrimSpace(u.Scheme)) + "://" + strings.TrimSpace(u.Host)
	return strings.EqualFold(got, want)
}

func validCSRFToken(r *http.Request) bool {
	headerToken := strings.TrimSpace(r.Header.Get(csrfHeaderName))
	if headerToken == "" {
		return false
	}
	c, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	cookieToken := strings.TrimSpace(c.Value)
	if cookieToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookieToken)) == 1
}

func randomTokenHex(n int) string {
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

type fixedWindowLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	windowStart time.Time
	counts      map[string]int
}

func newFixedWindowLimiter(limit int, window time.Duration) *fixedWindowLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &fixedWindowLimiter{
		limit:       limit,
		window:      window,
		windowStart: time.Now(),
		counts:      make(map[string]int),
	}
}

func (l *fixedWindowLimiter) Allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	k := strings.TrimSpace(key)
	if k == "" {
		k = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.windowStart) >= l.window {
		l.windowStart = now
		l.counts = make(map[string]int)
	}
	next := l.counts[k] + 1
	if next > l.limit {
		return false
	}
	l.counts[k] = next
	return true
}

func (c *Controller) allowPreAuthRate(ip string) bool {
	if c.preAuthLimiter == nil {
		return true
	}
	return c.preAuthLimiter.Allow(ip, time.Now())
}

func (c *Controller) allowPostAuthRate(ip, principal string) bool {
	if c.postAuthLimiter == nil {
		return true
	}
	pr := strings.TrimSpace(principal)
	if pr == "" {
		pr = "unknown"
	}
	return c.postAuthLimiter.Allow(ip+"|"+pr, time.Now())
}

func (c *Controller) clientIP(r *http.Request) string {
	if c.trustProxyHeaders && c.isFromTrustedProxy(r) {
		xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if xff != "" {
			parts := strings.Split(xff, ",")
			candidate := strings.TrimSpace(parts[0])
			if net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	host = strings.TrimSpace(host)
	if net.ParseIP(host) != nil {
		return host
	}
	if host == "" {
		return "unknown"
	}
	return host
}

func (c *Controller) isFromTrustedProxy(r *http.Request) bool {
	if len(c.trustedProxies) == 0 {
		return false
	}
	remoteIP := parseRemoteIP(r.RemoteAddr)
	if remoteIP == nil {
		return false
	}
	for _, cidr := range c.trustedProxies {
		if cidr.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}
	return net.ParseIP(host)
}

func parseTrustedCIDRs(values []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		_, ipNet, err := net.ParseCIDR(v)
		if err != nil {
			continue
		}
		out = append(out, ipNet)
	}
	return out
}
