// Package roles implements IAM-02 Roles and the permission matrix: list, view, create, clone, edit and delete
// institute roles, and give users their roles.
//
// Rules enforced here (each has a test):
//   - nobody can grant what they do not hold, nor more widely than they hold it (authz.Effective.CanGrant): this
//     applies to every grant added to a role and to every grant of a role given to a user;
//   - system roles (Admin and the templates marked system) are read-only: clone them to customise;
//   - a role still held by someone cannot be deleted;
//   - the last active Admin cannot lose the Admin role (shared with IAM-01, users.CheckNotLastAdmin);
//   - every change is audited with the grants before and after, and holders' access changes on their next request
//     (database triggers bump users.access_version).
package roles

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var (
	// ErrNotFound is returned when the role or user does not exist in this institute.
	ErrNotFound = errors.New("roles: not found")
	// ErrSystemRole is returned when someone tries to change or delete a system role.
	ErrSystemRole = errors.New("This is a built-in role and cannot be changed. Clone it to make your own version.")
	// ErrInUse is returned when a role still held by someone is deleted.
	ErrInUse = errors.New("Some users still have this role. Give them another role first.")
)

// CannotGrantError lists the permissions the caller tried to grant without holding them.
type CannotGrantError struct{ Permissions []string }

func (e CannotGrantError) Error() string {
	return fmt.Sprintf("You can only give permissions you have yourself. Not allowed: %s.", strings.Join(e.Permissions, ", "))
}

// Scopes a role grant may carry (the role_permissions check constraint).
var Scopes = []string{"tenant", "campus", "department", "class_section", "assigned_route", "assigned_hostel", "own_children", "self", "owned"}

// Grant is one permission a role gives, and the records it reaches.
type Grant struct {
	Permission string `json:"permission"`
	Scope      string `json:"scope"`
}

// Role is a role as lists show it.
type Role struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Description     *string   `json:"description"`
	IsSystem        bool      `json:"isSystem"`
	HolderCount     int       `json:"holderCount"`
	PermissionCount int       `json:"permissionCount"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// Detail is one role with its grants expanded to concrete permission codes of the institute's enabled modules.
type Detail struct {
	Role
	Grants []Grant `json:"grants"`
}

// Input is what create and update accept. Update replaces the grants.
type Input struct {
	Name        string
	Description *string
	Grants      []Grant
}

// Permission is one entry of the catalog.
type Permission struct {
	Code      string `json:"code"`
	Module    string `json:"module"`
	Resource  string `json:"resource"`
	Action    string `json:"action"`
	Sensitive bool   `json:"sensitive"`
}

// Service reads and writes roles.
type Service struct{ DB tenancy.Beginner }

// Catalog returns every permission of the modules this institute has enabled, sorted by code.
func (s Service) Catalog(ctx context.Context) ([]Permission, error) {
	var out []Permission
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = catalog(ctx, tx)
		return err
	})
	return out, err
}

func catalog(ctx context.Context, tx pgx.Tx) ([]Permission, error) {
	rows, err := tx.Query(ctx, `
		SELECT p.code, p.module, p.resource, p.action, p.sensitive FROM permissions p
		WHERE EXISTS (SELECT 1 FROM tenant_modules m WHERE m.module_code = p.module AND m.enabled)
		ORDER BY p.code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Permission{}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Code, &p.Module, &p.Resource, &p.Action, &p.Sensitive); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const holders = `(SELECT count(DISTINCT ur.user_id) FROM user_roles ur JOIN users u ON u.tenant_id = ur.tenant_id AND u.id = ur.user_id AND u.deleted_at IS NULL
	WHERE ur.role_id = r.id AND ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now()))`

const roleColumns = `r.id::text, r.code, r.name, r.description, r.is_system, ` + holders + `, r.updated_at`

func scanRole(row interface{ Scan(...any) error }, r *Role) error {
	return row.Scan(&r.ID, &r.Code, &r.Name, &r.Description, &r.IsSystem, &r.HolderCount, &r.UpdatedAt)
}

