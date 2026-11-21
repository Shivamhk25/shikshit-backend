// Package sections implements ACA-21 Sections (labels such as A and B).
package sections

import (
	"context"
	"strings"
	"time"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the section does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Section is a section as the API shows it.
type Section struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	ShortName   *string   `json:"shortName"`
	Description *string   `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code        string
	Name        string
	ShortName   *string
	Description *string
	Status      string
}

// Filter narrows a list.
type Filter struct{ Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "createdAt": "created_at"}

// DefaultSort orders by name.
const DefaultSort = "name"

const columns = `id::text, code, name, short_name, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Section, error) {
	var s Section
	err := r.Scan(&s.ID, &s.Code, &s.Name, &s.ShortName, &s.Description, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// Normalise trims, defaults and validates.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	in.ShortName, in.Description = crud.TrimPtr(in.ShortName), crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Code == "" {
		errs.Add("code", "required", "Enter a code.")
	} else if len([]rune(in.Code)) > 20 {
		errs.Add("code", "too_long", "Code can be at most 20 characters.")
	}
	if in.Name == "" {
		errs.Add("name", "required", "Enter a name.")
	} else if len([]rune(in.Name)) > 100 {
		errs.Add("name", "too_long", "Name can be at most 100 characters.")
	}
	if in.ShortName != nil && len([]rune(*in.ShortName)) > 20 {
		errs.Add("shortName", "too_long", "Short name can be at most 20 characters.")
	}
	if in.Description != nil && len([]rune(*in.Description)) > 500 {
		errs.Add("description", "too_long", "Description can be at most 500 characters.")
	}
	if in.Status != "active" && in.Status != "inactive" {
		errs.Add("status", "invalid_choice", "Status must be active or inactive.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Section, Input]{
	Table: "sections", Action: "sections", ResourceType: "section",
	Columns: columns, Scan: scan, ID: func(s Section) string { return s.ID },
	WriteColumns:  []string{"code", "name", "short_name", "description", "status"},
	Args:          func(in Input) []any { return []any{in.Code, in.Name, in.ShortName, in.Description, in.Status} },
	SearchColumns: []string{"code", "name", "short_name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Constraints: map[string]crud.FieldError{
		"sections_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another section already uses this code."},
	},
}

// Service reads and writes sections.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Section, Input] { return s.store() }

func (s Service) store() resource.Store[Section, Input] {
	return resource.Store[Section, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of sections.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Section], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one section.
func (s Service) Get(ctx context.Context, id string) (Section, error) { return s.store().Get(ctx, id) }

// Create adds a section.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Section, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a section's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Section, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a section.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
