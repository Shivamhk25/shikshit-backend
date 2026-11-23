package rollover_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/ten/rollover"
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

// world: year A (2026-27) with two terms, three class-section mappings (one room deleted afterwards), three subject
// mappings, four holidays and one event; empty year B (2027-28) right after it; B2 (2028-04-01 to 2028-12-31), a short
// year two years later. A second institute has its own pair of years.
type world struct {
	f                  modtest.Fixture
	svc                rollover.Service
	a, b, b2           string
	c5, c6, secA, secB string
	math, eng          string
	room1, room2       string
	bravoA, bravoB     string
}

func (w world) ctx() context.Context { return modtest.In(w.f.Alpha) }

func (w world) in(step string) rollover.Input {
	return rollover.Input{Step: step, FromYearID: w.a, ToYearID: w.b, Confirm: true}
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, svc: rollover.Service{DB: f.App}}
	ins := func(dst *string, q string, args ...any) {
		t.Helper()
		must(t, f.Admin.QueryRow(bg, q, args...).Scan(dst))
	}
	exec := func(q string, args ...any) {
		t.Helper()
		_, err := f.Admin.Exec(bg, q, args...)
		must(t, err)
	}
	year := func(dst *string, code, from, to string) {
		ins(dst, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, $2::text, 'Year ' || $2::text, $3::date, $4::date) RETURNING id::text`, f.Alpha.ID, code, from, to)
	}
	year(&w.a, "A", "2026-04-01", "2027-03-31")
	year(&w.b, "B", "2027-04-01", "2028-03-31")
	year(&w.b2, "B2", "2028-04-01", "2028-12-31")
	exec(`UPDATE academic_years SET is_current = true WHERE id = $1`, w.a)

	for _, c := range []struct {
		dst        *string
		code, name string
		order      int
	}{{&w.c5, "C5", "Class 5", 5}, {&w.c6, "C6", "Class 6", 6}} {
		ins(c.dst, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, $2, $3, $4) RETURNING id::text`, f.Alpha.ID, c.code, c.name, c.order)
	}
	ins(&w.secA, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Alpha.ID)
	ins(&w.secB, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'B', 'Section B') RETURNING id::text`, f.Alpha.ID)
	for _, s := range []struct {
		dst        *string
		code, name string
	}{{&w.math, "MATH", "Mathematics"}, {&w.eng, "ENG", "English"}} {
		ins(s.dst, `INSERT INTO subjects (tenant_id, code, name, category, area, credits) VALUES ($1, $2, $3, 'core', 'languages', 1) RETURNING id::text`, f.Alpha.ID, s.code, s.name)
	}
	ins(&w.room1, `INSERT INTO rooms (tenant_id, name, capacity) VALUES ($1, 'Room 1', 40) RETURNING id::text`, f.Alpha.ID)
	ins(&w.room2, `INSERT INTO rooms (tenant_id, name, capacity) VALUES ($1, 'Room 2', 40) RETURNING id::text`, f.Alpha.ID)

	for _, tm := range [][4]string{{"T1", "First term", "2026-04-01", "2026-09-30"}, {"T2", "Second term", "2026-10-01", "2027-03-31"}} {
		exec(`INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date, color) VALUES ($1, $2, $3, $4, $5::date, $6::date, '#2F5BFF')`, f.Alpha.ID, w.a, tm[0], tm[1], tm[2], tm[3])
	}
	exec(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id, capacity, room_id, remarks) VALUES ($1, $2, $3, $4, 40, $5, 'front')`, f.Alpha.ID, w.a, w.c5, w.secA, w.room1)
	exec(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, w.a, w.c5, w.secB)
	exec(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id, room_id) VALUES ($1, $2, $3, $4, $5)`, f.Alpha.ID, w.a, w.c6, w.secA, w.room2)
	exec(`UPDATE rooms SET deleted_at = now() WHERE id = $1`, w.room2)
	exec(`INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, periods_per_week, is_elective) VALUES ($1, $2, $3, $4, 6, false)`, f.Alpha.ID, w.a, w.c5, w.math)
	exec(`INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, periods_per_week, is_elective, has_practical) VALUES ($1, $2, $3, $4, $5, 5, true, true)`, f.Alpha.ID, w.a, w.c5, w.eng, w.secA)
	exec(`INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, w.a, w.c6, w.math)
	for _, h := range []struct {
		title, kind, from, to string
		recurring             bool
	}{{"Republic Day", "public", "2027-01-26", "2027-01-26", false}, {"Diwali", "public", "2026-11-08", "2026-11-09", false},
		{"Founders Day", "school", "2026-08-15", "2026-08-15", false}, {"Labour Day", "public", "2026-05-01", "2026-05-01", true}} {
		exec(`INSERT INTO holidays (tenant_id, year_id, title, holiday_type, start_date, end_date, is_recurring_annual) VALUES ($1, $2, $3, $4, $5::date, $6::date, $7)`, f.Alpha.ID, w.a, h.title, h.kind, h.from, h.to, h.recurring)
	}
	exec(`INSERT INTO calendar_events (tenant_id, year_id, title, event_type, start_date, end_date) VALUES ($1, $2, 'Annual Day', 'event', '2027-02-10', '2027-02-10')`, f.Alpha.ID, w.a)

	ins(&w.bravoA, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'A', 'Bravo A', '2026-04-01', '2027-03-31') RETURNING id::text`, f.Bravo.ID)
	ins(&w.bravoB, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'B', 'Bravo B', '2027-04-01', '2028-03-31') RETURNING id::text`, f.Bravo.ID)
	exec(`INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date) VALUES ($1, $2, 'T1', 'Bravo term', '2026-04-01', '2027-03-31')`, f.Bravo.ID, w.bravoA)
	return w
}

