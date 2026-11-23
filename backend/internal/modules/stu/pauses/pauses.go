// Package pauses implements STU-09 Student pauses: a student is away for a while and is expected back (a long illness,
// a family moving for a term).
//
// Pausing is one transaction: it saves the pause and sets the student's status to "paused". Ending or cancelling a
// pause is one transaction too: it closes the pause and sets the student back to "active". A student has at most one
// active pause, and a transferred or alumni student cannot be paused. There is no approval step: a pause takes nothing
// away from the student and ending it undoes it, so the permission to create and edit is the whole control.
//
// What the family owes while the student is away and how many days of school were missed depend on fees and
// attendance, which are not built. Responses carry Figures with "not_tracked" in their place; the one place to fill
// them in is figuresFor (HOOK).
package pauses

import (
	"regexp"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the pause does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Statuses of a pause.
const (
	StatusActive    = "active"
	StatusEnded     = "ended"
	StatusCancelled = "cancelled"
)

// ConflictError means the pause or the student is not in a state that allows this. The API answers 409 with Code as
// the problem code.
type ConflictError struct{ Code, Message string }

func (e ConflictError) Error() string { return "pauses: " + e.Message }

// Reasons are the allowed reasons.
var Reasons = []string{"illness", "family_relocation", "financial", "family_emergency", "other"}

// ReasonLabel is the text shown for a reason code.
func ReasonLabel(code string) string {
	switch code {
	case "illness":
		return "Illness"
	case "family_relocation":
		return "Family relocation"
	case "financial":
		return "Financial reasons"
	case "family_emergency":
		return "Family emergency"
	case "other":
		return "Other"
	}
	return code
}

// Figure is a number that depends on a module that is not built yet. Status is always "not_tracked" for now.
type Figure struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// Figures are the amounts the reference list shows: what was owed when the pause began and the school days missed.
type Figures struct {
	Owed   Figure `json:"owed"`
	Missed Figure `json:"missed"`
}

// figuresFor gives the figures of a pause.
//
// HOOK(FEE, ATT): fees will fill Owed (the dues at paused_from) and attendance will fill Missed (the working days
// absent between paused_from and today or ended_on). Until then both say "not tracked yet". This is the only place to
// change; the API and the screens already carry the field.
func figuresFor(Pause) Figures {
	return Figures{
		Owed:   Figure{Status: "not_tracked", Note: "Fee dues are not tracked yet"},
		Missed: Figure{Status: "not_tracked", Note: "Attendance is not tracked yet"},
	}
}

// StudentRef names the student of a pause.
type StudentRef struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RegistrationCode string `json:"registrationCode"`
}

