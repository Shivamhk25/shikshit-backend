package controltower_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/onb/controltower"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type world struct {
	f       modtest.Fixture
	svc     controltower.Service
	year    string
	classes []string
	section string
}

func (w world) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	_, err := w.f.Admin.Exec(bg, q, args...)
	must(t, err)
}

func (w world) id(t *testing.T, q string, args ...any) string {
	t.Helper()
	var id string
	must(t, w.f.Admin.QueryRow(bg, q, args...).Scan(&id))
	return id
}

// setup enables aca, stu and fee for alpha and builds a complete academic structure: a current year with a term, two
// classes and a section mapped to both, and a subject.
func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, svc: controltower.Service{DB: f.App, ImportDomains: map[string]string{"students": "students", "classes": "academic"}}}
	a := f.Alpha.ID
	w.exec(t, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'aca'), ($1, 'stu'), ($1, 'fee')`, a)
	w.year = w.id(t, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y1', '2026-27', '2026-04-01', '2027-03-31', true) RETURNING id::text`, a)
	w.exec(t, `INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date) VALUES ($1, $2, 'T1', 'Term 1', '2026-04-01', '2026-09-30')`, a, w.year)
	for i, c := range []string{"C1", "C2"} {
		w.classes = append(w.classes, w.id(t, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, $2::text, $2::text, $3) RETURNING id::text`, a, c, i+1))
	}
	w.section = w.id(t, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'A') RETURNING id::text`, a)
	w.exec(t, `INSERT INTO subjects (tenant_id, code, name, category, area, credits) VALUES ($1, 'MATH', 'Maths', 'core', 'mathematics', 1)`, a)
	for _, c := range w.classes {
		w.exec(t, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, a, w.year, c, w.section)
	}
	return w
}

// student adds an active student of alpha. enrol puts it in class 1 for the current year; guardian links one guardian.
func (w world) student(t *testing.T, code string, enrol, guardian bool) {
	t.Helper()
	a := w.f.Alpha.ID
	sid := w.id(t, `INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth) VALUES ($1, $2, $2, 'female', '2015-01-01') RETURNING id::text`, a, code)
	if enrol {
		w.exec(t, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4, $5)`, a, sid, w.year, w.classes[0], w.section)
	}
	if guardian {
		gid := w.id(t, `INSERT INTO guardians (tenant_id, name, phone) VALUES ($1, $2, '9999900000') RETURNING id::text`, a, "G "+code)
		w.exec(t, `INSERT INTO student_guardians (tenant_id, student_id, guardian_id, relation) VALUES ($1, $2, $3, 'mother')`, a, sid, gid)
	}
}

func (w world) domain(t *testing.T, id string) controltower.Item {
	t.Helper()
	v, err := w.svc.Overview(modtest.In(w.f.Alpha))
	must(t, err)
	for _, d := range v.Domains {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("domain %s not listed", id)
	return controltower.Item{}
}

func check(t *testing.T, it controltower.Item, key string) controltower.Check {
	t.Helper()
	for _, c := range it.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("no check %s", key)
	return controltower.Check{}
}

func TestNumbersEqualTheUnderlyingCounts(t *testing.T) {
	w := setup(t)
	w.student(t, "S1", true, true)
	w.student(t, "S2", true, false)
	w.student(t, "S3", false, true)
	// A deleted student, a paused one and a deleted guardian link must not be counted.
	w.student(t, "S4", true, true)
	w.exec(t, `UPDATE students SET deleted_at = now() WHERE registration_code = 'S4'`)
	w.student(t, "S5", false, false)
	w.exec(t, `UPDATE students SET status = 'paused' WHERE registration_code = 'S5'`)

	ac := w.domain(t, "academic")
	if c := check(t, ac, "classes_mapped"); c.Done != 2 || c.Total != 2 {
		t.Errorf("classes_mapped = %+v, want 2 of 2", c)
	}
	st := w.domain(t, "students")
	want := map[string][2]int{"students": {1, 1}, "enrolled": {2, 3}, "guardians": {2, 3}, "enrollments_mapped": {2, 2}}
	for k, v := range want {
		if c := check(t, st, k); c.Done != v[0] || c.Total != v[1] {
			t.Errorf("%s = %d of %d, want %d of %d", k, c.Done, c.Total, v[0], v[1])
		}
	}
	// 1+2+2+2 of 1+3+3+2 = 7 of 9, rounded down.
	if st.Percent != 7*100/9 {
		t.Errorf("percent = %d", st.Percent)
	}
	if st.Status != controltower.InProgress {
		t.Errorf("status = %s", st.Status)
	}

	// Change the data: the numbers follow it, with no stored copy to refresh.
	w.exec(t, `UPDATE students SET status = 'transferred' WHERE registration_code = 'S3'`)
	st = w.domain(t, "students")
	for k, v := range map[string][2]int{"enrolled": {2, 2}, "guardians": {1, 2}} {
		if c := check(t, st, k); c.Done != v[0] || c.Total != v[1] {
			t.Errorf("after a transfer %s = %d of %d, want %d of %d", k, c.Done, c.Total, v[0], v[1])
		}
	}
}

func TestClassWithoutASectionInTheCurrentYearIsReported(t *testing.T) {
	w := setup(t)
	w.exec(t, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C3', 'C3', 3)`, w.f.Alpha.ID)
	if c := check(t, w.domain(t, "academic"), "classes_mapped"); c.Done != 2 || c.Total != 3 {
		t.Errorf("classes_mapped = %+v, want 2 of 3", c)
	}
}

