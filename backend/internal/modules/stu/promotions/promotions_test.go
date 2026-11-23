package promotions_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/promotions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
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

// world: three academic years, four classes in promotion order (5, 6, 7, 8), sections A and B mapped where noted, C and
// D never mapped. Class 5 section A of 2026-27 holds five students; Class 8 section A holds two.
type world struct {
	f                modtest.Fixture
	svc              promotions.Service
	y1, y2, y3       string
	c5, c6, c7, c8   string
	secA, secB, secC string
	five             map[string]string // first name -> student id, class 5 A
	fiveEnrol        map[string]string // first name -> year 1 enrolment id
	grad1, grad2     string            // class 8 A students
	bravoBatchSrc    struct{ year, toYear, class, section, student string }
}

func (w world) a() context.Context { return modtest.In(w.f.Alpha) }

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, svc: promotions.Service{DB: f.App}, five: map[string]string{}, fiveEnrol: map[string]string{}}
	ins := func(dst *string, q string, args ...any) {
		t.Helper()
		must(t, f.Admin.QueryRow(bg, q, args...).Scan(dst))
	}
	for _, y := range []struct {
		dst        *string
		code, from string
		to         string
	}{{&w.y1, "Y1", "2026-04-01", "2027-03-31"}, {&w.y2, "Y2", "2027-04-01", "2028-03-31"}, {&w.y3, "Y3", "2028-04-01", "2029-03-31"}} {
		ins(y.dst, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, $2::text, 'Year ' || $2::text, $3::date, $4::date) RETURNING id::text`, f.Alpha.ID, y.code, y.from, y.to)
	}
	for _, c := range []struct {
		dst *string
		n   int
	}{{&w.c5, 5}, {&w.c6, 6}, {&w.c7, 7}, {&w.c8, 8}} {
		ins(c.dst, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C' || $2::int, 'Class ' || $2::int, $2::int) RETURNING id::text`, f.Alpha.ID, c.n)
	}
	for _, s := range []struct {
		dst  *string
		code string
	}{{&w.secA, "A"}, {&w.secB, "B"}, {&w.secC, "C"}} {
		ins(s.dst, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, $2::text, 'Section ' || $2::text) RETURNING id::text`, f.Alpha.ID, s.code)
	}
	mapIt := func(year, class, section string) {
		t.Helper()
		_, err := f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, year, class, section)
		must(t, err)
	}
	for _, m := range [][3]string{{w.y1, w.c5, w.secA}, {w.y1, w.c5, w.secB}, {w.y1, w.c8, w.secA},
		{w.y2, w.c5, w.secA}, {w.y2, w.c6, w.secA}, {w.y2, w.c6, w.secB}, {w.y2, w.c7, w.secA}, {w.y2, w.c8, w.secA}, {w.y3, w.c7, w.secA}} {
		mapIt(m[0], m[1], m[2])
	}
	enrol := func(code, first, class, section, roll string) (student, enrolment string) {
		t.Helper()
		s, err := students.Service{DB: f.App}.Create(w.a(), f.Actor, students.Input{RegistrationCode: code, FirstName: first, LastName: ptr("Rao"), Gender: "female", DateOfBirth: "2015-06-01"})
		must(t, err)
		e, err := enrollments.Service{DB: f.App}.Create(w.a(), f.Actor, enrollments.Input{StudentID: s.ID, YearID: w.y1, ClassID: class, SectionID: section, RollNumber: ptr(roll)})
		must(t, err)
		return s.ID, e.ID
	}
	for first, roll := range map[string]string{"Asha": "7", "Bina": "3", "Chitra": "1", "Dev": "5", "Esha": "2"} {
		w.five[first], w.fiveEnrol[first] = enrol("S-"+first, first, w.c5, w.secA, roll)
	}
	w.grad1, _ = enrol("G-1", "Farid", w.c8, w.secA, "1")
	w.grad2, _ = enrol("G-2", "Gita", w.c8, w.secA, "2")

	// the other institute has a promotion of its own
	var by, byTo, bc, bc6, bs string
	ins(&by, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y1', 'B1', '2026-04-01', '2027-03-31') RETURNING id::text`, f.Bravo.ID)
	ins(&byTo, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y2', 'B2', '2027-04-01', '2028-03-31') RETURNING id::text`, f.Bravo.ID)
	ins(&bc, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C5', 'Class 5', 5) RETURNING id::text`, f.Bravo.ID)
	ins(&bc6, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C6', 'Class 6', 6) RETURNING id::text`, f.Bravo.ID)
	ins(&bs, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Bravo.ID)
	for _, m := range [][2]string{{by, bc}, {byTo, bc6}} {
		_, err := f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Bravo.ID, m[0], m[1], bs)
		must(t, err)
	}
	bst, err := students.Service{DB: f.App}.Create(modtest.In(f.Bravo), f.Actor, students.Input{RegistrationCode: "B-1", FirstName: "Bravo", Gender: "male", DateOfBirth: "2015-06-01"})
	must(t, err)
	_, err = enrollments.Service{DB: f.App}.Create(modtest.In(f.Bravo), f.Actor, enrollments.Input{StudentID: bst.ID, YearID: by, ClassID: bc, SectionID: bs})
	must(t, err)
	w.bravoBatchSrc.year, w.bravoBatchSrc.toYear, w.bravoBatchSrc.class, w.bravoBatchSrc.section, w.bravoBatchSrc.student = by, byTo, bc, bs, bst.ID
	return w
}

// draft proposes the decisions for class 5 section A (or the given source) and saves them, after mut adjusts the input.
func (w world) draft(t *testing.T, mut func(*promotions.Input)) promotions.Detail {
	t.Helper()
	d, err := w.draftErr(mut)
	must(t, err)
	return d
}

func (w world) draftErr(mut func(*promotions.Input)) (promotions.Detail, error) {
	return w.draftFor(w.y1, w.y2, w.c5, w.secA, mut)
}

func (w world) draftFor(from, to, class, section string, mut func(*promotions.Input)) (promotions.Detail, error) {
	p, err := w.svc.Candidates(w.a(), w.f.Actor, from, to, class, section)
	if err != nil {
		return promotions.Detail{}, err
	}
	in := promotions.Input{FromYearID: from, ToYearID: to, FromClassID: class, FromSectionID: section}
	for _, c := range p.Candidates {
		in.Rows = append(in.Rows, promotions.RowInput{StudentID: c.StudentID, Decision: c.Decision, ToClassID: c.ToClassID})
	}
	if mut != nil {
		mut(&in)
	}
	return w.svc.Create(w.a(), w.f.Actor, in)
}

// setRow changes the row of a student by first name in an input.
func (w world) setRow(in *promotions.Input, first string, mut func(*promotions.RowInput)) {
	for i := range in.Rows {
		if in.Rows[i].StudentID == w.five[first] {
			mut(&in.Rows[i])
		}
	}
}

func (w world) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, q, args...).Scan(&n))
	return n
}

