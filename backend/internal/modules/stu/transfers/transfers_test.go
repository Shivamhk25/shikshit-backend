package transfers_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

func ptr[T any](v T) *T { return &v }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// person is a user of institute alpha with the access the approval engine and the API would resolve for them.
type person struct {
	actor  audit.Actor
	access authz.Access
}

type env struct {
	f                       modtest.Fixture
	svc                     transfers.Service
	approvals               approvals.Service
	requester, approver     person
	scoped, viewOnly        person
	year, class, sec        string
	otherStudent, otherCode string
}

func today() string { return transfers.Today(time.Now()) }

func (e env) user(t *testing.T, name string, grants map[string]string, assignments ...string) person {
	t.Helper()
	var uid, rid string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, $2::text, $2::text, 'active') RETURNING id::text`, e.f.Alpha.ID, name).Scan(&uid))
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, e.f.Alpha.ID, "r_"+name).Scan(&rid))
	for code, scope := range grants {
		_, err := e.f.Admin.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, e.f.Alpha.ID, rid, code, scope)
		must(t, err)
	}
	_, err := e.f.Admin.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, e.f.Alpha.ID, uid, rid)
	must(t, err)
	for _, ref := range assignments {
		_, err := e.f.Admin.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', $3)`, e.f.Alpha.ID, uid, ref)
		must(t, err)
	}
	acc, err := authz.Service{DB: e.f.App}.Resolve(modtest.In(e.f.Alpha), uid)
	must(t, err)
	return person{actor: audit.Actor{UserID: uid, Source: "web", RequestID: "req-" + name}, access: acc}
}

func setup(t *testing.T) env {
	t.Helper()
	f := modtest.New(t)
	e := env{f: f}
	_, err := f.Admin.Exec(bg, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'stu'), ($1, 'wfl')`, f.Alpha.ID)
	must(t, err)
	reg := approvals.NewRegistry()
	must(t, transfers.Register(reg))
	e.approvals = approvals.Service{DB: f.App, Reg: reg, Access: authz.Service{DB: f.App}}
	e.svc = transfers.Service{DB: f.App, Approvals: e.approvals}

	now := time.Now().UTC()
	must(t, f.Admin.QueryRow(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', $2, $3, true) RETURNING id::text`,
		f.Alpha.ID, now.AddDate(0, -2, 0), now.AddDate(0, 10, 0)).Scan(&e.year))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'V', 'Class 5') RETURNING id::text`, f.Alpha.ID).Scan(&e.class))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Alpha.ID).Scan(&e.sec))

	all := map[string]string{"stu.student_transfers.view": "tenant", "stu.student_transfers.create": "tenant", "stu.student_transfers.edit": "tenant", "stu.student_transfers.delete": "tenant"}
	e.requester = person{actor: f.Actor}
	// the fixture's actor "asha" is the requester; give them the same grants
	var rid string
	must(t, f.Admin.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'r_asha', 'r_asha') RETURNING id::text`, f.Alpha.ID).Scan(&rid))
	for code, scope := range all {
		_, err := f.Admin.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, f.Alpha.ID, rid, code, scope)
		must(t, err)
	}
	_, err = f.Admin.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, f.Alpha.ID, f.Actor.UserID, rid)
	must(t, err)
	e.requester.access, err = authz.Service{DB: f.App}.Resolve(modtest.In(f.Alpha), f.Actor.UserID)
	must(t, err)
	e.approver = e.user(t, "bala", map[string]string{"stu.student_transfers.edit": "tenant", "stu.student_transfers.view": "tenant"})
	e.scoped = e.user(t, "chitra", map[string]string{"stu.student_transfers.edit": "class_section"}, "cs-1")
	e.viewOnly = e.user(t, "dev", map[string]string{"stu.student_transfers.view": "tenant"})
	return e
}

// student adds a student of alpha with an enrolment in the current class (unless noEnrol).
func (e env) student(t *testing.T, code string, noEnrol bool) string {
	t.Helper()
	var id string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth, admission_date)
		VALUES ($1, $2::text, 'Asha', 'Rao ' || $2::text, 'female', DATE '2015-04-01', DATE '2020-04-01') RETURNING id::text`, e.f.Alpha.ID, code).Scan(&id))
	if !noEnrol {
		_, err := e.f.Admin.Exec(bg, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`,
			e.f.Alpha.ID, id, e.year, e.class, e.sec, code)
		must(t, err)
	}
	return id
}

func (e env) in() context.Context { return modtest.In(e.f.Alpha) }

func (e env) request(t *testing.T, student string) transfers.Transfer {
	t.Helper()
	x, err := e.svc.Create(e.in(), e.requester.actor, transfers.Input{StudentID: student, Reason: "relocation", DestinationSchool: ptr("Delhi Public School")})
	must(t, err)
	return x
}

func (e env) decide(t *testing.T, x transfers.Transfer, who person, approve bool) {
	t.Helper()
	_, err := e.approvals.Decide(e.in(), who.actor, who.access, *x.ApprovalRequestID, approvals.DecideInput{Approve: approve, Comment: "reason given"})
	must(t, err)
}

// approved returns a transfer that has been requested and approved.
func (e env) approved(t *testing.T, student string) transfers.Transfer {
	t.Helper()
	x := e.request(t, student)
	e.decide(t, x, e.approver, true)
	return e.get(t, x.ID)
}

