package seed_test

import (
	"context"
	"os"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/seed"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func TestDemoCreatesTheInstituteOnceAndIsQueryable(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t)
	adm := d.Admin(t)

	first, err := seed.Demo(ctx, adm)
	if err != nil {
		t.Fatal(err)
	}
	if first.TenantID == "" || first.RolesCreated != 19 || first.ModulesAdded < 20 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	second, err := seed.Demo(ctx, adm)
	if err != nil {
		t.Fatal(err)
	}
	if second.TenantID != first.TenantID || second.RolesCreated != 0 || second.ModulesAdded != 0 {
		t.Fatalf("re-running must change nothing, got %+v", second)
	}

	// Queryable the way the API will query it: as the runtime role, bound to the institute.
	app := d.App(t)
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', 'tenant', true)", first.TenantID); err != nil {
		t.Fatal(err)
	}
	var roles, modules int
	if err := tx.QueryRow(ctx, "SELECT (SELECT count(*) FROM roles), (SELECT count(*) FROM tenant_modules WHERE enabled)").Scan(&roles, &modules); err != nil {
		t.Fatal(err)
	}
	if roles != 19 || modules != first.ModulesAdded {
		t.Fatalf("the institute sees roles=%d modules=%d", roles, modules)
	}
	var admin bool
	if err := tx.QueryRow(ctx, "SELECT is_system FROM roles WHERE code = 'admin'").Scan(&admin); err != nil || !admin {
		t.Fatalf("admin role missing or not a system role: %v", err)
	}
}
