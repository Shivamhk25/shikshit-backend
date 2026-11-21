package tenancy_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

type fakeResolver struct {
	tenants map[string]tenancy.Tenant
	err     error
	calls   int
}

func (f *fakeResolver) Resolve(_ context.Context, key string) (tenancy.Tenant, error) {
	f.calls++
	if f.err != nil {
		return tenancy.Tenant{}, f.err
	}
	if t, ok := f.tenants[key]; ok {
		return t, nil
	}
	return tenancy.Tenant{}, tenancy.ErrNotFound
}

func serve(t *testing.T, cfg tenancy.MiddlewareConfig, host, path string, hdr map[string]string) (*httptest.ResponseRecorder, *tenancy.Tenant) {
	t.Helper()
	e := echo.New()
	var seen *tenancy.Tenant
	e.Use(tenancy.Middleware(cfg))
	e.GET("/*", func(c echo.Context) error {
		if tn, ok := tenancy.FromContext(c.Request().Context()); ok {
			seen = &tn
		}
		return c.NoContent(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec, seen
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem document: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

func TestMiddlewareBindsTheTenantFromTheHostOnly(t *testing.T) {
	res := &fakeResolver{tenants: map[string]tenancy.Tenant{
		"alpha": {ID: "11111111-1111-1111-1111-111111111111", Code: "alpha", Status: "live"},
		"bravo": {ID: "22222222-2222-2222-2222-222222222222", Code: "bravo", Status: "onboarding"},
	}}
	cfg := tenancy.MiddlewareConfig{Config: tenancy.Config{BaseDomains: []string{"shikshit.app"}}, Resolver: res}

	rec, seen := serve(t, cfg, "alpha.shikshit.app", "/x", nil)
	if rec.Code != http.StatusNoContent || seen == nil || seen.Code != "alpha" {
		t.Fatalf("host alpha.shikshit.app must bind alpha, got code=%d tenant=%+v", rec.Code, seen)
	}
	rec, seen = serve(t, cfg, "bravo.shikshit.app", "/x", nil)
	if rec.Code != http.StatusNoContent || seen == nil || seen.Code != "bravo" {
		t.Fatalf("onboarding institutes must be served, got code=%d tenant=%+v", rec.Code, seen)
	}

	// Attacker-controlled hints must not select a different institute.
	for _, h := range []map[string]string{
		{"X-Institute-ID": "22222222-2222-2222-2222-222222222222"},
		{"X-Tenant": "bravo"},
		{"X-Tenant-Code": "bravo"},
		{"X-Forwarded-Host": "bravo.shikshit.app"}, // ignored unless TrustForwardedHost is on
	} {
		_, seen = serve(t, cfg, "alpha.shikshit.app", "/x?tenant=bravo", h)
		if seen == nil || seen.Code != "alpha" {
			t.Fatalf("headers %v must not change the tenant, got %+v", h, seen)
		}
	}

	cfg.TrustForwardedHost = true
	_, seen = serve(t, cfg, "internal-lb", "/x", map[string]string{"X-Forwarded-Host": "bravo.shikshit.app"})
	if seen == nil || seen.Code != "bravo" {
		t.Fatalf("behind a trusted proxy the forwarded host applies, got %+v", seen)
	}
}

func TestMiddlewareRejectsUnknownSuspendedAndArchivedInstitutes(t *testing.T) {
	res := &fakeResolver{tenants: map[string]tenancy.Tenant{
		"paused":  {ID: "33333333-3333-3333-3333-333333333333", Code: "paused", Status: "suspended"},
		"retired": {ID: "44444444-4444-4444-4444-444444444444", Code: "retired", Status: "archived"},
	}}
	cfg := tenancy.MiddlewareConfig{Config: tenancy.Config{BaseDomains: []string{"shikshit.app"}}, Resolver: res}

	for host, want := range map[string]struct {
		status int
		code   string
	}{
		"ghost.shikshit.app":   {http.StatusNotFound, "tenant_not_found"},
		"retired.shikshit.app": {http.StatusNotFound, "tenant_not_found"}, // indistinguishable from unknown
		"paused.shikshit.app":  {http.StatusForbidden, "tenant_suspended"},
		"shikshit.app":         {http.StatusNotFound, "tenant_not_found"}, // no key in the host
	} {
		rec, seen := serve(t, cfg, host, "/x", nil)
		if rec.Code != want.status || problemCode(t, rec) != want.code || seen != nil {
			t.Errorf("%s: got %d %s (tenant bound: %v), want %d %s", host, rec.Code, rec.Body.String(), seen != nil, want.status, want.code)
		}
	}

	res.err = errors.New("database unavailable")
	rec, _ := serve(t, cfg, "paused.shikshit.app", "/x", nil)
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != "tenant_lookup_failed" {
		t.Errorf("a failing lookup must fail closed with 503, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestMiddlewareSkipAndDevelopmentDefault(t *testing.T) {
	res := &fakeResolver{tenants: map[string]tenancy.Tenant{"demo": {ID: "55555555-5555-5555-5555-555555555555", Code: "demo", Status: "live"}}}
	cfg := tenancy.MiddlewareConfig{
		Config:   tenancy.Config{BaseDomains: []string{"shikshit.app"}, DefaultTenant: "demo"},
		Resolver: res,
		Skip:     func(c echo.Context) bool { return c.Request().URL.Path == "/healthz" },
	}
	rec, seen := serve(t, cfg, "localhost:5173", "/anything", nil)
	if rec.Code != http.StatusNoContent || seen == nil || seen.Code != "demo" {
		t.Fatalf("the configured development default must apply on localhost, got %d %+v", rec.Code, seen)
	}
	before := res.calls
	rec, seen = serve(t, cfg, "localhost:5173", "/healthz", nil)
	if rec.Code != http.StatusNoContent || seen != nil || res.calls != before {
		t.Fatalf("skipped routes must not resolve a tenant, got %d tenant=%v calls=%d->%d", rec.Code, seen, before, res.calls)
	}
	cfg.DefaultTenant = "" // production
	if rec, _ = serve(t, cfg, "localhost:5173", "/anything", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("without a default, an unknown host must be rejected, got %d", rec.Code)
	}
}

func TestCachedResolver(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	inner := &fakeResolver{tenants: map[string]tenancy.Tenant{"alpha": {ID: "1", Code: "alpha", Status: "live"}}}
	c := &tenancy.CachedResolver{Next: inner, TTL: 30 * time.Second, NegativeTTL: 5 * time.Second, MaxEntries: 3, Now: func() time.Time { return now }}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Resolve(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("hits must be cached, inner called %d times", inner.calls)
	}
	now = now.Add(31 * time.Second)
	_, _ = c.Resolve(ctx, "alpha")
	if inner.calls != 2 {
		t.Fatalf("entries must expire after the TTL, inner called %d times", inner.calls)
	}

	// Misses are cached only briefly, so a newly created institute becomes reachable quickly.
	_, _ = c.Resolve(ctx, "new")
	_, _ = c.Resolve(ctx, "new")
	if inner.calls != 3 {
		t.Fatalf("misses must be cached, inner called %d times", inner.calls)
	}
	inner.tenants["new"] = tenancy.Tenant{ID: "2", Code: "new", Status: "live"}
	now = now.Add(6 * time.Second)
	if _, err := c.Resolve(ctx, "new"); err != nil {
		t.Fatalf("a new institute must resolve once the negative TTL passed: %v", err)
	}

	// Infrastructure errors are never cached.
	inner.err = errors.New("db down")
	_, _ = c.Resolve(ctx, "flaky")
	_, _ = c.Resolve(ctx, "flaky")
	inner.err = nil
	inner.tenants["flaky"] = tenancy.Tenant{ID: "3", Code: "flaky", Status: "live"}
	if _, err := c.Resolve(ctx, "flaky"); err != nil {
		t.Fatalf("an outage must not poison the cache: %v", err)
	}

	// The key space is attacker-controlled, so memory is bounded.
	for i := 0; i < 50; i++ {
		_, _ = c.Resolve(ctx, "junk"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	c.Invalidate("alpha")
}
