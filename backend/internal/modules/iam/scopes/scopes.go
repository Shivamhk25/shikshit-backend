// Package scopes implements IAM-03 Scope assignments: which records a user may touch, for permissions granted with a
// narrower scope than the whole institute. Today the one scope with a target is class_section, pointing at a
// class-section mapping (one class and section in one academic year).
package scopes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the assignment does not exist in this institute.
var ErrNotFound = errors.New("scopes: not found")

// Types are the scope types that can be assigned today. The others in the database (campus, route, hostel, own
// children, department) need their target records first.
var Types = []string{"class_section"}

// Assignment is a scope assignment as the API shows it.
type Assignment struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	UserName  string     `json:"userName"`
	ScopeType string     `json:"scopeType"`
	ScopeRef  string     `json:"scopeRef"`
	Label     string     `json:"label"`
	StartsAt  time.Time  `json:"startsAt"`
	EndsAt    *time.Time `json:"endsAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Input is what create accepts.
type Input struct {
	UserID    string
	ScopeType string
	ScopeRef  string
	StartsAt  *time.Time
	EndsAt    *time.Time
}

// Filter narrows a list.
type Filter struct{ UserID, ScopeType, ScopeRef string }

const columns = `a.id::text, a.user_id::text, u.display_name, a.scope_type, a.scope_ref,
	CASE a.scope_type WHEN 'class_section' THEN COALESCE(
		(SELECT c.name || ' ' || s.name || ' (' || y.name || ')' FROM class_section_mappings m
			JOIN classes c ON c.tenant_id = m.tenant_id AND c.id = m.class_id
			JOIN sections s ON s.tenant_id = m.tenant_id AND s.id = m.section_id
			JOIN academic_years y ON y.tenant_id = m.tenant_id AND y.id = m.year_id
			WHERE m.tenant_id = a.tenant_id AND m.id::text = a.scope_ref AND m.deleted_at IS NULL), 'Removed class section')
	ELSE a.scope_ref END AS label,
	a.starts_at, a.ends_at, a.created_at`

const from = ` FROM scope_assignments a JOIN users u ON u.tenant_id = a.tenant_id AND u.id = a.user_id`

func scan(r interface{ Scan(...any) error }) (Assignment, error) {
	var x Assignment
	err := r.Scan(&x.ID, &x.UserID, &x.UserName, &x.ScopeType, &x.ScopeRef, &x.Label, &x.StartsAt, &x.EndsAt, &x.CreatedAt)
	return x, err
}

// Normalise trims and validates one assignment on its own.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.UserID, in.ScopeType, in.ScopeRef = strings.TrimSpace(in.UserID), strings.ToLower(strings.TrimSpace(in.ScopeType)), strings.TrimSpace(in.ScopeRef)
	refs.Require(&errs, "userId", "a user", in.UserID)
	errs.Choice("scopeType", in.ScopeType, Types...)
	if !refs.IsUUID(in.ScopeRef) {
		errs.Add("scopeRef", "required", "Choose what the user is assigned to.")
	}
	if in.StartsAt != nil && in.EndsAt != nil && !in.EndsAt.After(*in.StartsAt) {
		errs.Add("endsAt", "before_start", "The end must come after the start.")
	}
	return in, errs.OrNil()
}

// Service reads and writes scope assignments.
type Service struct{ DB tenancy.Beginner }

// List returns the assignments, newest first, at most 500 (a user is assigned to a handful of records).
func (s Service) List(ctx context.Context, f Filter) ([]Assignment, error) {
	out := []Assignment{}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+columns+from+`
			WHERE ($1 = '' OR a.user_id::text = $1) AND ($2 = '' OR a.scope_type = $2) AND ($3 = '' OR a.scope_ref = $3)
			ORDER BY a.created_at DESC, a.id LIMIT 500`, f.UserID, f.ScopeType, f.ScopeRef)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scan(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

func get(ctx context.Context, tx pgx.Tx, id string) (Assignment, error) {
	x, err := scan(tx.QueryRow(ctx, `SELECT `+columns+from+` WHERE a.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// Create assigns a user to a record. The target must be live in this institute. The user's access is recomputed on
// their next request (a trigger bumps their access version).
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Assignment, error) {
	in, err := Normalise(in)
	if err != nil {
		return Assignment{}, err
	}
	var out Assignment
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		var fe crud.FieldErrors
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = $1`, in.UserID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			fe.Add("userId", "not_found", "This user does not exist.")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM class_section_mappings WHERE id::text = $1 AND deleted_at IS NULL`, in.ScopeRef).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			fe.Add("scopeRef", "not_found", "This class section does not exist.")
		}
		if len(fe) > 0 {
			return fe
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref, starts_at, ends_at)
			VALUES ($1, $2::uuid, $3, $4, COALESCE($5, now()), $6) RETURNING id::text`, t.ID, in.UserID, in.ScopeType, in.ScopeRef, in.StartsAt, in.EndsAt).Scan(&id)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return crud.FieldErrors{{Field: "scopeRef", Code: "duplicate", Message: "This user is already assigned to it."}}
		}
		if errors.As(err, &pg) && pg.Code == "23514" {
			return crud.FieldErrors{{Field: "endsAt", Code: "before_start", Message: "The end must come after the start."}}
		}
		if err != nil {
			return err
		}
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "scope_assignments.create", ResourceType: "scope_assignment", ResourceID: id, After: out})
	})
	return out, err
}

// Delete removes an assignment. Assignments are not soft-deleted: the audit trail keeps what was removed.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM scope_assignments WHERE id::text = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "scope_assignments.delete", ResourceType: "scope_assignment", ResourceID: id, Before: before})
	})
}
