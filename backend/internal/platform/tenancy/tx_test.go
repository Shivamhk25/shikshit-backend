package tenancy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

var bg = context.Background()

type fixture struct {
	pool       *pgxpool.Pool // runtime role, RLS applies; ONE connection so reuse between requests is guaranteed
	alpha, bra string
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	mk := func(code string) string {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b := mk("alpha"), mk("bravo")
	for _, u := range []struct{ tenant, name string }{{a, "asha"}, {a, "arun"}, {b, "bala"}} {
		if _, err := adm.Exec(bg, "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, $2::text, $2::text)", u.tenant, u.name); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(d.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return fixture{pool: pool, alpha: a, bra: b}
}

func usernames(t *testing.T, tx pgx.Tx) string {
	t.Helper()
	rows, err := tx.Query(bg, "SELECT username::text FROM users ORDER BY username")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		names = append(names, n)
	}
	return strings.Join(names, ",")
}

func TestPgResolverWorksBeforeLoginThroughTheRuntimeRole(t *testing.T) {
	f := setup(t)
	r := tenancy.PgResolver{DB: f.pool}
	got, err := r.Resolve(bg, "alpha")
	if err != nil || got.ID != f.alpha || got.Status != "onboarding" {
		t.Fatalf("Resolve(alpha) = %+v, %v", got, err)
	}
	if _, err := r.Resolve(bg, "nobody"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatalf("unknown institute must be ErrNotFound, got %v", err)
	}
}

func TestWithTxBindsRowLevelSecurityToTheTenant(t *testing.T) {
	f := setup(t)
	err := tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
		if got := usernames(t, tx); got != "arun,asha" {
			t.Errorf("alpha sees %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = tenancy.WithTx(bg, f.pool, f.bra, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
		if got := usernames(t, tx); got != "bala" {
			t.Errorf("bravo sees %q", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The pool has a single connection, so every call below reuses the same session. Tenant context must not survive
// the transaction, otherwise one request could read the previous request's institute.
func TestTenantContextNeverLeaksToTheNextRequestOnAReusedConnection(t *testing.T) {
	f := setup(t)
	if err := tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pool.QueryRow(bg, "SELECT count(*) FROM users").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a later query on the reused connection saw %d users: tenant context leaked", n)
	}
	// ...including after a rollback caused by an error.
	_ = tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error { return errors.New("boom") })
	if err := f.pool.QueryRow(bg, "SELECT count(*) FROM users").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after a failed request the connection still carries context: n=%d err=%v", n, err)
	}
}

func TestWithTxRollsBackOnErrorAndRejectsBadInput(t *testing.T) {
	f := setup(t)
	boom := errors.New("boom")
	err := tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, 'ghost', 'Ghost')", f.alpha); e != nil {
			t.Fatal(e)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("the callback's error must be returned, got %v", err)
	}
	_ = tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
		if got := usernames(t, tx); strings.Contains(got, "ghost") {
			t.Errorf("the failed request's insert was committed: %q", got)
		}
		return nil
	})

	if err := tenancy.WithTx(bg, f.pool, "", tenancy.TierTenant, func(context.Context, pgx.Tx) error { return nil }); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("an institute user without a tenant must be rejected, got %v", err)
	}
	if err := tenancy.WithTx(bg, f.pool, "x'; DROP TABLE users;--", tenancy.TierTenant, func(context.Context, pgx.Tx) error { return nil }); err == nil {
		t.Error("a malformed tenant id must be rejected before touching the database")
	}
	if err := tenancy.WithTx(bg, f.pool, f.alpha, "root", func(context.Context, pgx.Tx) error { return nil }); err == nil {
		t.Error("an unknown tier must be rejected")
	}
}

func TestInstituteWritesCannotTargetAnotherInstitute(t *testing.T) {
	f := setup(t)
	err := tenancy.WithTx(bg, f.pool, f.alpha, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, 'mallory', 'M')", f.bra)
		return e
	})
	if err == nil {
		t.Fatal("an institute user must not be able to insert into another institute")
	}
}

func TestPlatformTierNeedsAnExplicitInstituteToReadItsData(t *testing.T) {
	f := setup(t)
	// Platform tier WITHOUT entering an institute: no institute data (decision D1).
	_ = tenancy.WithTx(bg, f.pool, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		if got := usernames(t, tx); got != "" {
			t.Errorf("platform tier saw users without entering an institute: %q", got)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM tenants").Scan(&n); err != nil || n != 2 {
			t.Errorf("platform tier must still see the institute directory, got %d (%v)", n, err)
		}
		return nil
	})
	// Entering an institute is explicit and scoped to that institute only.
	_ = tenancy.WithTx(bg, f.pool, f.bra, tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		if got := usernames(t, tx); got != "bala" {
			t.Errorf("platform tier inside bravo sees %q", got)
		}
		return nil
	})
}

// End to end: host -> middleware -> handler -> WithTenantTx -> RLS.
func TestRequestForOneInstituteNeverSeesAnothersRows(t *testing.T) {
	f := setup(t)
	e := echo.New()
	e.Use(tenancy.Middleware(tenancy.MiddlewareConfig{
		Config:   tenancy.Config{BaseDomains: []string{"shikshit.app"}},
		Resolver: &tenancy.CachedResolver{Next: tenancy.PgResolver{DB: f.pool}},
	}))
	e.GET("/users", func(c echo.Context) error {
		var out string
		err := tenancy.WithTenantTx(c.Request().Context(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
			out = usernames(t, tx)
			return nil
		})
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, out)
	})
	get := func(host string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/users?tenant=bravo", nil)
		req.Host = host
		req.Header.Set("X-Institute-ID", f.bra) // a hostile client hint must be ignored
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := get("alpha.shikshit.app"); code != 200 || body != "arun,asha" {
		t.Fatalf("alpha.shikshit.app -> %d %q", code, body)
	}
	if code, body := get("bravo.shikshit.app"); code != 200 || body != "bala" {
		t.Fatalf("bravo.shikshit.app -> %d %q", code, body)
	}
	if code, _ := get("ghost.shikshit.app"); code != http.StatusNotFound {
		t.Fatalf("unknown institute -> %d", code)
	}
}
