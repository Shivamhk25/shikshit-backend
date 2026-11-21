// Package holidays implements ACA-17 Holiday Calendars: the holidays of an academic year.
package holidays

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/yearcheck"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the holiday does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Types a holiday can have. Recommended list; product may extend it.
var Types = []string{"public", "school", "regional", "restricted", "other"}

// Holiday is a holiday as the API shows it.
type Holiday struct {
	ID                string    `json:"id"`
	YearID            string    `json:"yearId"`
	YearName          string    `json:"yearName"`
	Title             string    `json:"title"`
	HolidayType       string    `json:"holidayType"`
	StartDate         string    `json:"startDate"`
	EndDate           string    `json:"endDate"`
	IsOptional        bool      `json:"isOptional"`
	IsRecurringAnnual bool      `json:"isRecurringAnnual"`
	ApplicableFor     *string   `json:"applicableFor"`
	Description       *string   `json:"description"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	YearID            string
	Title             string
	HolidayType       string
	StartDate         string
	EndDate           string
	IsOptional        bool
	IsRecurringAnnual bool
	ApplicableFor     *string
	Description       *string
	Status            string
}

// Filter narrows a list.
type Filter struct{ YearID, HolidayType, Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"title": "title", "startDate": "start_date", "endDate": "end_date", "createdAt": "created_at"}

// DefaultSort lists holidays in calendar order.
const DefaultSort = "startDate"

const columns = `id::text, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = holidays.tenant_id AND y.id = holidays.year_id) AS year_name,
	title, holiday_type, start_date::text, end_date::text, is_optional, is_recurring_annual, applicable_for, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Holiday, error) {
	var h Holiday
	err := r.Scan(&h.ID, &h.YearID, &h.YearName, &h.Title, &h.HolidayType, &h.StartDate, &h.EndDate, &h.IsOptional, &h.IsRecurringAnnual, &h.ApplicableFor, &h.Description, &h.Status, &h.CreatedAt, &h.UpdatedAt)
	return h, err
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Normalise trims, defaults and validates one holiday on its own; rules that need the year are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.YearID, in.Title = strings.TrimSpace(in.YearID), strings.TrimSpace(in.Title)
	in.ApplicableFor, in.Description = crud.TrimPtr(in.ApplicableFor), crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	if !uuidRE.MatchString(in.YearID) {
		errs.Add("yearId", "required", "Choose an academic year.")
	}
	errs.Required("title", "a title", in.Title, 100)
	errs.Choice("holidayType", in.HolidayType, Types...)
	errs.DateRange("startDate", "endDate", in.StartDate, in.EndDate)
	errs.Optional("applicableFor", "applicable for", in.ApplicableFor, 100)
	errs.Optional("description", "description", in.Description, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

func date(s string) time.Time {
	t, _ := time.Parse(crud.DateLayout, s) // validated by Normalise
	return t
}

var spec = resource.Spec[Holiday, Input]{
	Table: "holidays", Action: "holidays", ResourceType: "holiday",
	Columns: columns, Scan: scan, ID: func(h Holiday) string { return h.ID },
	WriteColumns: []string{"year_id", "title", "holiday_type", "start_date", "end_date", "is_optional", "is_recurring_annual", "applicable_for", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.YearID, in.Title, in.HolidayType, date(in.StartDate), date(in.EndDate), in.IsOptional, in.IsRecurringAnnual, in.ApplicableFor, in.Description, in.Status}
	},
	SearchColumns: []string{"title", "applicable_for"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		yStart, yEnd, err := yearcheck.Dates(ctx, tx, in.YearID)
		if err != nil {
			return err
		}
		var fe crud.FieldErrors
		if !in.IsRecurringAnnual { // a holiday that repeats every year is entered once, in whichever year it was first set
			yearcheck.Inside(&fe, "startDate", in.StartDate, in.EndDate, yStart, yEnd)
		}
		return fe.OrNil()
	},
}

// Service reads and writes holidays.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Holiday, Input] { return s.store() }

func (s Service) store() resource.Store[Holiday, Input] {
	return resource.Store[Holiday, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of holidays.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Holiday], error) {
	var conds []resource.Cond
	if f.YearID != "" {
		conds = append(conds, resource.Cond{SQL: "year_id::text = ?", Args: []any{f.YearID}})
	}
	if f.HolidayType != "" {
		conds = append(conds, resource.Cond{SQL: "holiday_type = ?", Args: []any{f.HolidayType}})
	}
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one holiday.
func (s Service) Get(ctx context.Context, id string) (Holiday, error) { return s.store().Get(ctx, id) }

// Create adds a holiday.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Holiday, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a holiday's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Holiday, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a holiday.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
