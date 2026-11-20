// Package resource is the engine behind master-data resources (rooms, academic years, classes, ...). A resource is
// described by a Spec; the engine provides list (paging, sorting, search, filters), get, create, replace and soft
// delete, each in an institute-bound transaction with the audit row written in the same transaction.
//
// What stays in the resource's own package: its types, validation (Normalise) and, where needed, a Check that runs
// inside the write transaction (for example "the academic year must exist"). Everything else is shared, so a new
// resource is a table, a Spec and a few dozen lines instead of a copy of an existing service.
package resource

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the row does not exist in this institute (or was deleted).
var ErrNotFound = errors.New("resource: not found")

// InUseError means a delete was refused because other live records still depend on the row. The API answers 409.
type InUseError struct{ Message string }

func (e InUseError) Error() string { return "resource: in use: " + e.Message }

// Scanner is satisfied by pgx rows.
type Scanner interface{ Scan(dest ...any) error }

// Cond is one extra WHERE condition. SQL uses "?" for each argument; the engine numbers them.
type Cond struct {
	SQL  string
	Args []any
}

// Spec describes a resource. T is the stored row, I the input of create and replace.
type Spec[T, I any] struct {
	Table string // table name; must have id, tenant_id, created_by, updated_by, deleted_at
	// Names used in the audit trail: Action prefix ("rooms" -> rooms.create) and ResourceType ("room").
	Action, ResourceType string

	// Columns is the SELECT list, in the order Scan reads it. Scan must read exactly those columns.
	Columns string
	Scan    func(Scanner) (T, error)
	ID      func(T) string

	// WriteColumns are the columns set by create and replace, in the order Args returns their values.
	WriteColumns []string
	Args         func(I) []any

	// SearchColumns are matched with a case-insensitive "contains" when a search text is given.
	SearchColumns []string
	Sorts         crud.Sorts
	DefaultSort   string

	// Normalise cleans and validates input; it returns crud.FieldErrors for the client to show.
	Normalise func(I) (I, error)
	// Check runs inside the write transaction after Normalise (for existence or cross-field rules). id is empty on
	// create. Optional.
	Check func(ctx context.Context, tx pgx.Tx, id string, in I) error
	// BeforeDelete runs inside the delete transaction; return InUseError to refuse. Optional.
	BeforeDelete func(ctx context.Context, tx pgx.Tx, id string) error
	// Constraints maps a database constraint name to the field error to show when it is violated.
	Constraints map[string]crud.FieldError
}

// Store runs a Spec against the database.
type Store[T, I any] struct {
	DB   tenancy.Beginner
	Spec Spec[T, I]
}

type listScanner struct {
	rows  pgx.Rows
	total *int
}

func (l listScanner) Scan(dest ...any) error { return l.rows.Scan(append(dest, l.total)...) }

func (s Store[T, I]) fieldError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if fe, ok := s.Spec.Constraints[pg.ConstraintName]; ok {
			return crud.FieldErrors{fe}
		}
	}
	return err
}

