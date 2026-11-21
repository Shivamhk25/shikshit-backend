package authz

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrUnknownUser is returned when the user does not exist in the institute or is not active.
var ErrUnknownUser = errors.New("authz: unknown or inactive user")

// Access is a user's resolved access inside one institute.
type Access struct {
	UserID, UserName string
	Tenant           tenancy.Tenant
	Modules          []string // enabled, sorted
	Effective        Effective
	Home             string // workspace the user lands in: "admin", or the code of their most senior role
	Screens          []Screen
	Version          int64 // users.access_version
}

// Service resolves access from the database.
type Service struct{ DB tenancy.Beginner }

// Resolve loads the user's roles, overrides, assignments and the institute's modules in one transaction bound to
// the institute (row-level security applies) and computes the effective access.
func (s Service) Resolve(ctx context.Context, userID string) (Access, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Access{}, tenancy.ErrNoTenant
	}
	a := Access{UserID: userID, Tenant: t}
	in := Inputs{EnabledModules: map[string]bool{}}
	bestRank, home := 1<<30, "member"

	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT display_name, status, access_version FROM users WHERE id = $1 AND deleted_at IS NULL`, userID).
			Scan(&a.UserName, &status, &a.Version)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "active") {
			return ErrUnknownUser
		}
		if err != nil {
			return err
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

		if err := collect(ctx, tx, `
			SELECT rp.permission_code, rp.scope, r.code, r.is_system, COALESCE(rt.sort_order, 1000)
			FROM user_roles ur
			JOIN roles r ON r.tenant_id = ur.tenant_id AND r.id = ur.role_id AND r.deleted_at IS NULL
			JOIN role_permissions rp ON rp.tenant_id = r.tenant_id AND rp.role_id = r.id
			LEFT JOIN role_templates rt ON rt.code = r.template_code
			WHERE ur.user_id = $1 AND ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now())`, func(r pgx.Rows) error {
			var g Grant
			var roleCode string
			var system bool
			var rank int
			if err := r.Scan(&g.Pattern, &g.Scope, &roleCode, &system, &rank); err != nil {
				return err
			}
			in.Grants = append(in.Grants, g)
			if system {
				rank = -1 // the Admin role always defines the workspace
			}
			if rank < bestRank {
				bestRank, home = rank, roleCode
			}
			return nil
		}, userID); err != nil {
			return err
		}
		if err := collect(ctx, tx, `
			SELECT permission_code, effect, scope FROM user_permission_overrides
			WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now())`, func(r pgx.Rows) error {
			var o Override
			if err := r.Scan(&o.Pattern, &o.Effect, &o.Scope); err != nil {
				return err
			}
			in.Overrides = append(in.Overrides, o)
			return nil
		}, userID); err != nil {
			return err
		}
		return collect(ctx, tx, `
			SELECT scope_type, scope_ref FROM scope_assignments
			WHERE user_id = $1 AND starts_at <= now() AND (ends_at IS NULL OR ends_at > now())`, func(r pgx.Rows) error {
			var as Assignment
			if err := r.Scan(&as.Type, &as.Ref); err != nil {
				return err
			}
			in.Assignments = append(in.Assignments, as)
			return nil
		}, userID)
	})
	if err != nil {
		return Access{}, err
	}

	a.Effective = Compute(in)
	for m := range in.EnabledModules {
		a.Modules = append(a.Modules, m)
	}
	sort.Strings(a.Modules)
	a.Screens = Visible(a.Effective, in.EnabledModules)
	a.Home = home
	return a, nil
}

func collect(ctx context.Context, tx pgx.Tx, sql string, each func(pgx.Rows) error, args ...any) error {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