func (w world) newEnrolments(t *testing.T) int {
	return w.count(t, `SELECT count(*) FROM student_enrollments WHERE tenant_id = $1 AND year_id = $2 AND deleted_at IS NULL`, w.f.Alpha.ID, w.y2)
}

func problemCodes(r promotions.OutcomeRow) string {
	codes := make([]string, len(r.Problems))
	for i, p := range r.Problems {
		codes[i] = p.Code
	}
	return strings.Join(codes, ",")
}

func rowOf(o promotions.Outcome, first string) promotions.OutcomeRow {
	for _, r := range o.Rows {
		if strings.HasPrefix(r.StudentName, first+" ") {
			return r
		}
	}
	return promotions.OutcomeRow{}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// ---- the first step

func TestCandidatesProposeTheNextClassAndGraduateTheLast(t *testing.T) {
	w := setup(t)
	if _, err := w.f.Admin.Exec(bg, `UPDATE students SET status = 'paused' WHERE id::text = $1`, w.five["Dev"]); err != nil {
		t.Fatal(err)
	}
	p, err := w.svc.Candidates(w.a(), w.f.Actor, w.y1, w.y2, w.c5, w.secA)
	must(t, err)
	if p.IsLastClass || p.NextClassID == nil || *p.NextClassID != w.c6 || len(p.Candidates) != 5 || p.ExistingBatchID != nil {
		t.Fatalf("proposal %+v", p)
	}
	for _, c := range p.Candidates {
		want := promotions.DecisionPromote
		if c.StudentName == "Dev Rao" {
			want = promotions.DecisionLeaveOut // not active
		}
		if c.Decision != want || (want == promotions.DecisionPromote && (c.ToClassID == nil || *c.ToClassID != w.c6)) {
			t.Errorf("%s proposed %s -> %s", c.StudentName, c.Decision, str(c.ToClassID))
		}
	}
	if p.Candidates[0].StudentName != "Asha Rao" || p.Candidates[4].StudentName != "Esha Rao" {
		t.Errorf("candidates are not in name order: %v", p.Candidates)
	}
	last, err := w.svc.Candidates(w.a(), w.f.Actor, w.y1, w.y2, w.c8, w.secA)
	must(t, err)
	if !last.IsLastClass || last.NextClassID != nil || len(last.Candidates) != 2 || last.Candidates[0].Decision != promotions.DecisionGraduate {
		t.Errorf("last class proposal %+v", last)
	}
	// problems with the request
	for name, c := range map[string]struct{ from, to, class, field string }{
		"same year":        {w.y1, w.y1, w.c5, "toYearId"},
		"new year earlier": {w.y2, w.y1, w.c5, "toYearId"},
		"bad id":           {"x", w.y2, w.c5, "fromYearId"},
	} {
		_, err := w.svc.Candidates(w.a(), w.f.Actor, c.from, c.to, c.class, w.secA)
		var fe crud.FieldErrors
		if !errors.As(err, &fe) || fe[0].Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	// once a batch exists the proposal points at it
	d := w.draft(t, nil)
	p, err = w.svc.Candidates(w.a(), w.f.Actor, w.y1, w.y2, w.c5, w.secA)
	must(t, err)
	if p.ExistingBatchID == nil || *p.ExistingBatchID != d.ID || *p.ExistingBatchStatus != promotions.StatusDraft {
		t.Errorf("existing batch %v %v", p.ExistingBatchID, p.ExistingBatchStatus)
	}
}

func TestDraftIsStoredAndChecked(t *testing.T) {
	w := setup(t)
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Bina", func(r *promotions.RowInput) { r.Decision = promotions.DecisionRepeat; r.ToClassID = nil })
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.Decision = promotions.DecisionLeaveOut; r.ToClassID = ptr(w.c7) })
		in.Note = ptr(" end of year ")
	})
	if d.Status != promotions.StatusDraft || d.Students != 5 || d.Promote != 3 || d.Repeat != 1 || d.LeaveOut != 1 || d.RollMode != promotions.RollAlphabetical ||
		d.Note == nil || *d.Note != "end of year" || d.FromClassName != "Class 5" || d.ToYearName != "Year Y2" {
		t.Fatalf("batch %+v", d.Batch)
	}
	for _, r := range d.Rows {
		switch r.StudentName {
		case "Bina Rao": // a repeating student is always in the source class
			if r.ToClassID == nil || *r.ToClassID != w.c5 {
				t.Errorf("repeat row %+v", r)
			}
		case "Chitra Rao": // a student left out takes no class
			if r.ToClassID != nil {
				t.Errorf("left-out row has class %v", r.ToClassID)
			}
		case "Asha Rao":
			if r.ToClassID == nil || *r.ToClassID != w.c6 || r.RollNumber == nil || *r.RollNumber != "7" {
				t.Errorf("promote row %+v", r)
			}
		}
	}
	// a second live batch for the same source and years is refused, a discarded one frees the slot
	if _, err := w.draftErr(nil); err == nil {
		t.Error("a second batch for the same class section was accepted")
	} else {
		var fe crud.FieldErrors
		if !errors.As(err, &fe) || fe[0].Field != "fromSectionId" || fe[0].Code != "duplicate" {
			t.Errorf("duplicate: %v", err)
		}
	}
	must(t, w.svc.Discard(w.a(), w.f.Actor, d.ID))
	if _, err := w.svc.View(w.a(), w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("a discarded draft is still visible: %v", err)
	}
	d2 := w.draft(t, nil)

	// rows that do not belong are refused with the row named
	p, _ := w.svc.Candidates(w.a(), w.f.Actor, w.y1, w.y2, w.c8, w.secA)
	cases := map[string]func(*promotions.UpdateInput){
		"student of another class": func(in *promotions.UpdateInput) { in.Rows[0].StudentID = p.Candidates[0].StudentID },
		"unknown decision":         func(in *promotions.UpdateInput) { in.Rows[0].Decision = "skip" },
		"student twice":            func(in *promotions.UpdateInput) { in.Rows[1].StudentID = in.Rows[0].StudentID },
		"bad roll mode":            func(in *promotions.UpdateInput) { in.RollMode = "random" },
		"class not found":          func(in *promotions.UpdateInput) { in.Rows[0].ToClassID = ptr("00000000-0000-0000-0000-000000000001") },
		"roll number too long":     func(in *promotions.UpdateInput) { in.Rows[0].RollNumber = ptr(strings.Repeat("9", 21)) },
	}
	for name, mut := range cases {
		in := promotions.UpdateInput{Rows: toRows(d2)}
		mut(&in)
		_, err := w.svc.Update(w.a(), w.f.Actor, d2.ID, in)
		var fe crud.FieldErrors
		if !errors.As(err, &fe) {
			t.Errorf("%s: want field errors, got %v", name, err)
		}
	}
	// a promoted student without a class goes to the next class
	rows := toRows(d2)
	rows[0].ToClassID = nil
	d3, err := w.svc.Update(w.a(), w.f.Actor, d2.ID, promotions.UpdateInput{Rows: rows})
	must(t, err)
	if d3.Rows[0].ToClassID == nil || *d3.Rows[0].ToClassID != w.c6 {
		t.Errorf("default class %v", d3.Rows[0].ToClassID)
	}
	if _, err := w.svc.Update(w.a(), w.f.Actor, d2.ID, promotions.UpdateInput{Rows: nil}); !errors.Is(err, promotions.ErrTooMany) {
		t.Errorf("empty rows: %v", err)
	}
}