// List returns one page. extra are additional filters already validated by the caller.
func (s Store[T, I]) List(ctx context.Context, q crud.ListQuery, extra ...Cond) (crud.Page[T], error) {
	page := crud.Page[T]{Items: []T{}, Page: q.Page, Size: q.Size}
	args := []any{}
	where := "deleted_at IS NULL"
	add := func(sql string, a ...any) {
		for _, v := range a {
			args = append(args, v)
			sql = strings.Replace(sql, "?", "$"+strconv.Itoa(len(args)), 1)
		}
		where += " AND " + sql
	}
	if q.Search != "" && len(s.Spec.SearchColumns) > 0 {
		parts := make([]string, len(s.Spec.SearchColumns))
		for i, c := range s.Spec.SearchColumns {
			parts[i] = c + ` ILIKE $1 ESCAPE '\'`
		}
		// the same pattern is reused for every column, so bind it once
		args = append(args, crud.LikePattern(q.Search))
		where += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	for _, c := range extra {
		add(c.SQL, c.Args...)
	}
	args = append(args, q.Size, q.Offset())

	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		// Table, columns and OrderBy come from the Spec and the sort allow-list, never from request text.
		sql := fmt.Sprintf(`SELECT %s, count(*) OVER() FROM %s WHERE %s ORDER BY %s, id LIMIT $%d OFFSET $%d`,
			s.Spec.Columns, s.Spec.Table, where, q.OrderBy, len(args)-1, len(args))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := s.Spec.Scan(listScanner{rows, &page.Total})
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	return page, err
}

// Get returns one row.
func (s Store[T, I]) Get(ctx context.Context, id string) (T, error) {
	var out T
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		out, err = s.Spec.Scan(tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id::text = $1 AND deleted_at IS NULL`, s.Spec.Columns, s.Spec.Table), id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// Create validates, inserts and audits.
func (s Store[T, I]) Create(ctx context.Context, actor audit.Actor, in I) (T, error) {
	var zero T
	in, err := s.Spec.Normalise(in)
	if err != nil {
		return zero, err
	}
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return zero, tenancy.ErrNoTenant
	}
	cols := append([]string{"tenant_id"}, s.Spec.WriteColumns...)
	cols = append(cols, "created_by", "updated_by")
	holders := make([]string, len(cols))
	for i := range cols {
		holders[i] = "$" + strconv.Itoa(i+1)
	}
	n := len(cols)
	holders[n-2], holders[n-1] = fmt.Sprintf("NULLIF($%d, '')::uuid", n-1), fmt.Sprintf("NULLIF($%d, '')::uuid", n-1)
	args := append([]any{t.ID}, s.Spec.Args(in)...)
	args = append(args, actor.UserID)

	var out T
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if s.Spec.Check != nil {
			if err := s.Spec.Check(ctx, tx, "", in); err != nil {
				return err
			}
		}
		sql := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) RETURNING %s`, s.Spec.Table, strings.Join(cols, ", "), strings.Join(holders, ", "), s.Spec.Columns)
		var err error
		if out, err = s.Spec.Scan(tx.QueryRow(ctx, sql, args...)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: s.Spec.Action + ".create", ResourceType: s.Spec.ResourceType, ResourceID: s.Spec.ID(out), After: out})
	})
	if err != nil {
		return zero, s.fieldError(err)
	}
	return out, nil
}

// Update replaces every field and audits before and after.
func (s Store[T, I]) Update(ctx context.Context, actor audit.Actor, id string, in I) (T, error) {
	var zero T
	in, err := s.Spec.Normalise(in)
	if err != nil {
		return zero, err
	}
	sets := make([]string, len(s.Spec.WriteColumns))
	for i, c := range s.Spec.WriteColumns {
		sets[i] = fmt.Sprintf("%s = $%d", c, i+2)
	}
	by := len(s.Spec.WriteColumns) + 2
	sets = append(sets, fmt.Sprintf("updated_by = NULLIF($%d, '')::uuid", by))
	args := append([]any{id}, s.Spec.Args(in)...)
	args = append(args, actor.UserID)

	var after T
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := s.Spec.Scan(tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, s.Spec.Columns, s.Spec.Table), id))
		if err != nil {
			return err
		}
		if s.Spec.Check != nil {
			if err := s.Spec.Check(ctx, tx, id, in); err != nil {
				return err
			}
		}
		sql := fmt.Sprintf(`UPDATE %s SET %s WHERE id = $1::uuid RETURNING %s`, s.Spec.Table, strings.Join(sets, ", "), s.Spec.Columns)
		if after, err = s.Spec.Scan(tx.QueryRow(ctx, sql, args...)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: s.Spec.Action + ".update", ResourceType: s.Spec.ResourceType, ResourceID: id, Before: before, After: after})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrNotFound
	}
	if err != nil {
		return zero, s.fieldError(err)
	}
	return after, nil
}

// Delete soft-deletes a row, which frees its unique code.
func (s Store[T, I]) Delete(ctx context.Context, actor audit.Actor, id string) error {
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := s.Spec.Scan(tx.QueryRow(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, s.Spec.Columns, s.Spec.Table), id))
		if err != nil {
			return err
		}
		if s.Spec.BeforeDelete != nil {
			if err := s.Spec.BeforeDelete(ctx, tx, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET deleted_at = now(), updated_by = NULLIF($2, '')::uuid WHERE id = $1::uuid`, s.Spec.Table), id, actor.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: s.Spec.Action + ".delete", ResourceType: s.Spec.ResourceType, ResourceID: id, Before: before})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return s.fieldError(err)
}