func (e env) get(t *testing.T, id string) transfers.Transfer {
	t.Helper()
	x, err := e.svc.View(e.in(), e.requester.actor, id)
	must(t, err)
	return x
}

func (e env) scalar(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var s *string
	must(t, e.f.Admin.QueryRow(bg, `SELECT (`+sql+`)::text`, args...).Scan(&s))
	if s == nil {
		return "<null>"
	}
	return *s
}

func (e env) counter(t *testing.T) string {
	return e.scalar(t, `SELECT COALESCE((SELECT last_value FROM transfer_certificate_counters WHERE tenant_id = $1), 0)`, e.f.Alpha.ID)
}

func conflictCode(t *testing.T, name string, err error, code string) {
	t.Helper()
	var ce transfers.ConflictError
	if !errors.As(err, &ce) || ce.Code != code {
		t.Errorf("%s: want conflict %q, got %v", name, code, err)
	}
}

func fieldErr(t *testing.T, name string, err error, field, code string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Errorf("%s: want field errors, got %v", name, err)
		return
	}
	for _, f := range fe {
		if f.Field == field && (code == "" || f.Code == code) {
			return
		}
	}
	t.Errorf("%s: no %s error on %q: %v", name, code, field, fe)
}

func TestARequestedTransferIsApprovedCompletedAndCertified(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", false)
	x := e.request(t, st)
	if x.Status != "requested" || x.ApprovalRequestID == nil || x.Student.RegistrationCode != "S-1" || x.Student.Name != "Asha Rao S-1" ||
		x.ClassName == nil || *x.ClassName != "Class 5" || x.SectionName == nil || *x.SectionName != "Section A" || x.RequestedOn != today() ||
		x.TCNumber != nil || x.Dues.Status != "not_tracked" || x.Dues.Note == "" {
		t.Fatalf("requested = %+v", x)
	}
	// the approval request exists, names the requester and the right text, and is scoped to this transfer
	if got := e.scalar(t, `SELECT summary || '|' || scope_type || '|' || scope_ref || '|' || status || '|' || type_key FROM approval_requests WHERE id = $1::uuid`, *x.ApprovalRequestID); got !=
		"Transfer of Asha Rao S-1 (S-1) — Relocation|student_transfer|"+x.ID+"|pending|student_transfer" {
		t.Fatalf("approval request = %q", got)
	}
	// nothing else changed yet
	if got := e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, st); got != "active" {
		t.Fatalf("student = %s", got)
	}
	_, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	conflictCode(t, "complete before approval", err, "not_approved")

	e.decide(t, x, e.approver, true)
	ap := e.get(t, x.ID)
	if ap.Status != "approved" || ap.TCNumber != nil {
		t.Fatalf("approved = %+v", ap)
	}
	if got := e.scalar(t, `SELECT (approved_at IS NOT NULL AND approved_by = $2::uuid)::text FROM student_transfers WHERE id = $1::uuid`, x.ID, e.approver.actor.UserID); got != "true" {
		t.Fatalf("approved_by not recorded: %s", got)
	}

	res, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	must(t, err)
	c := res.Transfer
	if res.AlreadyCompleted || c.Status != "completed" || c.TCNumber == nil || *c.TCNumber != "TC-000001" || c.TCIssuedOn == nil || *c.TCIssuedOn != today() {
		t.Fatalf("completed = %+v", res)
	}
	if got := e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, st); got != "transferred" {
		t.Errorf("student status = %s", got)
	}
	if got := e.scalar(t, `SELECT status FROM student_enrollments WHERE student_id = $1::uuid`, st); got != "withdrawn" {
		t.Errorf("enrolment status = %s", got)
	}
	for action, want := range map[string]int{"student_transfers.create": 1, "student_transfers.approve": 1, "student_transfers.complete": 1, "students.update": 1, "student_enrollments.update": 1, "approval.submit": 1, "approval.approve": 1} {
		if n := e.f.AuditCount(t, action); n != want {
			t.Errorf("audit %s = %d, want %d", action, n, want)
		}
	}

	// the certificate
	cert, err := e.svc.Certificate(e.in(), e.requester.actor, x.ID)
	must(t, err)
	if cert.TCNumber != "TC-000001" || cert.SchoolName != "alpha" || cert.Student.FullName != "Asha Rao S-1" || cert.Student.DateOfBirth != "2015-04-01" || cert.Student.AdmissionDate == nil ||
		*cert.Student.AdmissionDate != "2020-04-01" || cert.Student.Gender != "female" || cert.ClassName == nil || *cert.ClassName != "Class 5" || cert.YearName == nil || *cert.YearName != "2026-27" ||
		cert.ReasonLabel != "Relocation" || cert.DestinationSchool == nil || cert.Guardians == nil {
		t.Fatalf("certificate = %+v", cert)
	}
	if n := e.f.AuditCount(t, "student_transfers.certificate"); n != 1 {
		t.Errorf("certificate reads audited %d times", n)
	}

	// a second completion returns the same result and writes nothing
	before := e.f.AuditCount(t, "student_transfers.complete") + e.f.AuditCount(t, "students.update") + e.f.AuditCount(t, "student_enrollments.update")
	again, err := e.svc.Complete(e.in(), e.approver.actor, x.ID, ptr("2020-01-01"))
	must(t, err)
	if !again.AlreadyCompleted || *again.Transfer.TCNumber != "TC-000001" || *again.Transfer.TCIssuedOn != today() {
		t.Fatalf("second completion = %+v", again)
	}
	if after := e.f.AuditCount(t, "student_transfers.complete") + e.f.AuditCount(t, "students.update") + e.f.AuditCount(t, "student_enrollments.update"); after != before {
		t.Errorf("a second completion wrote audit rows: %d -> %d", before, after)
	}
	if got := e.counter(t); got != "1" {
		t.Errorf("counter = %s", got)
	}
	// a completed transfer is final
	_, err = e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "health"})
	conflictCode(t, "edit completed", err, "not_editable")
	_, err = e.svc.Cancel(e.in(), e.requester.actor, e.requester.access, x.ID, "")
	conflictCode(t, "cancel completed", err, "not_cancellable")
	conflictCode(t, "delete completed", e.svc.Delete(e.in(), e.requester.actor, x.ID), "not_deletable")
	// and the student cannot be transferred again
	_, err = e.svc.Create(e.in(), e.requester.actor, transfers.Input{StudentID: st, Reason: "health"})
	fieldErr(t, "transferred student", err, "studentId", "not_transferable")
}

