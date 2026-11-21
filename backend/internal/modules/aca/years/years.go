// Package years implements ACA-15 Academic Years.
package years

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the year does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Year is an academic year as the API shows it. Dates are "YYYY-MM-DD".
type Year struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	StartDate string    `json:"startDate"`
	EndDate   string    `json:"endDate"`
	IsCurrent bool      `json:"isCurrent"`
	Remarks   *string   `json:"remarks"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code      string
	Name      string
	StartDate string
	EndDate   string
	IsCurrent bool
	Remarks   *string
	Status    string
}

// Filter narrows a list.
type Filter struct {
	Status    string
	IsCurrent *bool
}

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "startDate": "start_date", "endDate": "end_date", "createdAt": "created_at"}

// DefaultSort shows the newest year first.
const DefaultSort = "-startDate"

const columns = `id::text, code, name, start_date::text, end_date::text, is_current, remarks, status, created_at, updated_at`

func scan(r resource.Scanner) (Year, error) {
	var y Year
	err := r.Scan(&y.ID, &y.Code, &y.Name, &y.StartDate, &y.EndDate, &y.IsCurrent, &y.Remarks, &y.Status, &y.CreatedAt, &y.UpdatedAt)
	return y, err
}

var codeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._/-]*$`)

// Normalise trims, applies defaults and validates. A current year must be active.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	in.Remarks = crud.TrimPtr(in.Remarks)
	if in.Status == "" {
		in.Status = "active"
	}
	switch {
	case in.Code == "":
		errs.Add("code", "required", "Enter a code.")
	case len([]rune(in.Code)) > 20 || !codeRE.MatchString(in.Code):
		errs.Add("code", "invalid", "Use up to 20 letters, digits, spaces or . _ / -.")
	}
	switch {
	case in.Name == "":
		errs.Add("name", "required", "Enter a name.")
	case len([]rune(in.Name)) > 100:
		errs.Add("name", "too_long", "Name can be at most 100 characters.")
	}
	start, startErr := time.Parse(crud.DateLayout, in.StartDate)
	end, endErr := time.Parse(crud.DateLayout, in.EndDate)
	if startErr != nil {
		errs.Add("startDate", "invalid_date", "Enter a valid start date.")
	}
	if endErr != nil {
		errs.Add("endDate", "invalid_date", "Enter a valid end date.")
	}
	if startErr == nil && endErr == nil && !end.After(start) {
		errs.Add("endDate", "before_start", "End date must be after the start date.")
	}
	if in.Remarks != nil && len([]rune(*in.Remarks)) > 500 {
		errs.Add("remarks", "too_long", "Remarks can be at most 500 characters.")
	}
	if in.Status != "active" && in.Status != "inactive" {
		errs.Add("status", "invalid_choice", "Status must be active or inactive.")
	}
	if in.IsCurrent && in.Status == "inactive" {
		errs.Add("isCurrent", "inactive_year", "The current year must be active.")
	}
	return in, errs.OrNil()
}

func date(s string) time.Time {
	t, _ := time.Parse(crud.DateLayout, s) // validated by Normalise
	return t
}

var spec = resource.Spec[Year, Input]{
	Table: "academic_years", Action: "academic_years", ResourceType: "academic_year",
	Columns: columns, Scan: scan, ID: func(y Year) string { return y.ID },
	WriteColumns: []string{"code", "name", "start_date", "end_date", "is_current", "remarks", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, date(in.StartDate), date(in.EndDate), in.IsCurrent, in.Remarks, in.Status}
	},
	SearchColumns: []string{"code", "name"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, id string, in Input) error {
		// Serialise year writes per institute so two concurrent saves cannot both pass the overlap check.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('academic_years:' || current_setting('app.tenant_id')))`); err != nil {
			return err
		}
		var clash bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM academic_years WHERE deleted_at IS NULL AND ($1 = '' OR id::text <> $1)
			AND daterange(start_date, end_date, '[]') && daterange($2::date, $3::date, '[]'))`, id, date(in.StartDate), date(in.EndDate)).Scan(&clash)
		if err != nil {
			return err
		}
		if clash {
			var fe crud.FieldErrors
			fe.Add("startDate", "overlaps_year", "These dates overlap another academic year.")
			return fe
		}
		return nil
	},
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		var terms int
		var current bool
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM academic_terms t WHERE t.year_id::text = $1 AND t.deleted_at IS NULL),
			(SELECT is_current FROM academic_years WHERE id::text = $1)`, id).Scan(&terms, &current); err != nil {
			return err
		}
		if current {
			return resource.InUseError{Message: "This is the current academic year. Make another year current first."}
		}
		if terms > 0 {
			return resource.InUseError{Message: "This year still has terms. Delete them first."}
		}
		return refs.Used(ctx, tx, id, `SELECT (SELECT count(*) FROM class_section_mappings WHERE year_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM subject_class_mappings WHERE year_id::text = $1 AND deleted_at IS NULL)`,
			"This year still has class-section or subject-class mappings. Delete them first.")
	},
	Constraints: map[string]crud.FieldError{
		"academic_years_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another academic year already uses this code."},
		"academic_years_one_current_uq": {Field: "isCurrent", Code: "another_current", Message: "Another year is already current. Unset it first."},
	},
}

// Service reads and writes academic years.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Year, Input] { return s.store() }

func (s Service) store() resource.Store[Year, Input] {
	return resource.Store[Year, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of years.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Year], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	if f.IsCurrent != nil {
		conds = append(conds, resource.Cond{SQL: "is_current = ?", Args: []any{*f.IsCurrent}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one year.
func (s Service) Get(ctx context.Context, id string) (Year, error) { return s.store().Get(ctx, id) }

// Create adds a year.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Year, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a year's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Year, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a year unless it is current or still has terms.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