// List returns every role of the institute: system roles first, then by name.
func (s Service) List(ctx context.Context) ([]Role, error) {
	var out []Role
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		cat, err := catalog(ctx, tx)
		if err != nil {
			return err
		}
		patterns, err := allGrants(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+roleColumns+` FROM roles r WHERE r.deleted_at IS NULL ORDER BY r.is_system DESC, lower(r.name)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Role{}
		for rows.Next() {
			var r Role
			if err := scanRole(rows, &r); err != nil {
				return err
			}
			r.PermissionCount = len(expand(patterns[r.ID], cat))
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// Get returns one role with its grants.
func (s Service) Get(ctx context.Context, id string) (Detail, error) {
	var d Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = get(ctx, tx, id, false)
		return err
	})
	return d, err
}

func get(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (Detail, error) {
	var d Detail
	if _, err := uuid.Parse(id); err != nil {
		return d, ErrNotFound
	}
	sql := `SELECT ` + roleColumns + ` FROM roles r WHERE r.id = $1::uuid AND r.deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE OF r`
	}
	if err := scanRole(tx.QueryRow(ctx, sql, id), &d.Role); errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	} else if err != nil {
		return d, err
	}
	cat, err := catalog(ctx, tx)
	if err != nil {
		return d, err
	}
	patterns, err := roleGrants(ctx, tx, id)
	if err != nil {
		return d, err
	}
	d.Grants = expand(patterns, cat)
	d.PermissionCount = len(d.Grants)
	return d, nil
}

// expand turns stored grants (which may be wildcard patterns, like the Admin role's *.*.*) into concrete permission
// codes of the enabled catalog. One scope per code: the widest of the matching grants (tenant first, then in Scopes
// order), so a code granted twice is shown once.
func expand(patterns []Grant, cat []Permission) []Grant {
	rank := map[string]int{}
	for i, s := range Scopes {
		rank[s] = i
	}
	best := map[string]string{}
	for _, p := range cat {
		for _, g := range patterns {
			if authz.MatchPermission(g.Permission, p.Code) {
				if cur, ok := best[p.Code]; !ok || rank[g.Scope] < rank[cur] {
					best[p.Code] = g.Scope
				}
			}
		}
	}
	out := make([]Grant, 0, len(best))
	for code, scope := range best {
		out = append(out, Grant{Permission: code, Scope: scope})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Permission < out[j].Permission })
	return out
}

func roleGrants(ctx context.Context, tx pgx.Tx, roleID string) ([]Grant, error) {
	rows, err := tx.Query(ctx, `SELECT permission_code, scope FROM role_permissions WHERE role_id = $1::uuid ORDER BY permission_code`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Permission, &g.Scope); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func allGrants(ctx context.Context, tx pgx.Tx) (map[string][]Grant, error) {
	rows, err := tx.Query(ctx, `SELECT role_id::text, permission_code, scope FROM role_permissions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Grant{}
	for rows.Next() {
		var id string
		var g Grant
		if err := rows.Scan(&id, &g.Permission, &g.Scope); err != nil {
			return nil, err
		}
		out[id] = append(out[id], g)
	}
	return out, rows.Err()
}

// normalise trims and validates input; grants must be concrete codes of the enabled catalog with a known scope.
func normalise(in Input, cat []Permission) (Input, error) {
	var errs crud.FieldErrors
	in.Name = strings.TrimSpace(in.Name)
	in.Description = crud.TrimPtr(in.Description)
	errs.Required("name", "a name", in.Name, 100)
	if in.Description != nil && len(*in.Description) > 500 {
		errs.Add("description", "too_long", "A description has at most 500 characters.")
	}
	known := map[string]bool{}
	for _, p := range cat {
		known[p.Code] = true
	}
	seen := map[string]bool{}
	clean := []Grant{}
	for _, g := range in.Grants {
		switch {
		case !known[g.Permission]:
			errs.Add("grants", "unknown_permission", "A permission in the list does not exist or its module is not enabled: "+g.Permission)
		case !contains(Scopes, g.Scope):
			errs.Add("grants", "invalid_scope", "A permission has a scope that does not exist: "+g.Scope)
		case seen[g.Permission]:
			errs.Add("grants", "duplicate", "A permission is listed twice: "+g.Permission)
		default:
			seen[g.Permission] = true
			clean = append(clean, g)
		}
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].Permission < clean[j].Permission })
	in.Grants = clean
	return in, errs.OrNil()
}

// checkGrantable returns CannotGrantError listing every grant the caller does not hold widely enough.
func checkGrantable(caller authz.Effective, grants []Grant) error {
	var bad []string
	for _, g := range grants {
		if !caller.CanGrant(g.Permission, g.Scope) {
			bad = append(bad, g.Permission)
		}
	}
	if len(bad) > 0 {
		return CannotGrantError{Permissions: bad}
	}
	return nil
}

// added returns the grants in next that are new or have a different scope than in prev.
func added(prev, next []Grant) []Grant {
	had := map[string]string{}
	for _, g := range prev {
		had[g.Permission] = g.Scope
	}
	var out []Grant
	for _, g := range next {
		if s, ok := had[g.Permission]; !ok || s != g.Scope {
			out = append(out, g)
		}
	}
	return out
}

