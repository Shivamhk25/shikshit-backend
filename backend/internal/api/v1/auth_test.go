package apiv1_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	apiv1 "github.com/pie360/backend/internal/api/v1"
	"github.com/pie360/backend/internal/platform/identity"
	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

const password = "S3cure-pass!"

// stack is the real thing end to end: host -> tenancy middleware -> routes -> identity service -> Postgres (RLS on).
type stack struct {
	e      *echo.Echo
	signer *identity.Signer
}

type tenantsByCode map[string]tenancy.Tenant

func (m tenantsByCode) Resolve(_ context.Context, key string) (tenancy.Tenant, error) {
	if t, ok := m[key]; ok {
		return t, nil
	}
	return tenancy.Tenant{}, tenancy.ErrNotFound
}

func newStack(t *testing.T) stack {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	tenants := tenantsByCode{}
	for _, code := range []string{"alpha", "bravo"} {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tenants[code] = tenancy.Tenant{ID: id, Code: code, Name: code, Status: "live"}
		if _, err := adm.Exec(bg, "INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3)",
			id, "user-"+code, hash); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	signer, _ := identity.NewSigner([]byte(strings.Repeat("k", 32)), "shikshit", 15*time.Minute)
	svc, err := identity.NewService(pool, signer, identity.DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	e.HTTPErrorHandler = apiv1.ErrorHandler
	e.Use(tenancy.Middleware(tenancy.MiddlewareConfig{
		Config:   tenancy.Config{BaseDomains: []string{"shikshit.test"}},
		Resolver: tenants,
	}))
	apiv1.Register(e, apiv1.New("test", apiv1.WithAuth(svc)))
	return stack{e: e, signer: signer}
}

func (s stack) post(host, path, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, apiv1.BasePath+path, strings.NewReader(body))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

type tokenBody struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not the expected JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

type problemBody struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
}

func login(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return string(b)
}

func TestLoginRefreshLogoutOverHTTP(t *testing.T) {
	s := newStack(t)
	rec := s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "android", "X-Client-Version", "1.2.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	tok := decode[tokenBody](t, rec)
	claims, err := s.signer.Verify(tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.TenantID == "" || claims.Tier != "tenant" {
		t.Errorf("claims = %+v", claims)
	}

	refresh := func(token string) *httptest.ResponseRecorder {
		return s.post("alpha.shikshit.test", "/auth/refresh", `{"refreshToken":"`+token+`"}`)
	}
	rec = refresh(tok.RefreshToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body)
	}
	next := decode[tokenBody](t, rec)
	if next.RefreshToken == tok.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}

	// Replaying the first token is theft: 401 refresh_reuse, and the new token dies too.
	rec = refresh(tok.RefreshToken)
	if rec.Code != http.StatusUnauthorized || decode[problemBody](t, rec).Code != "refresh_reuse" {
		t.Fatalf("replay = %d %s", rec.Code, rec.Body)
	}
	if rec = refresh(next.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token after reuse = %d", rec.Code)
	}

	// Logout is 204 and idempotent.
	fresh := decode[tokenBody](t, s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password)))
	for i := 0; i < 2; i++ {
		if rec = s.post("alpha.shikshit.test", "/auth/logout", `{"refreshToken":"`+fresh.RefreshToken+`"}`); rec.Code != http.StatusNoContent {
			t.Fatalf("logout #%d = %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec = refresh(fresh.RefreshToken); rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d", rec.Code)
	}
}

func TestLoginErrors(t *testing.T) {
	s := newStack(t)
	cases := []struct {
		name, host, body string
		status           int
		code             string
	}{
		{"wrong password", "alpha.shikshit.test", login("user-alpha", "nope"), 401, "invalid_credentials"},
		{"unknown user", "alpha.shikshit.test", login("ghost", password), 401, "invalid_credentials"},
		{"user of another institute", "bravo.shikshit.test", login("user-alpha", password), 401, "invalid_credentials"},
		{"unknown institute", "nowhere.shikshit.test", login("user-alpha", password), 404, "tenant_not_found"},
		{"missing password", "alpha.shikshit.test", `{"username":"user-alpha"}`, 400, "validation_failed"},
		{"short username", "alpha.shikshit.test", login("ab", password), 400, "validation_failed"},
		{"malformed json", "alpha.shikshit.test", `{not json`, 400, "bad_request"},
	}
	for _, c := range cases {
		rec := s.post(c.host, "/auth/login", c.body)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.name, rec.Code, c.status, rec.Body)
			continue
		}
		if got := decode[problemBody](t, rec).Code; got != c.code {
			t.Errorf("%s: code %q, want %q", c.name, got, c.code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s: content type %q", c.name, ct)
		}
	}
}

func TestLockedAccountOverHTTP(t *testing.T) {
	s := newStack(t)
	for i := 0; i < identity.DefaultPolicy.MaxFailedLogins; i++ {
		s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", "bad"))
	}
	rec := s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password))
	if rec.Code != http.StatusLocked || decode[problemBody](t, rec).Code != "account_locked" {
		t.Fatalf("locked = %d %s", rec.Code, rec.Body)
	}
}

func TestTenantComesFromHostNotFromHeaders(t *testing.T) {
	s := newStack(t)
	// A client that claims another institute in a header must not change which institute signs it in.
	rec := s.post("bravo.shikshit.test", "/auth/login", login("user-alpha", password), "X-Institute-ID", "alpha", "X-Tenant", "alpha")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: headers must not select the institute", rec.Code)
	}
}
