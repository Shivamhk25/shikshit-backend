package pauses_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/pauses"
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

type env struct {
	f                 modtest.Fixture
	svc               pauses.Service
	year, class, sec  string
	otherCls, otherSc string
}

func today() string { return pauses.Today(time.Now()) }

func daysAgo(n int) string { return time.Now().In(time.UTC).AddDate(0, 0, -n).Format(crud.DateLayout) }
func daysAhead(n int) string {
	return time.Now().In(time.UTC).AddDate(0, 0, n).Format(crud.DateLayout)
}

func setup(t *testing.T) env {
	t.Helper()
	f := modtest.New(t)
	e := env{f: f, svc: pauses.Service{DB: f.App}}
	now := time.Now().UTC()
	must(t, f.Admin.QueryRow(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', $2, $3, true) RETURNING id::text`,
		f.Alpha.ID, now.AddDate(-1, 0, 0), now.AddDate(0, 10, 0)).Scan(&e.year))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'V', 'Class 5') RETURNING id::text`, f.Alpha.ID).Scan(&e.class))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Alpha.ID).Scan(&e.sec))
	return e
}

func (e env) in() context.Context { return modtest.In(e.f.Alpha) }

// student adds a student of alpha with an enrolment (unless noEnrol) and the given status.
func (e env) student(t *testing.T, code, status string, noEnrol bool) string {
	t.Helper()
	var id string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth, admission_date, status)
		VALUES ($1, $2::text, 'Asha', 'Rao ' || $2::text, 'female', DATE '2015-04-01', DATE '2020-04-01', $3) RETURNING id::text`, e.f.Alpha.ID, code, status).Scan(&id))
	if !noEnrol {
		_, err := e.f.Admin.Exec(bg, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`,
			e.f.Alpha.ID, id, e.year, e.class, e.sec, code)
		must(t, err)
	}
	return id
}

func (e env) pause(t *testing.T, student string) pauses.Pause {
	t.Helper()
	x, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: student, PausedFrom: ptr(daysAgo(3)), ExpectedBack: ptr(daysAhead(10)), Reason: "illness"})
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

func (e env) status(t *testing.T, student string) string {
	return e.scalar(t, `SELECT status FROM students WHERE id = $1::uuid`, student)
}

func conflictCode(t *testing.T, name string, err error, code string) {
	t.Helper()
	var ce pauses.ConflictError
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

func TestPausingSetsTheStudentPausedAndEndingSetsThemActive(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)

	x := e.pause(t, st)
	if x.Status != "active" || x.Student.RegistrationCode != "S-1" || x.Student.Name != "Asha Rao S-1" || x.ClassName == nil || *x.ClassName != "Class 5" ||
		x.SectionName == nil || *x.SectionName != "Section A" || x.YearName == nil || *x.YearName != "2026-27" || x.PausedFrom != daysAgo(3) ||
		x.Days == nil || *x.Days < 2 || *x.Days > 4 || x.Overdue || x.EndedOn != nil {
		t.Fatalf("paused = %+v", x)
	}
	if x.Figures.Owed.Status != "not_tracked" || x.Figures.Missed.Status != "not_tracked" || x.Figures.Owed.Note == "" {
		t.Fatalf("figures = %+v", x.Figures)
	}
	if got := e.status(t, st); got != "paused" {
		t.Fatalf("student after pause = %s", got)
	}

	ended, err := e.svc.End(e.in(), e.f.Actor, x.ID, ptr(daysAgo(1)), ptr("back after treatment"))
	must(t, err)
	if ended.Status != "ended" || ended.EndedOn == nil || *ended.EndedOn != daysAgo(1) || ended.Days == nil || *ended.Days != 2 || ended.CloseNote == nil {
		t.Fatalf("ended = %+v", ended)
	}
	if got := e.status(t, st); got != "active" {
		t.Fatalf("student after end = %s", got)
	}
	// an ended pause can no longer be ended, changed or cancelled
	_, err = e.svc.End(e.in(), e.f.Actor, x.ID, nil, nil)
	conflictCode(t, "end twice", err, "not_active")
	_, err = e.svc.Cancel(e.in(), e.f.Actor, x.ID, nil)
	conflictCode(t, "cancel ended", err, "not_active")
	_, err = e.svc.Update(e.in(), e.f.Actor, x.ID, pauses.Input{Reason: "illness"})
	conflictCode(t, "update ended", err, "not_active")

	// the student can be paused again; the earlier pause stays as history
	y := e.pause(t, st)
	if y.ID == x.ID || e.status(t, st) != "paused" {
		t.Fatalf("second pause = %+v", y)
	}
	if n := e.scalar(t, `SELECT count(*) FROM student_pauses WHERE student_id = $1::uuid`, st); n != "2" {
		t.Errorf("pauses kept = %s", n)
	}
	for action, want := range map[string]int{"student_pauses.create": 2, "student_pauses.end": 1, "students.update": 3} {
		if n := e.f.AuditCount(t, action); n != want {
			t.Errorf("audit %s = %d, want %d", action, n, want)
		}
	}
}

func TestCancellingAPauseReturnsTheStudentToActive(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	x := e.pause(t, st)
	c, err := e.svc.Cancel(e.in(), e.f.Actor, x.ID, ptr("wrong child"))
	must(t, err)
	if c.Status != "cancelled" || c.EndedOn != nil || c.Days != nil || e.status(t, st) != "active" {
		t.Fatalf("cancelled = %+v, student %s", c, e.status(t, st))
	}
	if n := e.f.AuditCount(t, "student_pauses.cancel"); n != 1 {
		t.Errorf("cancel audited %d times", n)
	}
	// the cancelled pause no longer stands in the way
	e.pause(t, st)
}

func TestOnlyOneActivePausePerStudentEvenWhenCreatedTogether(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	e.pause(t, st)
	_, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: st, Reason: "illness"})
	conflictCode(t, "second pause", err, "already_paused")

	other := e.student(t, "S-2", "active", false)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: other, Reason: "illness"})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		var ce pauses.ConflictError
		if !errors.As(err, &ce) || ce.Code != "already_paused" {
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d simultaneous pauses succeeded, want 1", ok)
	}
	if n := e.scalar(t, `SELECT count(*) FROM student_pauses WHERE student_id = $1::uuid AND status = 'active'`, other); n != "1" {
		t.Errorf("active pauses = %s", n)
	}
}

func TestAFormerStudentOrAnUnknownStudentCannotBePaused(t *testing.T) {
	e := setup(t)
	for _, status := range []string{"transferred", "alumni"} {
		st := e.student(t, "F-"+status, status, false)
		_, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: st, Reason: "illness"})
		fieldErr(t, status, err, "studentId", "not_pausable")
		if n := e.scalar(t, `SELECT count(*) FROM student_pauses WHERE student_id = $1::uuid`, st); n != "0" || e.status(t, st) != status {
			t.Errorf("%s student changed: pauses=%s status=%s", status, n, e.status(t, st))
		}
	}
	_, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: "00000000-0000-0000-0000-000000000000", Reason: "illness"})
	fieldErr(t, "unknown", err, "studentId", "not_found")
	_, err = e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: "nope", Reason: "illness"})
	fieldErr(t, "bad id", err, "studentId", "invalid")

	// a removed student is not found either
	gone := e.student(t, "S-9", "active", false)
	_, err = e.f.Admin.Exec(bg, `UPDATE students SET deleted_at = now() WHERE id = $1::uuid`, gone)
	must(t, err)
	_, err = e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: gone, Reason: "illness"})
	fieldErr(t, "removed", err, "studentId", "not_found")
}

func TestAStudentAlreadyMarkedPausedByHandCanGetAPauseRecord(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "paused", false)
	x := e.pause(t, st)
	if x.Status != "active" {
		t.Fatalf("pause = %+v", x)
	}
	if n := e.f.AuditCount(t, "students.update"); n != 0 {
		t.Errorf("student status was already paused, but %d status changes were audited", n)
	}
}

func TestAPauseThatFailsToMoveTheStudentIsNotKept(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	_, err := e.f.Admin.Exec(bg, `CREATE FUNCTION fail_student_status() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
		CREATE TRIGGER fail_student_status BEFORE UPDATE OF status ON students FOR EACH ROW EXECUTE FUNCTION fail_student_status();`)
	must(t, err)
	_, err = e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: st, Reason: "illness"})
	if err == nil {
		t.Fatal("pause was saved although the student could not be updated")
	}
	if n := e.scalar(t, `SELECT count(*) FROM student_pauses`); n != "0" || e.status(t, st) != "active" || e.f.AuditCount(t, "student_pauses.create") != 0 {
		t.Fatalf("pauses=%s student=%s audit=%d", n, e.status(t, st), e.f.AuditCount(t, "student_pauses.create"))
	}

	// ending: the pause stays active when the student cannot be moved back
	_, err = e.f.Admin.Exec(bg, `DROP TRIGGER fail_student_status ON students`)
	must(t, err)
	x := e.pause(t, st)
	_, err = e.f.Admin.Exec(bg, `CREATE TRIGGER fail_student_status BEFORE UPDATE OF status ON students FOR EACH ROW EXECUTE FUNCTION fail_student_status()`)
	must(t, err)
	if _, err := e.svc.End(e.in(), e.f.Actor, x.ID, nil, nil); err == nil {
		t.Fatal("pause ended although the student could not be updated")
	}
	if _, err := e.svc.Cancel(e.in(), e.f.Actor, x.ID, nil); err == nil {
		t.Fatal("pause cancelled although the student could not be updated")
	}
	if got := e.scalar(t, `SELECT status FROM student_pauses WHERE id = $1::uuid`, x.ID); got != "active" || e.status(t, st) != "paused" {
		t.Fatalf("pause=%s student=%s", got, e.status(t, st))
	}
}

func TestPauseDatesAndReasonAreChecked(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	create := func(in pauses.Input) error {
		in.StudentID = st
		_, err := e.svc.Create(e.in(), e.f.Actor, in)
		return err
	}
	fieldErr(t, "no reason", create(pauses.Input{}), "reason", "required")
	fieldErr(t, "bad reason", create(pauses.Input{Reason: "holiday"}), "reason", "invalid_choice")
	fieldErr(t, "other without note", create(pauses.Input{Reason: "other"}), "reasonNote", "required")
	fieldErr(t, "long note", create(pauses.Input{Reason: "illness", ReasonNote: ptr(string(make([]rune, 1001)) + "x")}), "reasonNote", "too_long")
	fieldErr(t, "future start", create(pauses.Input{Reason: "illness", PausedFrom: ptr(daysAhead(3))}), "pausedFrom", "in_future")
	fieldErr(t, "bad date", create(pauses.Input{Reason: "illness", PausedFrom: ptr("2026-13-40")}), "pausedFrom", "invalid_date")
	fieldErr(t, "before admission", create(pauses.Input{Reason: "illness", PausedFrom: ptr("2019-01-01")}), "pausedFrom", "before_admission")
	fieldErr(t, "back before start", create(pauses.Input{Reason: "illness", PausedFrom: ptr(daysAgo(2)), ExpectedBack: ptr(daysAgo(5))}), "expectedBack", "before_start")
	if n := e.scalar(t, `SELECT count(*) FROM student_pauses`); n != "0" || e.status(t, st) != "active" {
		t.Fatalf("a refused pause left %s rows, student %s", n, e.status(t, st))
	}
	// "other" with a note, and an open-ended pause, are fine
	x, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: st, Reason: "other", ReasonNote: ptr("  Sibling's wedding  ")})
	must(t, err)
	if x.PausedFrom != today() || x.ExpectedBack != nil || x.ReasonNote == nil || *x.ReasonNote != "Sibling's wedding" || x.Days == nil || *x.Days != 0 {
		t.Fatalf("open-ended = %+v", x)
	}

	// ending
	_, err = e.svc.End(e.in(), e.f.Actor, x.ID, ptr(daysAhead(2)), nil)
	fieldErr(t, "back in the future", err, "endedOn", "in_future")
	_, err = e.svc.End(e.in(), e.f.Actor, x.ID, ptr(daysAgo(4)), nil)
	fieldErr(t, "back before start", err, "endedOn", "before_start")
	_, err = e.svc.End(e.in(), e.f.Actor, x.ID, ptr("soon"), nil)
	fieldErr(t, "bad end date", err, "endedOn", "invalid_date")
	if e.status(t, st) != "paused" || e.scalar(t, `SELECT status FROM student_pauses WHERE id = $1::uuid`, x.ID) != "active" {
		t.Fatal("a refused end changed something")
	}
	done, err := e.svc.End(e.in(), e.f.Actor, x.ID, nil, nil)
	must(t, err)
	if *done.EndedOn != today() || *done.Days != 0 {
		t.Fatalf("ended today = %+v", done)
	}
}

func TestAnActivePauseCanBeEditedAndShowsWhenOverdue(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	x, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: st, PausedFrom: ptr(daysAgo(20)), ExpectedBack: ptr(daysAgo(5)), Reason: "illness"})
	must(t, err)
	if !x.Overdue || *x.Days < 19 {
		t.Fatalf("overdue = %+v", x)
	}
	_, err = e.svc.Update(e.in(), e.f.Actor, x.ID, pauses.Input{Reason: "illness", ExpectedBack: ptr(daysAgo(25))})
	fieldErr(t, "before start", err, "expectedBack", "before_start")
	_, err = e.svc.Update(e.in(), e.f.Actor, x.ID, pauses.Input{Reason: "other"})
	fieldErr(t, "other needs a note", err, "reasonNote", "required")
	u, err := e.svc.Update(e.in(), e.f.Actor, x.ID, pauses.Input{Reason: "family_emergency", ExpectedBack: ptr(daysAhead(7)), ReasonNote: ptr("Grandmother unwell")})
	must(t, err)
	if u.Overdue || u.Reason != "family_emergency" || *u.ExpectedBack != daysAhead(7) || u.PausedFrom != x.PausedFrom || u.Student.ID != st {
		t.Fatalf("updated = %+v", u)
	}
	if n := e.f.AuditCount(t, "student_pauses.update"); n != 1 {
		t.Errorf("update audited %d times", n)
	}
	// an ended pause is never overdue
	end, err := e.svc.End(e.in(), e.f.Actor, x.ID, nil, nil)
	must(t, err)
	if end.Overdue {
		t.Error("an ended pause shows as overdue")
	}
	if _, err := e.svc.Update(e.in(), e.f.Actor, "00000000-0000-0000-0000-000000000000", pauses.Input{Reason: "illness"}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("unknown pause: %v", err)
	}
}

func TestAStudentWhoLeavesPausedHasTheirPauseEnded(t *testing.T) {
	e := setup(t)
	for _, to := range []string{"transferred", "alumni", "active"} {
		st := e.student(t, "S-"+to, "active", false)
		x := e.pause(t, st)
		_, err := e.f.Admin.Exec(bg, `UPDATE students SET status = $2 WHERE id = $1::uuid`, st, to)
		must(t, err)
		got := e.scalar(t, `SELECT status || '|' || (ended_on IS NOT NULL)::text || '|' || (ended_on >= paused_from)::text || '|' || COALESCE(close_note, '') FROM student_pauses WHERE id = $1::uuid`, x.ID)
		if got != "ended|true|true|Ended automatically: the student became "+to {
			t.Errorf("student became %s: pause = %s", to, got)
		}
		if e.status(t, st) != to {
			t.Errorf("student status = %s, want %s", e.status(t, st), to)
		}
	}
	// a student who was never paused, or whose pause was closed by the service, is not touched by the trigger
	st := e.student(t, "S-x", "active", false)
	x := e.pause(t, st)
	_, err := e.svc.Cancel(e.in(), e.f.Actor, x.ID, nil)
	must(t, err)
	if got := e.scalar(t, `SELECT status FROM student_pauses WHERE id = $1::uuid`, x.ID); got != "cancelled" {
		t.Errorf("cancelled pause became %s", got)
	}
}

func TestListsFilterSearchSortAndAreAudited(t *testing.T) {
	e := setup(t)
	a := e.student(t, "S-1", "active", false)
	b := e.student(t, "S-2", "active", true)
	c := e.student(t, "S-3", "active", false)
	pa := e.pause(t, a)
	pb, err := e.svc.Create(e.in(), e.f.Actor, pauses.Input{StudentID: b, PausedFrom: ptr(daysAgo(10)), Reason: "financial"})
	must(t, err)
	pc := e.pause(t, c)
	_, err = e.svc.End(e.in(), e.f.Actor, pc.ID, nil, nil)
	must(t, err)

	list := func(q crud.ListQuery, f pauses.Filter) crud.Page[pauses.Pause] {
		t.Helper()
		p, err := e.svc.ListAudited(e.in(), e.f.Actor, q, f)
		must(t, err)
		return p
	}
	base, err := crud.ParseList(1, 50, "", "", pauses.SortKeys, pauses.DefaultSort)
	must(t, err)
	if p := list(base, pauses.Filter{}); p.Total != 3 || p.Items[0].ID != pb.ID && p.Items[0].PausedFrom != daysAgo(3) {
		t.Fatalf("all = %d first %+v", p.Total, p.Items[0])
	}
	if p := list(base, pauses.Filter{Status: "active"}); p.Total != 2 {
		t.Errorf("active = %d", p.Total)
	}
	if p := list(base, pauses.Filter{Status: "ended"}); p.Total != 1 || p.Items[0].ID != pc.ID {
		t.Errorf("ended = %+v", p)
	}
	if p := list(base, pauses.Filter{ClassID: e.class}); p.Total != 2 {
		t.Errorf("class filter = %d (the student without an enrolment has no class)", p.Total)
	}
	if p := list(base, pauses.Filter{ClassID: "00000000-0000-0000-0000-000000000000"}); p.Total != 0 {
		t.Errorf("other class = %d", p.Total)
	}
	search := base
	search.Search = "s-2"
	if p := list(search, pauses.Filter{}); p.Total != 1 || p.Items[0].ID != pb.ID {
		t.Errorf("search = %+v", p)
	}
	asc, err := crud.ParseList(1, 50, "from", "", pauses.SortKeys, pauses.DefaultSort)
	must(t, err)
	if p := list(asc, pauses.Filter{}); p.Items[0].ID != pb.ID {
		t.Errorf("sort by from = %+v", p.Items[0])
	}
	_ = pa
	// the audit row carries the filters, never the search text
	if n := e.f.AuditCount(t, "student_pauses.list"); n != 7 {
		t.Errorf("list audited %d times, want 7", n)
	}
	if got := e.scalar(t, `SELECT count(*) FROM audit_log WHERE action = 'student_pauses.list' AND after::text ILIKE '%s-2%'`); got != "0" {
		t.Errorf("the search text reached the audit trail")
	}
	v, err := e.svc.View(e.in(), e.f.Actor, pa.ID)
	must(t, err)
	if v.ID != pa.ID || e.f.AuditCount(t, "student_pauses.view") != 1 {
		t.Errorf("view = %+v, audited %d", v, e.f.AuditCount(t, "student_pauses.view"))
	}
}

func TestAnotherInstituteSeesAndChangesNothing(t *testing.T) {
	e := setup(t)
	st := e.student(t, "S-1", "active", false)
	x := e.pause(t, st)
	bravo := modtest.In(e.f.Bravo)

	if p, err := e.svc.ListAudited(bravo, e.f.Actor, crud.ListQuery{Page: 1, Size: 50, OrderBy: "created_at DESC"}, pauses.Filter{}); err != nil || p.Total != 0 {
		t.Errorf("bravo list = %+v %v", p, err)
	}
	if _, err := e.svc.View(bravo, e.f.Actor, x.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bravo view = %v", err)
	}
	if _, err := e.svc.End(bravo, e.f.Actor, x.ID, nil, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bravo end = %v", err)
	}
	if _, err := e.svc.Cancel(bravo, e.f.Actor, x.ID, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bravo cancel = %v", err)
	}
	if _, err := e.svc.Update(bravo, e.f.Actor, x.ID, pauses.Input{Reason: "illness"}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bravo update = %v", err)
	}
	_, err := e.svc.Create(bravo, e.f.Actor, pauses.Input{StudentID: st, Reason: "illness"})
	fieldErr(t, "bravo pauses alpha's student", err, "studentId", "not_found")
	if e.status(t, st) != "paused" || e.scalar(t, `SELECT status FROM student_pauses WHERE id = $1::uuid`, x.ID) != "active" {
		t.Error("alpha's records changed")
	}
}
