// Package electives implements ACA-10 Elective Groups (how many subjects of a group a student must or may take).
package electives

import (
	"context"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the group does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Group is an elective group as the API shows it.
type Group struct {
	ID            string    `json:"id"`
	Code          *string   `json:"code"`
	Name          string    `json:"name"`
	MinSubjects   int       `json:"minSubjects"`
	MaxSubjects   int       `json:"maxSubjects"`
	EnforceLimits bool      `json:"enforceLimits"`
	DisplayOrder  int       `json:"displayOrder"`
	Description   *string   `json:"description"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code          *string
	Name          string
	MinSubjects   int
	MaxSubjects   int
	EnforceLimits bool
	DisplayOrder  int
	Description   *string
	Status        string
}

// Filter narrows a list.
type Filter struct{ Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "displayOrder": "display_order", "createdAt": "created_at"}

// DefaultSort follows the school's own order.
const DefaultSort = "displayOrder,name"

const columns = `id::text, code, name, min_subjects, max_subjects, enforce_limits, display_order, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Group, error) {
	var g Group
	err := r.Scan(&g.ID, &g.Code, &g.Name, &g.MinSubjects, &g.MaxSubjects, &g.EnforceLimits, &g.DisplayOrder, &g.Description, &g.Status, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// Normalise trims, defaults and validates. The minimum cannot exceed the maximum.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Name = strings.TrimSpace(in.Name)
	in.Code, in.Description = crud.TrimPtr(in.Code), crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	errs.Optional("code", "code", in.Code, 20)
	errs.Required("name", "a name", in.Name, 100)
	errs.Range("minSubjects", "minimum", in.MinSubjects, 0, 50)
	errs.Range("maxSubjects", "maximum", in.MaxSubjects, 1, 50)
	if in.MinSubjects > in.MaxSubjects {
		errs.Add("maxSubjects", "below_minimum", "The maximum cannot be below the minimum.")
	}
	errs.Range("displayOrder", "display order", in.DisplayOrder, 0, 100000)
	errs.Optional("description", "description", in.Description, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

var spec = resource.Spec[Group, Input]{
	Table: "elective_groups", Action: "elective_groups", ResourceType: "elective_group",
	Columns: columns, Scan: scan, ID: func(g Group) string { return g.ID },
	WriteColumns: []string{"code", "name", "min_subjects", "max_subjects", "enforce_limits", "display_order", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, in.MinSubjects, in.MaxSubjects, in.EnforceLimits, in.DisplayOrder, in.Description, in.Status}
	},
	SearchColumns: []string{"code", "name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Constraints: map[string]crud.FieldError{
		"elective_groups_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another group already uses this code."},
	},
}

// Service reads and writes elective groups.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Group, Input] {
	return resource.Store[Group, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of groups.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Group], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one group.
func (s Service) Get(ctx context.Context, id string) (Group, error) { return s.store().Get(ctx, id) }

// Create adds a group.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Group, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a group's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Group, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a group.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