func (w world) count(t *testing.T, table, year string) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM `+table+` WHERE year_id = $1 AND deleted_at IS NULL`, year).Scan(&n))
	return n
}

func (w world) runRows(t *testing.T, status string) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM year_rollover_runs WHERE tenant_id = $1 AND status = $2`, w.f.Alpha.ID, status).Scan(&n))
	return n
}

func (w world) run(t *testing.T, step string) rollover.Result {
	t.Helper()
	r, err := w.svc.Run(w.ctx(), w.f.Actor, w.in(step))
	must(t, err)
	return r
}

func fieldCode(err error, field string) string {
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		for _, e := range fe {
			if e.Field == field {
				return e.Code
			}
		}
	}
	return ""
}

func items(r rollover.Result, status string) []string {
	var out []string
	for _, it := range r.Items {
		if it.Status == status {
			out = append(out, it.Label)
		}
	}
	return out
}

// ---------------------------------------------------------------- date shifting

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestShiftByWholeYearsWhenBothYearsStartOnTheSameDay(t *testing.T) {
	s := rollover.ComputeShift(d("2026-04-01"), d("2027-04-01"))
	if s.Mode != rollover.ShiftYears || s.Years != 1 {
		t.Fatalf("shift = %+v", s)
	}
	for in, want := range map[string]string{"2026-04-01": "2027-04-01", "2027-01-26": "2028-01-26", "2027-03-31": "2028-03-31", "2026-12-31": "2027-12-31"} {
		if got := s.Apply(d(in)).Format("2006-01-02"); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	two := rollover.ComputeShift(d("2026-04-01"), d("2028-04-01"))
	if two.Years != 2 {
		t.Errorf("two years apart = %+v", two)
	}
}

func TestShiftKeepsFebruary29InALeapYearAndClampsItOtherwise(t *testing.T) {
	into := rollover.ComputeShift(d("2027-04-01"), d("2028-04-01")) // 2027 -> 2028 is leap
	if got := into.Apply(d("2027-02-28")).Format("2006-01-02"); got != "2028-02-28" {
		t.Errorf("28 Feb = %s", got)
	}
	from := rollover.ComputeShift(d("2027-04-01"), d("2028-04-01"))
	if got := from.Apply(d("2028-02-29")).Format("2006-01-02"); got != "2029-02-28" {
		t.Errorf("29 Feb into a common year = %s, want 2029-02-28", got)
	}
	leap := rollover.ComputeShift(d("2027-04-01"), d("2031-04-01"))
	if got := leap.Apply(d("2028-02-29")).Format("2006-01-02"); got != "2032-02-29" {
		t.Errorf("29 Feb into a leap year = %s, want 2032-02-29", got)
	}
}

func TestShiftByDaysWhenTheYearsStartOnDifferentDays(t *testing.T) {
	s := rollover.ComputeShift(d("2026-04-01"), d("2027-06-01"))
	if s.Mode != rollover.ShiftDays || s.Days != 426 {
		t.Fatalf("shift = %+v", s)
	}
	if got := s.Apply(d("2026-04-01")).Format("2006-01-02"); got != "2027-06-01" {
		t.Errorf("start maps to %s", got)
	}
	if got := s.Apply(d("2027-03-31")).Format("2006-01-02"); got != "2028-05-30" {
		t.Errorf("end maps to %s", got)
	}
}

// ---------------------------------------------------------------- preview equals run, idempotent

func TestPreviewChangesNothingAndEqualsTheRunForEveryStep(t *testing.T) {
	steps := []string{rollover.StepTerms, rollover.StepClassSections, rollover.StepSubjectClasses, rollover.StepHolidays, rollover.StepCalendarEvents}
	tables := map[string]string{rollover.StepTerms: "academic_terms", rollover.StepClassSections: "class_section_mappings", rollover.StepSubjectClasses: "subject_class_mappings",
		rollover.StepHolidays: "holidays", rollover.StepCalendarEvents: "calendar_events"}
	w := setup(t)
	for _, step := range steps {
		before := w.count(t, tables[step], w.b)
		prev, err := w.svc.Preview(w.ctx(), w.f.Actor, w.in(step))
		must(t, err)
		if !prev.DryRun || prev.RunID != nil {
			t.Errorf("%s: preview flags = dry %v run %v", step, prev.DryRun, prev.RunID)
		}
		if w.count(t, tables[step], w.b) != before {
			t.Fatalf("%s: preview left rows behind", step)
		}
		run := w.run(t, step)
		if run.DryRun || run.RunID == nil {
			t.Errorf("%s: run flags = dry %v run %v", step, run.DryRun, run.RunID)
		}
		if after := w.count(t, tables[step], w.b); after != before+run.Created {
			t.Errorf("%s: rows %d -> %d, run says created %d", step, before, after, run.Created)
		}
		a, b := prev, run
		a.DryRun, b.DryRun, a.RunID, b.RunID, a.RanAt, b.RanAt = false, false, nil, nil, nil, nil
		if !equalResults(a, b) {
			t.Errorf("%s: preview %+v differs from run %+v", step, a, b)
		}
		if run.Created == 0 {
			t.Errorf("%s: nothing was created", step)
		}
	}
	if n := w.runRows(t, "completed"); n != len(steps) {
		t.Errorf("run rows = %d, want %d (previews are not logged)", n, len(steps))
	}
}

func equalResults(a, b rollover.Result) bool {
	if a.Created != b.Created || a.Existing != b.Existing || a.Conflicts != b.Conflicts || a.Skipped != b.Skipped || a.Review != b.Review || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if a.Items[i] != b.Items[i] {
			return false
		}
	}
	return strings.Join(a.Warnings, "|") == strings.Join(b.Warnings, "|")
}

