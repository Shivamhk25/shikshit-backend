package migrations_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
	"github.com/Shivamhk25/shikshit-backend/backend/migrations"
)

var principalGrants = []string{"onb.workspace.view", "onb.imports.*", "onb.academic.*"}

func grantsOf(t *testing.T, d *dbtest.DB, tenant, role string) map[string]bool {
	t.Helper()
	conn := d.Admin(t)
	defer conn.Close(bg)
	rows, err := conn.Query(bg, `SELECT rp.permission_code FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.tenant_id = $1 AND r.code = $2`, tenant, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		out[c] = true
	}
	return out
}

func TestANewInstitutesPrincipalCanImportAcademicData(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)
	tenant := newTenant(t, conn, "alpha")
	provision(t, conn, tenant)
	got := grantsOf(t, d, tenant, "principal")
	for _, g := range principalGrants {
		if !got[g] {
			t.Errorf("principal template lacks %s", g)
		}
	}
	for _, role := range []string{"teacher", "registrar"} {
		if grantsOf(t, d, tenant, role)["onb.academic.*"] {
			t.Errorf("%s gained the academic import grant", role)
		}
	}
}

// Institutes created before migration 000012 already have a Principal role without the grants. Re-running the
// migration must add them to roles that still carry the template, leave other roles alone, bump holders' access so
// signed-in principals pick it up, and change nothing the second time.
func TestMigrationGrantsExistingPrincipalRolesAndIsIdempotent(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)
	tenant := newTenant(t, conn, "alpha")
	provision(t, conn, tenant)
	user := newUser(t, conn, tenant, "pria")
	admin(t, conn, `INSERT INTO user_roles (tenant_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE tenant_id = $1 AND code = 'principal'`, tenant, user)

	// put the institute back in its pre-migration state, and add a role that merely resembles a principal
	admin(t, conn, `DELETE FROM role_permissions USING roles r WHERE role_permissions.role_id = r.id AND r.tenant_id = $1 AND r.code = 'principal' AND role_permissions.permission_code = ANY($2)`, tenant, principalGrants)
	admin(t, conn, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'vice_principal', 'Vice principal')`, tenant)
	before := accessVersion(t, conn, user)

	sql, err := migrations.FS.ReadFile("000012_principal_import_access.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(bg, string(sql)); err != nil {
		t.Fatal(err)
	}
	got := grantsOf(t, d, tenant, "principal")
	for _, g := range principalGrants {
		if !got[g] {
			t.Errorf("existing principal role lacks %s after the migration", g)
		}
	}
	if len(grantsOf(t, d, tenant, "vice_principal")) != 0 {
		t.Error("a role that only looks like a principal was granted access")
	}
	if after := accessVersion(t, conn, user); after <= before {
		t.Errorf("the principal's access version did not move (%d -> %d)", before, after)
	}

	n := len(got)
	again := accessVersion(t, conn, user)
	if _, err := conn.Exec(bg, string(sql)); err != nil {
		t.Fatal(err)
	}
	if len(grantsOf(t, d, tenant, "principal")) != n {
		t.Error("running the migration twice changed the grants")
	}
	if accessVersion(t, conn, user) != again {
		t.Error("an idempotent re-run still bumped the access version")
	}
}

func TestAnInstituteCanChangeOnlyItsOwnApprovalSwitch(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)
	a, b := newTenant(t, conn, "alpha"), newTenant(t, conn, "bravo")
	setting := func(tenant string) *string {
		var v *string
		_ = conn.QueryRow(bg, `SELECT settings->>'importApprovalRequired' FROM tenants WHERE id = $1`, tenant).Scan(&v)
		return v
	}
	if setting(a) != nil || setting(b) != nil {
		t.Fatal("the setting should start absent")
	}

	// a tenant session changes its own institute and no other
	commitCtx(t, d.App(t), a, "tenant", func(tx pgx.Tx) {
		if _, err := tx.Exec(bg, `SELECT set_import_approval_required(false)`); err != nil {
			t.Fatal(err)
		}
	})
	if v := setting(a); v == nil || *v != "false" {
		t.Fatalf("alpha's setting = %v", v)
	}
	if setting(b) != nil {
		t.Fatal("another institute's setting changed")
	}

	// it cannot be used without an institute, and an anonymous session cannot reach the table directly either
	expectCode(t, d.App(t), "", "tenant", "42501", `SELECT set_import_approval_required(true)`)
	inCtx(t, d.App(t), a, "tenant", func(tx pgx.Tx) {
		tag, err := tx.Exec(bg, `UPDATE tenants SET settings = '{}'::jsonb WHERE id = $1`, a)
		if err != nil || tag.RowsAffected() != 0 {
			t.Fatalf("a tenant session wrote to tenants directly: %v rows=%d", err, tag.RowsAffected())
		}
	})
	if v := setting(a); v == nil || *v != "false" {
		t.Fatalf("a refused write changed the setting: %v", v)
	}

	// other keys of the settings object are left alone
	admin(t, conn, `UPDATE tenants SET settings = settings || '{"keep":"me"}'::jsonb WHERE id = $1`, a)
	commitCtx(t, d.App(t), a, "tenant", func(tx pgx.Tx) {
		if _, err := tx.Exec(bg, `SELECT set_import_approval_required(true)`); err != nil {
			t.Fatal(err)
		}
	})
	if mustScan[string](t, conn.QueryRow(bg, `SELECT settings->>'keep' FROM tenants WHERE id = $1`, a)) != "me" {
		t.Fatal("another settings key was lost")
	}
}

func TestThePrincipalCanManageImportApproval(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)
	tenant := newTenant(t, conn, "alpha")
	provision(t, conn, tenant)
	if !grantsOf(t, d, tenant, "principal")["onb.maker_checker.*"] {
		t.Fatal("a new institute's principal cannot manage approval")
	}
	for _, role := range []string{"teacher", "registrar"} {
		if grantsOf(t, d, tenant, role)["onb.maker_checker.*"] {
			t.Errorf("%s can manage approval", role)
		}
	}

	// an institute created before this migration gets it, a lookalike role does not, and a re-run changes nothing
	user := newUser(t, conn, tenant, "pria")
	admin(t, conn, `INSERT INTO user_roles (tenant_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE tenant_id = $1 AND code = 'principal'`, tenant, user)
	admin(t, conn, `DELETE FROM role_permissions USING roles r WHERE role_permissions.role_id = r.id AND r.tenant_id = $1 AND r.code = 'principal' AND role_permissions.permission_code = 'onb.maker_checker.*'`, tenant)
	admin(t, conn, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'vice_principal', 'Vice principal')`, tenant)
	before := accessVersion(t, conn, user)
	sql, err := migrations.FS.ReadFile("000015_principal_manages_approval.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(bg, string(sql)); err != nil {
		t.Fatal(err)
	}
	if !grantsOf(t, d, tenant, "principal")["onb.maker_checker.*"] {
		t.Fatal("an existing principal role did not get the grant")
	}
	if len(grantsOf(t, d, tenant, "vice_principal")) != 0 {
		t.Fatal("a lookalike role was granted access")
	}
	if accessVersion(t, conn, user) <= before {
		t.Fatal("the principal's access version did not move")
	}
	again := accessVersion(t, conn, user)
	if _, err := conn.Exec(bg, string(sql)); err != nil {
		t.Fatal(err)
	}
	if accessVersion(t, conn, user) != again {
		t.Fatal("an idempotent re-run bumped the access version")
	}
}
