// Package transfers implements STU-08 Student transfers: a student leaves the school.
//
// A transfer is requested (the request and its approval request are saved together), approved or rejected by an
// approver through the approval engine (type "student_transfer", step permission stu.student_transfers.edit, decided
// only by holders whose grant covers the whole institute), and then completed. Completing is one transaction: it gives
// the transfer certificate (TC) number, marks the student "transferred" and closes the student's enrolment. Completing
// again returns the same result and writes nothing.
//
// Fee dues are not tracked yet (FEE is not built). Each blocking rule registers a Gate on Service.Gates; every gate runs
// before anything is written. Responses say so with Dues{Status: "not_tracked"}.
package transfers

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the transfer does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Statuses of a transfer.
const (
	StatusRequested = "requested"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusCompleted = "completed"
)

// TypeKey is the approval workflow type of a transfer; Permission is what an approver needs. The catalog has no
// separate approve code, so approving takes the edit permission, held institute-wide.
const (
	TypeKey    = "student_transfer"
	Permission = "stu.student_transfers.edit"
)

// ConflictError means the transfer is not in a state that allows this, or another record stands in the way. The API
// answers 409 with Code as the problem code.
type ConflictError struct{ Code, Message string }

func (e ConflictError) Error() string { return "transfers: " + e.Message }

// NotEligible means a gate (fee dues, ...) refused the completion. The API answers 409 "not_eligible".
type NotEligible struct{ Problems crud.FieldErrors }

func (e NotEligible) Error() string { return "transfers: not eligible: " + e.Problems.Error() }

// Gate is one rule that can hold a transfer back from completion. FEE (dues cleared) adds its own to Service.Gates. A
// gate reads inside the completion transaction; it returns the reasons the transfer may not be completed, or none.
type Gate interface {
	Name() string
	Check(ctx context.Context, tx pgx.Tx, transferID string) (crud.FieldErrors, error)
}

// DuesGate is the placeholder for fee dues: fees are not built, so it never blocks.
type DuesGate struct{}

// Name implements Gate.
func (DuesGate) Name() string { return "dues" }

// Check implements Gate: nothing is tracked, so nothing blocks.
func (DuesGate) Check(context.Context, pgx.Tx, string) (crud.FieldErrors, error) { return nil, nil }

// DefaultGates are the gates used when Service.Gates is nil.
var DefaultGates = []Gate{DuesGate{}}

// Dues says where fee dues stand. Until fees exist the status is always "not_tracked".
type Dues struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// NotTracked is the dues answer while fees are not built.
var NotTracked = Dues{Status: "not_tracked", Note: "Fee dues are not tracked yet"}

// Reasons are the allowed reasons, with the label shown to people.
var Reasons = []string{"relocation", "another_school", "financial", "health", "other"}

// ReasonLabel is the text shown for a reason code.
func ReasonLabel(code string) string {
	switch code {
	case "relocation":
		return "Relocation"
	case "another_school":
		return "Admission to another school"
	case "financial":
		return "Financial reasons"
	case "health":
		return "Health reasons"
	case "other":
		return "Other"
	}
	return code
}

// StudentRef names the student of a transfer.
type StudentRef struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RegistrationCode string `json:"registrationCode"`
}