func TestRunningAStepAgainCopiesNothingTwice(t *testing.T) {
	w := setup(t)
	for _, step := range []string{rollover.StepTerms, rollover.StepClassSections, rollover.StepSubjectClasses, rollover.StepHolidays, rollover.StepCalendarEvents} {
		first := w.run(t, step)
		second := w.run(t, step)
		if second.Created != 0 || second.Existing+second.Skipped != first.Created+first.Existing+first.Skipped {
			t.Errorf("%s: first %+v then %+v", step, first, second)
		}
	}
	if n := w.count(t, "academic_terms", w.b); n != 2 {
		t.Errorf("terms in B = %d", n)
	}
	if n := w.count(t, "holidays", w.b); n != 3 {
		t.Errorf("holidays in B = %d", n)
	}
}

func TestTwoPeopleRunningTheSameStepAtOnceCopyOnce(t *testing.T) {
	w := setup(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = w.svc.Run(w.ctx(), w.f.Actor, w.in(rollover.StepTerms))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		must(t, err)
	}
	if n := w.count(t, "academic_terms", w.b); n != 2 {
		t.Errorf("terms in B = %d, want 2", n)
	}
}

// ---------------------------------------------------------------- terms

func TestTermsMoveByAYearAndKeepTheirDetails(t *testing.T) {
	w := setup(t)
	r := w.run(t, rollover.StepTerms)
	if r.Created != 2 || r.Shift.Mode != rollover.ShiftYears || r.Shift.Years != 1 {
		t.Fatalf("result = %+v", r)
	}
	var start, end, color string
	must(t, w.f.Admin.QueryRow(bg, `SELECT start_date::text, end_date::text, color FROM academic_terms WHERE year_id = $1 AND code = 'T2'`, w.b).Scan(&start, &end, &color))
	if start != "2027-10-01" || end != "2028-03-31" || color != "#2F5BFF" {
		t.Errorf("T2 in B = %s to %s %s", start, end, color)
	}
	var by *string
	must(t, w.f.Admin.QueryRow(bg, `SELECT created_by::text FROM academic_terms WHERE year_id = $1 AND code = 'T1'`, w.b).Scan(&by))
	if by == nil || *by != w.f.Actor.UserID {
		t.Errorf("created_by = %v", by)
	}
}

func TestTermsOutsideTheNewYearAreRefusedAndTheRestIsCopied(t *testing.T) {
	w := setup(t)
	in := w.in(rollover.StepTerms)
	in.ToYearID = w.b2 // 2028-04-01 .. 2028-12-31: T2 would end on 2029-03-31
	r, err := w.svc.Run(w.ctx(), w.f.Actor, in)
	must(t, err)
	if r.Created != 1 || r.Conflicts != 1 {
		t.Fatalf("result = %+v", r)
	}
	if got := items(r, rollover.ItemConflict); len(got) != 1 || !strings.HasPrefix(got[0], "T2") || r.Items[0].Reason != "outside_year" {
		t.Errorf("conflict = %+v", r.Items)
	}
	if w.count(t, "academic_terms", w.b2) != 1 {
		t.Error("only T1 should have been copied")
	}
}

