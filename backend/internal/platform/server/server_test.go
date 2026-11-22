package server_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/server"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/seed"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

var mfaKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32)))

// totp computes an authenticator code the way a phone app would (RFC 6238, SHA-1, 6 digits, 30 s).
func totp(t *testing.T, otpauth string, at time.Time) string {
	t.Helper()
	u, err := url.Parse(otpauth)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

var goodEnv = map[string]string{
	"SHIKSHIT_DATABASE_URL": "postgres://x",
	"SHIKSHIT_JWT_SECRET":   strings.Repeat("s", 32),
}

func with(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range goodEnv {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestLoadConfigDefaults(t *testing.T) {
	c, err := server.LoadConfig(env(goodEnv))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.JWTIssuer != "shikshit" || c.AccessTTL.Minutes() != 15 || c.Production {
		t.Errorf("defaults = %+v", c)
	}
}

func TestLoadConfigRejectsUnsafeSettings(t *testing.T) {
	cases := map[string]map[string]string{
		"no database":            {"SHIKSHIT_DATABASE_URL": ""},
		"no secret":              {"SHIKSHIT_JWT_SECRET": ""},
		"short secret":           {"SHIKSHIT_JWT_SECRET": "too-short"},
		"wildcard cors":          {"SHIKSHIT_ALLOWED_ORIGINS": "https://a.example, *"},
		"bad ttl":                {"SHIKSHIT_ACCESS_TTL": "5s"},
		"ttl too long":           {"SHIKSHIT_ACCESS_TTL": "24h"},
		"bad bool":               {"SHIKSHIT_TRUST_FORWARDED_HOST": "maybe"},
		"prod default tenant":    {"SHIKSHIT_ENV": "production", "SHIKSHIT_BASE_DOMAINS": "shikshit.app", "SHIKSHIT_DEFAULT_TENANT": "demo"},
		"prod without base host": {"SHIKSHIT_ENV": "production"},
		"prod without mfa key":   {"SHIKSHIT_ENV": "production", "SHIKSHIT_BASE_DOMAINS": "shikshit.app"},
		"prod mfa not required":  {"SHIKSHIT_ENV": "production", "SHIKSHIT_BASE_DOMAINS": "shikshit.app", "SHIKSHIT_MFA_KEY": mfaKey, "SHIKSHIT_MFA_REQUIRED_FOR_ADMINS": "false"},
		"short mfa key":          {"SHIKSHIT_MFA_KEY": base64.StdEncoding.EncodeToString([]byte("short"))},
		"mfa key not base64":     {"SHIKSHIT_MFA_KEY": "not base64!"},
	}
	for name, extra := range cases {
		if _, err := server.LoadConfig(env(with(extra))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := server.LoadConfig(env(with(map[string]string{"SHIKSHIT_ENV": "production", "SHIKSHIT_BASE_DOMAINS": "shikshit.app", "SHIKSHIT_MFA_KEY": mfaKey}))); err != nil {
		t.Errorf("valid production config rejected: %v", err)
	}
}

func TestLoadConfigErrorsNeverEchoTheSecret(t *testing.T) {
	_, err := server.LoadConfig(env(with(map[string]string{"SHIKSHIT_JWT_SECRET": "short-secret-value"})))
	if err == nil || strings.Contains(err.Error(), "short-secret-value") {
		t.Fatalf("err = %v", err)
	}
}

func newServer(t *testing.T) *echo.Echo {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	t.Cleanup(func() { _ = adm.Close(bg) })
	res, err := seed.Demo(bg, adm)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := identity.HashPassword("S3cure-pass!")
	var uid string
	if err := adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash)
		VALUES ($1, 'boss', 'Boss', 'active', $2) RETURNING id::text`, res.TenantID, hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE tenant_id = $1 AND code = 'admin'`, res.TenantID, uid); err != nil {
		t.Fatal(err)
	}
	pc, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cfg, err := server.LoadConfig(env(with(map[string]string{
		"SHIKSHIT_BASE_DOMAINS":    "shikshit.test",
		"SHIKSHIT_ALLOWED_ORIGINS": "https://demo.shikshit.test",
	})))
	if err != nil {
		t.Fatal(err)
	}
	e, err := server.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func do(e *echo.Echo, method, host, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != "" && hdr["Content-Type"] == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestSignInAndReadAccessThroughTheAssembledServer(t *testing.T) {
	e := newServer(t)
	login := func() *httptest.ResponseRecorder {
		return do(e, "POST", "demo.shikshit.test", "/api/v1/auth/login", `{"username":"boss","password":"S3cure-pass!"}`, map[string]string{"X-Client": "ios"})
	}
	// The demo Admin has no two-step sign-in yet, and the server requires it for Admins: no tokens, a setup step.
	rec := login()
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"mfaRequired":"setup"`) || strings.Contains(rec.Body.String(), "accessToken") {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	var ch struct{ ChallengeToken string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	rec = do(e, "POST", "demo.shikshit.test", "/api/v1/auth/mfa/setup", `{"challengeToken":"`+ch.ChallengeToken+`"}`, nil)
	var setup struct{ Secret, OtpauthUri string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &setup) != nil || !strings.HasPrefix(setup.OtpauthUri, "otpauth://totp/") {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	confirm := func(code string) *httptest.ResponseRecorder {
		return do(e, "POST", "demo.shikshit.test", "/api/v1/auth/mfa/setup/confirm", `{"challengeToken":"`+ch.ChallengeToken+`","code":"`+code+`"}`, map[string]string{"X-Client": "ios"})
	}
	if rec := confirm("000000"); rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid_code") {
		t.Fatalf("wrong first code = %d %s", rec.Code, rec.Body)
	}
	rec = confirm(totp(t, setup.OtpauthUri, time.Now()))
	var done struct {
		AccessToken   string
		RecoveryCodes []string
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &done) != nil || done.AccessToken == "" || len(done.RecoveryCodes) != 10 {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body)
	}
	// The challenge is used up.
	if rec := confirm(totp(t, setup.OtpauthUri, time.Now())); rec.Code != 401 || !strings.Contains(rec.Body.String(), "challenge_expired") {
		t.Fatalf("reused challenge = %d %s", rec.Code, rec.Body)
	}
	// Next sign-in asks for a code; a recovery code works once.
	rec = login()
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"mfaRequired":"verify"`) {
		t.Fatalf("second login = %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	verify := func(token, code string) *httptest.ResponseRecorder {
		return do(e, "POST", "demo.shikshit.test", "/api/v1/auth/mfa/verify", `{"challengeToken":"`+token+`","code":"`+code+`"}`, map[string]string{"X-Client": "ios"})
	}
	rec = verify(ch.ChallengeToken, strings.ToUpper(done.RecoveryCodes[0]))
	if rec.Code != 200 {
		t.Fatalf("recovery code = %d %s", rec.Code, rec.Body)
	}
	var tok struct{ AccessToken string }
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	_ = json.Unmarshal(login().Body.Bytes(), &ch)
	if rec := verify(ch.ChallengeToken, done.RecoveryCodes[0]); rec.Code != 401 {
		t.Fatalf("recovery code used twice = %d %s", rec.Code, rec.Body)
	}

	rec = do(e, "GET", "demo.shikshit.test", "/api/v1/me/access", "", map[string]string{"Authorization": "Bearer " + tok.AccessToken})
	if rec.Code != 200 {
		t.Fatalf("access = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("X-Request-Id") == "" || rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("hardening headers missing: %v", rec.Header())
	}
}

func TestLivenessNeedsNoInstitute(t *testing.T) {
	e := newServer(t)
	if rec := do(e, "GET", "10.0.0.5:8080", "/livez", "", nil); rec.Code != 200 {
		t.Fatalf("/livez = %d", rec.Code)
	}
	if rec := do(e, "GET", "10.0.0.5:8080", "/api/v1/healthz", "", nil); rec.Code != 404 {
		t.Fatalf("API without an institute = %d, want 404 tenant_not_found", rec.Code)
	}
}

func TestUnknownInstituteIsNotFound(t *testing.T) {
	e := newServer(t)
	rec := do(e, "POST", "ghost.shikshit.test", "/api/v1/auth/login", `{"username":"boss","password":"S3cure-pass!"}`, nil)
	if rec.Code != 404 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestCORSAllowsOnlyListedOrigins(t *testing.T) {
	e := newServer(t)
	pre := func(origin string) *httptest.ResponseRecorder {
		return do(e, "OPTIONS", "demo.shikshit.test", "/api/v1/me/access", "", map[string]string{
			"Origin": origin, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization,x-client",
		})
	}
	ok := pre("https://demo.shikshit.test")
	if ok.Code != http.StatusNoContent || ok.Header().Get("Access-Control-Allow-Origin") != "https://demo.shikshit.test" {
		t.Errorf("listed origin: %d %v", ok.Code, ok.Header())
	}
	bad := pre("https://evil.example")
	if bad.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("unlisted origin was allowed: %v", bad.Header())
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	e := newServer(t)
	big := `{"username":"boss","password":"` + strings.Repeat("a", 2<<20) + `"}`
	if rec := do(e, "POST", "demo.shikshit.test", "/api/v1/auth/login", big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestClientAddressIsNotTakenFromForwardedHeaderUnlessTrusted(t *testing.T) {
	e := newServer(t) // TrustForwardedHost is false
	rec := do(e, "POST", "demo.shikshit.test", "/api/v1/auth/login", `{"username":"boss","password":"S3cure-pass!"}`,
		map[string]string{"X-Forwarded-For": "203.0.113.99", "X-Forwarded-Host": "ghost.shikshit.test"})
	if rec.Code != 202 { // the demo institute's Admin, reached through the real host: password right, second step next
		t.Fatalf("an untrusted X-Forwarded-Host must be ignored; status = %d", rec.Code)
	}
}
