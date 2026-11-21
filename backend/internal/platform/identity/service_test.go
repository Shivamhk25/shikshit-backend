package identity_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

const password = "S3cure-pass!"

type fixture struct {
	svc          *identity.Service
	signer       *identity.Signer
	adminPool    *pgxpool.Pool
	alpha, bravo tenancy.Tenant
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(code string) tenancy.Tenant {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return tenancy.Tenant{ID: id, Code: code, Name: code, Status: "live"}
	}
	f := fixture{alpha: mk("alpha"), bravo: mk("bravo")}
	for _, u := range []struct{ tenant, name, status, hash string }{
		{f.alpha.ID, "asha", "active", hash},
		{f.alpha.ID, "dormant", "disabled", hash},
		{f.alpha.ID, "googleonly", "active", ""},
		{f.bravo.ID, "bala", "active", hash},
	} {
		if _, err := adm.Exec(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash)
			VALUES ($1, $2::text, $2::text, $3, NULLIF($4, ''))`, u.tenant, u.name, u.status, u.hash); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := pgxpool.ParseConfig(d.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	adminCfg, _ := pgxpool.ParseConfig(d.AdminDSN)
	f.adminPool, err = pgxpool.NewWithConfig(bg, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.adminPool.Close)

	f.signer, err = identity.NewSigner([]byte(strings.Repeat("k", 32)), "shikshit", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	f.svc, err = identity.NewService(pool, f.signer, identity.DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func in(t tenancy.Tenant) context.Context { return tenancy.WithTenant(bg, t) }

var web = identity.Client{Name: "web", Version: "1.0.0", IP: "203.0.113.7", UserAgent: "test"}

func TestLoginSuccess(t *testing.T) {
	f := setup(t)
	tok, err := f.svc.Login(in(f.alpha), "asha", password, web)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.signer.Verify(tok.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.TenantID != f.alpha.ID || c.Subject != tok.UserID || c.Tier != "tenant" {
		t.Errorf("claims = %+v", c)
	}
	if tok.RefreshToken == "" || !tok.RefreshExpiresAt.After(time.Now().Add(29*24*time.Hour)) {
		t.Errorf("refresh token = %+v", tok)
	}

	// The plain refresh token is never stored.
	var n int
	if err := f.adminPool.QueryRow(bg, "SELECT count(*) FROM sessions WHERE token_hash = $1", tok.RefreshToken).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refresh token stored in clear: n=%d err=%v", n, err)
	}
	if err := f.adminPool.QueryRow(bg, "SELECT count(*) FROM sessions WHERE tenant_id = $1 AND user_id::text = $2", f.alpha.ID, tok.UserID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("sessions = %d err=%v", n, err)
	}
	var last *time.Time
	_ = f.adminPool.QueryRow(bg, "SELECT last_login_at FROM users WHERE id::text = $1", tok.UserID).Scan(&last)
	if last == nil {
		t.Error("last_login_at not set")
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	f := setup(t)
	for name, c := range map[string]struct{ user, pass string }{
		"unknown user":      {"nobody", password},
		"wrong password":    {"asha", "nope"},
		"disabled account":  {"dormant", password},
		"google-only user":  {"googleonly", password},
		"other institute's": {"bala", password}, // exists only in bravo
	} {
		_, err := f.svc.Login(in(f.alpha), c.user, c.pass, web)
		if !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
		}
	}
}

func TestLoginIsScopedToTheInstitute(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Login(in(f.bravo), "bala", password, web); err != nil {
		t.Fatalf("bala in bravo: %v", err)
	}
	if _, err := f.svc.Login(in(f.bravo), "asha", password, web); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("asha in bravo: %v", err)
	}
}

func TestLoginNeedsTenant(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Login(bg, "asha", password, web); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Fatalf("err = %v", err)
	}
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	f := setup(t)
	for i := 0; i < identity.DefaultPolicy.MaxFailedLogins; i++ {
		if _, err := f.svc.Login(in(f.alpha), "asha", "bad", web); !errors.Is(err, identity.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Correct password while locked is refused, and says why.
	if _, err := f.svc.Login(in(f.alpha), "asha", password, web); !errors.Is(err, identity.ErrLocked) {
		t.Fatalf("locked login: %v", err)
	}
	// A wrong password while locked must not reveal the lock.
	if _, err := f.svc.Login(in(f.alpha), "asha", "bad", web); !errors.Is(err, identity.ErrInvalidCredentials) {
		t.Fatalf("locked + wrong password: %v", err)
	}
	// Lock expires.
	if _, err := f.adminPool.Exec(bg, "UPDATE users SET locked_until = now() - interval '1 second' WHERE username = 'asha'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Login(in(f.alpha), "asha", password, web); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
}

func TestSuccessResetsFailureCounter(t *testing.T) {
	f := setup(t)
	for i := 0; i < identity.DefaultPolicy.MaxFailedLogins-1; i++ {
		_, _ = f.svc.Login(in(f.alpha), "asha", "bad", web)
	}
	if _, err := f.svc.Login(in(f.alpha), "asha", password, web); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < identity.DefaultPolicy.MaxFailedLogins-1; i++ {
		_, _ = f.svc.Login(in(f.alpha), "asha", "bad", web)
	}
	if _, err := f.svc.Login(in(f.alpha), "asha", password, web); err != nil {
		t.Fatalf("counter was not reset by the successful login: %v", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	f := setup(t)
	first, err := f.svc.Login(in(f.alpha), "asha", password, web)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.Refresh(in(f.alpha), first.RefreshToken, web)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken || second.AccessToken == "" || second.UserID != first.UserID {
		t.Fatalf("rotation did not produce a new token: %+v", second)
	}
	third, err := f.svc.Refresh(in(f.alpha), second.RefreshToken, web)
	if err != nil {
		t.Fatalf("second rotation: %v", err)
	}

	var family int
	_ = f.adminPool.QueryRow(bg, "SELECT count(DISTINCT family_id) FROM sessions WHERE user_id::text = $1", third.UserID).Scan(&family)
	if family != 1 {
		t.Errorf("rotation must stay in one family, got %d", family)
	}
	var replaced int
	_ = f.adminPool.QueryRow(bg, "SELECT count(*) FROM sessions WHERE replaced_by IS NOT NULL").Scan(&replaced)
	if replaced != 2 {
		t.Errorf("replaced_by links = %d, want 2", replaced)
	}
}

func TestRefreshReuseRevokesTheFamily(t *testing.T) {
	f := setup(t)
	first, _ := f.svc.Login(in(f.alpha), "asha", password, web)
	second, err := f.svc.Refresh(in(f.alpha), first.RefreshToken, web)
	if err != nil {
		t.Fatal(err)
	}
	// An attacker (or a buggy client) replays the old token.
	if _, err := f.svc.Refresh(in(f.alpha), first.RefreshToken, web); !errors.Is(err, identity.ErrRefreshReuse) {
		t.Fatalf("replay: %v", err)
	}
	// The legitimate newest token is now dead too.
	if _, err := f.svc.Refresh(in(f.alpha), second.RefreshToken, web); !errors.Is(err, identity.ErrRefreshReuse) {
		t.Fatalf("newest token after reuse: %v", err)
	}
	var live int
	_ = f.adminPool.QueryRow(bg, "SELECT count(*) FROM sessions WHERE revoked_at IS NULL").Scan(&live)
	if live != 0 {
		t.Errorf("%d sessions still live after reuse", live)
	}
}

func TestRefreshRejections(t *testing.T) {
	f := setup(t)
	tok, _ := f.svc.Login(in(f.alpha), "asha", password, web)

	if _, err := f.svc.Refresh(in(f.alpha), "unknown-token", web); !errors.Is(err, identity.ErrInvalidRefresh) {
		t.Errorf("unknown: %v", err)
	}
	// A token from another institute must not work here.
	if _, err := f.svc.Refresh(in(f.bravo), tok.RefreshToken, web); !errors.Is(err, identity.ErrInvalidRefresh) {
		t.Errorf("cross-institute: %v", err)
	}
	// Expired.
	if _, err := f.adminPool.Exec(bg, "UPDATE sessions SET expires_at = now() - interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(in(f.alpha), tok.RefreshToken, web); !errors.Is(err, identity.ErrInvalidRefresh) {
		t.Errorf("expired: %v", err)
	}
}

func TestRefreshRefusedWhenUserDisabled(t *testing.T) {
	f := setup(t)
	tok, _ := f.svc.Login(in(f.alpha), "asha", password, web)
	if _, err := f.adminPool.Exec(bg, "UPDATE users SET status = 'disabled' WHERE username = 'asha'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(in(f.alpha), tok.RefreshToken, web); !errors.Is(err, identity.ErrInvalidRefresh) {
		t.Fatalf("err = %v", err)
	}
}

func TestLogoutRevokesTheFamilyAndIsIdempotent(t *testing.T) {
	f := setup(t)
	tok, _ := f.svc.Login(in(f.alpha), "asha", password, web)
	rotated, _ := f.svc.Refresh(in(f.alpha), tok.RefreshToken, web)

	if err := f.svc.Logout(in(f.alpha), rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Refresh(in(f.alpha), rotated.RefreshToken, web); !errors.Is(err, identity.ErrRefreshReuse) && !errors.Is(err, identity.ErrInvalidRefresh) {
		t.Fatalf("refresh after logout: %v", err)
	}
	if err := f.svc.Logout(in(f.alpha), rotated.RefreshToken); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if err := f.svc.Logout(in(f.alpha), "never-issued"); err != nil {
		t.Fatalf("unknown token logout: %v", err)
	}
}

// A refresh must wait for whoever holds the session row, then see the result: otherwise two requests carrying the
// same token could both rotate it. The row is held by an admin transaction so the order of events is deterministic.
func TestRefreshWaitsForTheSessionRowLock(t *testing.T) {
	f := setup(t)
	tok, _ := f.svc.Login(in(f.alpha), "asha", password, web)

	holder, err := f.adminPool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(bg) //nolint:errcheck
	if _, err := holder.Exec(bg, "SELECT 1 FROM sessions WHERE user_id::text = $1 FOR UPDATE", tok.UserID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := f.svc.Refresh(in(f.alpha), tok.RefreshToken, web)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("refresh did not wait for the row lock (returned %v)", err)
	case <-time.After(500 * time.Millisecond):
	}

	// The holder rotates the token itself, then releases the lock.
	if _, err := holder.Exec(bg, "UPDATE sessions SET revoked_at = now() WHERE user_id::text = $1", tok.UserID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(bg); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, identity.ErrRefreshReuse) {
			t.Fatalf("after the lock was released the refresh must see the revoked token, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh never finished")
	}
}