func TestTermsThatOverlapATermAlreadyInTheNewYearAreRefused(t *testing.T) {
	w := setup(t)
	_, err := w.f.Admin.Exec(bg, `INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date) VALUES ($1, $2, 'X', 'Existing', '2027-09-01', '2027-11-30')`, w.f.Alpha.ID, w.b)
	must(t, err)
	r := w.run(t, rollover.StepTerms)
	if r.Created != 0 || r.Conflicts != 2 {
		t.Fatalf("result = %+v", r)
	}
	for _, it := range r.Items {
		if it.Reason != "overlaps_term" {
			t.Errorf("item = %+v", it)
		}
	}
}

func TestATermWhoseCodeIsAlreadyInTheNewYearIsLeftAlone(t *testing.T) {
	w := setup(t)
	_, err := w.f.Admin.Exec(bg, `INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date) VALUES ($1, $2, 't1', 'Mine', '2027-04-01', '2027-07-31')`, w.f.Alpha.ID, w.b)
	must(t, err)
	r := w.run(t, rollover.StepTerms)
	if r.Existing != 1 || r.Created != 1 {
		t.Fatalf("result = %+v", r)
	}
	var title string
	must(t, w.f.Admin.QueryRow(bg, `SELECT title FROM academic_terms WHERE year_id = $1 AND lower(code) = 't1'`, w.b).Scan(&title))
	if title != "Mine" {
		t.Errorf("existing term was changed to %q", title)
	}
}

// ---------------------------------------------------------------- class sections and subject classes

func TestClassSectionsCopyCapacityAndRoomAndDropADeletedRoom(t *testing.T) {
	w := setup(t)
	r := w.run(t, rollover.StepClassSections)
	if r.Created != 3 || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "without a room") {
		t.Fatalf("result = %+v", r)
	}
	var capacity int
	var room, remarks string
	must(t, w.f.Admin.QueryRow(bg, `SELECT capacity, room_id::text, remarks FROM class_section_mappings WHERE year_id = $1 AND class_id = $2 AND section_id = $3`, w.b, w.c5, w.secA).Scan(&capacity, &room, &remarks))
	if capacity != 40 || room != w.room1 || remarks != "front" {
		t.Errorf("copied mapping = %d %s %s", capacity, room, remarks)
	}
	var n int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM class_section_mappings WHERE year_id = $1 AND class_id = $2 AND room_id IS NULL`, w.b, w.c6).Scan(&n))
	if n != 1 {
		t.Errorf("class 6 mapping without room = %d", n)
	}
}

func TestClassSectionsOfADeletedClassAreRefused(t *testing.T) {
	w := setup(t)
	_, err := w.f.Admin.Exec(bg, `UPDATE classes SET deleted_at = now() WHERE id = $1`, w.c6)
	must(t, err)
	r := w.run(t, rollover.StepClassSections)
	if r.Created != 2 || r.Conflicts != 1 || r.Items[0].Reason != "class_deleted" {
		t.Fatalf("result = %+v", r)
	}
}

func TestASectionSpecificSubjectNeedsItsSectionMappedInTheNewYear(t *testing.T) {
	w := setup(t)
	before := w.run(t, rollover.StepSubjectClasses)
	if before.Created != 2 || before.Conflicts != 1 || before.Items[0].Reason != "section_not_mapped" {
		t.Fatalf("without class sections: %+v", before)
	}
	w.run(t, rollover.StepClassSections)
	after := w.run(t, rollover.StepSubjectClasses)
	if after.Created != 1 || after.Existing != 2 || after.Conflicts != 0 {
		t.Fatalf("with class sections: %+v", after)
	}
	var periods int
	var elective, practical bool
	must(t, w.f.Admin.QueryRow(bg, `SELECT periods_per_week, is_elective, has_practical FROM subject_class_mappings WHERE year_id = $1 AND subject_id = $2`, w.b, w.eng).Scan(&periods, &elective, &practical))
	if periods != 5 || !elective || !practical {
		t.Errorf("copied flags = %d %v %v", periods, elective, practical)
	}
}

// ---------------------------------------------------------------- holidays and events

func TestHolidaysMoveFlagFestivalsAndSkipThoseThatRepeatEveryYear(t *testing.T) {
	w := setup(t)
	r := w.run(t, rollover.StepHolidays)
	if r.Created != 3 || r.Skipped != 1 || r.Review != 2 || r.Conflicts != 0 {
		t.Fatalf("result = %+v", r)
	}
	var start, end string
	must(t, w.f.Admin.QueryRow(bg, `SELECT start_date::text, end_date::text FROM holidays WHERE year_id = $1 AND title = 'Diwali'`, w.b).Scan(&start, &end))
	if start != "2027-11-08" || end != "2027-11-09" {
		t.Errorf("Diwali in B = %s to %s", start, end)
	}
	byLabel := map[string]rollover.Item{}
	for _, it := range r.Items {
		byLabel[it.Label] = it
	}
	if !byLabel["Republic Day"].Review || !byLabel["Diwali"].Review || byLabel["Founders Day"].Review {
		t.Errorf("review flags = %+v", byLabel)
	}
	if lab := byLabel["Labour Day"]; lab.Status != rollover.ItemSkipped || lab.Reason != "recurring_annual" {
		t.Errorf("recurring holiday = %+v", lab)
	}
	var recurring int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM holidays WHERE year_id = $1 AND is_recurring_annual`, w.b).Scan(&recurring))
	if recurring != 0 {
		t.Error("a recurring holiday was copied")
	}
}