func toRows(d promotions.Detail) []promotions.RowInput {
	rows := make([]promotions.RowInput, len(d.Rows))
	for i, r := range d.Rows {
		rows[i] = promotions.RowInput{StudentID: r.StudentID, Decision: r.Decision, ToClassID: r.ToClassID, ToSectionID: r.ToSectionID, RollNumber: r.NewRollNumber}
	}
	return rows
}

// ---- preview and commit

func TestPreviewIsWhatCommitDoes(t *testing.T) {
	w := setup(t)
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Bina", func(r *promotions.RowInput) { r.Decision = promotions.DecisionRepeat; r.ToClassID = nil })
		w.setRow(in, "Dev", func(r *promotions.RowInput) { r.Decision = promotions.DecisionLeaveOut })
	})
	before := w.newEnrolments(t)
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	if w.newEnrolments(t) != before || pre.Committed || pre.ProblemCount != 0 || pre.Enrolled != 4 || pre.LeftOut != 1 {
		t.Fatalf("preview %+v (enrolments %d -> %d)", pre, before, w.newEnrolments(t))
	}
	if b, _ := w.svc.View(w.a(), w.f.Actor, d.ID); b.Status != promotions.StatusDraft {
		t.Fatalf("the preview changed the batch: %s", b.Status)
	}
	com, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	if !com.Committed || com.AlreadyCommitted || com.Batch.Status != promotions.StatusCommitted || com.Batch.UndoUntil == nil || !com.Batch.CanUndo || com.Enrolled != 4 {
		t.Fatalf("commit %+v", com)
	}
	if w.newEnrolments(t) != before+4 {
		t.Errorf("new enrolments %d, want %d", w.newEnrolments(t), before+4)
	}
	for i, p := range pre.Rows {
		c := com.Rows[i]
		if p.StudentID != c.StudentID || p.Outcome != c.Outcome || str(p.ToClassID) != str(c.ToClassID) || str(p.ToSectionID) != str(c.ToSectionID) || str(p.RollNumber) != str(c.RollNumber) {
			t.Errorf("row %d: preview %+v, commit %+v", i, p, c)
		}
	}
	// rolls are numbered per target section in name order: Asha 1, Chitra 2, Esha 3 in class 6 A; Bina, who repeats, is
	// the only one in class 5 A (Dev is left out)
	want := map[string][3]string{"Asha": {w.c6, w.secA, "1"}, "Bina": {w.c5, w.secA, "1"}, "Chitra": {w.c6, w.secA, "2"}, "Esha": {w.c6, w.secA, "3"}}
	for first, exp := range want {
		var class, section, roll, status string
		var date *string
		must(t, w.f.Admin.QueryRow(bg, `SELECT class_id::text, section_id::text, roll_number, status, enrollment_date::text FROM student_enrollments
			WHERE student_id::text = $1 AND year_id::text = $2 AND deleted_at IS NULL`, w.five[first], w.y2).Scan(&class, &section, &roll, &status, &date))
		if class != exp[0] || section != exp[1] || roll != exp[2] || status != "enrolled" || date == nil || *date != "2027-04-01" {
			t.Errorf("%s enrolment %s %s %s %s %v", first, class, section, roll, status, date)
		}
	}
	if w.count(t, `SELECT count(*) FROM student_enrollments WHERE student_id::text = $1 AND year_id::text = $2`, w.five["Dev"], w.y2) != 0 {
		t.Error("the student left out was enrolled")
	}
	// the year being closed is untouched
	if w.count(t, `SELECT count(*) FROM student_enrollments WHERE year_id::text = $1 AND status = 'enrolled' AND deleted_at IS NULL AND class_id::text = $2`, w.y1, w.c5) != 5 {
		t.Error("the enrolments of the year being closed changed")
	}
}

