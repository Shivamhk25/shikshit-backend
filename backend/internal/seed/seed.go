// Package seed creates development data. It is safe to run repeatedly.
package seed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// DemoCode is the institute code (and subdomain) of the demo institute.
const DemoCode = "demo"

// Result describes what Demo did.
type Result struct {
	TenantID     string
	ModulesAdded int
	RolesCreated int
}

// Demo creates the demo institute on the premium plan, enables every module of that plan and copies the default
// role templates into it. Existing data is left untouched, so re-running only fills gaps.
// It runs as the platform tier, exactly like the Super Admin console will, and needs a connection that may
// write to the institute tables (the migration owner in development).
func Demo(ctx context.Context, conn *pgx.Conn) (Result, error) {
	var res Result
	tx, err := conn.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.actor_tier', 'platform', true)"); err != nil {
		return res, fmt.Errorf("set platform context: %w", err)
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO tenants (code, name, plan_id, status, brand)
		VALUES ($1, 'Demo Public School', (SELECT id FROM plans WHERE code = 'premium'), 'onboarding', '{"primary":"#2F5BFF"}')
		ON CONFLICT (code) DO UPDATE SET code = EXCLUDED.code
		RETURNING id::text`, DemoCode).Scan(&res.TenantID)
	if err != nil {
		return res, fmt.Errorf("upsert tenant: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO tenant_modules (tenant_id, module_code)
		SELECT t.id, m FROM tenants t JOIN plans p ON p.id = t.plan_id, unnest(p.modules) AS m
		WHERE t.id = $1
		ON CONFLICT (tenant_id, module_code) DO NOTHING`, res.TenantID)
	if err != nil {
		return res, fmt.Errorf("enable modules: %w", err)
	}
	res.ModulesAdded = int(tag.RowsAffected())

	if err := tx.QueryRow(ctx, "SELECT provision_tenant_roles($1)", res.TenantID).Scan(&res.RolesCreated); err != nil {
		return res, fmt.Errorf("provision roles: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}
