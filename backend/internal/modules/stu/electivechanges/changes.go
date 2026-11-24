// Package electivechanges implements STU-05 Student elective approvals: a request to swap one chosen elective subject for
// another, decided through the approval engine.
//
// A request is saved together with its approval request (type "student_elective_change"), exactly like a transfer. The
// step permission is stu.student_elective_approvals.edit (the catalog has no separate approve code).
//
// Who may decide: the approval is scoped to the student's class section (class_section, <class_section_mappings.id> of
// the student's enrolment in the year of the choice). An approver whose grant covers the whole institute may decide any
// request; one whose grant is limited to class sections may decide the requests of the students of their own sections
// only; a request of a student who has no class section can be decided by institute-wide approvers only. The person who
// made the request can never decide it (the engine's rule).
//
// Approving runs inside the deciding transaction: the choice is checked again against what the group offers now, the
// subject is swapped, a history row (source change_request) is written and the request is marked approved. If the swap no
// longer fits (the student left the class, the subject is no longer offered, the group's limits changed, the subject was
// already changed by hand) the approval is refused with 409 and nothing is changed; the approver can then reject the
// request. Rejecting and cancelling never touch the choice. Every hook acts only on a request that is still "requested",
// so running one twice changes nothing.
package electivechanges

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/stuscope"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the request does not exist in this institute, or the caller may not see its student.
var ErrNotFound = resource.ErrNotFound