func TestCommitTwiceChangesNothing(t *testing.T) {
	w := setup(t)
	d := w.draft(t, nil)
	first, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	enrolments, audits := w.newEnrolments(t), w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.commit'`)
	second, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	if !second.AlreadyCommitted || second.Committed || second.Enrolled != first.Enrolled || w.newEnrolments(t) != enrolments ||
		w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.commit'`) != audits {
		t.Errorf("second commit %+v", second)
	}
	for i := range first.Rows {
		if str(first.Rows[i].EnrollmentID) != str(second.Rows[i].EnrollmentID) || str(second.Rows[i].EnrollmentID) == "<nil>" {
			t.Errorf("row %d enrolment %s vs %s", i, str(first.Rows[i].EnrollmentID), str(second.Rows[i].EnrollmentID))
		}
	}
	// a draft cannot be edited, previewed or discarded once committed
	if _, err := w.svc.Update(w.a(), w.f.Actor, d.ID, promotions.UpdateInput{Rows: toRows(d)}); !errors.Is(err, promotions.ErrNotDraft) {
		t.Errorf("update after commit: %v", err)
	}
	if _, err := w.svc.Preview(w.a(), w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotDraft) {
		t.Errorf("preview after commit: %v", err)
	}
	if err := w.svc.Discard(w.a(), w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotDraft) {
		t.Errorf("discard after commit: %v", err)
	}
}

func TestSimultaneousCommitsMakeOneSetOfEnrolments(t *testing.T) {
	w := setup(t)
	d := w.draft(t, nil)
	var wg sync.WaitGroup
	results := make([]promotions.Outcome, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
		}()
	}
	wg.Wait()
	committed := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("commit %d: %v", i, errs[i])
		}
		if results[i].Committed {
			committed++
		}
	}
	if committed != 1 || w.newEnrolments(t) != 5 {
		t.Errorf("%d commits made it, %d enrolments", committed, w.newEnrolments(t))
	}
}

func TestCommitIsAllOrNothing(t *testing.T) {
	w := setup(t)
	// Bina's section is not mapped for class 6 in the new year; Farid-style graduation is in another batch, so give this
	// batch a graduating student too by sending Chitra to graduate.
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Bina", func(r *promotions.RowInput) { r.ToSectionID = ptr(w.secC) })
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.Decision = promotions.DecisionGraduate })
	})
	out, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	var nr promotions.NotReady
	if !errors.As(err, &nr) || out.ProblemCount != 1 || problemCodes(rowOf(out, "Bina")) != "section_not_mapped" {
		t.Fatalf("commit with a problem: %v %+v", err, out)
	}
	if w.newEnrolments(t) != 0 || w.count(t, `SELECT count(*) FROM students WHERE status = 'alumni'`) != 0 {
		t.Error("a refused commit left changes behind")
	}
	if b, _ := w.svc.View(w.a(), w.f.Actor, d.ID); b.Status != promotions.StatusDraft {
		t.Errorf("status %s", b.Status)
	}
	if w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.commit_refused'`) != 1 {
		t.Error("the refusal is not in the audit trail")
	}
	// fixing the row lets the same batch commit
	_, err = w.svc.Update(w.a(), w.f.Actor, d.ID, promotions.UpdateInput{Rows: func() []promotions.RowInput {
		rows := toRows(d)
		for i := range rows {
			if rows[i].StudentID == w.five["Bina"] {
				rows[i].ToSectionID = nil
			}
		}
		return rows
	}()})
	must(t, err)
	if _, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil); err != nil {
		t.Fatal(err)
	}
	if w.newEnrolments(t) != 4 {
		t.Errorf("enrolments %d, want 4", w.newEnrolments(t))
	}
}

// A failure the rules cannot foresee, in the middle of the writes, still keeps nothing.
func TestAFailureWhileWritingKeepsNothing(t *testing.T) {
	w := setup(t)
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Esha", func(r *promotions.RowInput) { r.Decision = promotions.DecisionGraduate })
	})
	_, err := w.f.Admin.Exec(bg, `CREATE FUNCTION fail_third() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.year_id::text = '`+w.y2+`' AND NEW.roll_number = '3' THEN RAISE EXCEPTION 'disk on fire'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER fail_third BEFORE INSERT ON student_enrollments FOR EACH ROW EXECUTE FUNCTION fail_third();`)
	must(t, err)
	if _, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil); err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("commit: %v", err)
	}
	if w.newEnrolments(t) != 0 || w.count(t, `SELECT count(*) FROM students WHERE status = 'alumni'`) != 0 || w.count(t, `SELECT count(*) FROM promotion_batches WHERE status = 'committed'`) != 0 {
		t.Error("a failed commit left changes behind")
	}
}

