package migrations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pie360/backend/internal/shared/dbtest"
	"github.com/pie360/backend/migrations"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

// ---------------------------------------------------------------- helpers

// inCtx runs fn in a transaction bound to the given tenant and actor tier, exactly like an API request, then rolls back.
func inCtx(t *testing.T, conn *pgx.Conn, tenant, tier string, fn func(tx pgx.Tx)) {
	t.Helper()
	tx, err := conn.Begin(bg)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := tx.Exec(bg, "SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', $2, true)", tenant, tier); err != nil {
		t.Fatalf("set context: %v", err)
	}
	fn(tx)
}

// expectCode runs one statement in its own request-scoped transaction and requires a Postgres error with the given SQLSTATE.
func expectCode(t *testing.T, conn *pgx.Conn, tenant, tier, want, sql string, args ...any) {
	t.Helper()
	inCtx(t, conn, tenant, tier, func(tx pgx.Tx) {
		_, err := tx.Exec(bg, sql, args...)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) {
			t.Fatalf("want SQLSTATE %s, got err=%v for: %s", want, err, sql)
		}
		if pe.Code != want {
			t.Fatalf("want SQLSTATE %s, got %s (%s) for: %s", want, pe.Code, pe.Message, sql)
		}
	})
}

func mustScan[T any](t *testing.T, row pgx.Row) T {
	t.Helper()
	var v T
	if err := row.Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return v
}

func admin(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(bg, sql, args...); err != nil {
		t.Fatalf("arrange: %v\n%s", err, sql)
	}
}

func newTenant(t *testing.T, conn *pgx.Conn, code string) string {
	t.Helper()
	return mustScan[string](t, conn.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1, $2) RETURNING id::text", code, strings.ToUpper(code)))
}

func newUser(t *testing.T, conn *pgx.Conn, tenant, username string) string {
	t.Helper()
	return mustScan[string](t, conn.QueryRow(bg,
		"INSERT INTO users (tenant_id, username, email, display_name, status) VALUES ($1, $2::text, $2::text || '@x.test', $2::text, 'active') RETURNING id::text", tenant, username))
}

func accessVersion(t *testing.T, conn *pgx.Conn, user string) int64 {
	t.Helper()
	return mustScan[int64](t, conn.QueryRow(bg, "SELECT access_version FROM users WHERE id = $1", user))
}

// ---------------------------------------------------------------- migrations

func TestMigrationsApplyCleanlyAndRoundTrip(t *testing.T) {
	d := dbtest.NewEmpty(t)
	conn := d.Admin(t)
	if err := dbtest.ApplyUp(bg, conn, migrations.FS); err != nil {
		t.Fatalf("up: %v", err)
	}
	before := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'"))
	if before < 14 {
		t.Fatalf("expected the baseline tables, found %d", before)
	}
	if err := dbtest.ApplyDown(bg, conn, migrations.FS); err != nil {
		t.Fatalf("down: %v", err)
	}
	if left := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'")); left != 0 {
		t.Fatalf("down migrations left %d tables behind", left)
	}
	if err := dbtest.ApplyUp(bg, conn, migrations.FS); err != nil {
		t.Fatalf("second up: %v", err)
	}
}

func TestRolesAreDataNotAnEnum(t *testing.T) {
	conn := dbtest.New(t).Admin(t)
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM pg_type WHERE typname = 'user_role'")); n != 0 {
		t.Fatal("the user_role enum must not exist; roles are rows")
	}
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'role'")); n != 0 {
		t.Fatal("users must not have a role column; use user_roles")
	}
}