func TestARejectedAndCancelledTransferEndsWithoutSideEffects(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", false)

	x := e.request(t, st)
	// a rejection needs the approver's reason; the student stays as they were
	_, err := e.approvals.Decide(e.in(), e.approver.actor, e.approver.access, *x.ApprovalRequestID, approvals.DecideInput{Approve: false})
	fieldErr(t, "reject without comment", err, "comment", "")
	e.decide(t, x, e.approver, false)
	r := e.get(t, x.ID)
	if r.Status != "rejected" || e.scalar(t, `SELECT (rejected_at IS NOT NULL)::text FROM student_transfers WHERE id = $1::uuid`, x.ID) != "true" {
		t.Fatalf("rejected = %+v", r)
	}
	_, err = e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	conflictCode(t, "complete rejected", err, "not_approved")
	_, err = e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "health"})
	conflictCode(t, "edit rejected", err, "not_editable")
	_, err = e.svc.Cancel(e.in(), e.requester.actor, e.requester.access, x.ID, "")
	conflictCode(t, "cancel rejected", err, "not_cancellable")
	if got := e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, st); got != "active" {
		t.Fatalf("student = %s", got)
	}
	// the rejected one frees the student for a new request, and can be deleted
	y := e.request(t, st)

	// a requested transfer is cancelled through its approval request: only the requester may
	_, err = e.svc.Cancel(e.in(), e.approver.actor, e.approver.access, y.ID, "")
	var fo approvals.ForbiddenError
	if !errors.As(err, &fo) {
		t.Fatalf("cancel by someone else = %v", err)
	}
	if e.get(t, y.ID).Status != "requested" {
		t.Fatal("a refused cancel changed the transfer")
	}
	c, err := e.svc.Cancel(e.in(), e.requester.actor, e.requester.access, y.ID, "changed our minds")
	must(t, err)
	if c.Status != "cancelled" || e.scalar(t, `SELECT status FROM approval_requests WHERE id = $1::uuid`, *y.ApprovalRequestID) != "cancelled" {
		t.Fatalf("cancelled = %+v", c)
	}
	_, err = e.svc.Cancel(e.in(), e.requester.actor, e.requester.access, y.ID, "")
	conflictCode(t, "cancel twice", err, "not_cancellable")

	// an approved transfer is cancelled directly, by anyone allowed to edit
	z := e.approved(t, st)
	c, err = e.svc.Cancel(e.in(), e.approver.actor, e.approver.access, z.ID, "parents withdrew")
	must(t, err)
	if c.Status != "cancelled" || e.scalar(t, `SELECT (cancelled_at IS NOT NULL AND cancelled_by = $2::uuid)::text FROM student_transfers WHERE id = $1::uuid`, z.ID, e.approver.actor.UserID) != "true" {
		t.Fatalf("cancelled approved = %+v", c)
	}
	if n := e.f.AuditCount(t, "student_transfers.cancel"); n != 2 {
		t.Errorf("cancellations audited %d times", n)
	}

	// only cancelled or rejected ones can be deleted; they leave the lists but the rows stay
	for _, id := range []string{x.ID, y.ID, z.ID} {
		must(t, e.svc.Delete(e.in(), e.requester.actor, id))
		if _, err := e.svc.View(e.in(), e.requester.actor, id); !errors.Is(err, resource.ErrNotFound) {
			t.Errorf("deleted transfer still visible: %v", err)
		}
	}
	if got := e.scalar(t, `SELECT count(*) FROM student_transfers WHERE deleted_at IS NOT NULL AND deleted_by IS NOT NULL`); got != "3" {
		t.Errorf("soft-deleted rows = %s", got)
	}
	if err := e.svc.Delete(e.in(), e.requester.actor, x.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
	open := e.request(t, st)
	conflictCode(t, "delete requested", e.svc.Delete(e.in(), e.requester.actor, open.ID), "not_deletable")
	e.decide(t, open, e.approver, true)
	conflictCode(t, "delete approved", e.svc.Delete(e.in(), e.requester.actor, open.ID), "not_deletable")
}