func TestGraduatesBecomeAlumniAndUndoRestoresThem(t *testing.T) {
	w := setup(t)
	d, err := w.draftFor(w.y1, w.y2, w.c8, w.secA, nil)
	must(t, err)
	if d.Graduate != 2 {
		t.Fatalf("graduates %d", d.Graduate)
	}
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	if pre.Graduated != 2 || pre.Enrolled != 0 || pre.Rows[0].Outcome != promotions.OutcomeGraduate || pre.Rows[0].ToClassID != nil {
		t.Fatalf("preview %+v", pre)
	}
	if w.count(t, `SELECT count(*) FROM students WHERE status = 'alumni'`) != 0 {
		t.Fatal("the preview changed a student")
	}
	com, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	if com.Graduated != 2 || w.newEnrolments(t) != 0 {
		t.Fatalf("commit %+v", com)
	}
	for _, id := range []string{w.grad1, w.grad2} {
		var status string
		must(t, w.f.Admin.QueryRow(bg, `SELECT status FROM students WHERE id::text = $1`, id).Scan(&status))
		if status != "alumni" {
			t.Errorf("student %s is %s", id, status)
		}
	}
	if w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'students.update' AND resource_type = 'student'`) != 2 {
		t.Error("each student's status change is not audited")
	}
	und, err := w.svc.Undo(w.a(), w.f.Actor, d.ID, "wrong class")
	must(t, err)
	if !und.Undone || und.Batch.Status != promotions.StatusUndone || und.Batch.UndoReason == nil || *und.Batch.UndoReason != "wrong class" {
		t.Fatalf("undo %+v", und)
	}
	if w.count(t, `SELECT count(*) FROM students WHERE status = 'alumni'`) != 0 {
		t.Error("graduates were not restored")
	}
}

func TestSectionsMustBeMappedForTheNewYear(t *testing.T) {
	w := setup(t)
	// class 7 is mapped to section A only; class 6 to A and B; section C is mapped nowhere. Section B of class 5 has no
	// class 7 in the new year, so a student of 5B sent to class 7 has no same section there.
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Asha", func(r *promotions.RowInput) { r.ToSectionID = ptr(w.secB) })                                      // mapped for class 6: fine
		w.setRow(in, "Bina", func(r *promotions.RowInput) { r.ToSectionID = ptr(w.secC) })                                      // mapped nowhere
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.ToClassID = ptr(w.c7) })                                        // 7A exists: same section is fine
		w.setRow(in, "Dev", func(r *promotions.RowInput) { r.ToClassID = ptr(w.c7); r.ToSectionID = ptr(w.secB) })              // 7B does not exist
		w.setRow(in, "Esha", func(r *promotions.RowInput) { r.ToClassID = ptr(w.c5); r.Decision = promotions.DecisionPromote }) // same class
	})
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	got := map[string]string{}
	for _, f := range []string{"Asha", "Bina", "Chitra", "Dev", "Esha"} {
		got[f] = rowOf(pre, f).Outcome + ":" + problemCodes(rowOf(pre, f))
	}
	want := map[string]string{"Asha": "enrol:", "Bina": "problem:section_not_mapped", "Chitra": "enrol:", "Dev": "problem:section_not_mapped", "Esha": "problem:same_class"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if str(rowOf(pre, "Asha").ToSectionID) != w.secB {
		t.Errorf("Asha section %s", str(rowOf(pre, "Asha").ToSectionID))
	}
}

func TestOtherRulesOfARow(t *testing.T) {
	w := setup(t)
	// a student who already has a new-year enrolment, one who is not active, one promoted backwards, one whose
	// enrolment moved after the draft was saved
	_, err := enrollments.Service{DB: w.f.App}.Create(w.a(), w.f.Actor, enrollments.Input{StudentID: w.five["Asha"], YearID: w.y2, ClassID: w.c6, SectionID: w.secA, RollNumber: ptr("9")})
	must(t, err)
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.ToClassID = ptr(w.c5) }) // a class that is not after class 5
	})
	for _, q := range []string{
		fmt.Sprintf(`UPDATE students SET status = 'transferred' WHERE id::text = '%s'`, w.five["Bina"]),
		fmt.Sprintf(`UPDATE student_enrollments SET status = 'withdrawn' WHERE id::text = '%s'`, w.fiveEnrol["Dev"]),
		fmt.Sprintf(`UPDATE classes SET status = 'inactive' WHERE id::text = '%s'`, w.c7),
	} {
		_, err := w.f.Admin.Exec(bg, q)
		must(t, err)
	}
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	want := map[string]string{"Asha": "already_enrolled", "Bina": "student_not_active", "Chitra": "same_class", "Dev": "source_changed", "Esha": ""}
	for first, code := range want {
		if got := problemCodes(rowOf(pre, first)); got != code {
			t.Errorf("%s: %q, want %q", first, got, code)
		}
	}
	// an inactive target class and one that comes before
	d2 := w.draft2(t, func(in *promotions.Input) {
		w.setRow(in, "Esha", func(r *promotions.RowInput) { r.ToClassID = ptr(w.c7) })
	})
	pre2, err := w.svc.Preview(w.a(), w.f.Actor, d2.ID)
	must(t, err)
	if got := problemCodes(rowOf(pre2, "Esha")); got != "class_inactive" {
		t.Errorf("inactive class: %q", got)
	}
}

// draft2 replaces the decisions of the only draft of class 5 A.
func (w world) draft2(t *testing.T, mut func(*promotions.Input)) promotions.Detail {
	t.Helper()
	p, err := w.svc.Candidates(w.a(), w.f.Actor, w.y1, w.y2, w.c5, w.secA)
	must(t, err)
	in := promotions.Input{FromYearID: w.y1, ToYearID: w.y2, FromClassID: w.c5, FromSectionID: w.secA}
	for _, c := range p.Candidates {
		in.Rows = append(in.Rows, promotions.RowInput{StudentID: c.StudentID, Decision: c.Decision, ToClassID: c.ToClassID})
	}
	mut(&in)
	if p.ExistingBatchID == nil {
		d, err := w.svc.Create(w.a(), w.f.Actor, in)
		must(t, err)
		return d
	}
	d, err := w.svc.Update(w.a(), w.f.Actor, *p.ExistingBatchID, promotions.UpdateInput{RollMode: in.RollMode, Rows: in.Rows})
	must(t, err)
	return d
}

func TestRollNumberModes(t *testing.T) {
	w := setup(t)
	// the target section already has students, numbered 1 to 10 and "A-1"
	for i, roll := range []string{"1", "10", "A-1"} {
		s, err := students.Service{DB: w.f.App}.Create(w.a(), w.f.Actor, students.Input{RegistrationCode: fmt.Sprintf("X-%d", i), FirstName: "Zed", Gender: "male", DateOfBirth: "2015-01-01"})
		must(t, err)
		_, err = enrollments.Service{DB: w.f.App}.Create(w.a(), w.f.Actor, enrollments.Input{StudentID: s.ID, YearID: w.y2, ClassID: w.c6, SectionID: w.secA, RollNumber: ptr(roll)})
		must(t, err)
	}
	rolls := func(o promotions.Outcome) string {
		var parts []string
		for _, r := range o.Rows {
			parts = append(parts, strings.Fields(r.StudentName)[0]+"="+str(r.RollNumber))
		}
		return strings.Join(parts, " ")
	}
	d := w.draft(t, nil)
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	if got := rolls(pre); got != "Asha=11 Bina=12 Chitra=13 Dev=14 Esha=15" { // after the highest numeric roll, in name order
		t.Errorf("alphabetical: %s", got)
	}
	// keep: 7 3 1 5 2 carried; 1 is taken by Zed
	w.draft2(t, func(in *promotions.Input) { in.RollMode = promotions.RollKeep })
	pre, err = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	if got := problemCodes(rowOf(pre, "Chitra")); got != "roll_taken" || pre.ProblemCount != 1 || str(rowOf(pre, "Asha").RollNumber) != "7" {
		t.Errorf("keep: %s (%d problems) %s", got, pre.ProblemCount, rolls(pre))
	}
	// a typed roll fixes it; two students cannot be given the same one
	w.draft2(t, func(in *promotions.Input) {
		in.RollMode = promotions.RollKeep
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.RollNumber = ptr("21") })
	})
	pre, _ = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	if pre.ProblemCount != 0 || str(rowOf(pre, "Chitra").RollNumber) != "21" {
		t.Errorf("typed: %d problems %s", pre.ProblemCount, rolls(pre))
	}
	w.draft2(t, func(in *promotions.Input) {
		in.RollMode = promotions.RollKeep
		w.setRow(in, "Chitra", func(r *promotions.RowInput) { r.RollNumber = ptr("30") })
		w.setRow(in, "Dev", func(r *promotions.RowInput) { r.RollNumber = ptr("30") })
	})
	pre, _ = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	if pre.ProblemCount != 1 || problemCodes(rowOf(pre, "Dev")) != "roll_taken" {
		t.Errorf("same typed roll twice: %d problems %s", pre.ProblemCount, rolls(pre))
	}
	// blank leaves them empty; typed still wins
	w.draft2(t, func(in *promotions.Input) {
		in.RollMode = promotions.RollBlank
		w.setRow(in, "Esha", func(r *promotions.RowInput) { r.RollNumber = ptr("40") })
	})
	pre, _ = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	if got := rolls(pre); got != "Asha=<nil> Bina=<nil> Chitra=<nil> Dev=<nil> Esha=40" {
		t.Errorf("blank: %s", got)
	}
	// and the commit gives exactly the previewed numbers
	w.draft2(t, func(in *promotions.Input) {})
	pre, _ = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	com, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	if rolls(pre) != rolls(com) {
		t.Errorf("preview %s, commit %s", rolls(pre), rolls(com))
	}
}

func TestWarningsForCapacityAndStudentsNotInTheBatch(t *testing.T) {
	w := setup(t)
	_, err := w.f.Admin.Exec(bg, `UPDATE class_section_mappings SET capacity = 3 WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3`, w.y2, w.c6, w.secA)
	must(t, err)
	d := w.draft(t, func(in *promotions.Input) { in.Rows = in.Rows[:4] })
	pre, err := w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	joined := strings.Join(pre.Warnings, " | ")
	if len(pre.Warnings) != 2 || !strings.Contains(joined, "would have 4 students; its capacity is 3") || !strings.Contains(joined, "1 student is enrolled in this class section but not in this batch") {
		t.Errorf("warnings: %v", pre.Warnings)
	}
	if pre.ProblemCount != 0 {
		t.Error("a warning is not a problem")
	}
}

// ---- dues gate

type stubGate struct{ blocked map[string]string }

func (g stubGate) Blocked(_ context.Context, _ pgx.Tx, _ string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if r, ok := g.blocked[id]; ok {
			out[id] = r
		}
	}
	return out, nil
}

func TestADuesGateBlocksUnlessOverridden(t *testing.T) {
	w := setup(t)
	svc := promotions.Service{DB: w.f.App, Gates: []promotions.Gate{stubGate{blocked: map[string]string{w.five["Bina"]: "Fees of 2026-27 are pending: Rs 4,500."}}}}
	w.svc = svc
	d := w.draft(t, func(in *promotions.Input) {
		w.setRow(in, "Dev", func(r *promotions.RowInput) { r.Decision = promotions.DecisionLeaveOut })
	})
	pre, err := svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	if got := problemCodes(rowOf(pre, "Bina")); got != "dues_pending" || pre.ProblemCount != 1 || !strings.Contains(rowOf(pre, "Bina").Problems[0].Message, "Rs 4,500") {
		t.Fatalf("preview %+v", pre)
	}
	if _, err := svc.Commit(w.a(), w.f.Actor, d.ID, nil); err == nil || w.newEnrolments(t) != 0 {
		t.Fatalf("commit with dues: %v (%d enrolments)", err, w.newEnrolments(t))
	}
	// an override needs a reason
	_, err = svc.Commit(w.a(), w.f.Actor, d.ID, &promotions.DuesOverride{Reason: "  "})
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || fe[0].Field != "reason" || w.newEnrolments(t) != 0 {
		t.Fatalf("override without a reason: %v", err)
	}
	com, err := svc.Commit(w.a(), w.f.Actor, d.ID, &promotions.DuesOverride{Reason: "Principal agreed: instalment plan"})
	must(t, err)
	if !com.DuesOverridden || com.Batch.DuesOverrideReason == nil || *com.Batch.DuesOverrideReason != "Principal agreed: instalment plan" || w.newEnrolments(t) != 4 {
		t.Errorf("override commit %+v", com)
	}
	var after string
	must(t, w.f.Admin.QueryRow(bg, `SELECT after::text FROM audit_log WHERE action = 'promotions.commit'`).Scan(&after))
	if !strings.Contains(after, "instalment plan") {
		t.Errorf("the override is not in the audit trail: %s", after)
	}
}

func TestAnOverrideThatBlockedNobodyIsNotRecorded(t *testing.T) {
	w := setup(t)
	w.svc = promotions.Service{DB: w.f.App, Gates: []promotions.Gate{stubGate{blocked: map[string]string{}}}}
	d := w.draft(t, nil)
	com, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, &promotions.DuesOverride{Reason: "just in case"})
	must(t, err)
	if com.DuesOverridden || com.Batch.DuesOverrideReason != nil {
		t.Errorf("override recorded without anything blocked: %+v", com.Batch)
	}
}

// ---- undo

func committed(t *testing.T, w world) promotions.Detail {
	t.Helper()
	d := w.draft(t, nil)
	_, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	return d
}

func reasonCodes(err error) string {
	var ur promotions.UndoRefused
	if !errors.As(err, &ur) {
		return fmt.Sprint("not refused: ", err)
	}
	codes := make([]string, len(ur.Reasons))
	for i, r := range ur.Reasons {
		codes[i] = r.Code
	}
	return strings.Join(codes, ",")
}

func TestUndoRemovesWhatTheBatchMadeAndFreesTheSlot(t *testing.T) {
	w := setup(t)
	d := committed(t, w)
	if _, err := w.svc.Undo(w.a(), w.f.Actor, d.ID, "  "); err == nil {
		t.Error("undo without a reason")
	}
	out, err := w.svc.Undo(w.a(), w.f.Actor, d.ID, "chose the wrong year")
	must(t, err)
	if !out.Undone || w.newEnrolments(t) != 0 || out.Batch.Status != promotions.StatusUndone || out.Batch.CanUndo {
		t.Fatalf("undo %+v (%d enrolments)", out, w.newEnrolments(t))
	}
	if w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'student_enrollments.delete'`) != 5 || w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.undo'`) != 1 {
		t.Error("the undo is not fully audited")
	}
	// undoing again changes nothing
	again, err := w.svc.Undo(w.a(), w.f.Actor, d.ID, "again")
	must(t, err)
	if !again.AlreadyUndone || again.Undone || w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.undo'`) != 1 {
		t.Errorf("second undo %+v", again)
	}
	// the undone batch cannot be committed, but the class section can be promoted afresh
	if _, err := w.svc.Commit(w.a(), w.f.Actor, d.ID, nil); !errors.Is(err, promotions.ErrUndone) {
		t.Errorf("commit of an undone batch: %v", err)
	}
	d2 := w.draft(t, nil)
	if _, err := w.svc.Commit(w.a(), w.f.Actor, d2.ID, nil); err != nil || w.newEnrolments(t) != 5 {
		t.Errorf("recommit: %v (%d enrolments)", err, w.newEnrolments(t))
	}
	// there is nothing to undo on a draft
	d3, err := w.draftFor(w.y1, w.y2, w.c8, w.secA, nil)
	must(t, err)
	if _, err := w.svc.Undo(w.a(), w.f.Actor, d3.ID, "x"); !errors.Is(err, promotions.ErrNotCommitted) {
		t.Errorf("undo of a draft: %v", err)
	}
}

