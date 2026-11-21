package migrations_test

import (
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
	"github.com/Shivamhk25/shikshit-backend/backend/migrations"
)

// Role templates grant wildcard patterns. A pattern that matches nothing in the catalog is a dead grant: the role
// would silently get no access to that module. This ties the templates to the catalog so a typo cannot slip in.
func TestEveryRoleTemplateGrantMatchesTheCatalog(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)

	rows, err := conn.Query(bg, "SELECT code FROM permissions")
	if err != nil {
		t.Fatal(err)
	}
	var catalog []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, c)
	}
	rows.Close()
	if len(catalog) < 900 {
		t.Fatalf("catalog has only %d permissions", len(catalog))
	}

	grants, err := conn.Query(bg, "SELECT template_code, permission_code FROM role_template_permissions")
	if err != nil {
		t.Fatal(err)
	}
	defer grants.Close()
	for grants.Next() {
		var tmpl, pattern string
		if err := grants.Scan(&tmpl, &pattern); err != nil {
			t.Fatal(err)
		}
		matched := false
		for _, c := range catalog {
			if authz.MatchPermission(pattern, c) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("template %q grants %q, which matches no permission in the catalog", tmpl, pattern)
		}
	}
}

func TestCatalogCodesAreWellFormedAndModulesAreInThePlans(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)

	var bad int
	if err := conn.QueryRow(bg, `SELECT count(*) FROM permissions WHERE code <> module || '.' || resource || '.' || action`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("malformed codes: %d (%v)", bad, err)
	}
	// every catalog module must be sold in at least one plan, otherwise nobody could ever use it
	rows, err := conn.Query(bg, `SELECT DISTINCT p.module FROM permissions p
		WHERE NOT EXISTS (SELECT 1 FROM plans pl WHERE p.module = ANY (pl.modules))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var orphans []string
	for rows.Next() {
		var m string
		_ = rows.Scan(&m)
		orphans = append(orphans, m)
	}
	if len(orphans) > 0 {
		t.Errorf("catalog modules missing from every plan: %s", strings.Join(orphans, ", "))
	}
}

func TestCatalogMigrationRollsBackCleanly(t *testing.T) {
	d := dbtest.New(t)
	conn := d.Admin(t)
	defer conn.Close(bg)

	var before int
	_ = conn.QueryRow(bg, "SELECT count(*) FROM permissions").Scan(&before)
	down, err := migrations.FS.ReadFile("000007_permission_catalog.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(bg, string(down)); err != nil {
		t.Fatal(err)
	}
	var after int
	_ = conn.QueryRow(bg, "SELECT count(*) FROM permissions").Scan(&after)
	if after >= before || after < 60 {
		t.Fatalf("rollback left %d of %d permissions; it must remove the generated ones and keep the hand-seeded ones", after, before)
	}
}