func TestOnlyAnInstituteWideApproverWhoIsNotTheRequesterMayDecide(t *testing.T) {
	e := setup(t)
	x := e.request(t, e.student(t, "S-1", false))
	id := *x.ApprovalRequestID

	_, err := e.approvals.Decide(e.in(), e.requester.actor, e.requester.access, id, approvals.DecideInput{Approve: true})
	var fo approvals.ForbiddenError
	if !errors.As(err, &fo) {
		t.Errorf("requester deciding their own request = %v", err)
	}
	// a holder of the permission for a class section only never matches the transfer
	if _, err := e.approvals.Decide(e.in(), e.scoped.actor, e.scoped.access, id, approvals.DecideInput{Approve: true}); err == nil {
		t.Error("a class-section holder decided a transfer")
	}
	if _, err := e.approvals.Decide(e.in(), e.viewOnly.actor, e.viewOnly.access, id, approvals.DecideInput{Approve: true}); err == nil {
		t.Error("a view-only user decided a transfer")
	}
	if e.get(t, x.ID).Status != "requested" {
		t.Fatal("a refused decision changed the transfer")
	}
	// approvers are told, the class-section holder and the requester are not
	notified := func(p person) string {
		return e.scalar(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, p.actor.UserID)
	}
	if notified(e.approver) != "1" || notified(e.scoped) != "0" || notified(e.requester) != "0" {
		t.Errorf("notified approver=%s scoped=%s requester=%s", notified(e.approver), notified(e.scoped), notified(e.requester))
	}

	e.decide(t, x, e.approver, true)
	// the second decision on the same request is refused and changes nothing
	other := e.user(t, "second", map[string]string{"stu.student_transfers.edit": "tenant"})
	_, err = e.approvals.Decide(e.in(), other.actor, other.access, id, approvals.DecideInput{Approve: false, Comment: "no"})
	var ce approvals.ConflictError
	if !errors.As(err, &ce) {
		t.Errorf("second decision = %v", err)
	}
	if e.get(t, x.ID).Status != "approved" {
		t.Error("the second decision changed the transfer")
	}
}

func TestTheHooksOnlyActOnARequestedTransfer(t *testing.T) {
	e := setup(t)
	x := e.request(t, e.student(t, "S-1", false))
	// the transfer was changed behind the approval's back: the approval hook must not resurrect it
	_, err := e.f.Admin.Exec(bg, `UPDATE student_transfers SET status = 'cancelled', cancelled_at = now() WHERE id = $1::uuid`, x.ID)
	must(t, err)
	e.decide(t, x, e.approver, true)
	if got := e.get(t, x.ID).Status; got != "cancelled" {
		t.Errorf("status = %s", got)
	}
	if n := e.f.AuditCount(t, "student_transfers.approve"); n != 0 {
		t.Errorf("hook audited %d approvals", n)
	}
}