func TestUndoIsRefusedWhenSomethingChanged(t *testing.T) {
	type tc struct {
		name, want string
		change     func(t *testing.T, w world, d promotions.Detail)
	}
	for _, c := range []tc{
		{"window closed", "window_closed", func(t *testing.T, w world, d promotions.Detail) {
			_, err := w.f.Admin.Exec(bg, `UPDATE promotion_batches SET undo_until = now() - interval '1 minute' WHERE id::text = $1`, d.ID)
			must(t, err)
		}},
		{"enrolment edited", "enrolment_changed", func(t *testing.T, w world, d promotions.Detail) {
			var id string
			must(t, w.f.Admin.QueryRow(bg, `SELECT id::text FROM student_enrollments WHERE student_id::text = $1 AND year_id::text = $2`, w.five["Asha"], w.y2).Scan(&id))
			e, err := enrollments.Service{DB: w.f.App}.Get(w.a(), id)
			must(t, err)
			_, err = enrollments.Service{DB: w.f.App}.Update(w.a(), w.f.Actor, id, enrollments.Input{StudentID: e.StudentID, YearID: e.YearID, ClassID: e.ClassID, SectionID: e.SectionID, RollNumber: ptr("99"), Status: "enrolled"})
			must(t, err)
		}},
		{"enrolment deleted", "enrolment_removed", func(t *testing.T, w world, d promotions.Detail) {
			_, err := w.f.Admin.Exec(bg, `UPDATE student_enrollments SET deleted_at = now() WHERE student_id::text = $1 AND year_id::text = $2`, w.five["Asha"], w.y2)
			must(t, err)
		}},
		{"promoted onward", "promoted_onward", func(t *testing.T, w world, d promotions.Detail) {
			next, err := w.draftFor(w.y2, w.y3, w.c6, w.secA, nil)
			must(t, err)
			_, err = w.svc.Commit(w.a(), w.f.Actor, next.ID, nil)
			must(t, err)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := setup(t)
			// the onward batch needs class 6 to have a next class mapped in year 3: class 7 section A is
			d := committed(t, w)
			c.change(t, w, d)
			before := w.newEnrolments(t)
			_, err := w.svc.Undo(w.a(), w.f.Actor, d.ID, "undo")
			if got := reasonCodes(err); !strings.Contains(got, c.want) {
				t.Fatalf("undo: %s (%v), want %s", got, err, c.want)
			}
			if w.newEnrolments(t) != before || w.count(t, `SELECT count(*) FROM promotion_batches WHERE status = 'undone'`) != 0 {
				t.Error("a refused undo changed something")
			}
			if w.count(t, `SELECT count(*) FROM audit_log WHERE action = 'promotions.undo_refused'`) != 1 {
				t.Error("the refusal is not in the audit trail")
			}
		})
	}
	t.Run("graduate status changed", func(t *testing.T) {
		w := setup(t)
		d, err := w.draftFor(w.y1, w.y2, w.c8, w.secA, nil)
		must(t, err)
		_, err = w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
		must(t, err)
		_, err = w.f.Admin.Exec(bg, `UPDATE students SET status = 'transferred' WHERE id::text = $1`, w.grad1)
		must(t, err)
		_, err = w.svc.Undo(w.a(), w.f.Actor, d.ID, "undo")
		if got := reasonCodes(err); got != "status_changed" {
			t.Errorf("undo: %s", got)
		}
		var status string
		must(t, w.f.Admin.QueryRow(bg, `SELECT status FROM students WHERE id::text = $1`, w.grad2).Scan(&status))
		if status != "alumni" {
			t.Errorf("the other graduate was restored although the undo was refused: %s", status)
		}
	})
}