var nonCode = regexp.MustCompile(`[^a-z0-9]+`)

// codeFor derives a role code from the name, unique in the institute: "Class teacher" -> class_teacher, then
// class_teacher_2 and so on.
func codeFor(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	base := strings.Trim(nonCode.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "role_" + base
	}
	if len(base) > 40 {
		base = strings.TrimRight(base[:40], "_")
	}
	for n := 1; n < 1000; n++ {
		code := base
		if n > 1 {
			code = fmt.Sprintf("%s_%d", base, n)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE code = $1 AND deleted_at IS NULL)`, code).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return code, nil
		}
	}
	return "", errors.New("roles: no free code")
}

func checkNameFree(ctx context.Context, tx pgx.Tx, name, exceptID string) error {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE lower(name) = lower($1) AND deleted_at IS NULL AND ($2 = '' OR id::text <> $2))`, name, exceptID).Scan(&taken)
	if err != nil {
		return err
	}
	if taken {
		return crud.FieldErrors{{Field: "name", Code: "duplicate", Message: "Another role already has this name."}}
	}
	return nil
}

func writeGrants(ctx context.Context, tx pgx.Tx, tenantID, roleID string, grants []Grant) error {
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1::uuid`, roleID); err != nil {
		return systemError(err)
	}
	if len(grants) == 0 {
		return nil
	}
	codes, scopes := make([]string, len(grants)), make([]string, len(grants))
	for i, g := range grants {
		codes[i], scopes[i] = g.Permission, g.Scope
	}
	_, err := tx.Exec(ctx, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT $1, $2::uuid, c, s FROM unnest($3::text[], $4::text[]) AS t(c, s)`, tenantID, roleID, codes, scopes)
	return systemError(err)
}

// systemError maps the database guard on system roles to ErrSystemRole.
func systemError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42501" {
		return ErrSystemRole
	}
	return err
}

// Create adds a role from the matrix.
func (s Service) Create(ctx context.Context, a audit.Actor, caller authz.Effective, in Input) (Detail, error) {
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		cat, err := catalog(ctx, tx)
		if err != nil {
			return err
		}
		if in, err = normalise(in, cat); err != nil {
			return err
		}
		if err := checkGrantable(caller, in.Grants); err != nil {
			return err
		}
		if err := checkNameFree(ctx, tx, in.Name, ""); err != nil {
			return err
		}
		code, err := codeFor(ctx, tx, in.Name)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO roles (tenant_id, code, name, description) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			t.ID, code, in.Name, in.Description).Scan(&id); err != nil {
			return err
		}
		if err := writeGrants(ctx, tx, t.ID, id, in.Grants); err != nil {
			return err
		}
		if out, err = get(ctx, tx, id, false); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "roles.create", ResourceType: "role", ResourceID: id, After: out})
	})
	return out, err
}

// Clone copies a role (often a system role) under a new name, with its grants expanded to concrete permissions.
func (s Service) Clone(ctx context.Context, a audit.Actor, caller authz.Effective, id, name string) (Detail, error) {
	src, err := s.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	return s.Create(ctx, a, caller, Input{Name: name, Description: src.Description, Grants: src.Grants})
}

// Update renames a role and replaces its grants. Only grants that are new, or wider, are checked against the
// caller: keeping or removing a grant the caller does not hold is allowed, so a narrow role manager can still tidy
// a role up.
func (s Service) Update(ctx context.Context, a audit.Actor, caller authz.Effective, id string, in Input) (Detail, error) {
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		before, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if before.IsSystem {
			return ErrSystemRole
		}
		cat, err := catalog(ctx, tx)
		if err != nil {
			return err
		}
		if in, err = normalise(in, cat); err != nil {
			return err
		}
		if err := checkGrantable(caller, added(before.Grants, in.Grants)); err != nil {
			return err
		}
		if err := checkNameFree(ctx, tx, in.Name, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE roles SET name = $2, description = $3 WHERE id = $1::uuid`, id, in.Name, in.Description); err != nil {
			return systemError(err)
		}
		// Grants of disabled modules are not shown, so they are kept as they are rather than silently removed.
		kept, err := hiddenGrants(ctx, tx, id, cat)
		if err != nil {
			return err
		}
		if err := writeGrants(ctx, tx, t.ID, id, append(kept, in.Grants...)); err != nil {
			return err
		}
		if out, err = get(ctx, tx, id, false); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "roles.update", ResourceType: "role", ResourceID: id,
			Before: map[string]any{"name": before.Name, "grants": before.Grants}, After: map[string]any{"name": out.Name, "grants": out.Grants}})
	})
	return out, err
}

// hiddenGrants returns the role's stored grants that match nothing in the enabled catalog (a module turned off).
func hiddenGrants(ctx context.Context, tx pgx.Tx, roleID string, cat []Permission) ([]Grant, error) {
	stored, err := roleGrants(ctx, tx, roleID)
	if err != nil {
		return nil, err
	}
	var out []Grant
	for _, g := range stored {
		if len(expand([]Grant{g}, cat)) == 0 {
			out = append(out, g)
		}
	}
	return out, nil
}

// Delete removes a role nobody holds. System roles cannot be deleted.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		r, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if r.IsSystem {
			return ErrSystemRole
		}
		var anyHolder bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles ur JOIN users u ON u.tenant_id = ur.tenant_id AND u.id = ur.user_id AND u.deleted_at IS NULL
			WHERE ur.role_id = $1::uuid)`, id).Scan(&anyHolder); err != nil {
			return err
		}
		if anyHolder {
			return ErrInUse
		}
		if _, err := tx.Exec(ctx, `UPDATE roles SET deleted_at = now() WHERE id = $1::uuid`, id); err != nil {
			return systemError(err)
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "roles.delete", ResourceType: "role", ResourceID: id, Before: r})
	})
}