// Pause is a pause as the API shows it.
type Pause struct {
	ID           string     `json:"id"`
	Student      StudentRef `json:"student"`
	ClassName    *string    `json:"className"`
	SectionName  *string    `json:"sectionName"`
	YearName     *string    `json:"yearName"`
	PausedFrom   string     `json:"pausedFrom"`
	Days         *int       `json:"days"`
	ExpectedBack *string    `json:"expectedBack"`
	Overdue      bool       `json:"overdue"`
	Reason       string     `json:"reason"`
	ReasonNote   *string    `json:"reasonNote"`
	Status       string     `json:"status"`
	EndedOn      *string    `json:"endedOn"`
	CloseNote    *string    `json:"closeNote"`
	Figures      Figures    `json:"figures"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// Input is what a new pause or an edit carries. StudentID and PausedFrom matter only when pausing.
type Input struct {
	StudentID    string
	PausedFrom   *string
	ExpectedBack *string
	Reason       string
	ReasonNote   *string
}

// Filter narrows a list.
type Filter struct{ Status, ClassID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"pause": "created_at", "student": studentName, "from": "paused_from", "expectedBack": "expected_back", "status": "status"}

// DefaultSort lists the latest pauses first.
const DefaultSort = "-from"

const t = "student_pauses"

const studentName = `(SELECT btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')) FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`
const studentCode = `(SELECT s.registration_code FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`

// today is the date in India, computed by the database so a list and its DAYS agree with the clock the database keeps.
const today = `(now() AT TIME ZONE 'Asia/Kolkata')::date`

func enrolSub(col, join string) string {
	return `(SELECT ` + col + ` FROM student_enrollments e ` + join + ` WHERE e.tenant_id = ` + t + `.tenant_id AND e.id = ` + t + `.enrolment_id)`
}

var columns = `id::text, student_id::text, ` + studentName + ` AS student_name, ` + studentCode + ` AS student_code,
	` + enrolSub("c.name", "JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id") + ` AS class_name,
	` + enrolSub("sc.name", "JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id") + ` AS section_name,
	` + enrolSub("y.name", "JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id") + ` AS year_name,
	paused_from::text,
	CASE status WHEN 'active' THEN GREATEST(0, ` + today + ` - paused_from) WHEN 'ended' THEN ended_on - paused_from END AS days,
	expected_back::text, (status = 'active' AND expected_back IS NOT NULL AND expected_back < ` + today + `) AS overdue,
	reason, reason_note, status, ended_on::text, close_note, created_at, updated_at`

func scan(r resource.Scanner) (Pause, error) {
	var x Pause
	err := r.Scan(&x.ID, &x.Student.ID, &x.Student.Name, &x.Student.RegistrationCode, &x.ClassName, &x.SectionName, &x.YearName,
		&x.PausedFrom, &x.Days, &x.ExpectedBack, &x.Overdue, &x.Reason, &x.ReasonNote, &x.Status, &x.EndedOn, &x.CloseNote, &x.CreatedAt, &x.UpdatedAt)
	x.Figures = figuresFor(x)
	return x, err
}

// spec reads pauses; writes are the Service's own, because every change also moves the student's status.
var spec = resource.Spec[Pause, struct{}]{
	Table: t, Action: "student_pauses", ResourceType: "student_pause",
	Columns: columns, Scan: scan, ID: func(x Pause) string { return x.ID },
	SearchColumns: []string{studentName, studentCode},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads and writes pauses.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Pause, struct{}] {
	return resource.Store[Pause, struct{}]{DB: s.DB, Spec: spec}
}

var india = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return l
	}
	return time.FixedZone("IST", 5*3600+1800)
}()

// Today is the date in India, in the form the API uses.
func Today(now time.Time) string { return now.In(india).Format(crud.DateLayout) }

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// parseDate checks a YYYY-MM-DD date; ok=false means the error was added.
func parseDate(fe *crud.FieldErrors, field, label, v string) bool {
	d, err := time.Parse(crud.DateLayout, v)
	switch {
	case err != nil:
		fe.Add(field, "invalid_date", "Enter the "+label+" as YYYY-MM-DD.")
	case d.Year() < 2000 || d.Year() > 2100:
		fe.Add(field, "invalid_date", "Check the year of the "+label+".")
	default:
		return true
	}
	return false
}

// normalise trims and validates what needs no other record; today is passed in so the date rules are testable.
func normalise(in Input, today string, create bool) (Input, error) {
	var fe crud.FieldErrors
	in.StudentID, in.Reason = strings.TrimSpace(in.StudentID), strings.ToLower(strings.TrimSpace(in.Reason))
	in.PausedFrom, in.ExpectedBack, in.ReasonNote = crud.TrimPtr(in.PausedFrom), crud.TrimPtr(in.ExpectedBack), crud.TrimPtr(in.ReasonNote)
	if create {
		switch {
		case in.StudentID == "":
			fe.Add("studentId", "required", "Choose the student.")
		case !uuidRE.MatchString(in.StudentID):
			fe.Add("studentId", "invalid", "Choose a student from the list.")
		}
		if in.PausedFrom == nil {
			in.PausedFrom = &today
		}
		if parseDate(&fe, "pausedFrom", "start date", *in.PausedFrom) && *in.PausedFrom > today {
			fe.Add("pausedFrom", "in_future", "The pause cannot start in the future. Record it when it begins.")
		}
	}
	if in.ExpectedBack != nil && parseDate(&fe, "expectedBack", "expected back date", *in.ExpectedBack) && in.PausedFrom != nil && *in.ExpectedBack < *in.PausedFrom {
		fe.Add("expectedBack", "before_start", "The expected back date cannot be before the pause starts.")
	}
	if in.Reason == "" {
		fe.Add("reason", "required", "Choose a reason.")
	} else {
		fe.Choice("reason", in.Reason, Reasons...)
	}
	fe.Optional("reasonNote", "note", in.ReasonNote, 1000)
	if in.Reason == "other" && in.ReasonNote == nil {
		fe.Add("reasonNote", "required", "Explain the reason.")
	}
	return in, fe.OrNil()
}
