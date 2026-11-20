// Package divisions implements ACA-18 School Divisions (for example Primary and Senior).
package divisions

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the division does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Division is a school division as the API shows it.
type Division struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	DisplayOrder int       `json:"displayOrder"`
	Description  *string   `json:"description"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code         string
	Name         string
	DisplayOrder int
	Description  *string
	Status       string
}

// Filter narrows a list.
type Filter struct{ Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "displayOrder": "display_order", "createdAt": "created_at"}

// DefaultSort follows the school's own order.
const DefaultSort = "displayOrder,name"

const columns = `id::text, code, name, display_order, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Division, error) {
	var d Division
	err := r.Scan(&d.ID, &d.Code, &d.Name, &d.DisplayOrder, &d.Description, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// Normalise trims, defaults and validates.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	in.Description = crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	errs.Required("code", "a code", in.Code, 20)
	errs.Required("name", "a name", in.Name, 100)
	errs.Range("displayOrder", "display order", in.DisplayOrder, 0, 100000)
	errs.Optional("description", "description", in.Description, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

var spec = resource.Spec[Division, Input]{
	Table: "school_divisions", Action: "school_divisions", ResourceType: "school_division",
	Columns: columns, Scan: scan, ID: func(d Division) string { return d.ID },
	WriteColumns:  []string{"code", "name", "display_order", "description", "status"},
	Args:          func(in Input) []any { return []any{in.Code, in.Name, in.DisplayOrder, in.Description, in.Status} },
	SearchColumns: []string{"code", "name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM classes WHERE division_id::text = $1 AND deleted_at IS NULL`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return resource.InUseError{Message: "Classes still belong to this division. Move or delete them first."}
		}
		return nil
	},
	Constraints: map[string]crud.FieldError{
		"school_divisions_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another division already uses this code."},
	},
}

// Service reads and writes school divisions.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Division, Input] { return s.store() }

func (s Service) store() resource.Store[Division, Input] {
	return resource.Store[Division, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of divisions.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Division], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one division.
func (s Service) Get(ctx context.Context, id string) (Division, error) { return s.store().Get(ctx, id) }

// Create adds a division.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Division, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a division's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Division, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a division unless classes still belong to it.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
