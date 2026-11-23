package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// limitedApp is a minimal API with the real middleware in front of it.
func limitedApp(trustForwarded bool, cfg RateLimitConfig, clk *fakeClock) *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {}
	e.IPExtractor = ipExtractor(trustForwarded)
	e.Use(middleware.RequestID(), secureHeaders(true), rateLimits(cfg, clk.now))
	ok := func(c echo.Context) error { return c.String(http.StatusOK, "ok") }
	e.POST("/api/v1/auth/login", ok)
	e.POST("/api/v1/auth/mfa/verify", ok)
	e.POST("/api/v1/auth/invite/accept", ok)
	e.POST("/api/v1/auth/support-visit", ok)
	e.POST("/api/v1/platform/auth/login", ok)
	e.GET("/api/v1/rooms", ok)
	e.GET("/livez", ok)
	return e
}

func hit(e *echo.Echo, method, path, remote string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

var testLimits = RateLimitConfig{Enabled: true, GeneralPerMin: 600, StrictPerMin: 6, MaxKeys: 1000} // strict burst 6, general burst 200

func newClk() *fakeClock { return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestSignInIsLimitedPerClientAndAnswersWithRetryAfter(t *testing.T) {
	clk := newClk()
	e := limitedApp(false, testLimits, clk)
	for i := 0; i < 6; i++ {
		if rec := hit(e, "POST", "/api/v1/auth/login", "198.51.100.1:5000"); rec.Code != 200 {
			t.Fatalf("attempt %d = %d", i+1, rec.Code)
		}
	}
	rec := hit(e, "POST", "/api/v1/auth/login", "198.51.100.1:5000")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("7th attempt = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "10" {
		t.Errorf("Retry-After = %q, want 10", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("content type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "rate_limited" || body["status"] != float64(429) || body["requestId"] == "" {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("a 429 must not be cacheable")
	}
	clk.t = clk.t.Add(10 * time.Second)
	if rec := hit(e, "POST", "/api/v1/auth/login", "198.51.100.1:5000"); rec.Code != 200 {
		t.Errorf("after the wait = %d, want 200", rec.Code)
	}
}

func TestOneClientsFloodDoesNotBlockAnother(t *testing.T) {
	e := limitedApp(false, testLimits, newClk())
	for i := 0; i < 20; i++ {
		hit(e, "POST", "/api/v1/auth/login", "198.51.100.1:5000")
	}
	if rec := hit(e, "POST", "/api/v1/auth/login", "198.51.100.2:5000"); rec.Code != 200 {
		t.Fatalf("a different client got %d", rec.Code)
	}
}

func TestForwardedHeaderIsIgnoredUnlessProxyIsTrusted(t *testing.T) {
	// Not trusted (even when the peer is a private address, as a proxy would be): rotating X-Forwarded-For must not
	// give a fresh bucket.
	e := limitedApp(false, testLimits, newClk())
	var last int
	for i := 0; i < 8; i++ {
		last = hit(e, "POST", "/api/v1/auth/login", "10.0.0.5:5000", "X-Forwarded-For", "203.0.113."+string(rune('1'+i))).Code
	}
	if last != 429 {
		t.Fatalf("spoofed X-Forwarded-For escaped the limit (last = %d)", last)
	}
	// Behind a trusted proxy (private address): the client the proxy reports is the key, and a spoofed leftmost
	// value cannot hide the real one the proxy appended.
	e = limitedApp(true, testLimits, newClk())
	for i := 0; i < 8; i++ {
		last = hit(e, "POST", "/api/v1/auth/login", "10.0.0.5:5000", "X-Forwarded-For", "9.9.9."+string(rune('1'+i))+", 203.0.113.50").Code
	}
	if last != 429 {
		t.Fatalf("spoofed leftmost X-Forwarded-For escaped the limit behind a proxy (last = %d)", last)
	}
	if rec := hit(e, "POST", "/api/v1/auth/login", "10.0.0.5:5000", "X-Forwarded-For", "203.0.113.51"); rec.Code != 200 {
		t.Fatalf("another real client behind the proxy got %d", rec.Code)
	}
}

func TestAllSecretCheckingOperationsShareOneStrictBucket(t *testing.T) {
	e := limitedApp(false, testLimits, newClk())
	paths := []string{"/api/v1/auth/login", "/api/v1/auth/mfa/verify", "/api/v1/auth/invite/accept", "/api/v1/auth/support-visit", "/api/v1/platform/auth/login"}
	for i := 0; i < 6; i++ {
		hit(e, "POST", paths[i%len(paths)], "198.51.100.1:5000")
	}
	for _, p := range paths {
		if rec := hit(e, "POST", p, "198.51.100.1:5000"); rec.Code != 429 {
			t.Errorf("%s = %d after the shared strict bucket was spent, want 429", p, rec.Code)
		}
	}
}

func TestGeneralTrafficHasItsOwnLargerBucketAndProbesAreNotCounted(t *testing.T) {
	e := limitedApp(false, testLimits, newClk())
	for i := 0; i < 10; i++ { // would exhaust the strict bucket
		if rec := hit(e, "GET", "/api/v1/rooms", "198.51.100.1:5000"); rec.Code != 200 {
			t.Fatalf("general request %d = %d", i+1, rec.Code)
		}
	}
	if rec := hit(e, "POST", "/api/v1/auth/login", "198.51.100.1:5000"); rec.Code != 200 {
		t.Fatal("general traffic spent the sign-in bucket")
	}
	for i := 0; i < 500; i++ {
		if rec := hit(e, "GET", "/livez", "198.51.100.1:5000"); rec.Code != 200 {
			t.Fatalf("/livez was limited at request %d", i+1)
		}
	}
	n := 0
	for i := 0; i < 400; i++ {
		if hit(e, "GET", "/api/v1/rooms", "198.51.100.9:1").Code == 429 {
			n++
		}
	}
	if n == 0 {
		t.Error("the general bucket never limited")
	}
}

func TestTrackedClientsStayBounded(t *testing.T) {
	cfg := testLimits
	cfg.MaxKeys = 100
	e := limitedApp(false, cfg, newClk())
	for i := 0; i < 2000; i++ { // each IPv4 address is its own key; the server must not remember all of them
		hit(e, "GET", "/api/v1/rooms", "10.1."+itoa(i/250)+"."+itoa(i%250)+":1")
	}
	// No handle on the limiter from here; the bound itself is tested in the ratelimit package. This asserts the
	// middleware still answers after heavy key churn.
	if rec := hit(e, "GET", "/api/v1/rooms", "10.9.9.9:1"); rec.Code != 200 {
		t.Fatalf("after churn = %d", rec.Code)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestSecurityHeaders(t *testing.T) {
	for _, prod := range []bool{true, false} {
		e := echo.New()
		e.Use(secureHeaders(prod))
		e.GET("/x", func(c echo.Context) error { return c.String(200, "x") })
		rec := hit(e, "GET", "/x", "198.51.100.1:1")
		want := map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"Cache-Control":                "no-store",
			"X-Frame-Options":              "DENY",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-site",
		}
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("production=%v %s = %q, want %q", prod, k, got, v)
			}
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, d := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'none'"} {
			if !strings.Contains(csp, d) {
				t.Errorf("CSP %q lacks %s", csp, d)
			}
		}
		if rec.Header().Get("Permissions-Policy") == "" {
			t.Error("no Permissions-Policy")
		}
		hsts := rec.Header().Get("Strict-Transport-Security")
		if prod && !strings.Contains(hsts, "max-age=31536000") {
			t.Errorf("production HSTS = %q", hsts)
		}
		if !prod && hsts != "" {
			t.Errorf("HSTS must not be sent outside production (it would pin localhost): %q", hsts)
		}
	}
}

func TestOriginGuardRefusesCrossOriginRequestsThatCarryCookies(t *testing.T) {
	e := echo.New()
	e.Use(originGuard([]string{"https://app.shikshit.app"}))
	ok := func(c echo.Context) error { return c.String(200, "ok") }
	e.POST("/logout", ok)
	e.GET("/me", ok)
	cookie := "__Secure-shikshit_rt=abc"
	cases := []struct {
		name, method, origin, cookie string
		want                         int
	}{
		{"allowed origin", "POST", "https://app.shikshit.app", cookie, 200},
		{"sibling tenant origin", "POST", "https://evil.shikshit.app", cookie, 403},
		{"foreign origin", "POST", "https://evil.example", cookie, 403},
		{"no origin (mobile, curl)", "POST", "", cookie, 200},
		{"no cookie", "POST", "https://evil.example", "", 200},
		{"read-only method", "GET", "https://evil.example", cookie, 200},
		{"own origin", "POST", "http://api.shikshit.test", cookie, 200},
	}
	for _, tc := range cases {
		path := "/logout"
		if tc.method == "GET" {
			path = "/me"
		}
		req := httptest.NewRequest(tc.method, path, nil)
		req.Host = "api.shikshit.test"
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.cookie != "" {
			req.Header.Set("Cookie", tc.cookie)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
}

func TestRateLimitConfig(t *testing.T) {
	c, err := LoadConfig(func(k string) string {
		return map[string]string{"SHIKSHIT_DATABASE_URL": "postgres://x", "SHIKSHIT_JWT_SECRET": strings.Repeat("s", 32)}[k]
	})
	if err != nil || !c.RateLimit.Enabled || c.RateLimit.StrictPerMin != 20 || c.RateLimit.GeneralPerMin != 1200 || c.RateLimit.MaxKeys != 100000 {
		t.Fatalf("defaults: %+v %v", c.RateLimit, err)
	}
	for _, env := range []map[string]string{
		{"SHIKSHIT_RATE_LIMIT_STRICT_PER_MIN": "0"},
		{"SHIKSHIT_RATE_LIMIT_GENERAL_PER_MIN": "abc"},
		{"SHIKSHIT_RATE_LIMIT_MAX_KEYS": "5"},
		{"SHIKSHIT_RATE_LIMIT_ENABLED": "maybe"},
	} {
		_, err := LoadConfig(func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return map[string]string{"SHIKSHIT_DATABASE_URL": "postgres://x", "SHIKSHIT_JWT_SECRET": strings.Repeat("s", 32)}[k]
		})
		if err == nil {
			t.Errorf("%v accepted", env)
		}
	}
}