// ---- institutes and audit

func TestBatchesBelongToOneInstitute(t *testing.T) {
	w := setup(t)
	d := w.draft(t, nil)
	b := modtest.In(w.f.Bravo)
	if _, err := w.svc.View(b, w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("view: %v", err)
	}
	if _, err := w.svc.Preview(b, w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("preview: %v", err)
	}
	if _, err := w.svc.Commit(b, w.f.Actor, d.ID, nil); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("commit: %v", err)
	}
	if _, err := w.svc.Undo(b, w.f.Actor, d.ID, "x"); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("undo: %v", err)
	}
	if err := w.svc.Discard(b, w.f.Actor, d.ID); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("discard: %v", err)
	}
	q, _ := crud.ParseList(1, 50, "", "", promotions.SortKeys, promotions.DefaultSort)
	if page, err := w.svc.List(b, w.f.Actor, q, promotions.Filter{}); err != nil || page.Total != 0 {
		t.Errorf("list: %v %+v", err, page)
	}
	// alpha's years, class and students are unknown to bravo, and bravo's to alpha
	if _, err := w.svc.Candidates(b, w.f.Actor, w.y1, w.y2, w.c5, w.secA); err == nil {
		t.Error("bravo proposed a promotion from alpha's data")
	}
	if _, err := w.svc.Candidates(w.a(), w.f.Actor, w.bravoBatchSrc.year, w.bravoBatchSrc.toYear, w.bravoBatchSrc.class, w.bravoBatchSrc.section); err == nil {
		t.Error("alpha proposed a promotion from bravo's data")
	}
	_, err := w.svc.Create(b, w.f.Actor, promotions.Input{FromYearID: w.bravoBatchSrc.year, ToYearID: w.bravoBatchSrc.toYear, FromClassID: w.bravoBatchSrc.class, FromSectionID: w.bravoBatchSrc.section,
		Rows: []promotions.RowInput{{StudentID: w.five["Asha"], Decision: promotions.DecisionPromote}}})
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || fe[0].Code != "not_enrolled" {
		t.Errorf("bravo promoting alpha's student: %v", err)
	}
	bd, err := w.svc.Create(b, w.f.Actor, promotions.Input{FromYearID: w.bravoBatchSrc.year, ToYearID: w.bravoBatchSrc.toYear, FromClassID: w.bravoBatchSrc.class, FromSectionID: w.bravoBatchSrc.section,
		Rows: []promotions.RowInput{{StudentID: w.bravoBatchSrc.student, Decision: promotions.DecisionPromote}}})
	must(t, err)
	if _, err := w.svc.Commit(b, w.f.Actor, bd.ID, nil); err != nil {
		t.Fatal(err)
	}
	if w.newEnrolments(t) != 0 {
		t.Error("bravo's commit created enrolments in alpha")
	}
	if _, err := w.svc.Commit(w.a(), w.f.Actor, bd.ID, nil); !errors.Is(err, promotions.ErrNotFound) {
		t.Errorf("alpha committing bravo's batch: %v", err)
	}
}

