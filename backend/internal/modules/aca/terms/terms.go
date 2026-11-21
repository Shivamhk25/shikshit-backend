// Package terms implements ACA-16 Academic Terms: dated periods inside an academic year.
package terms

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the term does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Term is a term as the API shows it. Days counts both end dates.
type Term struct {
	ID        string    `json:"id"`
	YearID    string    `json:"yearId"`
	YearName  string    `json:"yearName"`
	Code      string    `json:"code"`
	Title     string    `json:"title"`
	StartDate string    `json:"startDate"`
	EndDate   string    `json:"endDate"`
	Days      int       `json:"days"`
	Color     *string   `json:"color"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	YearID    string
	Code      string
	Title     string
	StartDate string
	EndDate   string
	Color     *string
}

// Filter narrows a list.
type Filter struct{ YearID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "title": "title", "startDate": "start_date", "endDate": "end_date", "createdAt": "created_at"}

// DefaultSort lists terms in calendar order.
const DefaultSort = "startDate"

const columns = `id::text, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = academic_terms.tenant_id AND y.id = academic_terms.year_id) AS year_name,
	code, title, start_date::text, end_date::text, (end_date - start_date + 1), color, created_at, updated_at`

func scan(r resource.Scanner) (Term, error) {
	var t Term
	err := r.Scan(&t.ID, &t.YearID, &t.YearName, &t.Code, &t.Title, &t.StartDate, &t.EndDate, &t.Days, &t.Color, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

var (
	uuidRE  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	colorRE = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// Normalise trims and validates one term on its own; rules that need the year are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.YearID, in.Code, in.Title = strings.TrimSpace(in.YearID), strings.TrimSpace(in.Code), strings.TrimSpace(in.Title)
	in.Color = crud.TrimPtr(in.Color)
	if !uuidRE.MatchString(in.YearID) {
		errs.Add("yearId", "required", "Choose an academic year.")
	}
	if in.Code == "" {
		errs.Add("code", "required", "Enter a code.")
	} else if len([]rune(in.Code)) > 20 {
		errs.Add("code", "too_long", "Code can be at most 20 characters.")
	}
	if in.Title == "" {
		errs.Add("title", "required", "Enter a title.")
	} else if len([]rune(in.Title)) > 100 {
		errs.Add("title", "too_long", "Title can be at most 100 characters.")
	}
	start, startErr := time.Parse(crud.DateLayout, in.StartDate)
	end, endErr := time.Parse(crud.DateLayout, in.EndDate)
	if startErr != nil {
		errs.Add("startDate", "invalid_date", "Enter a valid start date.")
	}
	if endErr != nil {
		errs.Add("endDate", "invalid_date", "Enter a valid end date.")
	}
	if startErr == nil && endErr == nil && end.Before(start) {
		errs.Add("endDate", "before_start", "End date cannot be before the start date.")
	}
	if in.Color != nil && !colorRE.MatchString(*in.Color) {
		errs.Add("color", "invalid_color", "Use a colour like #2F5BFF.")
	}
	return in, errs.OrNil()
}

func date(s string) time.Time {
	t, _ := time.Parse(crud.DateLayout, s) // validated by Normalise
	return t
}

var spec = resource.Spec[Term, Input]{
	Table: "academic_terms", Action: "academic_terms", ResourceType: "academic_term",
	Columns: columns, Scan: scan, ID: func(t Term) string { return t.ID },
	WriteColumns: []string{"year_id", "code", "title", "start_date", "end_date", "color"},
	Args: func(in Input) []any {
		return []any{in.YearID, in.Code, in.Title, date(in.StartDate), date(in.EndDate), in.Color}
	},
	SearchColumns: []string{"code", "title"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, id string, in Input) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('academic_terms:' || $1))`, in.YearID); err != nil {
			return err
		}
		var yStart, yEnd string
		err := tx.QueryRow(ctx, `SELECT start_date::text, end_date::text FROM academic_years WHERE id::text = $1 AND deleted_at IS NULL`, in.YearID).Scan(&yStart, &yEnd)
		if err == pgx.ErrNoRows {
			var fe crud.FieldErrors
			fe.Add("yearId", "not_found", "This academic year does not exist.")
			return fe
		}
		if err != nil {
			return err
		}
		var fe crud.FieldErrors
		if in.StartDate < yStart || in.EndDate > yEnd { // ISO dates compare correctly as text
			fe.Add("startDate", "outside_year", "Term dates must lie inside the academic year ("+yStart+" to "+yEnd+").")
		}
		var clash bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM academic_terms WHERE deleted_at IS NULL AND year_id::text = $1 AND ($2 = '' OR id::text <> $2)
			AND daterange(start_date, end_date, '[]') && daterange($3::date, $4::date, '[]'))`, in.YearID, id, date(in.StartDate), date(in.EndDate)).Scan(&clash); err != nil {
			return err
		}
		if clash {
			fe.Add("startDate", "overlaps_term", "These dates overlap another term of this year.")
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"academic_terms_year_code_uq": {Field: "code", Code: "duplicate", Message: "Another term of this year already uses this code."},
	},
}

// Service reads and writes terms.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Term, Input] { return s.store() }

func (s Service) store() resource.Store[Term, Input] {
	return resource.Store[Term, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of terms, optionally of one year.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Term], error) {
	var conds []resource.Cond
	if f.YearID != "" {
		conds = append(conds, resource.Cond{SQL: "year_id::text = ?", Args: []any{f.YearID}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one term.
func (s Service) Get(ctx context.Context, id string) (Term, error) { return s.store().Get(ctx, id) }

// Create adds a term.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Term, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a term's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Term, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a term.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
