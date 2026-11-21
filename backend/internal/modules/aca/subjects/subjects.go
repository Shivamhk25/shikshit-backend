// Package subjects implements ACA-22 Subjects.
package subjects

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the subject does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Subject is a subject as the API shows it.
type Subject struct {
	ID            string    `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	ShortName     *string   `json:"shortName"`
	Color         *string   `json:"color"`
	Category      string    `json:"category"`
	Area          string    `json:"area"`
	Scholastic    bool      `json:"scholastic"`
	CoScholastic  bool      `json:"coScholastic"`
	NonScholastic bool      `json:"nonScholastic"`
	Credits       float64   `json:"credits"`
	Description   *string   `json:"description"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code          string
	Name          string
	ShortName     *string
	Color         *string
	Category      string
	Area          string
	Scholastic    bool
	CoScholastic  bool
	NonScholastic bool
	Credits       float64
	Description   *string
	Status        string
}

// Filter narrows a list.
type Filter struct{ Status, Category string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "category": "category", "area": "area", "credits": "credits", "createdAt": "created_at"}

// DefaultSort orders by name.
const DefaultSort = "name"

const columns = `id::text, code, name, short_name, color, category, area, scholastic, co_scholastic, non_scholastic, credits::float8, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Subject, error) {
	var s Subject
	err := r.Scan(&s.ID, &s.Code, &s.Name, &s.ShortName, &s.Color, &s.Category, &s.Area, &s.Scholastic, &s.CoScholastic, &s.NonScholastic, &s.Credits, &s.Description, &s.Status, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// Categories and Areas are the recommended lists (stored as these codes); product may extend them with a migration.
var (
	Categories = []string{"core", "elective", "language", "optional", "vocational"}
	Areas      = []string{"languages", "mathematics", "science", "social_science", "arts", "physical_education", "technology", "vocational", "life_skills", "other"}
)

var colorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Normalise trims, defaults and validates. Category and area are free text until product confirms the lists.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	in.Category, in.Area = strings.ToLower(strings.TrimSpace(in.Category)), strings.ToLower(strings.TrimSpace(in.Area))
	in.ShortName, in.Color, in.Description = crud.TrimPtr(in.ShortName), crud.TrimPtr(in.Color), crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	in.Credits = math.Round(in.Credits*10) / 10
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
	errs.Choice("category", in.Category, Categories...)
	errs.Choice("area", in.Area, Areas...)
	if in.ShortName != nil && len([]rune(*in.ShortName)) > 20 {
		errs.Add("shortName", "too_long", "Short name can be at most 20 characters.")
	}
	if in.Color != nil && !colorRE.MatchString(*in.Color) {
		errs.Add("color", "invalid_color", "Use a colour like #2F5BFF.")
	}
	if math.IsNaN(in.Credits) || in.Credits < 0 || in.Credits > 99.9 {
		errs.Add("credits", "out_of_range", "Credits must be between 0 and 99.9.")
	}
	if in.Description != nil && len([]rune(*in.Description)) > 500 {
		errs.Add("description", "too_long", "Description can be at most 500 characters.")
	}
	if in.Status != "active" && in.Status != "inactive" {
		errs.Add("status", "invalid_choice", "Status must be active or inactive.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Subject, Input]{
	Table: "subjects", Action: "subjects", ResourceType: "subject",
	Columns: columns, Scan: scan, ID: func(s Subject) string { return s.ID },
	WriteColumns: []string{"code", "name", "short_name", "color", "category", "area", "scholastic", "co_scholastic", "non_scholastic", "credits", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, in.ShortName, in.Color, in.Category, in.Area, in.Scholastic, in.CoScholastic, in.NonScholastic, in.Credits, in.Description, in.Status}
	},
	SearchColumns: []string{"code", "name", "short_name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Constraints: map[string]crud.FieldError{
		"subjects_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another subject already uses this code."},
	},
}

// Service reads and writes subjects.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Subject, Input] {
	return resource.Store[Subject, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of subjects.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Subject], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	if f.Category != "" {
		conds = append(conds, resource.Cond{SQL: "lower(category) = lower(?)", Args: []any{f.Category}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one subject.
func (s Service) Get(ctx context.Context, id string) (Subject, error) { return s.store().Get(ctx, id) }

// Create adds a subject.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Subject, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a subject's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Subject, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a subject.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