func TestEveryStepIsAudited(t *testing.T) {
	w := setup(t)
	q, _ := crud.ParseList(1, 50, "", "", promotions.SortKeys, promotions.DefaultSort)
	d := w.draft(t, nil)
	_, err := w.svc.Update(w.a(), w.f.Actor, d.ID, promotions.UpdateInput{RollMode: promotions.RollKeep, Rows: toRows(d)})
	must(t, err)
	_, err = w.svc.Preview(w.a(), w.f.Actor, d.ID)
	must(t, err)
	_, err = w.svc.Commit(w.a(), w.f.Actor, d.ID, nil)
	must(t, err)
	_, err = w.svc.View(w.a(), w.f.Actor, d.ID)
	must(t, err)
	_, err = w.svc.List(w.a(), w.f.Actor, q, promotions.Filter{})
	must(t, err)
	_, err = w.svc.Undo(w.a(), w.f.Actor, d.ID, "why")
	must(t, err)
	audits := func(action string) int {
		return w.count(t, `SELECT count(*) FROM audit_log WHERE action = $1 AND tenant_id = $2 AND actor_id::text = $3`, action, w.f.Alpha.ID, w.f.Actor.UserID)
	}
	for action, n := range map[string]int{"promotions.candidates": 1, "promotions.create": 1, "promotions.update": 1, "promotions.preview": 1, "promotions.commit": 1,
		"promotions.view": 1, "promotions.list": 1, "promotions.undo": 1, "student_enrollments.delete": 5} {
		if got := audits(action); got != n {
			t.Errorf("%s audited %d times, want %d", action, got, n)
		}
	}
	// seven enrolments were set up and five committed; the preview's own are rolled back with it
	if got := audits("student_enrollments.create"); got != 12 {
		t.Errorf("enrolment create rows %d, want 12", got)
	}
}

func TestListFiltersAndPages(t *testing.T) {
	w := setup(t)
	w.draft(t, nil)
	if _, err := w.draftFor(w.y1, w.y2, w.c8, w.secA, nil); err != nil {
		t.Fatal(err)
	}
	q, _ := crud.ParseList(1, 1, "", "", promotions.SortKeys, promotions.DefaultSort)
	page, err := w.svc.List(w.a(), w.f.Actor, q, promotions.Filter{})
	must(t, err)
	if page.Total != 2 || len(page.Items) != 1 {
		t.Errorf("page %+v", page)
	}
	q, _ = crud.ParseList(1, 50, "", "", promotions.SortKeys, promotions.DefaultSort)
	page, _ = w.svc.List(w.a(), w.f.Actor, q, promotions.Filter{ClassID: w.c8})
	if page.Total != 1 || page.Items[0].FromClassName != "Class 8" {
		t.Errorf("class filter %+v", page)
	}
	page, _ = w.svc.List(w.a(), w.f.Actor, q, promotions.Filter{Status: promotions.StatusCommitted})
	if page.Total != 0 {
		t.Errorf("status filter %+v", page)
	}
}