// UserRoles returns the roles a user holds now.
func (s Service) UserRoles(ctx context.Context, userID string) ([]Role, error) {
	var out []Role
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := userExists(ctx, tx, userID, false); err != nil {
			return err
		}
		var err error
		out, err = userRoles(ctx, tx, userID)
		return err
	})
	return out, err
}

func userRoles(ctx context.Context, tx pgx.Tx, userID string) ([]Role, error) {
	rows, err := tx.Query(ctx, `SELECT `+roleColumns+` FROM roles r JOIN user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = $1::uuid AND r.deleted_at IS NULL AND ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now())
		ORDER BY r.is_system DESC, lower(r.name)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var r Role
		if err := scanRole(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func userExists(ctx context.Context, tx pgx.Tx, userID string, forUpdate bool) error {
	if _, err := uuid.Parse(userID); err != nil {
		return ErrNotFound
	}
	sql := `SELECT 1 FROM users WHERE id = $1::uuid AND deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	var one int
	if err := tx.QueryRow(ctx, sql, userID).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	return nil
}

// SetUserRoles replaces the roles a user holds. Every role being added must be grantable by the caller in full;
// removing the Admin role from the last active Admin is refused.
func (s Service) SetUserRoles(ctx context.Context, a audit.Actor, caller authz.Effective, userID string, roleIDs []string) ([]Role, error) {
	var out []Role
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		if err := users.LockAdmins(ctx, tx, t.ID); err != nil {
			return err
		}
		if err := userExists(ctx, tx, userID, true); err != nil {
			return err
		}
		before, err := userRoles(ctx, tx, userID)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, id := range roleIDs {
			if _, err := uuid.Parse(id); err != nil {
				return crud.FieldErrors{{Field: "roleIds", Code: "invalid", Message: "Choose roles from the list."}}
			}
			want[id] = true
		}
		had := map[string]Role{}
		for _, r := range before {
			had[r.ID] = r
		}
		cat, err := catalog(ctx, tx)
		if err != nil {
			return err
		}
		for id := range want {
			if _, ok := had[id]; ok {
				continue
			}
			d, err := get(ctx, tx, id, false)
			if errors.Is(err, ErrNotFound) {
				return crud.FieldErrors{{Field: "roleIds", Code: "not_found", Message: "A chosen role does not exist."}}
			}
			if err != nil {
				return err
			}
			if err := checkGrantable(caller, expand(d.Grants, cat)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_roles (tenant_id, user_id, role_id, granted_by) VALUES ($1, $2::uuid, $3::uuid, $4)
				ON CONFLICT (user_id, role_id) DO UPDATE SET starts_at = now(), ends_at = NULL, granted_by = EXCLUDED.granted_by`,
				t.ID, userID, id, nullable(a.UserID)); err != nil {
				return err
			}
		}
		for id, r := range had {
			if want[id] {
				continue
			}
			if r.Code == "admin" {
				if err := users.CheckNotLastAdmin(ctx, tx, userID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1::uuid AND role_id = $2::uuid`, userID, id); err != nil {
				return err
			}
		}
		if out, err = userRoles(ctx, tx, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.set_roles", ResourceType: "user", ResourceID: userID,
			Before: names(before), After: names(out)})
	})
	return out, err
}

func names(rs []Role) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Code
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