func TestDomainsWithoutADataSourceAreNotStartedWithANote(t *testing.T) {
	w := setup(t)
	f := w.domain(t, "fees")
	if f.Status != controltower.NotStarted || f.HasSource || f.Percent != 0 || len(f.Checks) != 0 || f.Note != controltower.NoSource {
		t.Errorf("fees = %+v", f)
	}
	if len(f.Blockers) != 2 {
		t.Errorf("fees blockers = %+v", f.Blockers)
	}
	if _, err := w.svc.Transition(modtest.In(w.f.Alpha), w.f.Actor, "fees", "review"); !errors.As(err, new(controltower.Blocked)) {
		t.Errorf("review without a source = %v", err)
	}
}

func TestDomainsOfDisabledModulesAreHidden(t *testing.T) {
	w := setup(t)
	w.exec(t, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code IN ('stu', 'fee')`, w.f.Alpha.ID)
	v, err := w.svc.Overview(modtest.In(w.f.Alpha))
	must(t, err)
	if len(v.Domains) != 1 || v.Domains[0].ID != "academic" {
		t.Fatalf("domains = %+v", v.Domains)
	}
	_, err = w.svc.Transition(modtest.In(w.f.Alpha), w.f.Actor, "students", "review")
	if !errors.Is(err, controltower.ErrNotFound) {
		t.Errorf("hidden domain = %v", err)
	}
	_, err = w.svc.Assign(modtest.In(w.f.Alpha), w.f.Actor, "students", controltower.Assignment{})
	if !errors.Is(err, controltower.ErrNotFound) {
		t.Errorf("hidden domain assign = %v", err)
	}
}

func TestReviewNeedsRulesAndReadyNeedsPrerequisites(t *testing.T) {
	w := setup(t)
	ctx := modtest.In(w.f.Alpha)
	w.student(t, "S1", true, true)

	// Students are complete, so students can ask for review, but cannot be confirmed Ready before academic is.
	it, err := w.svc.Transition(ctx, w.f.Actor, "students", "review")
	must(t, err)
	if it.Status != controltower.NeedsReview {
		t.Fatalf("status = %s", it.Status)
	}
	_, err = w.svc.Transition(ctx, w.f.Actor, "students", "ready")
	var b controltower.Blocked
	if !errors.As(err, &b) {
		t.Fatalf("ready before academic = %v", err)
	}

	// Academic: ready needs the review mark first.
	if _, err = w.svc.Transition(ctx, w.f.Actor, "academic", "ready"); !errors.As(err, &b) {
		t.Fatalf("ready without review = %v", err)
	}
	_, err = w.svc.Transition(ctx, w.f.Actor, "academic", "review")
	must(t, err)
	it, err = w.svc.Transition(ctx, w.f.Actor, "academic", "ready")
	must(t, err)
	if it.Status != controltower.Ready || it.ReadyAt == nil {
		t.Fatalf("academic = %+v", it)
	}
	it, err = w.svc.Transition(ctx, w.f.Actor, "students", "ready")
	must(t, err)
	if it.Status != controltower.Ready {
		t.Fatalf("students = %s", it.Status)
	}

	// Taking academic's mark back drops students out of Ready too, though its own mark stays.
	_, err = w.svc.Transition(ctx, w.f.Actor, "academic", "open")
	must(t, err)
	st := w.domain(t, "students")
	if st.Status == controltower.Ready || st.State != controltower.StateReady {
		t.Errorf("students = %s / %s", st.Status, st.State)
	}

	// Data that stops meeting the rules drops a Ready domain back to in progress.
	w.exec(t, `UPDATE classes SET deleted_at = now() WHERE code = 'C2'`)
	w.exec(t, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C9', 'C9', 9)`, w.f.Alpha.ID)
	if s := w.domain(t, "academic").Status; s != controltower.InProgress {
		t.Errorf("academic after a class lost its section = %s", s)
	}
}

