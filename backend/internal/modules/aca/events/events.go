// Package events implements ACA-29 Academic Calendars: the dated events of an academic year.
package events

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

// ErrNotFound is returned when the event does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Types an event can have. Recommended list; product may extend it.
var Types = []string{"academic", "exam", "event", "meeting", "activity", "other"}

// Event is a calendar event as the API shows it.
type Event struct {
	ID                 string    `json:"id"`
	YearID             string    `json:"yearId"`
	YearName           string    `json:"yearName"`
	Title              string    `json:"title"`
	EventType          string    `json:"eventType"`
	StartDate          string    `json:"startDate"`
	EndDate            string    `json:"endDate"`
	IsAllDay           bool      `json:"isAllDay"`
	IsInstructionalDay bool      `json:"isInstructionalDay"`
	IsExaminationDay   bool      `json:"isExaminationDay"`
	Venue              *string   `json:"venue"`
	Description        *string   `json:"description"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	YearID             string
	Title              string
	EventType          string
	StartDate          string
	EndDate            string
	IsAllDay           bool
	IsInstructionalDay bool
	IsExaminationDay   bool
	Venue              *string
	Description        *string
	Status             string
}

// Filter narrows a list.
type Filter struct{ YearID, EventType, Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"title": "title", "startDate": "start_date", "endDate": "end_date", "createdAt": "created_at"}

// DefaultSort lists events in calendar order.
const DefaultSort = "startDate"

const columns = `id::text, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = calendar_events.tenant_id AND y.id = calendar_events.year_id) AS year_name,
	title, event_type, start_date::text, end_date::text, is_all_day, is_instructional_day, is_examination_day, venue, description, status, created_at, updated_at`

func scan(r resource.Scanner) (Event, error) {
	var e Event
	err := r.Scan(&e.ID, &e.YearID, &e.YearName, &e.Title, &e.EventType, &e.StartDate, &e.EndDate, &e.IsAllDay, &e.IsInstructionalDay, &e.IsExaminationDay, &e.Venue, &e.Description, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Normalise trims, defaults and validates one event on its own; rules that need the year are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.YearID, in.Title = strings.TrimSpace(in.YearID), strings.TrimSpace(in.Title)
	in.Venue, in.Description = crud.TrimPtr(in.Venue), crud.TrimPtr(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	if !uuidRE.MatchString(in.YearID) {
		errs.Add("yearId", "required", "Choose an academic year.")
	}
	errs.Required("title", "a title", in.Title, 100)
	errs.Choice("eventType", in.EventType, Types...)
	errs.DateRange("startDate", "endDate", in.StartDate, in.EndDate)
	errs.Optional("venue", "venue", in.Venue, 100)
	errs.Optional("description", "description", in.Description, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

func date(s string) time.Time {
	t, _ := time.Parse(crud.DateLayout, s) // validated by Normalise
	return t
}

var spec = resource.Spec[Event, Input]{
	Table: "calendar_events", Action: "calendar_events", ResourceType: "calendar_event",
	Columns: columns, Scan: scan, ID: func(e Event) string { return e.ID },
	WriteColumns: []string{"year_id", "title", "event_type", "start_date", "end_date", "is_all_day", "is_instructional_day", "is_examination_day", "venue", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.YearID, in.Title, in.EventType, date(in.StartDate), date(in.EndDate), in.IsAllDay, in.IsInstructionalDay, in.IsExaminationDay, in.Venue, in.Description, in.Status}
	},
	SearchColumns: []string{"title", "venue"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		yStart, yEnd, err := yearcheck.Dates(ctx, tx, in.YearID)
		if err != nil {
			return err
		}
		var fe crud.FieldErrors
		yearcheck.Inside(&fe, "startDate", in.StartDate, in.EndDate, yStart, yEnd)
		return fe.OrNil()
	},
}

// Service reads and writes calendar events.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Event, Input] {
	return resource.Store[Event, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of events.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Event], error) {
	var conds []resource.Cond
	if f.YearID != "" {
		conds = append(conds, resource.Cond{SQL: "year_id::text = ?", Args: []any{f.YearID}})
	}
	if f.EventType != "" {
		conds = append(conds, resource.Cond{SQL: "event_type = ?", Args: []any{f.EventType}})
	}
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one event.
func (s Service) Get(ctx context.Context, id string) (Event, error) { return s.store().Get(ctx, id) }

// Create adds an event.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Event, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces an event's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Event, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes an event.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