func TestHolidaysAndEventsThatMoveOutsideTheNewYearAreRefused(t *testing.T) {
	w := setup(t)
	in := w.in(rollover.StepHolidays)
	in.ToYearID = w.b2 // 2028-04-01 .. 2028-12-31: Republic Day lands on 2029-01-26
	r, err := w.svc.Run(w.ctx(), w.f.Actor, in)
	must(t, err)
	if r.Conflicts != 1 || r.Created != 2 || items(r, rollover.ItemConflict)[0] != "Republic Day" {
		t.Fatalf("holidays: %+v", r)
	}
	in.Step = rollover.StepCalendarEvents // Annual Day on 2027-02-10 lands on 2029-02-10
	e, err := w.svc.Run(w.ctx(), w.f.Actor, in)
	must(t, err)
	if e.Conflicts != 1 || e.Created != 0 {
		t.Fatalf("events: %+v", e)
	}
}

func TestEventsAreCopiedWithTheirDatesMoved(t *testing.T) {
	w := setup(t)
	r := w.run(t, rollover.StepCalendarEvents)
	if r.Created != 1 {
		t.Fatalf("result = %+v", r)
	}
	var start string
	must(t, w.f.Admin.QueryRow(bg, `SELECT start_date::text FROM calendar_events WHERE year_id = $1`, w.b).Scan(&start))
	if start != "2028-02-10" {
		t.Errorf("event date = %s", start)
	}
}

