package authz

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ResolveSupport builds the access of a read-only support visit (SAD-05, decision 0018): what the institute's Admin
// role may see, and nothing it may change. Only ".view" permissions are kept, so screens show no edit buttons, and
// the API refuses every other operation for a visit anyway.
func (s Service) ResolveSupport(ctx context.Context, visitorID, visitorName string) (Access, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Access{}, tenancy.ErrNoTenant
	}
	a := Access{UserID: visitorID, UserName: visitorName, Tenant: t, Home: "admin"}
	in := Inputs{EnabledModules: map[string]bool{}}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var brand map[string]any
		if err := tx.QueryRow(ctx, `SELECT brand FROM tenants WHERE id = app_current_tenant()`).Scan(&brand); err == nil {
			a.Tenant.Brand = brand
		}
		if err := collect(ctx, tx, `SELECT module_code FROM tenant_modules WHERE enabled`, func(r pgx.Rows) error {
			var m string
			if err := r.Scan(&m); err != nil {
				return err
			}
			in.EnabledModules[m] = true
			return nil
		}); err != nil {
			return err
		}
		if err := collect(ctx, tx, `SELECT code FROM permissions`, func(r pgx.Rows) error {
			var c string
			if err := r.Scan(&c); err != nil {
				return err
			}
			in.Catalog = append(in.Catalog, c)
			return nil
		}); err != nil {
			return err
		}
		return collect(ctx, tx, `SELECT rp.permission_code, rp.scope FROM roles r JOIN role_permissions rp ON rp.tenant_id = r.tenant_id AND rp.role_id = r.id
			WHERE r.code = 'admin' AND r.is_system AND r.deleted_at IS NULL`, func(r pgx.Rows) error {
			var g Grant
			if err := r.Scan(&g.Pattern, &g.Scope); err != nil {
				return err
			}
			in.Grants = append(in.Grants, g)
			return nil
		})
	})
	if err != nil {
		return Access{}, err
	}
	e := Compute(in)
	view := e.Permissions[:0:0]
	for _, p := range e.Permissions {
		if strings.HasSuffix(p, ".view") {
			view = append(view, p)
		}
	}
	e.Permissions = view
	a.Effective = e
	for m := range in.EnabledModules {
		a.Modules = append(a.Modules, m)
	}
	sort.Strings(a.Modules)
	a.Screens = Visible(a.Effective, in.EnabledModules)
	return a, nil
}

// ResolveSupport passes through: a visit's access is small and read fresh every time.
func (c *Cache) ResolveSupport(ctx context.Context, visitorID, visitorName string) (Access, error) {
	return c.Next.ResolveSupport(ctx, visitorID, visitorName)
}
