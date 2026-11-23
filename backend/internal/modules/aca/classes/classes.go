// Package classes implements ACA-20 Classes (grades such as "Class 5").
package classes

import (
	"context"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the class does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Class is a class as the API shows it.
type Class struct {
	ID                string    `json:"id"`
	Code              string    `json:"code"`
	Name              string    `json:"name"`
	DivisionID        *string   `json:"divisionId"`
	DivisionName      *string   `json:"divisionName"`
	DisplayOrder      int       `json:"displayOrder"`
	YoungestAgeMonths *int      `json:"youngestAgeMonths"`
	OldestAgeMonths   *int      `json:"oldestAgeMonths"`
	Description       *string   `json:"description"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code              string
	Name              string
	DivisionID        *string
	DisplayOrder      int
	YoungestAgeMonths *int
	OldestAgeMonths   *int
	Description       *string
	Status            string
}

// Filter narrows a list.
type Filter struct{ Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "displayOrder": "display_order", "youngestAgeMonths": "youngest_age_months", "createdAt": "created_at"}

// DefaultSort follows the school's own class order.
const DefaultSort = "displayOrder,name"

const columns = `id::text, code, name, division_id::text,
	(SELECT d.name FROM school_divisions d WHERE d.tenant_id = classes.tenant_id AND d.id = classes.division_id) AS division_name, display_order, youngest_age_months, oldest_age_months, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Class, error) {
	var c Class
	err := r.Scan(&c.ID, &c.Code, &c.Name, &c.DivisionID, &c.DivisionName, &c.DisplayOrder, &c.YoungestAgeMonths, &c.OldestAgeMonths, &c.Description, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Normalise trims, defaults and validates.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	in.Description, in.DivisionID = crud.TrimPtr(in.Description), crud.TrimPtr(in.DivisionID)
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
	if in.DivisionID != nil && !uuidRE.MatchString(*in.DivisionID) {
		errs.Add("divisionId", "invalid", "Choose a division from the list.")
	}
	if in.DisplayOrder < 0 || in.DisplayOrder > 100000 {
		errs.Add("displayOrder", "out_of_range", "Display order must be between 0 and 100000.")
	}
	for field, v := range map[string]*int{"youngestAgeMonths": in.YoungestAgeMonths, "oldestAgeMonths": in.OldestAgeMonths} {
		if v != nil && (*v < 0 || *v > 600) {
			errs.Add(field, "out_of_range", "Age must be between 0 and 600 months.")
		}
	}
	if in.YoungestAgeMonths != nil && in.OldestAgeMonths != nil && *in.YoungestAgeMonths > *in.OldestAgeMonths {
		errs.Add("oldestAgeMonths", "before_youngest", "Oldest age cannot be below the youngest age.")
	}
	if in.Description != nil && len([]rune(*in.Description)) > 500 {
		errs.Add("description", "too_long", "Description can be at most 500 characters.")
	}
	if in.Status != "active" && in.Status != "inactive" {
		errs.Add("status", "invalid_choice", "Status must be active or inactive.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Class, Input]{
	Table: "classes", Action: "classes", ResourceType: "class",
	Columns: columns, Scan: scan, ID: func(c Class) string { return c.ID },
	WriteColumns: []string{"code", "name", "division_id", "display_order", "youngest_age_months", "oldest_age_months", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, in.DivisionID, in.DisplayOrder, in.YoungestAgeMonths, in.OldestAgeMonths, in.Description, in.Status}
	},
	SearchColumns: []string{"code", "name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		return refs.Used(ctx, tx, id, `SELECT (SELECT count(*) FROM class_section_mappings WHERE class_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM subject_class_mappings WHERE class_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM textbooks WHERE class_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM enquiries WHERE class_id::text = $1 AND deleted_at IS NULL)`,
			"Sections, subjects, textbooks or enquiries are still mapped to this class. Remove them first.")
	},
	Constraints: map[string]crud.FieldError{
		"classes_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another class already uses this code."},
		"classes_division_fk":    {Field: "divisionId", Code: "not_found", Message: "This division does not exist."},
	},
}

// Service reads and writes classes.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Class, Input] { return s.store() }

func (s Service) store() resource.Store[Class, Input] {
	return resource.Store[Class, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of classes.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Class], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one class.
func (s Service) Get(ctx context.Context, id string) (Class, error) { return s.store().Get(ctx, id) }

// Create adds a class.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Class, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a class's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Class, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a class.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