func TestShiftByDaysWhenTheNewYearStartsOnAnotherDay(t *testing.T) {
	w := setup(t)
	var odd string
	must(t, w.f.Admin.QueryRow(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'ODD', 'Odd', '2029-06-01', '2030-05-31') RETURNING id::text`, w.f.Alpha.ID).Scan(&odd))
	in := w.in(rollover.StepTerms)
	in.ToYearID = odd
	r, err := w.svc.Run(w.ctx(), w.f.Actor, in)
	must(t, err)
	if r.Shift.Mode != rollover.ShiftDays || r.Created != 2 {
		t.Fatalf("result = %+v", r)
	}
	var start, end string
	must(t, w.f.Admin.QueryRow(bg, `SELECT start_date::text, end_date::text FROM academic_terms WHERE year_id = $1 AND code = 'T2'`, odd).Scan(&start, &end))
	if start != "2029-12-01" || end != "2030-05-31" {
		t.Errorf("T2 = %s to %s", start, end)
	}
}

// ---------------------------------------------------------------- each step has its own transaction

func TestAFailingStepRollsBackAloneAndIsLogged(t *testing.T) {
	w := setup(t)
	w.run(t, rollover.StepTerms)
	_, err := w.f.Admin.Exec(bg, `CREATE FUNCTION boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.title = 'Diwali' THEN RAISE EXCEPTION 'boom'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER holidays_boom BEFORE INSERT ON holidays FOR EACH ROW EXECUTE FUNCTION boom()`)
	must(t, err)
	if _, err := w.svc.Run(w.ctx(), w.f.Actor, w.in(rollover.StepHolidays)); err == nil {
		t.Fatal("the step should have failed")
	}
	if n := w.count(t, "holidays", w.b); n != 0 {
		t.Errorf("holidays copied before the failure survived: %d", n)
	}
	if n := w.count(t, "academic_terms", w.b); n != 2 {
		t.Errorf("the earlier terms step was undone: %d", n)
	}
	if w.runRows(t, "failed") != 1 || w.runRows(t, "completed") != 1 {
		t.Errorf("log rows: failed %d completed %d", w.runRows(t, "failed"), w.runRows(t, "completed"))
	}
	if w.f.AuditCount(t, "year_rollover.holidays") != 0 || w.f.AuditCount(t, "year_rollover.holidays.failed") != 1 {
		t.Error("the failed step must leave a failure audit row and no success row")
	}
	_, err = w.f.Admin.Exec(bg, `DROP TRIGGER holidays_boom ON holidays`)
	must(t, err)
	if r := w.run(t, rollover.StepHolidays); r.Created != 3 {
		t.Errorf("the step cannot be re-run after the cause is fixed: %+v", r)
	}
}

// ---------------------------------------------------------------- make current

func TestMakeCurrentIsRefusedWithoutTermsOrClassSections(t *testing.T) {
	w := setup(t)
	p, err := w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	must(t, err)
	if len(p.Blockers) != 2 {
		t.Fatalf("blockers = %v", p.Blockers)
	}
	_, err = w.svc.Run(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	var blocked rollover.Blocked
	if !errors.As(err, &blocked) || len(blocked.Reasons) != 2 {
		t.Fatalf("run err = %v", err)
	}
	w.run(t, rollover.StepTerms)
	p, _ = w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "class sections") {
		t.Fatalf("blockers with terms = %v", p.Blockers)
	}
	if w.currentYear(t) != w.a {
		t.Error("a refused step changed the current year")
	}
	if w.runRows(t, "failed") != 1 {
		t.Errorf("a refused run is logged as failed: %d", w.runRows(t, "failed"))
	}
}

// promotionWarnings keeps the warnings about students not yet promoted; others, such as "starts in the future", depend on today.
func promotionWarnings(r rollover.Result) []string {
	var out []string
	for _, w := range r.Warnings {
		if strings.Contains(w, "promoted") {
			out = append(out, w)
		}
	}
	return out
}

func (w world) currentYear(t *testing.T) string {
	t.Helper()
	var id string
	must(t, w.f.Admin.QueryRow(bg, `SELECT id::text FROM academic_years WHERE tenant_id = $1 AND is_current AND deleted_at IS NULL`, w.f.Alpha.ID).Scan(&id))
	return id
}

func TestMakeCurrentSwitchesTheYearOnlyOnConfirmAndOnlyOnce(t *testing.T) {
	w := setup(t)
	w.run(t, rollover.StepTerms)
	w.run(t, rollover.StepClassSections)

	in := w.in(rollover.StepMakeCurrent)
	in.Confirm = false
	if _, err := w.svc.Run(w.ctx(), w.f.Actor, in); fieldCode(err, "confirm") != "required" {
		t.Fatalf("unconfirmed run err = %v", err)
	}
	prev, err := w.svc.Preview(w.ctx(), w.f.Actor, in)
	must(t, err)
	if prev.Created != 1 || w.currentYear(t) != w.a {
		t.Fatalf("preview %+v changed the year to %s", prev, w.currentYear(t))
	}

	r := w.run(t, rollover.StepMakeCurrent)
	if r.Created != 1 || w.currentYear(t) != w.b {
		t.Fatalf("run %+v, current %s", r, w.currentYear(t))
	}
	var n int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM academic_years WHERE tenant_id = $1 AND is_current`, w.f.Alpha.ID).Scan(&n))
	if n != 1 {
		t.Errorf("current years = %d", n)
	}
	again := w.run(t, rollover.StepMakeCurrent)
	if again.Created != 0 || again.Existing != 1 {
		t.Errorf("second run = %+v", again)
	}
}

func TestMakeCurrentRefusesAnInactiveNewYear(t *testing.T) {
	w := setup(t)
	w.run(t, rollover.StepTerms)
	w.run(t, rollover.StepClassSections)
	_, err := w.f.Admin.Exec(bg, `UPDATE academic_years SET status = 'inactive' WHERE id = $1`, w.b)
	must(t, err)
	p, err := w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	must(t, err)
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "inactive") {
		t.Fatalf("blockers = %v", p.Blockers)
	}
}