func TestEveryTableHasRowLevelSecurity(t *testing.T) {
	conn := dbtest.New(t).Admin(t)
	rows, err := conn.Query(bg, `
		SELECT c.relname, c.relrowsecurity,
		       (SELECT count(*) FROM pg_policies p WHERE p.schemaname = 'public' AND p.tablename = c.relname)
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var rls bool
		var policies int
		if err := rows.Scan(&name, &rls, &policies); err != nil {
			t.Fatal(err)
		}
		n++
		if !rls || policies == 0 {
			t.Errorf("table %s must have row-level security enabled and at least one policy (rls=%v policies=%d)", name, rls, policies)
		}
	}
	if n == 0 {
		t.Fatal("no tables found")
	}
}

func TestEveryTenantScopedTableDeniesAnonymousSessions(t *testing.T) {
	d := dbtest.New(t)
	admin1 := d.Admin(t)
	tenant := newTenant(t, admin1, "iso")
	newUser(t, admin1, tenant, "someone")
	app := d.App(t)

	rows, err := admin1.Query(bg, `SELECT table_name FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'tenant_id' AND table_name NOT IN ('tenants')`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 8 {
		t.Fatalf("expected the tenant-scoped tables, got %v", tables)
	}
	for _, tbl := range tables {
		inCtx(t, app, "", "anonymous", func(tx pgx.Tx) {
			var c int
			if err := tx.QueryRow(bg, "SELECT count(*) FROM "+tbl).Scan(&c); err != nil {
				t.Fatalf("%s: %v", tbl, err)
			}
			if c != 0 {
				t.Errorf("anonymous session can read %d rows of %s", c, tbl)
			}
		})
	}
}

// ---------------------------------------------------------------- tenant isolation

func TestTenantIsolationUnderRLS(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a, b := newTenant(t, adm, "alpha"), newTenant(t, adm, "bravo")
	ua, ub := newUser(t, adm, a, "asha"), newUser(t, adm, b, "bala")
	app := d.App(t)

	inCtx(t, app, "", "anonymous", func(tx pgx.Tx) {
		if c := mustScan[int](t, tx.QueryRow(bg, "SELECT count(*) FROM users")); c != 0 {
			t.Fatalf("no tenant context must see no users, saw %d", c)
		}
	})
	inCtx(t, app, a, "tenant", func(tx pgx.Tx) {
		if got := mustScan[string](t, tx.QueryRow(bg, "SELECT string_agg(id::text, ',') FROM users")); got != ua {
			t.Fatalf("tenant A must see only its own user %s, saw %s", ua, got)
		}
		if tag, _ := tx.Exec(bg, "UPDATE users SET display_name = 'hijack' WHERE id = $1", ub); tag.RowsAffected() != 0 {
			t.Fatal("tenant A updated tenant B's user")
		}
		if tag, _ := tx.Exec(bg, "DELETE FROM users WHERE id = $1", ub); tag.RowsAffected() != 0 {
			t.Fatal("tenant A deleted tenant B's user")
		}
	})
	// A handler bug that tries to write into another tenant is stopped by WITH CHECK.
	expectCode(t, app, a, "tenant", "42501", "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, 'mallory', 'M')", b)
	// A malformed context must fail closed, never open.
	expectCode(t, app, "not-a-uuid", "tenant", "22P02", "SELECT count(*) FROM users")
}

func TestCrossTenantReferencesAreImpossibleEvenForTheOwner(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t) // superuser: row-level security does not apply, only constraints do
	a, b := newTenant(t, adm, "alpha"), newTenant(t, adm, "bravo")
	newUser(t, adm, a, "asha")
	ub := newUser(t, adm, b, "bala")
	provision(t, adm, a)
	roleA := mustScan[string](t, adm.QueryRow(bg, "SELECT id::text FROM roles WHERE tenant_id = $1 AND code = 'teacher'", a))

	_, err := adm.Exec(bg, "INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2, $3)", a, ub, roleA)
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "23503" {
		t.Fatalf("assigning tenant A's role to tenant B's user must violate the composite foreign key, got %v", err)
	}
}

func TestUsernamesAndEmailsAreUniquePerTenant(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a, b := newTenant(t, adm, "alpha"), newTenant(t, adm, "bravo")
	newUser(t, adm, a, "asha")
	newUser(t, adm, b, "asha") // same username in another institute is fine

	for _, q := range []struct{ name, sql string }{
		{"duplicate username", "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, 'asha', 'x')"},
		{"username differing only by case", "INSERT INTO users (tenant_id, username, display_name) VALUES ($1, 'ASHA', 'x')"},
		{"duplicate email", "INSERT INTO users (tenant_id, username, email, display_name) VALUES ($1, 'other', 'asha@x.test', 'x')"},
	} {
		_, err := adm.Exec(bg, q.sql, a)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23505" {
			t.Errorf("%s must violate a unique index, got %v", q.name, err)
		}
	}
}

func TestTenantRowVisibilityAndWriteRules(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")
	newTenant(t, adm, "bravo")
	app := d.App(t)

	inCtx(t, app, "", "anonymous", func(tx pgx.Tx) {
		if c := mustScan[int](t, tx.QueryRow(bg, "SELECT count(*) FROM tenants")); c != 0 {
			t.Fatalf("anonymous sessions must see no institutes, saw %d", c)
		}
	})
	inCtx(t, app, a, "tenant", func(tx pgx.Tx) {
		if got := mustScan[string](t, tx.QueryRow(bg, "SELECT string_agg(id::text, ',') FROM tenants")); got != a {
			t.Fatalf("a tenant session must see only its own institute, saw %s", got)
		}
	})
	expectCode(t, app, a, "tenant", "42501", "INSERT INTO tenants (code, name) VALUES ('zulu', 'Z')")
	expectCode(t, app, a, "tenant", "42501", "INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'fee')", a)
	inCtx(t, app, "", "platform", func(tx pgx.Tx) {
		if _, err := tx.Exec(bg, "INSERT INTO tenants (code, name) VALUES ('zulu', 'Z')"); err != nil {
			t.Fatalf("platform tier must create institutes: %v", err)
		}
		if c := mustScan[int](t, tx.QueryRow(bg, "SELECT count(*) FROM tenants")); c != 3 {
			t.Fatalf("platform tier must see every institute, saw %d", c)
		}
	})
}

func TestResolveTenantWorksBeforeLoginAndHidesDeletedTenants(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")
	admin(t, adm, "UPDATE tenants SET custom_domain = 'school.example.org', brand = '{\"primary\":\"#2F5BFF\"}' WHERE id = $1", a)
	gone := newTenant(t, adm, "gone")
	admin(t, adm, "UPDATE tenants SET deleted_at = now() WHERE id = $1", gone)
	app := d.App(t)

	inCtx(t, app, "", "anonymous", func(tx pgx.Tx) {
		for _, key := range []string{"alpha", "ALPHA", "school.example.org"} {
			if got := mustScan[string](t, tx.QueryRow(bg, "SELECT id::text FROM resolve_tenant($1)", key)); got != a {
				t.Errorf("resolve_tenant(%q) = %s, want %s", key, got, a)
			}
		}
		var id string
		if err := tx.QueryRow(bg, "SELECT id::text FROM resolve_tenant('gone')").Scan(&id); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("a deleted institute must not resolve, got id=%q err=%v", id, err)
		}
		if err := tx.QueryRow(bg, "SELECT id::text FROM resolve_tenant('nope')").Scan(&id); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("an unknown key must not resolve, got id=%q err=%v", id, err)
		}
	})
}

func TestPlatformUserLookupWorksBeforeLoginButTableStaysPrivate(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	admin(t, adm, "INSERT INTO platform_users (email, display_name, password_hash) VALUES ('Root@Example.test', 'Root', 'hash')")
	app := d.App(t)
	inCtx(t, app, "", "anonymous", func(tx pgx.Tx) {
		if c := mustScan[int](t, tx.QueryRow(bg, "SELECT count(*) FROM platform_users")); c != 0 {
			t.Fatalf("platform_users must be invisible to anonymous sessions, saw %d", c)
		}
		if got := mustScan[string](t, tx.QueryRow(bg, "SELECT display_name FROM auth_lookup_platform_user('root@example.test')")); got != "Root" {
			t.Fatalf("lookup (case-insensitive email) returned %q", got)
		}
	})
}

// ---------------------------------------------------------------- roles, grants and validation

func provision(t *testing.T, conn *pgx.Conn, tenant string) int {
	t.Helper()
	var n int
	commitCtx(t, conn, "", "platform", func(tx pgx.Tx) {
		n = mustScan[int](t, tx.QueryRow(bg, "SELECT provision_tenant_roles($1)", tenant))
	})
	return n
}

// commitCtx is inCtx but commits, for arranging data through the same code path the API uses.
func commitCtx(t *testing.T, conn *pgx.Conn, tenant, tier string, fn func(tx pgx.Tx)) {
	t.Helper()
	tx, err := conn.Begin(bg)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(bg, "SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', $2, true)", tenant, tier); err != nil {
		_ = tx.Rollback(bg)
		t.Fatalf("set context: %v", err)
	}
	fn(tx)
	if err := tx.Commit(bg); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestProvisioningCopiesTemplatesOnceAndOnlyForThePlatform(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")

	expectCode(t, d.App(t), a, "tenant", "42501", "SELECT provision_tenant_roles($1)", a)

	if n := provision(t, adm, a); n != 19 {
		t.Fatalf("first provisioning must create the 19 template roles, created %d", n)
	}
	if n := provision(t, adm, a); n != 0 {
		t.Fatalf("provisioning must be idempotent, created %d on the second run", n)
	}
	if !mustScan[bool](t, adm.QueryRow(bg, "SELECT is_system FROM roles WHERE tenant_id = $1 AND code = 'admin'", a)) {
		t.Fatal("the admin role must be a system role")
	}
	if mustScan[bool](t, adm.QueryRow(bg, "SELECT is_system FROM roles WHERE tenant_id = $1 AND code = 'teacher'", a)) {
		t.Fatal("only admin is a system role")
	}
	grants := map[string]string{ // role.permission -> scope
		"admin.*.*.*":       "tenant",
		"teacher.att.*.*":   "class_section",
		"parent.slf.*.*":    "own_children",
		"student.slf.*.*":   "self",
		"driver.trn.*.view": "assigned_route",
	}
	for k, want := range grants {
		role, perm, _ := strings.Cut(k, ".")
		got := mustScan[string](t, adm.QueryRow(bg, `SELECT rp.scope FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
			WHERE r.tenant_id = $1 AND r.code = $2 AND rp.permission_code = $3`, a, role, perm))
		if got != want {
			t.Errorf("%s: scope = %s, want %s", k, got, want)
		}
	}
}

func TestSystemRolesAreReadOnlyForInstitutesButEditableByThePlatform(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")
	provision(t, adm, a)
	app := d.App(t)

	expectCode(t, app, a, "tenant", "42501", "UPDATE roles SET name = 'Renamed' WHERE code = 'admin'")
	expectCode(t, app, a, "tenant", "42501", "DELETE FROM roles WHERE code = 'admin'")
	expectCode(t, app, a, "tenant", "42501", "INSERT INTO role_permissions (tenant_id, role_id, permission_code) SELECT tenant_id, id, 'fee.*.view' FROM roles WHERE code = 'admin'")
	expectCode(t, app, a, "tenant", "42501", "DELETE FROM role_permissions WHERE role_id = (SELECT id FROM roles WHERE code = 'admin')")

	inCtx(t, app, a, "tenant", func(tx pgx.Tx) {
		tag, err := tx.Exec(bg, "UPDATE roles SET name = 'Head Teacher' WHERE code = 'teacher'")
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("a non-system role must be editable by the institute: err=%v rows=%d", err, tag.RowsAffected())
		}
	})
	inCtx(t, app, a, "platform", func(tx pgx.Tx) {
		if tag, err := tx.Exec(bg, "UPDATE roles SET description = 'maintained by platform' WHERE code = 'admin'"); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("the platform tier must be able to maintain system roles: err=%v rows=%d", err, tag.RowsAffected())
		}
	})
}

func TestPermissionCodesAreValidated(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")
	provision(t, adm, a)
	newUser(t, adm, a, "asha")
	app := d.App(t)
	insert := "INSERT INTO role_permissions (tenant_id, role_id, permission_code) SELECT tenant_id, id, $1 FROM roles WHERE code = 'counselor'"

	for _, ok := range []string{"iam.users.view", "fee.payments.*", "*.*.view", "exm.*.*"} {
		inCtx(t, app, a, "tenant", func(tx pgx.Tx) {
			if _, err := tx.Exec(bg, insert, ok); err != nil {
				t.Errorf("%q must be accepted: %v", ok, err)
			}
		})
	}
	expectCode(t, app, a, "tenant", "23503", insert, "bogus.thing.do") // exact code that is not in the catalog
	expectCode(t, app, a, "tenant", "23514", insert, "Fee.Payments")   // malformed
	expectCode(t, app, a, "tenant", "23514", insert, "a.b.c.d")        // too many segments
	expectCode(t, app, a, "tenant", "23503",
		"INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, reason) SELECT tenant_id, id, 'nope.nope.nope', 'allow', 'test' FROM users LIMIT 1")
}

// ---------------------------------------------------------------- access version

func TestAccessVersionBumpsWhenAnythingAffectingAccessChanges(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a := newTenant(t, adm, "alpha")
	provision(t, adm, a)
	u1, u2 := newUser(t, adm, a, "asha"), newUser(t, adm, a, "bala")
	if accessVersion(t, adm, u1) != 1 {
		t.Fatal("a new user starts at access_version 1")
	}

	step := func(name string, wantU1, wantU2 int64, tenant, tier string, sql string, args ...any) {
		t.Helper()
		commitCtx(t, adm, tenant, tier, func(tx pgx.Tx) {
			if _, err := tx.Exec(bg, sql, args...); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if g1, g2 := accessVersion(t, adm, u1), accessVersion(t, adm, u2); g1 != wantU1 || g2 != wantU2 {
			t.Fatalf("%s: versions = (%d, %d), want (%d, %d)", name, g1, g2, wantU1, wantU2)
		}
	}

	step("assign a role", 2, 1, a, "tenant",
		"INSERT INTO user_roles (tenant_id, user_id, role_id) SELECT $1, $2, id FROM roles WHERE tenant_id = $1 AND code = 'teacher'", a, u1)
	step("change the role's grants (affects its holders only)", 3, 1, a, "tenant",
		"INSERT INTO role_permissions (tenant_id, role_id, permission_code) SELECT tenant_id, id, 'fee.*.view' FROM roles WHERE tenant_id = $1 AND code = 'teacher'", a)
	step("assign a scope", 4, 1, a, "tenant",
		"INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2, 'class_section', '12-A')", a, u1)
	step("add a permission override", 5, 1, a, "tenant",
		"INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, reason, expires_at) VALUES ($1, $2, 'iam.users.view', 'allow', 'acting principal', now() + interval '7 days')", a, u1)
	step("the platform enables a module (every user of the institute)", 6, 2, "", "platform",
		"INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'fee')", a)
}

// ---------------------------------------------------------------- audit, sessions, reference data

func TestAuditLogIsAppendOnlyAndTenantScoped(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a, b := newTenant(t, adm, "alpha"), newTenant(t, adm, "bravo")
	app := d.App(t)
	ins := "INSERT INTO audit_log (tenant_id, actor_type, source, action, resource_type) VALUES ($1, 'user', 'web', 'fee.payments.create', 'payment')"

	commitCtx(t, app, a, "tenant", func(tx pgx.Tx) {
		if _, err := tx.Exec(bg, ins, a); err != nil {
			t.Fatalf("tenant must be able to append its own audit entries: %v", err)
		}
	})
	expectCode(t, app, a, "tenant", "42501", ins, b) // cannot write into another institute's trail
	expectCode(t, app, a, "tenant", "42501", "UPDATE audit_log SET action = 'x'")
	expectCode(t, app, a, "tenant", "42501", "DELETE FROM audit_log")
	if _, err := adm.Exec(bg, "UPDATE audit_log SET action = 'x'"); err == nil {
		t.Fatal("even the owner must not be able to rewrite the audit trail")
	}
	inCtx(t, app, b, "tenant", func(tx pgx.Tx) {
		if c := mustScan[int](t, tx.QueryRow(bg, "SELECT count(*) FROM audit_log")); c != 0 {
			t.Fatalf("tenant B must not see tenant A's audit entries, saw %d", c)
		}
	})
}

func TestSessionRowsAreEitherTenantOrPlatform(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	a, b := newTenant(t, adm, "alpha"), newTenant(t, adm, "bravo")
	ua := newUser(t, adm, a, "asha")
	pu := mustScan[string](t, adm.QueryRow(bg, "INSERT INTO platform_users (email, display_name) VALUES ('root@x.test', 'Root') RETURNING id::text"))
	base := "INSERT INTO sessions (tenant_id, user_id, platform_user_id, family_id, token_hash, client, expires_at) VALUES ($1, $2, $3, gen_random_uuid(), $4, 'web', now() + interval '1 day')"

	if _, err := adm.Exec(bg, base, a, ua, nil, "h1"); err != nil {
		t.Fatalf("a tenant session must be accepted: %v", err)
	}
	if _, err := adm.Exec(bg, base, nil, nil, pu, "h2"); err != nil {
		t.Fatalf("a platform session must be accepted: %v", err)
	}
	for name, args := range map[string][]any{
		"both user and platform user":        {a, ua, pu, "h3"},
		"neither":                            {nil, nil, nil, "h4"},
		"tenant user from another institute": {b, ua, nil, "h5"},
	} {
		_, err := adm.Exec(bg, base, args...)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || (pe.Code != "23514" && pe.Code != "23503") {
			t.Errorf("%s must be rejected by a constraint, got %v", name, err)
		}
	}
}

func TestReferenceDataIsSeeded(t *testing.T) {
	conn := dbtest.New(t).Admin(t)
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM role_templates")); n != 19 {
		t.Errorf("role templates = %d, want 19", n)
	}
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM plans")); n != 3 {
		t.Errorf("plans = %d, want 3", n)
	}
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM permissions")); n < 100 {
		t.Errorf("permission catalog has %d entries, want at least 100", n)
	}
	if !mustScan[bool](t, conn.QueryRow(bg, "SELECT sensitive FROM permissions WHERE code = 'onb.health.import'")) {
		t.Error("the health onboarding permission must be flagged sensitive")
	}
	if n := mustScan[int](t, conn.QueryRow(bg, "SELECT count(*) FROM role_templates t WHERE NOT EXISTS (SELECT 1 FROM role_template_permissions p WHERE p.template_code = t.code)")); n != 0 {
		t.Errorf("%d role templates have no permissions", n)
	}
}