func TestCreateValidatesTheStudentTheDatesAndTheReason(t *testing.T) {
	e := setup(t)
	good := e.student(t, "S-1", false)
	paused := e.student(t, "S-2", false)
	gone := e.student(t, "S-3", false)
	alumni := e.student(t, "S-4", false)
	_, err := e.f.Admin.Exec(bg, `UPDATE students SET status = 'paused' WHERE id = $1::uuid`, paused)
	must(t, err)
	_, err = e.f.Admin.Exec(bg, `UPDATE students SET status = 'transferred' WHERE id = $1::uuid`, gone)
	must(t, err)
	_, err = e.f.Admin.Exec(bg, `UPDATE students SET status = 'alumni' WHERE id = $1::uuid`, alumni)
	must(t, err)
	var foreign string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth) VALUES ($1, 'B-1', 'Bo', 'male', DATE '2015-01-01') RETURNING id::text`, e.f.Bravo.ID).Scan(&foreign))
	tomorrow := time.Now().AddDate(0, 0, 3).Format("2006-01-02")

	for name, c := range map[string]struct {
		in          transfers.Input
		field, code string
	}{
		"no student":                  {transfers.Input{Reason: "health"}, "studentId", "required"},
		"not an id":                   {transfers.Input{StudentID: "x", Reason: "health"}, "studentId", "invalid"},
		"unknown student":             {transfers.Input{StudentID: "00000000-0000-0000-0000-000000000001", Reason: "health"}, "studentId", "not_found"},
		"another institute":           {transfers.Input{StudentID: foreign, Reason: "health"}, "studentId", "not_found"},
		"already transferred":         {transfers.Input{StudentID: gone, Reason: "health"}, "studentId", "not_transferable"},
		"alumnus":                     {transfers.Input{StudentID: alumni, Reason: "health"}, "studentId", "not_transferable"},
		"no reason":                   {transfers.Input{StudentID: good}, "reason", "required"},
		"bad reason":                  {transfers.Input{StudentID: good, Reason: "boredom"}, "reason", ""},
		"other without a note":        {transfers.Input{StudentID: good, Reason: "other", ReasonNote: ptr("  ")}, "reasonNote", "required"},
		"note too long":               {transfers.Input{StudentID: good, Reason: "health", ReasonNote: ptr(strings.Repeat("x", 1001))}, "reasonNote", ""},
		"destination too long":        {transfers.Input{StudentID: good, Reason: "health", DestinationSchool: ptr(strings.Repeat("x", 201))}, "destinationSchool", ""},
		"request in the future":       {transfers.Input{StudentID: good, Reason: "health", RequestedOn: &tomorrow}, "requestedOn", "in_future"},
		"bad request date":            {transfers.Input{StudentID: good, Reason: "health", RequestedOn: ptr("1/2/2026")}, "requestedOn", "invalid_date"},
		"attendance in future":        {transfers.Input{StudentID: good, Reason: "health", LastAttendanceDate: &tomorrow}, "lastAttendanceDate", "in_future"},
		"attendance before admission": {transfers.Input{StudentID: good, Reason: "health", LastAttendanceDate: ptr("2019-12-31")}, "lastAttendanceDate", "before_admission"},
	} {
		_, err := e.svc.Create(e.in(), e.requester.actor, c.in)
		fieldErr(t, name, err, c.field, c.code)
	}
	if n := e.scalar(t, `SELECT (SELECT count(*) FROM student_transfers) + (SELECT count(*) FROM approval_requests)`); n != "0" {
		t.Fatalf("a refused request left %s rows", n)
	}

	// a paused student, and a student who has no class yet, can leave; the date defaults to today and an old one is kept
	x, err := e.svc.Create(e.in(), e.requester.actor, transfers.Input{StudentID: paused, Reason: "other", ReasonNote: ptr(" moving abroad "), RequestedOn: ptr("2026-01-05"), LastAttendanceDate: ptr("2026-01-03")})
	must(t, err)
	if x.RequestedOn != "2026-01-05" || *x.ReasonNote != "moving abroad" || *x.LastAttendanceDate != "2026-01-03" {
		t.Fatalf("transfer = %+v", x)
	}
	loose := e.student(t, "S-5", true)
	y := e.request(t, loose)
	if y.ClassName != nil || y.SectionName != nil || y.YearName != nil {
		t.Fatalf("a student with no enrolment shows a class: %+v", y)
	}

	// one open transfer per student, however it is asked
	_, err = e.svc.Create(e.in(), e.requester.actor, transfers.Input{StudentID: loose, Reason: "health"})
	conflictCode(t, "second open transfer", err, "open_transfer")
	e.decide(t, y, e.approver, true)
	_, err = e.svc.Create(e.in(), e.requester.actor, transfers.Input{StudentID: loose, Reason: "health"})
	conflictCode(t, "open transfer after approval", err, "open_transfer")
	_, err = e.f.Admin.Exec(bg, `INSERT INTO student_transfers (tenant_id, student_id, requested_on, reason) VALUES ($1, $2::uuid, CURRENT_DATE, 'health')`, e.f.Alpha.ID, loose)
	if err == nil || !strings.Contains(err.Error(), "student_transfers_open_uq") {
		t.Errorf("the database allowed a second open transfer: %v", err)
	}
}

func TestEditingAnOpenTransfer(t *testing.T) {
	e := setup(t)
	x := e.request(t, e.student(t, "S-1", false))
	up, err := e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "health", ReasonNote: ptr(" chronic illness "), DestinationSchool: ptr(" "), LastAttendanceDate: ptr("2026-02-01"),
		StudentID: "00000000-0000-0000-0000-000000000009", RequestedOn: ptr("2020-01-01")})
	must(t, err)
	if up.Reason != "health" || *up.ReasonNote != "chronic illness" || up.DestinationSchool != nil || *up.LastAttendanceDate != "2026-02-01" || up.Student.ID != x.Student.ID || up.RequestedOn != x.RequestedOn {
		t.Fatalf("updated = %+v", up)
	}
	// the approver reads the new reason
	if got := e.scalar(t, `SELECT summary FROM approval_requests WHERE id = $1::uuid`, *x.ApprovalRequestID); !strings.HasSuffix(got, "Health reasons") {
		t.Errorf("summary = %q", got)
	}
	_, err = e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "other"})
	fieldErr(t, "other needs a note", err, "reasonNote", "required")
	_, err = e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "health", LastAttendanceDate: ptr("2019-01-01")})
	fieldErr(t, "before admission", err, "lastAttendanceDate", "before_admission")
	if n := e.f.AuditCount(t, "student_transfers.update"); n != 1 {
		t.Errorf("updates audited %d times", n)
	}
	var before map[string]any
	must(t, e.f.Admin.QueryRow(bg, `SELECT before FROM audit_log WHERE action = 'student_transfers.update'`).Scan(&before))
	if before["reason"] != "relocation" {
		t.Errorf("audit before = %v", before)
	}

	e.decide(t, x, e.approver, true)
	if _, err := e.svc.Update(e.in(), e.requester.actor, x.ID, transfers.Input{Reason: "financial"}); err != nil {
		t.Errorf("an approved transfer can still be edited: %v", err)
	}
	if _, err := e.svc.Update(e.in(), e.requester.actor, "00000000-0000-0000-0000-000000000001", transfers.Input{Reason: "financial"}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("unknown id = %v", err)
	}
}

func TestCompletionIsIdempotentUnderConcurrency(t *testing.T) {
	e := setup(t)
	x := e.approved(t, e.student(t, "S-1", false))
	var wg sync.WaitGroup
	results := make([]transfers.CompleteResult, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
		}(i)
	}
	wg.Wait()
	first := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("completion %d: %v", i, errs[i])
		}
		if *r.Transfer.TCNumber != "TC-000001" {
			t.Errorf("completion %d got %s", i, *r.Transfer.TCNumber)
		}
		if !r.AlreadyCompleted {
			first++
		}
	}
	if first != 1 {
		t.Errorf("%d completions did the work, want 1", first)
	}
	if got := e.counter(t); got != "1" {
		t.Errorf("counter = %s", got)
	}
	for action, want := range map[string]int{"student_transfers.complete": 1, "students.update": 1, "student_enrollments.update": 1} {
		if n := e.f.AuditCount(t, action); n != want {
			t.Errorf("audit %s = %d", action, n)
		}
	}
}

func TestTCNumbersAreSequentialUniqueAndPerInstitute(t *testing.T) {
	e := setup(t)
	const n = 6
	ids := make([]string, n)
	for i := range ids {
		ids[i] = e.approved(t, e.student(t, fmt.Sprintf("S-%d", i), false)).ID
	}
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := e.svc.Complete(e.in(), e.requester.actor, ids[i], nil)
			errs[i] = err
			if err == nil {
				got[i] = *r.Transfer.TCNumber
			}
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range got {
		must(t, errs[i])
		seen[got[i]] = true
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprintf("TC-%06d", i)] {
			t.Errorf("TC-%06d was not given: %v", i, got)
		}
	}
	if len(seen) != n {
		t.Errorf("numbers = %v", got)
	}
	// another institute counts from its own start
	if b := e.scalar(t, `SELECT next_tc_number($1)`, e.f.Bravo.ID); b != "TC-000001" {
		t.Errorf("bravo's first = %s", b)
	}
	// a number already used, even by a hand-made row, is skipped
	_, err := e.f.Admin.Exec(bg, `UPDATE transfer_certificate_counters SET last_value = 6 WHERE tenant_id = $1`, e.f.Alpha.ID)
	must(t, err)
	if next := e.scalar(t, `SELECT next_tc_number($1)`, e.f.Alpha.ID); next != "TC-000007" {
		t.Errorf("next = %s", next)
	}
	_, err = e.f.Admin.Exec(bg, `UPDATE transfer_certificate_counters SET last_value = 5 WHERE tenant_id = $1`, e.f.Alpha.ID)
	must(t, err)
	if next := e.scalar(t, `SELECT next_tc_number($1)`, e.f.Alpha.ID); next != "TC-000007" {
		t.Errorf("a used number was given again: %s", next)
	}
}

func TestACompletionThatFailsKeepsNothing(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", false)
	x := e.approved(t, st)
	// make the last-but-one step fail after the TC number has been allocated
	_, err := e.f.Admin.Exec(bg, `CREATE FUNCTION fail_transfer() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
		CREATE TRIGGER fail_transfer BEFORE UPDATE ON students FOR EACH ROW WHEN (NEW.status = 'transferred') EXECUTE FUNCTION fail_transfer()`)
	must(t, err)
	if _, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("completion = %v", err)
	}
	got := e.get(t, x.ID)
	if got.Status != "approved" || got.TCNumber != nil || got.TCIssuedOn != nil {
		t.Errorf("transfer = %+v", got)
	}
	if e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, st) != "active" || e.scalar(t, `SELECT status FROM student_enrollments WHERE student_id = $1::uuid`, st) != "enrolled" {
		t.Error("the student or the enrolment changed")
	}
	if c := e.counter(t); c != "0" {
		t.Errorf("counter moved to %s", c)
	}
	if n := e.f.AuditCount(t, "student_transfers.complete") + e.f.AuditCount(t, "students.update"); n != 0 {
		t.Errorf("%d audit rows kept", n)
	}
	// once the fault is gone the same number is used: no gap
	_, err = e.f.Admin.Exec(bg, `DROP TRIGGER fail_transfer ON students`)
	must(t, err)
	r, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	must(t, err)
	if *r.Transfer.TCNumber != "TC-000001" {
		t.Errorf("tc = %s", *r.Transfer.TCNumber)
	}
}

type blockingGate struct{}

func (blockingGate) Name() string { return "test" }
func (blockingGate) Check(context.Context, pgx.Tx, string) (crud.FieldErrors, error) {
	return crud.FieldErrors{{Field: "dues", Code: "unpaid", Message: "Fees are unpaid."}}, nil
}

func TestABlockingGateStopsCompletionAndWritesNothing(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", false)
	x := e.approved(t, st)
	blocked := e.svc
	blocked.Gates = []transfers.Gate{blockingGate{}}
	_, err := blocked.Complete(e.in(), e.requester.actor, x.ID, nil)
	var ne transfers.NotEligible
	if !errors.As(err, &ne) || len(ne.Problems) != 1 || ne.Problems[0].Message != "Fees are unpaid." {
		t.Fatalf("completion = %v", err)
	}
	if got := e.get(t, x.ID); got.Status != "approved" || got.TCNumber != nil {
		t.Errorf("transfer = %+v", got)
	}
	if e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, st) != "active" || e.scalar(t, `SELECT status FROM student_enrollments WHERE student_id = $1::uuid`, st) != "enrolled" || e.counter(t) != "0" {
		t.Error("a blocked completion wrote something")
	}
	// the default gates (dues, not tracked) let it through
	if r, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil); err != nil || r.Transfer.Dues.Status != "not_tracked" {
		t.Errorf("default completion = %+v %v", r, err)
	}
}

func TestCompletionChecksTheStudentAndTheIssueDate(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", false)
	x := e.approved(t, st)
	tomorrow := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	for name, d := range map[string]string{"future": tomorrow, "bad": "soon", "before the request": "2019-01-01"} {
		_, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, &d)
		fieldErr(t, name, err, "issuedOn", "")
	}
	// the student was changed after the approval
	_, err := e.f.Admin.Exec(bg, `UPDATE students SET status = 'alumni' WHERE id = $1::uuid`, st)
	must(t, err)
	_, err = e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	conflictCode(t, "alumnus", err, "student_not_transferable")
	_, err = e.f.Admin.Exec(bg, `UPDATE students SET status = 'paused' WHERE id = $1::uuid`, st)
	must(t, err)
	if r, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, ptr(today())); err != nil || *r.Transfer.TCNumber != "TC-000001" {
		t.Errorf("a paused student could not be completed: %+v %v", r, err)
	}
	// every enrolled enrolment is closed, others are left alone
	st2 := e.student(t, "S-2", false)
	_, err = e.f.Admin.Exec(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Z', 'Old', DATE '2024-04-01', DATE '2025-03-31')`, e.f.Alpha.ID)
	must(t, err)
	_, err = e.f.Admin.Exec(bg, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, status) SELECT $1, $2::uuid, y.id, $3::uuid, $4::uuid, 'completed' FROM academic_years y WHERE y.code = 'Z'`,
		e.f.Alpha.ID, st2, e.class, e.sec)
	must(t, err)
	y := e.approved(t, st2)
	_, err = e.svc.Complete(e.in(), e.requester.actor, y.ID, nil)
	must(t, err)
	if got := e.scalar(t, `SELECT string_agg(status, ',' ORDER BY status) FROM student_enrollments WHERE student_id = $1::uuid`, st2); got != "completed,withdrawn" {
		t.Errorf("enrolments = %s", got)
	}
}

func TestTheDatabaseKeepsTheTCNumberFixedAndOnlyForCompletedTransfers(t *testing.T) {
	e := setup(t)
	x := e.approved(t, e.student(t, "S-1", false))
	exec := func(sql string, args ...any) error { _, err := e.f.Admin.Exec(bg, sql, args...); return err }
	var tc *string

	// a number typed on insert, or on an update before completion, is discarded
	st2 := e.student(t, "S-2", false)
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO student_transfers (tenant_id, student_id, requested_on, reason, tc_number, tc_issued_on, completed_by)
		VALUES ($1, $2::uuid, CURRENT_DATE, 'health', 'TC-999999', CURRENT_DATE, $3::uuid) RETURNING tc_number`, e.f.Alpha.ID, st2, e.approver.actor.UserID).Scan(&tc))
	if tc != nil {
		t.Errorf("a typed number survived the insert: %s", *tc)
	}
	must(t, e.f.Admin.QueryRow(bg, `UPDATE student_transfers SET tc_number = 'TC-999998' WHERE id = $1::uuid RETURNING tc_number`, x.ID).Scan(&tc))
	if tc != nil {
		t.Errorf("a typed number survived an update: %s", *tc)
	}
	if err := exec(`INSERT INTO student_transfers (tenant_id, student_id, requested_on, reason, status) VALUES ($1, $2::uuid, CURRENT_DATE, 'health', 'completed')`, e.f.Alpha.ID, e.student(t, "S-3", false)); err == nil {
		t.Error("a transfer can start completed")
	}
	// the status alone cannot make a transfer completed: the completion time and date are needed with the number
	if err := exec(`UPDATE student_transfers SET status = 'completed' WHERE id = $1::uuid`, x.ID); err == nil {
		t.Error("a transfer became completed without a completion time")
	}

	if _, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil); err != nil {
		t.Fatal(err)
	}
	// once completed the number never changes, whatever an update says
	must(t, e.f.Admin.QueryRow(bg, `UPDATE student_transfers SET tc_number = 'TC-123456', tc_issued_on = DATE '2020-01-01', completed_by = NULL WHERE id = $1::uuid RETURNING tc_number`, x.ID).Scan(&tc))
	if *tc != "TC-000001" || e.scalar(t, `SELECT (tc_issued_on = CURRENT_DATE AND completed_by IS NOT NULL)::text FROM student_transfers WHERE id = $1::uuid`, x.ID) != "true" {
		t.Errorf("tc = %s", *tc)
	}
	for name, sql := range map[string]string{
		"reopen":  `UPDATE student_transfers SET status = 'approved' WHERE id = $1::uuid`,
		"delete":  `UPDATE student_transfers SET deleted_at = now() WHERE id = $1::uuid`,
		"move":    `UPDATE student_transfers SET student_id = (SELECT id FROM students WHERE registration_code = 'S-2') WHERE id = $1::uuid`,
		"clearTC": `UPDATE student_transfers SET tc_number = NULL, status = 'requested' WHERE id = $1::uuid`,
	} {
		if err := exec(sql, x.ID); err == nil {
			t.Errorf("%s of a completed transfer was allowed", name)
		}
	}
	// the number is unique in the institute whatever its case, and a number of a deleted transfer is not given again
	if err := exec(`UPDATE transfer_certificate_counters SET last_value = 0 WHERE tenant_id = $1`, e.f.Alpha.ID); err != nil {
		t.Fatal(err)
	}
	if next := e.scalar(t, `SELECT next_tc_number($1)`, e.f.Alpha.ID); next != "TC-000002" {
		t.Errorf("after a reset the counter gave %s, want TC-000002 (TC-000001 is used)", next)
	}
	if got := e.scalar(t, `SELECT count(*) FROM pg_indexes WHERE indexname = 'student_transfers_tc_uq' AND indexdef ILIKE '%lower(tc_number)%'`); got != "1" {
		t.Errorf("the unique index on the TC number is missing")
	}
}

