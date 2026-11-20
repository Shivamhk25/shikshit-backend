// Package modtest is the shared fixture for module service tests: two institutes, one actor, a runtime-role pool
// (row-level security applies) and an owner pool for arranging and inspecting data.
package modtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

var bg = context.Background()

// Fixture is one freshly migrated database with institutes "alpha" and "bravo".
type Fixture struct {
	App          *pgxpool.Pool // runtime role
	Admin        *pgxpool.Pool // owner: bypasses row-level security
	Alpha, Bravo tenancy.Tenant
	Actor        audit.Actor // a real user of alpha, acting from the web
}

// New builds the fixture; everything is cleaned up when t ends.
func New(t *testing.T) Fixture {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	t.Cleanup(func() { _ = adm.Close(bg) })
	mk := func(code string) tenancy.Tenant {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return tenancy.Tenant{ID: id, Code: code, Status: "live"}
	}
	f := Fixture{Alpha: mk("alpha"), Bravo: mk("bravo")}
	var uid string
	if err := adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, 'asha', 'Asha', 'active') RETURNING id::text`, f.Alpha.ID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	f.Actor = audit.Actor{UserID: uid, Source: "web", RequestID: "req-1", IP: "203.0.113.5", UserAgent: "test"}

	pool := func(dsn string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		p, err := pgxpool.NewWithConfig(bg, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	f.App, f.Admin = pool(d.AppDSN), pool(d.AdminDSN)
	return f
}

// In returns a context bound to the institute, as the tenancy middleware would.
func In(t tenancy.Tenant) context.Context { return tenancy.WithTenant(bg, t) }

// AuditCount counts audit rows for an action.
func (f Fixture) AuditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