func TestDomainReadyNotifiesOwnersAndDependents(t *testing.T) {
	w := setup(t)
	ctx := modtest.In(w.f.Alpha)
	w.student(t, "S1", true, true)
	var other string
	must(t, w.f.Admin.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, 'ravi', 'Ravi', 'active') RETURNING id::text`, w.f.Alpha.ID).Scan(&other))
	_, err := w.svc.Assign(ctx, w.f.Actor, "students", controltower.Assignment{OwnerID: &other})
	must(t, err)
	_, err = w.svc.Transition(ctx, w.f.Actor, "academic", "review")
	must(t, err)
	if n := count(t, w, `SELECT count(*) FROM notifications WHERE kind = 'onboarding.domain_ready'`); n != 0 {
		t.Fatalf("notifications before ready = %d", n)
	}
	_, err = w.svc.Transition(ctx, w.f.Actor, "academic", "ready")
	must(t, err)
	// The reviewer (Asha) and the owner of the waiting students domain (Ravi).
	if n := count(t, w, `SELECT count(*) FROM notifications WHERE kind = 'onboarding.domain_ready' AND user_id = $1`, other); n != 1 {
		t.Errorf("dependent owner notified %d times", n)
	}
	if n := count(t, w, `SELECT count(*) FROM notifications WHERE kind = 'onboarding.domain_ready' AND user_id = $1`, w.f.Actor.UserID); n != 1 {
		t.Errorf("reviewer notified %d times", n)
	}
	// Confirming again is not a new event.
	_, err = w.svc.Transition(ctx, w.f.Actor, "academic", "ready")
	must(t, err)
	if n := count(t, w, `SELECT count(*) FROM notifications WHERE kind = 'onboarding.domain_ready'`); n != 2 {
		t.Errorf("notifications = %d after a repeat", n)
	}
	if w.f.AuditCount(t, "onb.control_tower.ready") != 2 || w.f.AuditCount(t, "onb.control_tower.assign") != 1 {
		t.Error("changes are not audited")
	}
}

func count(t *testing.T, w world, q string, args ...any) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, q, args...).Scan(&n))
	return n
}

func TestAssignChecksTheOwnerAndDueDate(t *testing.T) {
	w := setup(t)
	ctx := modtest.In(w.f.Alpha)
	var bravoUser string
	must(t, w.f.Admin.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, 'zed', 'Zed', 'active') RETURNING id::text`, w.f.Bravo.ID).Scan(&bravoUser))
	var fe crud.FieldErrors
	for _, in := range []controltower.Assignment{
		{OwnerID: &bravoUser}, {OwnerID: ptr("nope")}, {DueDate: ptr("31/12/2026")},
	} {
		if _, err := w.svc.Assign(ctx, w.f.Actor, "academic", in); !errors.As(err, &fe) {
			t.Errorf("%+v accepted: %v", in, err)
		}
	}
	it, err := w.svc.Assign(ctx, w.f.Actor, "academic", controltower.Assignment{OwnerID: &w.f.Actor.UserID, DueDate: ptr("2020-01-01")})
	must(t, err)
	if it.OwnerName == nil || *it.OwnerName != "Asha" || !it.Overdue {
		t.Errorf("assigned = %+v", it)
	}
	it, err = w.svc.Assign(ctx, w.f.Actor, "academic", controltower.Assignment{})
	must(t, err)
	if it.OwnerID != nil || it.DueDate != nil || it.Overdue {
		t.Errorf("cleared = %+v", it)
	}
}

func ptr[T any](v T) *T { return &v }

func TestOpenImportBatchesAreCountedPerDomain(t *testing.T) {
	w := setup(t)
	for _, b := range [][2]string{{"students", "validated"}, {"students", "committed"}, {"classes", "uploaded"}, {"rooms", "validated"}} {
		w.exec(t, `INSERT INTO import_batches (tenant_id, entity, strategy, commit_mode, status, file_name, template_version, expires_at)
			VALUES ($1, $2, 'insert', 'all_or_nothing', $3, 'f.csv', 1, now() + interval '1 day')`, w.f.Alpha.ID, b[0], b[1])
	}
	if n := w.domain(t, "students").OpenImports; n != 1 {
		t.Errorf("students open imports = %d, want 1", n)
	}
	if n := w.domain(t, "academic").OpenImports; n != 1 {
		t.Errorf("academic open imports = %d, want 1", n)
	}
}

func TestOneInstituteNeverSeesOrChangesAnothers(t *testing.T) {
	w := setup(t)
	w.student(t, "S1", true, true)
	_, err := w.svc.Transition(modtest.In(w.f.Alpha), w.f.Actor, "academic", "review")
	must(t, err)
	w.exec(t, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'aca'), ($1, 'stu')`, w.f.Bravo.ID)

	bctx := modtest.In(w.f.Bravo)
	v, err := w.svc.Overview(bctx)
	must(t, err)
	for _, d := range v.Domains {
		if d.Status != controltower.NotStarted || d.State != controltower.StateOpen || d.OwnerID != nil || d.Percent != 0 {
			t.Errorf("bravo sees alpha's progress in %s: %+v", d.ID, d)
		}
	}
	if _, err := w.svc.Transition(bctx, w.f.Actor, "academic", "review"); !errors.As(err, new(controltower.Blocked)) {
		t.Errorf("bravo reviewing with no data = %v", err)
	}
	if n := count(t, w, `SELECT count(*) FROM onboarding_domains WHERE tenant_id = $1`, w.f.Bravo.ID); n != 0 {
		t.Errorf("bravo rows = %d", n)
	}
	// Row-level security: the runtime role sees only its own institute's rows.
	var n int
	must(t, w.f.App.QueryRow(bg, `SELECT count(*) FROM onboarding_domains`).Scan(&n))
	if n != 0 {
		t.Errorf("runtime role without an institute sees %d rows", n)
	}
}