func TestReadsAreAuditedAndListFiltersWork(t *testing.T) {
	e := setup(t)
	a := e.request(t, e.student(t, "A-1", false))
	b := e.approved(t, e.student(t, "B-2", false))
	c := e.approved(t, e.student(t, "C-3", true))
	_, err := e.svc.Complete(e.in(), e.requester.actor, c.ID, nil)
	must(t, err)

	list := func(q crud.ListQuery, f transfers.Filter) []string {
		t.Helper()
		p, err := e.svc.ListAudited(e.in(), e.requester.actor, q, f)
		must(t, err)
		var codes []string
		for _, x := range p.Items {
			codes = append(codes, x.Student.RegistrationCode)
		}
		return codes
	}
	parse := func(sort, search string) crud.ListQuery {
		q, err := crud.ParseList(0, 0, sort, search, transfers.SortKeys, transfers.DefaultSort)
		must(t, err)
		return q
	}
	join := func(s []string) string { return strings.Join(s, ",") }
	if got := join(list(parse("student", ""), transfers.Filter{})); got != "A-1,B-2,C-3" {
		t.Errorf("by student = %s", got)
	}
	if got := join(list(parse("-student", ""), transfers.Filter{})); got != "C-3,B-2,A-1" {
		t.Errorf("by student desc = %s", got)
	}
	if got := join(list(parse("transfer", ""), transfers.Filter{})); got != "A-1,B-2,C-3" {
		t.Errorf("by transfer = %s", got)
	}
	if got := join(list(parse("status,student", ""), transfers.Filter{})); got != "B-2,C-3,A-1" && got != "B-2,A-1,C-3" {
		// approved < completed < requested alphabetically
		if got != "B-2,C-3,A-1" {
			t.Errorf("by status = %s", got)
		}
	}
	if got := join(list(parse("student", ""), transfers.Filter{Status: "approved"})); got != "B-2" {
		t.Errorf("status filter = %s", got)
	}
	if got := join(list(parse("student", ""), transfers.Filter{ClassID: e.class})); got != "A-1,B-2" {
		t.Errorf("class filter = %s", got)
	}
	if got := join(list(parse("student", "tc-000001"), transfers.Filter{})); got != "C-3" {
		t.Errorf("search by TC = %s", got)
	}
	if got := join(list(parse("student", "b-2"), transfers.Filter{})); got != "B-2" {
		t.Errorf("search by code = %s", got)
	}
	if got := join(list(parse("student", "asha rao a"), transfers.Filter{})); got != "A-1" {
		t.Errorf("search by name = %s", got)
	}
	if got := join(list(parse("student", "50%"), transfers.Filter{})); got != "" {
		t.Errorf("a wildcard matched: %s", got)
	}
	// every list and view is audited, and the search text is not copied into the trail
	if n := e.f.AuditCount(t, "student_transfers.list"); n != 10 {
		t.Errorf("list reads audited %d times", n)
	}
	if got := e.scalar(t, `SELECT count(*) FROM audit_log WHERE action = 'student_transfers.list' AND after::text ILIKE '%b-2%'`); got != "0" {
		t.Errorf("the search text reached the audit trail")
	}
	if _, err := e.svc.View(e.in(), e.requester.actor, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.f.AuditCount(t, "student_transfers.view"); n < 1 {
		t.Errorf("views audited %d times", n)
	}
	// only a completed transfer has a certificate
	for _, id := range []string{a.ID, b.ID} {
		_, err := e.svc.Certificate(e.in(), e.requester.actor, id)
		conflictCode(t, "certificate of an open transfer", err, "not_completed")
	}
	if n := e.f.AuditCount(t, "student_transfers.certificate"); n != 0 {
		t.Errorf("a refused certificate was audited %d times", n)
	}
}

func TestAnotherInstituteSeesNothingOfATransfer(t *testing.T) {
	e := setup(t)
	x := e.approved(t, e.student(t, "S-1", false))
	_, err := e.svc.Complete(e.in(), e.requester.actor, x.ID, nil)
	must(t, err)
	b := modtest.In(e.f.Bravo)
	// the fixture's actor is a user of alpha; the transfer is looked up in bravo
	bravo := e.requester
	if _, err := e.svc.View(b, bravo.actor, x.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("view = %v", err)
	}
	if _, err := e.svc.Update(b, bravo.actor, x.ID, transfers.Input{Reason: "health"}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("update = %v", err)
	}
	if _, err := e.svc.Complete(b, bravo.actor, x.ID, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("complete = %v", err)
	}
	if _, err := e.svc.Cancel(b, bravo.actor, bravo.access, x.ID, ""); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("cancel = %v", err)
	}
	if err := e.svc.Delete(b, bravo.actor, x.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("delete = %v", err)
	}
	if _, err := e.svc.Certificate(b, bravo.actor, x.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("certificate = %v", err)
	}
	q, perr := crud.ParseList(0, 0, "", "", transfers.SortKeys, transfers.DefaultSort)
	must(t, perr)
	if p, err := e.svc.ListAudited(b, bravo.actor, q, transfers.Filter{}); err != nil || len(p.Items) != 0 {
		t.Errorf("list = %+v %v", p, err)
	}
	// a request for a student of alpha, made in bravo, finds no student
	_, err = e.svc.Create(b, bravo.actor, transfers.Input{StudentID: x.Student.ID, Reason: "health"})
	fieldErr(t, "foreign student", err, "studentId", "not_found")
}