// Statuses of a change request.
const (
	StatusRequested = "requested"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

// TypeKey is the approval workflow type; Permission is what an approver needs.
const (
	TypeKey    = "student_elective_change"
	Permission = "stu.student_elective_approvals.edit"
)

// ConflictError means the request is not in a state that allows this, or another record stands in the way. The API
// answers 409 with Code as the problem code.
type ConflictError struct{ Code, Message string }

func (e ConflictError) Error() string { return "electivechanges: " + e.Message }

// Person is a user shown by name.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Decision is how an approved or rejected request was decided.
type Decision struct {
	Outcome   string    `json:"outcome"`
	DecidedBy *Person   `json:"decidedBy"`
	DecidedAt time.Time `json:"decidedAt"`
	Note      *string   `json:"note"`
}

// Change is a change request as the API shows it.
type Change struct {
	ID                string               `json:"id"`
	Student           electives.StudentRef `json:"student"`
	YearID            string               `json:"yearId"`
	YearName          string               `json:"yearName"`
	ClassName         *string              `json:"className"`
	SectionName       *string              `json:"sectionName"`
	Group             electives.Ref        `json:"group"`
	ChoiceID          string               `json:"choiceId"`
	FromSubject       electives.SubjectRef `json:"fromSubject"`
	ToSubject         electives.SubjectRef `json:"toSubject"`
	WantedFrom        string               `json:"wantedFrom"`
	Reason            string               `json:"reason"`
	Status            string               `json:"status"`
	ApprovalRequestID *string              `json:"approvalRequestId"`
	RequestedBy       *Person              `json:"requestedBy"`
	Decision          *Decision            `json:"decision"`
	CancelledAt       *time.Time           `json:"cancelledAt"`
	CreatedAt         time.Time            `json:"createdAt"`
	UpdatedAt         time.Time            `json:"updatedAt"`
}

// Input is what a request carries.
type Input struct {
	ChoiceID, FromSubjectID, ToSubjectID string
	WantedFrom                           *string // today (or the first day of the year, if that is later) when empty
	Reason                               string
}

// Filter narrows a list.
type Filter struct{ Status, StudentID, ChoiceID, GroupID, YearID, ClassID, SectionID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"student": studentName, "class": className, "wantedFrom": t + ".wanted_from", "status": t + ".status", "requested": t + ".created_at"}

// DefaultSort lists the newest requests first.
const DefaultSort = "-requested"

const t = "student_elective_changes"

const studentName = `(SELECT btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')) FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`
const studentCode = `(SELECT s.registration_code FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`

func enrol(col, join string) string {
	return `(SELECT ` + col + ` FROM student_enrollments e ` + join + ` WHERE e.tenant_id = ` + t + `.tenant_id AND e.student_id = ` + t + `.student_id AND e.year_id = ` + t + `.year_id AND e.deleted_at IS NULL LIMIT 1)`
}

var (
	classID     = enrol("e.class_id", "")
	sectionID   = enrol("e.section_id", "")
	className   = enrol("c.name", "JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id")
	sectionName = enrol("sc.name", "JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id")
)

func subject(col string) string {
	return `(SELECT json_build_object('id', s.id, 'code', s.code, 'name', s.name) FROM subjects s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.` + col + `)`
}

func person(col string) string {
	return `(SELECT json_build_object('id', u.id, 'name', u.display_name) FROM users u WHERE u.tenant_id = ` + t + `.tenant_id AND u.id = ` + t + `.` + col + `)`
}

var columns = `id::text, student_id::text, ` + studentName + `, ` + studentCode + `, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = ` + t + `.tenant_id AND y.id = ` + t + `.year_id),
	` + className + `, ` + sectionName + `,
	group_id::text, (SELECT g.name FROM elective_groups g WHERE g.tenant_id = ` + t + `.tenant_id AND g.id = ` + t + `.group_id),
	choice_id::text, ` + subject("from_subject_id") + `, ` + subject("to_subject_id") + `,
	wanted_from::text, reason, status, approval_request_id::text,
	` + person("created_by") + `, ` + person("decided_by") + `, decided_at,
	(SELECT e.comment FROM approval_events e WHERE e.tenant_id = ` + t + `.tenant_id AND e.request_id = ` + t + `.approval_request_id AND e.action IN ('approved', 'rejected')
	  ORDER BY e.created_at DESC, e.id DESC LIMIT 1),
	cancelled_at, created_at, updated_at`

func scan(r resource.Scanner) (Change, error) {
	var c Change
	var from, to, requestedBy, decidedBy []byte
	var decidedAt *time.Time
	var note *string
	err := r.Scan(&c.ID, &c.Student.ID, &c.Student.Name, &c.Student.RegistrationCode, &c.YearID, &c.YearName, &c.ClassName, &c.SectionName,
		&c.Group.ID, &c.Group.Name, &c.ChoiceID, &from, &to, &c.WantedFrom, &c.Reason, &c.Status, &c.ApprovalRequestID,
		&requestedBy, &decidedBy, &decidedAt, &note, &c.CancelledAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	for _, p := range []struct {
		raw []byte
		to  any
	}{{from, &c.FromSubject}, {to, &c.ToSubject}} {
		if err := json.Unmarshal(p.raw, p.to); err != nil {
			return c, fmt.Errorf("electivechanges: subject of request %s: %w", c.ID, err)
		}
	}
	if requestedBy != nil {
		if err := json.Unmarshal(requestedBy, &c.RequestedBy); err != nil {
			return c, err
		}
	}
	if c.Status == StatusApproved || c.Status == StatusRejected {
		c.Decision = &Decision{Outcome: c.Status, DecidedAt: time.Time{}, Note: note}
		if decidedAt != nil {
			c.Decision.DecidedAt = *decidedAt
		}
		if decidedBy != nil {
			if err := json.Unmarshal(decidedBy, &c.Decision.DecidedBy); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

var spec = resource.Spec[Change, struct{}]{
	Table: t, Action: "student_elective_changes", ResourceType: "student_elective_change",
	Columns: columns, Scan: scan, ID: func(c Change) string { return c.ID },
	SearchColumns: []string{studentName, studentCode},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads and writes change requests.
type Service struct {
	DB tenancy.Beginner
	// Approvals files and cancels the approval request of a change; its registry must hold the type from Register.
	Approvals approvals.Service
}

func (s Service) store() resource.Store[Change, struct{}] {
	return resource.Store[Change, struct{}]{DB: s.DB, Spec: spec}
}

// List returns one page of requests, narrowed to the students the caller may see.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Change], error) {
	var conds []resource.Cond
	add := func(sql, v string) {
		if v != "" {
			conds = append(conds, resource.Cond{SQL: sql, Args: []any{v}})
		}
	}
	add(t+".status = ?", f.Status)
	add(t+".student_id::text = ?", f.StudentID)
	add(t+".choice_id::text = ?", f.ChoiceID)
	add(t+".group_id::text = ?", f.GroupID)
	add(t+".year_id::text = ?", f.YearID)
	add(classID+"::text = ?", f.ClassID)
	add(sectionID+"::text = ?", f.SectionID)
	if c, ok := stuscope.Student(t, "student_id")(ctx); ok {
		conds = append(conds, c)
	}
	return s.store().List(ctx, q, conds...)
}

// visible returns the status and approval request of a live request the caller may see; ErrNotFound otherwise.
func visible(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (status string, approvalID *string, err error) {
	args := []any{id}
	scope, args := electives.Scoped(ctx, t, "student_id", args)
	sql := `SELECT status, approval_request_id::text FROM student_elective_changes WHERE id::text = $1 AND deleted_at IS NULL` + scope
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	err = tx.QueryRow(ctx, sql, args...).Scan(&status, &approvalID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// Get returns one request; a request of a student the caller may not see is not found.
func (s Service) Get(ctx context.Context, id string) (Change, error) {
	var c Change
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, err := visible(ctx, tx, id, false); err != nil {
			return err
		}
		var err error
		c, err = s.store().GetIn(ctx, tx, id)
		return err
	})
	return c, err
}

var uuidField = func(fe *crud.FieldErrors, field, label, v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "":
		fe.Add(field, "required", "Choose "+label+".")
	case !refs.IsUUID(v):
		fe.Add(field, "invalid", "Choose "+label+" from the list.")
	}
	return v
}

func summary(c Change) string {
	v := fmt.Sprintf("Elective change for %s (%s): %s to %s from %s", c.Student.Name, c.Student.RegistrationCode, c.FromSubject.Name, c.ToSubject.Name, c.WantedFrom)
	if utf8.RuneCountInString(v) > 300 {
		v = string([]rune(v)[:299]) + "…"
	}
	return v
}

func openChange() ConflictError {
	return ConflictError{"open_change", "There is already a request waiting to change this subject for this student."}
}

// Create files a change request and its approval request in one transaction: if either fails neither is kept.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Change, error) {
	var fe crud.FieldErrors
	in.ChoiceID = uuidField(&fe, "choiceId", "the elective choice", in.ChoiceID)
	in.FromSubjectID = uuidField(&fe, "fromSubjectId", "the subject to replace", in.FromSubjectID)
	in.ToSubjectID = uuidField(&fe, "toSubjectId", "the new subject", in.ToSubjectID)
	in.Reason = strings.TrimSpace(in.Reason)
	fe.Required("reason", "a reason", in.Reason, 500)
	in.WantedFrom = crud.TrimPtr(in.WantedFrom)
	if in.WantedFrom != nil {
		if _, err := time.Parse(crud.DateLayout, *in.WantedFrom); err != nil {
			fe.Add("wantedFrom", "invalid_date", "Enter the date as YYYY-MM-DD.")
		}
	}
	if err := fe.OrNil(); err != nil {
		return Change{}, err
	}
	var out Change
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ch, err := electives.LockChoice(ctx, tx, in.ChoiceID, true)
		if errors.Is(err, electives.ErrNotFound) {
			return crud.FieldErrors{{Field: "choiceId", Code: "not_found", Message: "This elective choice was not found."}}
		}
		if err != nil {
			return err
		}
		plan, problems, err := electives.PlanSwap(ctx, tx, ch, in.FromSubjectID, in.ToSubjectID)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return problems
		}
		wanted, err := wantedFrom(ctx, tx, ch.YearID, in.WantedFrom)
		if err != nil {
			return err
		}
		tn, ok := tenancy.FromContext(ctx)
		if !ok {
			return tenancy.ErrNoTenant
		}
		var id string
		err = tx.QueryRow(ctx, `INSERT INTO student_elective_changes (tenant_id, student_id, choice_id, year_id, group_id, from_subject_id, to_subject_id, wanted_from, reason, created_by, updated_by)
			VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8::date, $9, $10::uuid, $10::uuid) RETURNING id::text`,
			tn.ID, ch.StudentID, ch.ID, ch.YearID, ch.GroupID, plan.FromID, plan.ToID, wanted, in.Reason, a.UserID).Scan(&id)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.ConstraintName == "student_elective_changes_open_uq" {
			return openChange()
		}
		if err != nil {
			return err
		}
		c, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		req, err := s.Approvals.Submit(ctx, tx, a, approvals.SubmitInput{TypeKey: TypeKey, SubjectID: id, Summary: summary(c)})
		var ce approvals.ConflictError
		if errors.As(err, &ce) {
			return openChange()
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE student_elective_changes SET approval_request_id = $2::uuid WHERE id::text = $1`, id, req.ID); err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_elective_changes.create", ResourceType: "student_elective_change", ResourceID: id, After: out})
	})
	return out, err
}

// wantedFrom validates the date the change should start and defaults it. It must fall inside the academic year of the
// choice; when it is not given it is today, or the first day of the year if the year has not started.
func wantedFrom(ctx context.Context, tx pgx.Tx, yearID string, given *string) (string, error) {
	var start, end string
	if err := tx.QueryRow(ctx, `SELECT start_date::text, end_date::text FROM academic_years WHERE id::text = $1`, yearID).Scan(&start, &end); err != nil {
		return "", err
	}
	day := electives.Today(time.Now())
	if given != nil {
		day = *given
	} else {
		if day < start {
			day = start
		}
		if day > end {
			day = end
		}
	}
	switch {
	case day < start:
		return "", crud.FieldErrors{{Field: "wantedFrom", Code: "before_year", Message: "The change cannot start before the academic year begins (" + start + ")."}}
	case day > end:
		return "", crud.FieldErrors{{Field: "wantedFrom", Code: "after_year", Message: "The change cannot start after the academic year ends (" + end + ")."}}
	}
	return day, nil
}

// Cancel withdraws a request that is still waiting. Only the person who made it may (the approval engine refuses anyone
// else with a 403); a request that was already decided or cancelled answers 409 not_cancellable.
func (s Service) Cancel(ctx context.Context, a audit.Actor, access authz.Access, id, comment string) (Change, error) {
	var status string
	var reqID *string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		status, reqID, err = visible(ctx, tx, id, false)
		return err
	})
	if err != nil {
		return Change{}, err
	}
	if status != StatusRequested || reqID == nil {
		return Change{}, ConflictError{"not_cancellable", "A request that is " + status + " cannot be cancelled."}
	}
	if _, err := s.Approvals.Cancel(ctx, a, access, *reqID, comment); err != nil {
		return Change{}, err
	}
	return s.Get(ctx, id)
}
