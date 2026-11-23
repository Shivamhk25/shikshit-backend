// Package enquiries implements ADM-03 Enquiry: a parent asks about a place for a child. The record keeps who asked,
// for which child, class and year, how they came to us, where the enquiry stands and when to follow up. Turning an
// enquiry into a registration is ADM-06; here the status is only kept.
package enquiries

import (
	"context"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/colleagues"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the enquiry does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Sources are how an enquiry reached the school; Statuses are where it stands. OpenStatuses still need a follow-up.
var (
	Sources      = []string{"walk_in", "phone", "website", "referral", "social", "other"}
	Statuses     = []string{"new", "contacted", "visit_scheduled", "converted", "closed"}
	OpenStatuses = []string{"new", "contacted", "visit_scheduled"}
)

// Enquiry is an enquiry as the API shows it. FollowUpDue is computed: a follow-up date today or earlier on an enquiry
// that is still open.
type Enquiry struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	EnquirerName   string    `json:"enquirerName"`
	Phone          string    `json:"phone"`
	Email          *string   `json:"email"`
	ChildName      string    `json:"childName"`
	DateOfBirth    *string   `json:"dateOfBirth"`
	ClassID        *string   `json:"classId"`
	ClassCode      *string   `json:"classCode"`
	ClassName      *string   `json:"className"`
	YearID         *string   `json:"yearId"`
	YearName       *string   `json:"yearName"`
	Source         string    `json:"source"`
	Status         string    `json:"status"`
	FollowUpDate   *string   `json:"followUpDate"`
	FollowUpDue    bool      `json:"followUpDue"`
	Notes          *string   `json:"notes"`
	AssignedToID   *string   `json:"assignedToId"`
	AssignedToName *string   `json:"assignedToName"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Input is what create and replace accept. There is no code: the database numbers each enquiry (ENQ-000123) when it is
// created and the number never changes.
type Input struct {
	EnquirerName string
	Phone        string
	Email        *string
	ChildName    string
	DateOfBirth  *string
	ClassID      *string
	YearID       *string
	Source       string
	Status       string
	FollowUpDate *string
	Notes        *string
	AssignedToID *string
}

// Filter narrows a list. FollowUpDue true keeps open enquiries whose follow-up date has come; false keeps the rest.
type Filter struct {
	Status, ClassID, Source string
	FollowUpDue             *bool
}

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "childName": "lower(child_name)", "enquirerName": "lower(enquirer_name)",
	"followUpDate": "follow_up_date", "status": "status", "createdAt": "created_at"}

// DefaultSort lists the newest enquiries first.
const DefaultSort = "-createdAt"

// dueSQL is true for an open enquiry whose follow-up date is today or earlier, by the calendar in India.
const dueSQL = `(follow_up_date IS NOT NULL AND follow_up_date <= (now() AT TIME ZONE 'Asia/Kolkata')::date AND status IN ('new', 'contacted', 'visit_scheduled'))`

const columns = `id::text, code, enquirer_name, phone, email, child_name, date_of_birth::text,
	class_id::text,
	(SELECT c.code FROM classes c WHERE c.tenant_id = enquiries.tenant_id AND c.id = enquiries.class_id) AS class_code,
	(SELECT c.name FROM classes c WHERE c.tenant_id = enquiries.tenant_id AND c.id = enquiries.class_id) AS class_name,
	year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = enquiries.tenant_id AND y.id = enquiries.year_id) AS year_name,
	source, status, follow_up_date::text, ` + dueSQL + ` AS follow_up_due, notes,
	assigned_to::text,
	(SELECT u.display_name FROM users u WHERE u.tenant_id = enquiries.tenant_id AND u.id = enquiries.assigned_to) AS assigned_to_name,
	created_at, updated_at`

func scan(r resource.Scanner) (Enquiry, error) {
	var e Enquiry
	err := r.Scan(&e.ID, &e.Code, &e.EnquirerName, &e.Phone, &e.Email, &e.ChildName, &e.DateOfBirth, &e.ClassID, &e.ClassCode, &e.ClassName,
		&e.YearID, &e.YearName, &e.Source, &e.Status, &e.FollowUpDate, &e.FollowUpDue, &e.Notes, &e.AssignedToID, &e.AssignedToName, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// phoneRE is the phone rule every other record uses (guardians, students).
var phoneRE = regexp.MustCompile(`^[0-9+() -]{7,20}$`)

// Normalise trims, defaults and validates one enquiry on its own, using today's date for the date-of-birth rule; rules
// that need other records are in Check.
func Normalise(in Input) (Input, error) { return normalise(in, time.Now()) }

// NormaliseAt is Normalise with the date of "today" given, so the date-of-birth rule can be tested at its edges.
func NormaliseAt(in Input, today time.Time) (Input, error) { return normalise(in, today) }

func normalise(in Input, today time.Time) (Input, error) {
	var errs crud.FieldErrors
	in.EnquirerName, in.Phone, in.ChildName = strings.TrimSpace(in.EnquirerName), strings.TrimSpace(in.Phone), strings.TrimSpace(in.ChildName)
	in.Source, in.Status = strings.ToLower(strings.TrimSpace(in.Source)), strings.ToLower(strings.TrimSpace(in.Status))
	in.Email, in.DateOfBirth, in.FollowUpDate, in.Notes = crud.TrimPtr(in.Email), crud.TrimPtr(in.DateOfBirth), crud.TrimPtr(in.FollowUpDate), crud.TrimPtr(in.Notes)
	in.ClassID, in.YearID, in.AssignedToID = crud.TrimPtr(in.ClassID), crud.TrimPtr(in.YearID), crud.TrimPtr(in.AssignedToID)
	if in.Status == "" {
		in.Status = "new"
	}
	errs.Required("enquirerName", "the name of the parent or guardian who enquired", in.EnquirerName, 150)
	errs.Required("childName", "the child's name", in.ChildName, 150)
	if in.Phone == "" {
		errs.Add("phone", "required", "Enter a phone number.")
	} else if !phoneRE.MatchString(in.Phone) {
		errs.Add("phone", "invalid", "A phone number has 7 to 20 digits, and may include +, spaces, brackets or hyphens.")
	}
	if in.Email != nil {
		if a, err := mail.ParseAddress(*in.Email); err != nil || a.Address != *in.Email || len(*in.Email) > 254 {
			errs.Add("email", "invalid_email", "Enter a valid email address.")
		}
	}
	if in.DateOfBirth != nil {
		day := today.UTC().Truncate(24 * time.Hour)
		if dob, err := time.Parse("2006-01-02", *in.DateOfBirth); err != nil {
			errs.Add("dateOfBirth", "invalid_date", "Enter the date of birth as YYYY-MM-DD.")
		} else if dob.After(day) {
			errs.Add("dateOfBirth", "in_future", "The date of birth cannot be in the future.")
		} else if dob.Before(day.AddDate(-students.MaxAgeYears, 0, 0)) {
			errs.Add("dateOfBirth", "too_old", "A child cannot be more than 30 years old. Check the year.")
		}
	}
	if in.FollowUpDate != nil {
		if _, err := time.Parse("2006-01-02", *in.FollowUpDate); err != nil {
			errs.Add("followUpDate", "invalid_date", "Enter the follow-up date as YYYY-MM-DD.")
		}
	}
	errs.Optional("notes", "notes", in.Notes, 1000)
	errs.Choice("source", in.Source, Sources...)
	errs.Choice("status", in.Status, Statuses...)
	for _, r := range []struct {
		field string
		v     *string
	}{{"classId", in.ClassID}, {"yearId", in.YearID}, {"assignedToId", in.AssignedToID}} {
		if r.v != nil && !refs.IsUUID(*r.v) {
			errs.Add(r.field, "invalid", "This is not a valid id.")
		}
	}
	return in, errs.OrNil()
}

// keepsAssignee is true when the enquiry already has this assignee, so editing other fields still works after the
// assignee was locked or disabled; only a new assignee must be a colleague.
func keepsAssignee(ctx context.Context, tx pgx.Tx, id, assignee string) bool {
	if id == "" {
		return false
	}
	var current *string
	if err := tx.QueryRow(ctx, `SELECT assigned_to::text FROM enquiries WHERE id = $1::uuid`, id).Scan(&current); err != nil {
		return false
	}
	return current != nil && strings.EqualFold(*current, assignee)
}

var spec = resource.Spec[Enquiry, Input]{
	Table: "enquiries", Action: "enquiries", ResourceType: "enquiry",
	Columns: columns, Scan: scan, ID: func(e Enquiry) string { return e.ID },
	WriteColumns: []string{"enquirer_name", "phone", "email", "child_name", "date_of_birth", "class_id", "year_id", "source", "status", "follow_up_date", "notes", "assigned_to"},
	Args: func(in Input) []any {
		return []any{in.EnquirerName, in.Phone, in.Email, in.ChildName, in.DateOfBirth, in.ClassID, in.YearID, in.Source, in.Status, in.FollowUpDate, in.Notes, in.AssignedToID}
	},
	SearchColumns: []string{"code", "enquirer_name", "child_name", "phone", "email"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, id string, in Input) error {
		var fe crud.FieldErrors
		for _, r := range []struct {
			field, table string
			id           *string
		}{{"classId", "classes", in.ClassID}, {"yearId", "academic_years", in.YearID}} {
			if r.id == nil {
				continue
			}
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, *r.id); err != nil {
				return err
			}
		}
		if in.AssignedToID != nil && !keepsAssignee(ctx, tx, id, *in.AssignedToID) {
			// only a colleague (the people GET /colleagues offers) can be newly assigned
			ok, err := colleagues.Is(ctx, tx, *in.AssignedToID)
			if err != nil {
				return err
			}
			if !ok {
				fe.Add("assignedToId", "not_found", "This person is not an active staff member of this institute.")
			}
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"enquiries_tenant_id_class_id_fkey":    {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"enquiries_tenant_id_year_id_fkey":     {Field: "yearId", Code: "not_found", Message: "This academic year does not exist."},
		"enquiries_tenant_id_assigned_to_fkey": {Field: "assignedToId", Code: "not_found", Message: "This staff member does not exist."},
	},
}

// Service reads and writes enquiries.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Enquiry, Input] {
	return resource.Store[Enquiry, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"status", f.Status}, {"class_id::text", f.ClassID}, {"source", f.Source}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	if f.FollowUpDue != nil {
		if *f.FollowUpDue {
			conds = append(conds, resource.Cond{SQL: dueSQL})
		} else {
			conds = append(conds, resource.Cond{SQL: "NOT " + dueSQL})
		}
	}
	return conds
}

// List returns one page of enquiries.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Enquiry], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

// ListAudited is List for the API: the read is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Enquiry], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "",
		"status": f.Status, "classId": f.ClassID, "source": f.Source, "followUpDue": f.FollowUpDue}, q, filterConds(f)...)
}

// Get returns one enquiry.
func (s Service) Get(ctx context.Context, id string) (Enquiry, error) { return s.store().Get(ctx, id) }

// View is Get for the API: the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Enquiry, error) {
	return s.store().View(ctx, a, id)
}

// Create adds an enquiry.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Enquiry, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces an enquiry's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Enquiry, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes an enquiry. Its number is not given to another enquiry. Its discussions stay stored but are no longer shown.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