// Transfer is a transfer as the API shows it.
type Transfer struct {
	ID                 string     `json:"id"`
	Student            StudentRef `json:"student"`
	ClassName          *string    `json:"className"`
	SectionName        *string    `json:"sectionName"`
	YearName           *string    `json:"yearName"`
	RequestedOn        string     `json:"requestedOn"`
	Reason             string     `json:"reason"`
	ReasonNote         *string    `json:"reasonNote"`
	DestinationSchool  *string    `json:"destinationSchool"`
	LastAttendanceDate *string    `json:"lastAttendanceDate"`
	Status             string     `json:"status"`
	ApprovalRequestID  *string    `json:"approvalRequestId"`
	TCNumber           *string    `json:"tcNumber"`
	TCIssuedOn         *string    `json:"tcIssuedOn"`
	Dues               Dues       `json:"dues"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// Input is what a request or an edit carries. StudentID and RequestedOn matter only when requesting.
type Input struct {
	StudentID          string
	RequestedOn        *string
	Reason             string
	ReasonNote         *string
	DestinationSchool  *string
	LastAttendanceDate *string
}

// Filter narrows a list.
type Filter struct{ Status, ClassID string }

// SortKeys are the sort keys clients may use: "transfer" is the creation order, which is the TC order for completed ones.
var SortKeys = crud.Sorts{"transfer": "created_at", "student": studentName, "requested": "requested_on", "status": "status"}

// DefaultSort lists the newest requests first.
const DefaultSort = "-requested"

const t = "student_transfers"

const studentName = `(SELECT btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')) FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`
const studentCode = `(SELECT s.registration_code FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`

func enrolSub(col, join string) string {
	return `(SELECT ` + col + ` FROM student_enrollments e ` + join + ` WHERE e.tenant_id = ` + t + `.tenant_id AND e.id = ` + t + `.enrolment_id)`
}

var columns = `id::text, student_id::text, ` + studentName + ` AS student_name, ` + studentCode + ` AS student_code,
	` + enrolSub("c.name", "JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id") + ` AS class_name,
	` + enrolSub("sc.name", "JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id") + ` AS section_name,
	` + enrolSub("y.name", "JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id") + ` AS year_name,
	requested_on::text, reason, reason_note, destination_school, last_attendance_date::text, status, approval_request_id::text,
	tc_number, tc_issued_on::text, created_at, updated_at`

func scan(r resource.Scanner) (Transfer, error) {
	var x Transfer
	err := r.Scan(&x.ID, &x.Student.ID, &x.Student.Name, &x.Student.RegistrationCode, &x.ClassName, &x.SectionName, &x.YearName, &x.RequestedOn, &x.Reason,
		&x.ReasonNote, &x.DestinationSchool, &x.LastAttendanceDate, &x.Status, &x.ApprovalRequestID, &x.TCNumber, &x.TCIssuedOn, &x.CreatedAt, &x.UpdatedAt)
	x.Dues = NotTracked
	return x, err
}

// spec reads transfers; writes are the Service's own, because every change is a state change.
var spec = resource.Spec[Transfer, struct{}]{
	Table: t, Action: "student_transfers", ResourceType: "student_transfer",
	Columns: columns, Scan: scan, ID: func(x Transfer) string { return x.ID },
	SearchColumns: []string{studentName, studentCode, "tc_number"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads and writes transfers.
type Service struct {
	DB tenancy.Beginner
	// Approvals files and cancels the approval request of a transfer; its registry must hold the type from Register.
	Approvals approvals.Service
	// Gates run before a completion. nil means DefaultGates (dues, not tracked yet).
	Gates []Gate
}

func (s Service) store() resource.Store[Transfer, struct{}] {
	return resource.Store[Transfer, struct{}]{DB: s.DB, Spec: spec}
}

func (s Service) gates() []Gate {
	if s.Gates == nil {
		return DefaultGates
	}
	return s.Gates
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

// normalise trims and validates what needs no other record; today is passed in so the date rules are testable.
func normalise(in Input, today string, create bool) (Input, error) {
	var fe crud.FieldErrors
	in.StudentID, in.Reason = strings.TrimSpace(in.StudentID), strings.ToLower(strings.TrimSpace(in.Reason))
	in.RequestedOn, in.ReasonNote = crud.TrimPtr(in.RequestedOn), crud.TrimPtr(in.ReasonNote)
	in.DestinationSchool, in.LastAttendanceDate = crud.TrimPtr(in.DestinationSchool), crud.TrimPtr(in.LastAttendanceDate)
	if create {
		switch {
		case in.StudentID == "":
			fe.Add("studentId", "required", "Choose the student.")
		case !uuidRE.MatchString(in.StudentID):
			fe.Add("studentId", "invalid", "Choose a student from the list.")
		}
		if in.RequestedOn == nil {
			in.RequestedOn = &today
		}
		checkDate(&fe, "requestedOn", "request date", *in.RequestedOn, today)
	}
	if in.Reason == "" {
		fe.Add("reason", "required", "Choose a reason.")
	} else {
		fe.Choice("reason", in.Reason, Reasons...)
	}
	fe.Optional("reasonNote", "note", in.ReasonNote, 1000)
	fe.Optional("destinationSchool", "destination school", in.DestinationSchool, 200)
	if in.Reason == "other" && in.ReasonNote == nil {
		fe.Add("reasonNote", "required", "Explain the reason.")
	}
	if in.LastAttendanceDate != nil {
		checkDate(&fe, "lastAttendanceDate", "last attendance date", *in.LastAttendanceDate, today)
	}
	return in, fe.OrNil()
}

// checkDate validates a YYYY-MM-DD date that may not be in the future (today is a YYYY-MM-DD string, so strings compare).
func checkDate(fe *crud.FieldErrors, field, label, v, today string) {
	d, err := time.Parse(crud.DateLayout, v)
	switch {
	case err != nil:
		fe.Add(field, "invalid_date", "Enter the "+label+" as YYYY-MM-DD.")
	case v > today:
		fe.Add(field, "in_future", "The "+label+" cannot be in the future.")
	case d.Year() < 2000:
		fe.Add(field, "invalid_date", "Check the year of the "+label+".")
	}
}