func TestMakeCurrentWarnsWhenPromotionsAreNotDone(t *testing.T) {
	w := setup(t)
	st, err := students.Service{DB: w.f.App}.Create(w.ctx(), w.f.Actor, students.Input{RegistrationCode: "S-1", FirstName: "Asha", Gender: "female", DateOfBirth: "2015-06-01"})
	must(t, err)
	_, err = enrollments.Service{DB: w.f.App}.Create(w.ctx(), w.f.Actor, enrollments.Input{StudentID: st.ID, YearID: w.a, ClassID: w.c5, SectionID: w.secA})
	must(t, err)
	w.run(t, rollover.StepTerms)
	w.run(t, rollover.StepClassSections)

	r, err := w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	must(t, err)
	if len(promotionWarnings(r)) != 1 || !strings.Contains(promotionWarnings(r)[0], "1 of 1 class section") {
		t.Fatalf("warnings = %v", r.Warnings)
	}
	cl, err := w.svc.Checklist(w.ctx(), w.a, w.b)
	must(t, err)
	if cl.Promotions.Sections != 1 || cl.Promotions.Promoted != 0 {
		t.Errorf("promotion progress = %+v", cl.Promotions)
	}

	_, err = w.f.Admin.Exec(bg, `INSERT INTO promotion_batches (tenant_id, from_year_id, to_year_id, from_class_id, from_section_id, status, committed_at, undo_until)
		VALUES ($1, $2, $3, $4, $5, 'committed', now(), now() + interval '1 day')`, w.f.Alpha.ID, w.a, w.b, w.c5, w.secA)
	must(t, err)
	r, err = w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepMakeCurrent))
	must(t, err)
	if len(promotionWarnings(r)) != 0 {
		t.Errorf("warnings after the promotion = %v", r.Warnings)
	}
	cl, _ = w.svc.Checklist(w.ctx(), w.a, w.b)
	if cl.Promotions.Promoted != 1 {
		t.Errorf("promotion progress = %+v", cl.Promotions)
	}
}

// ---------------------------------------------------------------- the pair of years

func TestTheTwoYearsAreChecked(t *testing.T) {
	w := setup(t)
	ctx := w.ctx()
	for name, c := range map[string]struct {
		in          rollover.Input
		field, code string
	}{
		"same year":    {rollover.Input{Step: rollover.StepTerms, FromYearID: w.a, ToYearID: w.a}, "toYearId", "same_year"},
		"before":       {rollover.Input{Step: rollover.StepTerms, FromYearID: w.b, ToYearID: w.a}, "toYearId", "not_after"},
		"unknown":      {rollover.Input{Step: rollover.StepTerms, FromYearID: w.a, ToYearID: "00000000-0000-0000-0000-000000000001"}, "toYearId", "not_found"},
		"bad id":       {rollover.Input{Step: rollover.StepTerms, FromYearID: "x", ToYearID: w.b}, "fromYearId", "invalid"},
		"unknown step": {rollover.Input{Step: "nope", FromYearID: w.a, ToYearID: w.b}, "step", "invalid"},
	} {
		if _, err := w.svc.Preview(ctx, w.f.Actor, c.in); fieldCode(err, c.field) != c.code {
			t.Errorf("%s: preview err = %v", name, err)
		}
		if _, err := w.svc.Run(ctx, w.f.Actor, c.in); fieldCode(err, c.field) != c.code {
			t.Errorf("%s: run err = %v", name, err)
		}
	}
	if w.runRows(t, "failed") != 0 {
		t.Error("a bad request is not a failed step")
	}
}

func TestAnotherInstitutesYearsAreNotVisible(t *testing.T) {
	w := setup(t)
	bravo := modtest.In(w.f.Bravo)
	in := rollover.Input{Step: rollover.StepTerms, FromYearID: w.a, ToYearID: w.b}
	if _, err := w.svc.Run(bravo, w.f.Actor, in); fieldCode(err, "fromYearId") != "not_found" {
		t.Errorf("bravo ran alpha's years: %v", err)
	}
	if _, err := w.svc.Checklist(bravo, w.a, w.b); fieldCode(err, "fromYearId") != "not_found" {
		t.Errorf("checklist err = %v", err)
	}
	r, err := w.svc.Run(bravo, w.f.Actor, rollover.Input{Step: rollover.StepTerms, FromYearID: w.bravoA, ToYearID: w.bravoB})
	must(t, err)
	if r.Created != 1 {
		t.Fatalf("bravo's own rollover = %+v", r)
	}
	if w.count(t, "academic_terms", w.b) != 0 {
		t.Error("bravo's rollover wrote into alpha")
	}
	h, err := w.svc.History(w.ctx(), crud.ListQuery{Page: 1, Size: 50, OrderBy: "r.run_at DESC"}, rollover.HistoryFilter{})
	must(t, err)
	if h.Total != 0 {
		t.Errorf("alpha sees %d of bravo's runs", h.Total)
	}
}

// ---------------------------------------------------------------- checklist, history, audit, new year

func TestChecklistCountsAndLastRun(t *testing.T) {
	w := setup(t)
	before, err := w.svc.Checklist(w.ctx(), w.a, w.b)
	must(t, err)
	if len(before.Steps) != 6 || before.Shift.Years != 1 || before.From.ID != w.a || !before.From.IsCurrent || before.To.IsCurrent {
		t.Fatalf("checklist = %+v", before)
	}
	by := map[string]rollover.ChecklistStep{}
	for _, s := range before.Steps {
		by[s.ID] = s
	}
	if by["terms"].SourceCount != 2 || by["terms"].TargetCount != 0 || by["terms"].LastRun != nil || by["holidays"].SourceCount != 4 {
		t.Errorf("counts before = %+v", by)
	}
	if !by["make_current"].Irreversible || by["calendar_events"].DefaultOn || !by["terms"].DefaultOn || by["terms"].Irreversible {
		t.Errorf("flags = %+v", by)
	}
	w.run(t, rollover.StepTerms)
	after, _ := w.svc.Checklist(w.ctx(), w.a, w.b)
	terms := after.Steps[0]
	if terms.TargetCount != 2 || terms.LastRun == nil || terms.LastRun.Created != 2 || terms.LastRun.RunByName == nil || *terms.LastRun.RunByName != "Asha" {
		t.Errorf("terms after = %+v", terms)
	}
}

func TestEveryRunIsAuditedAndListedInTheHistory(t *testing.T) {
	w := setup(t)
	w.run(t, rollover.StepTerms)
	w.run(t, rollover.StepHolidays)
	if _, err := w.svc.Preview(w.ctx(), w.f.Actor, w.in(rollover.StepClassSections)); err != nil {
		t.Fatal(err)
	}
	if w.f.AuditCount(t, "year_rollover.terms") != 1 || w.f.AuditCount(t, "year_rollover.holidays") != 1 || w.f.AuditCount(t, "year_rollover.class_sections") != 0 {
		t.Error("audit rows do not match the runs")
	}
	var after string
	must(t, w.f.Admin.QueryRow(bg, `SELECT after::text FROM audit_log WHERE action = 'year_rollover.holidays'`).Scan(&after))
	for _, want := range []string{`"created": 3`, `"skipped": 1`, w.a, w.b} {
		if !strings.Contains(after, want) {
			t.Errorf("audit after %s lacks %s", after, want)
		}
	}
	h, err := w.svc.History(w.ctx(), crud.ListQuery{Page: 1, Size: 1, OrderBy: "r.run_at DESC"}, rollover.HistoryFilter{FromYearID: w.a, ToYearID: w.b})
	must(t, err)
	if h.Total != 2 || len(h.Items) != 1 || h.Items[0].Step != rollover.StepHolidays || h.Items[0].RunByName == nil || h.Items[0].ToYearName != "Year B" {
		t.Fatalf("history = %+v", h)
	}
	h, _ = w.svc.History(w.ctx(), crud.ListQuery{Page: 1, Size: 10, OrderBy: "r.run_at DESC"}, rollover.HistoryFilter{ToYearID: w.b2})
	if h.Total != 0 {
		t.Errorf("filter by year: %d", h.Total)
	}
}

func TestCreateYearFollowsTheYearRulesAndStartsAfterTheOldYear(t *testing.T) {
	w := setup(t)
	mk := func(code, from, to string) error {
		_, err := w.svc.CreateYear(w.ctx(), w.f.Actor, rollover.NewYearInput{FromYearID: w.a, Code: code, Name: "Year " + code, StartDate: from, EndDate: to})
		return err
	}
	if err := mk("N1", "2028-01-01", "2028-12-31"); fieldCode(err, "startDate") != "overlaps_year" {
		t.Errorf("overlap err = %v", err)
	}
	if err := mk("N2", "2025-04-01", "2026-03-31"); fieldCode(err, "startDate") != "not_after" {
		t.Errorf("before err = %v", err)
	}
	if err := mk("A", "2031-04-01", "2032-03-31"); fieldCode(err, "code") != "duplicate" {
		t.Errorf("duplicate code err = %v", err)
	}
	if err := mk("", "2031-04-01", "2032-03-31"); fieldCode(err, "code") != "required" {
		t.Errorf("empty code err = %v", err)
	}
	y, err := w.svc.CreateYear(w.ctx(), w.f.Actor, rollover.NewYearInput{FromYearID: w.a, Code: "2031-32", Name: "2031-32", StartDate: "2031-04-01", EndDate: "2032-03-31"})
	must(t, err)
	if y.IsCurrent || y.Status != "active" || w.currentYear(t) != w.a {
		t.Errorf("new year = %+v", y)
	}
	if w.f.AuditCount(t, "academic_years.create") != 1 {
		t.Error("the new year is not audited")
	}
	if _, err := w.svc.CreateYear(modtest.In(w.f.Bravo), w.f.Actor, rollover.NewYearInput{FromYearID: w.a, Code: "Z", Name: "Z", StartDate: "2031-04-01", EndDate: "2032-03-31"}); fieldCode(err, "fromYearId") != "not_found" {
		t.Errorf("cross-institute err = %v", err)
	}
}
