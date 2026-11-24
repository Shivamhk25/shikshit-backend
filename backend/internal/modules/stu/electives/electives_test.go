package electives_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	acaelectives "github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/scope"
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

// world is institute alpha with one academic year, two classes and two sections, and two elective groups:
//
//	lang  "Second language", exactly one subject, limits enforced:  Hindi, French, Sanskrit for Class 9 (all sections)
//	skill "Skill", two subjects, limits advisory: Music and Dance for Class 9; Art for Class 9 section B only
//
// Class 10 offers French in "lang" only. German is an elective of Class 9 that belongs to no group.
type world struct {
	f              modtest.Fixture
	svc            electives.Service
	year, c9, c10  string
	secA, secB     string
	csmA, csmB     string
	csm10          string
	lang, skill    string
	hindi, french  string
	sanskrit       string
	music, dance   string
	art, german    string
	s1, s2, s3, s4 string // s1, s2 in 9-A; s3 in 9-B; s4 not enrolled
}

func (w world) in() context.Context { return modtest.In(w.f.Alpha) }

func (w world) scoped(ids ...string) context.Context { return scope.WithClassSections(w.in(), ids) }

func (w world) id(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	must(t, w.f.Admin.QueryRow(bg, sql, args...).Scan(&id))
	return id
}

func (w world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := w.f.Admin.Exec(bg, sql, args...)
	must(t, err)
}

func (w world) subject(t *testing.T, code, name string) string {
	return w.id(t, `INSERT INTO subjects (tenant_id, code, name, category, area, scholastic, credits) VALUES ($1, $2::text, $3, 'elective', 'other', true, 1) RETURNING id::text`, w.f.Alpha.ID, code, name)
}

func (w world) group(t *testing.T, code, name string, min, max int, enforce bool) string {
	return w.id(t, `INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects, enforce_limits) VALUES ($1, $2::text, $3, $4, $5, $6) RETURNING id::text`,
		w.f.Alpha.ID, code, name, min, max, enforce)
}

// mapping offers a subject to a class (and one section, when given) in a group (when given).
func (w world) mapping(t *testing.T, class, subject string, section, group *string) {
	t.Helper()
	w.exec(t, `INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, is_elective, elective_group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, true, $6::uuid)`,
		w.f.Alpha.ID, w.year, class, subject, section, group)
}

func (w world) student(t *testing.T, code string, class, section *string) string {
	t.Helper()
	id := w.id(t, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth, admission_date)
		VALUES ($1, $2::text, 'Asha', 'Rao ' || $2::text, 'female', DATE '2012-04-01', DATE '2020-04-01') RETURNING id::text`, w.f.Alpha.ID, code)
	if class != nil {
		w.exec(t, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`,
			w.f.Alpha.ID, id, w.year, *class, *section, strings.TrimPrefix(code, "S-"))
	}
	return id
}

func ptr[T any](v T) *T { return &v }

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, svc: electives.Service{DB: f.App}}
	a := f.Alpha.ID
	w.year = w.id(t, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', DATE '2026-04-01', DATE '2027-03-31', true) RETURNING id::text`, a)
	w.c9 = w.id(t, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'IX', 'Class 9') RETURNING id::text`, a)
	w.c10 = w.id(t, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'X', 'Class 10') RETURNING id::text`, a)
	w.secA = w.id(t, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'A') RETURNING id::text`, a)
	w.secB = w.id(t, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'B', 'B') RETURNING id::text`, a)
	csm := func(class, sec string) string {
		return w.id(t, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`, a, w.year, class, sec)
	}
	w.csmA, w.csmB, w.csm10 = csm(w.c9, w.secA), csm(w.c9, w.secB), csm(w.c10, w.secA)

	w.lang, w.skill = w.group(t, "LANG", "Second language", 1, 1, true), w.group(t, "SKILL", "Skill", 2, 2, false)
	w.hindi, w.french, w.sanskrit = w.subject(t, "HIN", "Hindi"), w.subject(t, "FRE", "French"), w.subject(t, "SAN", "Sanskrit")
	w.music, w.dance, w.art, w.german = w.subject(t, "MUS", "Music"), w.subject(t, "DAN", "Dance"), w.subject(t, "ART", "Art"), w.subject(t, "GER", "German")
	for _, s := range []string{w.hindi, w.french, w.sanskrit} {
		w.mapping(t, w.c9, s, nil, &w.lang)
	}
	w.mapping(t, w.c9, w.music, nil, &w.skill)
	w.mapping(t, w.c9, w.dance, nil, &w.skill)
	w.mapping(t, w.c9, w.art, &w.secB, &w.skill)
	w.mapping(t, w.c9, w.german, nil, nil)
	w.mapping(t, w.c10, w.french, nil, &w.lang)

	w.s1, w.s2 = w.student(t, "S-1", &w.c9, &w.secA), w.student(t, "S-2", &w.c9, &w.secA)
	w.s3 = w.student(t, "S-3", &w.c9, &w.secB)
	w.s4 = w.student(t, "S-4", nil, nil)
	return w
}

func (w world) create(t *testing.T, student, group string, status string, subjects ...string) electives.Choice {
	t.Helper()
	c, err := w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: student, YearID: w.year, GroupID: group, SubjectIDs: subjects, Status: status})
	must(t, err)
	return c
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
	t.Errorf("%s: no %q error on %q: %v", name, code, field, fe)
}

func conflict(t *testing.T, name string, err error, code string) {
	t.Helper()
	var ce electives.ConflictError
	if !errors.As(err, &ce) || ce.Code != code {
		t.Errorf("%s: want conflict %q, got %v", name, code, err)
	}
}

func names(in []electives.SubjectRef) string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.Name
	}
	return strings.Join(out, ",")
}

func (w world) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, sql, args...).Scan(&n))
	return n
}

func TestAChoiceIsRecordedWithItsClassSectionGroupAndSubjects(t *testing.T) {
	w := setup(t)
	c := w.create(t, w.s1, w.lang, "", w.french)
	if c.Status != "chosen" || names(c.Subjects) != "French" || c.Group.Name != "Second language" || c.YearName != "2026-27" || !c.WithinLimits || len(c.Warnings) != 0 ||
		c.ClassName == nil || *c.ClassName != "Class 9" || c.SectionName == nil || *c.SectionName != "A" || c.Student.RegistrationCode != "S-1" || c.Student.Name != "Asha Rao S-1" {
		t.Fatalf("choice = %+v", c)
	}
	got, err := w.svc.Get(w.in(), c.ID)
	must(t, err)
	if names(got.Offered) != "French,Hindi,Sanskrit" {
		t.Fatalf("offered = %v", got.Offered)
	}
	if w.f.AuditCount(t, "student_electives.create") != 1 {
		t.Error("the creation was not audited")
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE choice_id = $1::uuid AND action = 'create' AND source = 'staff'`, c.ID); n != 1 {
		t.Errorf("history rows = %d", n)
	}
	confirmed := w.create(t, w.s2, w.lang, "confirmed", w.hindi)
	if confirmed.Status != "confirmed" {
		t.Errorf("status = %s", confirmed.Status)
	}
}

func TestTheLimitsOfAnEnforcingGroupRefuseTooFewAndTooManySubjects(t *testing.T) {
	w := setup(t)
	in := func(subjects ...string) electives.Input {
		return electives.Input{StudentID: w.s1, YearID: w.year, GroupID: w.lang, SubjectIDs: subjects}
	}
	_, err := w.svc.Create(w.in(), w.f.Actor, in())
	fieldErr(t, "no subject", err, "subjectIds", "required")
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.hindi, w.french))
	fieldErr(t, "above maximum", err, "subjectIds", "above_maximum")
	if n := w.count(t, `SELECT count(*) FROM student_elective_choices`); n != 0 {
		t.Fatalf("a refused choice left %d rows", n)
	}

	// the minimum: a group that needs two subjects refuses one when it enforces its limits
	w.exec(t, `UPDATE elective_groups SET min_subjects = 2, max_subjects = 3, enforce_limits = true WHERE id = $1::uuid`, w.skill)
	_, err = w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: w.s1, YearID: w.year, GroupID: w.skill, SubjectIDs: []string{w.music}})
	fieldErr(t, "below minimum", err, "subjectIds", "below_minimum")
	ok, err := w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: w.s1, YearID: w.year, GroupID: w.skill, SubjectIDs: []string{w.music, w.dance}})
	must(t, err)
	if !ok.WithinLimits || len(ok.Subjects) != 2 {
		t.Fatalf("within limits = %+v", ok)
	}
}

func TestTheLimitsOfANonEnforcingGroupOnlyWarn(t *testing.T) {
	w := setup(t) // skill: min 2, max 2, not enforced
	few := w.create(t, w.s1, w.skill, "", w.music)
	if few.WithinLimits || len(few.Warnings) != 1 || few.Warnings[0].Code != "below_minimum" {
		t.Fatalf("one subject = %+v", few)
	}
	many := w.create(t, w.s3, w.skill, "", w.music, w.dance, w.art)
	if many.WithinLimits || len(many.Warnings) != 1 || many.Warnings[0].Code != "above_maximum" || len(many.Subjects) != 3 {
		t.Fatalf("three subjects = %+v", many)
	}
	ok := w.create(t, w.s2, w.skill, "", w.music, w.dance)
	if !ok.WithinLimits || len(ok.Warnings) != 0 {
		t.Fatalf("two subjects = %+v", ok)
	}
	// the warnings stay visible when the choice is read again
	got, err := w.svc.Get(w.in(), few.ID)
	must(t, err)
	if got.WithinLimits || len(got.Warnings) != 1 {
		t.Fatalf("read back = %+v", got)
	}
}

func TestSubjectsMustBelongToTheGroupForTheStudentsClassSection(t *testing.T) {
	w := setup(t)
	create := func(student, group string, subjects ...string) error {
		_, err := w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: student, YearID: w.year, GroupID: group, SubjectIDs: subjects})
		return err
	}
	fieldErr(t, "a subject of another group", create(w.s1, w.lang, w.music), "subjectIds", "not_in_group")
	fieldErr(t, "an elective that is in no group", create(w.s1, w.lang, w.german), "subjectIds", "not_in_group")
	fieldErr(t, "a subject offered to another section only", create(w.s1, w.skill, w.art), "subjectIds", "not_in_group")
	fieldErr(t, "a subject that is not elective at all", create(w.s1, w.lang, w.id(t, `INSERT INTO subjects (tenant_id, code, name, category, area, credits) VALUES ($1, 'MAT', 'Maths', 'core', 'mathematics', 1) RETURNING id::text`, w.f.Alpha.ID)), "subjectIds", "not_in_group")
	fieldErr(t, "the same subject twice", create(w.s1, w.skill, w.music, w.music), "subjectIds", "duplicate")
	fieldErr(t, "not an id", create(w.s1, w.lang, "french"), "subjectIds", "invalid")
	if n := w.count(t, `SELECT count(*) FROM student_elective_choices`); n != 0 {
		t.Fatalf("refused choices left %d rows", n)
	}
	// the section row offers Art to section B, and only there
	if err := create(w.s3, w.skill, w.art, w.music); err != nil {
		t.Fatalf("section B: %v", err)
	}
	// a section row of the same subject with a different group overrides the class-wide mapping for that section
	w.exec(t, `UPDATE subject_class_mappings SET elective_group_id = NULL WHERE subject_id = $1::uuid AND section_id IS NULL AND class_id = $2::uuid`, w.hindi, w.c9)
	w.exec(t, `INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, is_elective, elective_group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, true, $6::uuid)`,
		w.f.Alpha.ID, w.year, w.c9, w.hindi, w.secB, w.lang)
	fieldErr(t, "Hindi has left the group for section A", create(w.s1, w.lang, w.hindi), "subjectIds", "not_in_group")
	if _, err := w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: w.s3, YearID: w.year, GroupID: w.lang, SubjectIDs: []string{w.hindi}}); err != nil {
		t.Fatalf("Hindi stays in the group for section B: %v", err)
	}
}

func TestTheGroupMustBeOfferedToTheStudentsClassAndTheStudentMustBeEnrolled(t *testing.T) {
	w := setup(t)
	in := func(student, year, group string, subjects ...string) electives.Input {
		return electives.Input{StudentID: student, YearID: year, GroupID: group, SubjectIDs: subjects}
	}
	// Class 10 offers French in "lang" and nothing in "skill"
	s10 := w.student(t, "S-10", &w.c10, &w.secA)
	_, err := w.svc.Create(w.in(), w.f.Actor, in(s10, w.year, w.skill, w.music))
	fieldErr(t, "group not offered to Class 10", err, "groupId", "not_offered")
	_, err = w.svc.Create(w.in(), w.f.Actor, in(s10, w.year, w.lang, w.hindi))
	fieldErr(t, "Hindi is Class 9's", err, "subjectIds", "not_in_group")
	if _, err = w.svc.Create(w.in(), w.f.Actor, in(s10, w.year, w.lang, w.french)); err != nil {
		t.Fatalf("French in Class 10: %v", err)
	}

	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s4, w.year, w.lang, w.hindi))
	fieldErr(t, "not enrolled", err, "studentId", "not_enrolled")
	w.exec(t, `UPDATE student_enrollments SET status = 'withdrawn' WHERE student_id = $1::uuid`, w.s2)
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s2, w.year, w.lang, w.hindi))
	fieldErr(t, "withdrawn", err, "studentId", "not_enrolled")
	other := w.id(t, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y2', '2027-28', DATE '2027-04-01', DATE '2028-03-31') RETURNING id::text`, w.f.Alpha.ID)
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s1, other, w.lang, w.hindi))
	fieldErr(t, "enrolled in another year only", err, "studentId", "not_enrolled")

	_, err = w.svc.Create(w.in(), w.f.Actor, in("00000000-0000-0000-0000-000000000001", w.year, w.lang, w.hindi))
	fieldErr(t, "no such student", err, "studentId", "not_found")
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s1, "00000000-0000-0000-0000-000000000001", w.lang, w.hindi))
	fieldErr(t, "no such year", err, "yearId", "not_found")
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s1, w.year, "00000000-0000-0000-0000-000000000001", w.hindi))
	fieldErr(t, "no such group", err, "groupId", "not_found")
	w.exec(t, `UPDATE elective_groups SET status = 'inactive' WHERE id = $1::uuid`, w.lang)
	_, err = w.svc.Create(w.in(), w.f.Actor, in(w.s1, w.year, w.lang, w.hindi))
	fieldErr(t, "inactive group", err, "groupId", "inactive")
	_, err = w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: "x", YearID: "y", GroupID: ""})
	fieldErr(t, "ids", err, "studentId", "invalid")
}

func TestAStudentHasOneChoicePerGroupAndYear(t *testing.T) {
	w := setup(t)
	first := w.create(t, w.s1, w.lang, "", w.hindi)
	_, err := w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: w.s1, YearID: w.year, GroupID: w.lang, SubjectIDs: []string{w.french}})
	conflict(t, "second choice for the same group", err, "choice_exists")
	// another group, another student, and another year are other choices
	w.create(t, w.s1, w.skill, "", w.music, w.dance)
	w.create(t, w.s2, w.lang, "", w.hindi)
	if n := w.count(t, `SELECT count(*) FROM student_elective_choices WHERE student_id = $1::uuid AND deleted_at IS NULL`, w.s1); n != 2 {
		t.Fatalf("live choices = %d", n)
	}
	// the database says it too, whatever the service does
	_, err = w.f.Admin.Exec(bg, `INSERT INTO student_elective_choices (tenant_id, student_id, year_id, group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid)`, w.f.Alpha.ID, w.s1, w.year, w.lang)
	if err == nil || !strings.Contains(err.Error(), "student_elective_choices_uq") {
		t.Fatalf("direct insert: %v", err)
	}
	// once removed, the student can choose again
	must(t, w.svc.Delete(w.in(), w.f.Actor, first.ID))
	again := w.create(t, w.s1, w.lang, "", w.french)
	if again.ID == first.ID {
		t.Error("a removed choice came back")
	}
}

func TestSimultaneousCreatesMakeOneChoice(t *testing.T) {
	w := setup(t)
	var wg sync.WaitGroup
	results := make([]error, 6)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = w.svc.Create(w.in(), w.f.Actor, electives.Input{StudentID: w.s1, YearID: w.year, GroupID: w.lang, SubjectIDs: []string{w.hindi}})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		var ce electives.ConflictError
		switch {
		case err == nil:
			ok++
		case !errors.As(err, &ce) || ce.Code != "choice_exists":
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || w.count(t, `SELECT count(*) FROM student_elective_choices`) != 1 {
		t.Fatalf("%d creates succeeded", ok)
	}
}

func TestReplacingAChoiceKeepsEveryChangeInTheHistory(t *testing.T) {
	w := setup(t)
	c := w.create(t, w.s1, w.lang, "", w.hindi)
	got, err := w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.french}})
	must(t, err)
	if names(got.Subjects) != "French" || got.Status != "chosen" {
		t.Fatalf("replaced = %+v", got)
	}
	got, err = w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.french}, Status: "confirmed"})
	must(t, err)
	if got.Status != "confirmed" {
		t.Fatalf("status = %s", got.Status)
	}
	// nothing differs: nothing is written
	before := w.f.AuditCount(t, "student_electives.update")
	if _, err = w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.french}}); err != nil {
		t.Fatal(err)
	}
	if w.f.AuditCount(t, "student_electives.update") != before {
		t.Error("an unchanged replace was audited")
	}
	_, err = w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.hindi, w.french}})
	fieldErr(t, "too many", err, "subjectIds", "above_maximum")
	_, err = w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.music}})
	fieldErr(t, "wrong group", err, "subjectIds", "not_in_group")
	_, err = w.svc.Replace(w.in(), w.f.Actor, c.ID, electives.Update{SubjectIDs: []string{w.french}, Status: "done"})
	fieldErr(t, "status", err, "status", "invalid_choice")

	h, err := w.svc.History(w.in(), c.ID)
	must(t, err)
	if len(h) != 3 || h[0].Action != "status" || h[1].Action != "replace" || h[2].Action != "create" {
		t.Fatalf("history = %+v", h)
	}
	if names(h[1].SubjectsBefore) != "Hindi" || names(h[1].SubjectsAfter) != "French" || h[1].Source != "staff" || h[1].ChangedBy == nil || h[1].ChangedBy.Name != "Asha" {
		t.Fatalf("the replace row = %+v", h[1])
	}
	if w.f.AuditCount(t, "student_electives.update") != 2 {
		t.Error("the updates were not audited")
	}
	if _, err := w.svc.Replace(w.in(), w.f.Actor, "00000000-0000-0000-0000-000000000001", electives.Update{SubjectIDs: []string{w.french}}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("unknown choice: %v", err)
	}
}

func TestRemovingAChoiceKeepsItsHistoryAndIsRefusedWhileARequestWaits(t *testing.T) {
	w := setup(t)
	c := w.create(t, w.s1, w.lang, "", w.hindi)
	other := w.create(t, w.s2, w.lang, "", w.hindi)
	w.exec(t, `INSERT INTO student_elective_changes (tenant_id, student_id, choice_id, year_id, group_id, from_subject_id, to_subject_id, wanted_from, reason)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, DATE '2026-09-01', 'x')`, w.f.Alpha.ID, w.s1, c.ID, w.year, w.lang, w.hindi, w.french)
	conflict(t, "request waiting", w.svc.Delete(w.in(), w.f.Actor, c.ID), "open_change_request")
	must(t, w.svc.Delete(w.in(), w.f.Actor, other.ID))
	if _, err := w.svc.Get(w.in(), other.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE choice_id = $1::uuid`, other.ID); n != 2 {
		t.Errorf("history rows kept = %d", n)
	}
	if w.f.AuditCount(t, "student_electives.delete") != 1 {
		t.Error("the delete was not audited")
	}
}

func TestTheListFiltersSortsAndSearches(t *testing.T) {
	w := setup(t)
	w.create(t, w.s1, w.lang, "", w.hindi)
	w.create(t, w.s2, w.lang, "confirmed", w.french)
	w.create(t, w.s3, w.skill, "", w.music, w.art)
	list := func(f electives.Filter, sort, search string) []string {
		t.Helper()
		q, err := crud.ParseList(1, 50, sort, search, electives.SortKeys, electives.DefaultSort)
		must(t, err)
		p, err := w.svc.List(w.in(), q, f)
		must(t, err)
		out := []string{}
		for _, c := range p.Items {
			out = append(out, c.Student.RegistrationCode)
		}
		return out
	}
	eq := func(name string, got []string, want string) {
		t.Helper()
		if strings.Join(got, ",") != want {
			t.Errorf("%s = %v, want %s", name, got, want)
		}
	}
	eq("all", list(electives.Filter{}, "", ""), "S-1,S-2,S-3")
	eq("group", list(electives.Filter{GroupID: w.lang}, "", ""), "S-1,S-2")
	eq("status", list(electives.Filter{Status: "confirmed"}, "", ""), "S-2")
	eq("section", list(electives.Filter{SectionID: w.secB}, "", ""), "S-3")
	eq("class", list(electives.Filter{ClassID: w.c9, YearID: w.year}, "-student", ""), "S-3,S-2,S-1")
	eq("class 10", list(electives.Filter{ClassID: w.c10}, "", ""), "")
	eq("student", list(electives.Filter{StudentID: w.s2}, "", ""), "S-2")
	eq("search", list(electives.Filter{}, "", "S-3"), "S-3")
	eq("sort by section", list(electives.Filter{}, "-section,student", ""), "S-3,S-1,S-2")
	p, err := w.svc.List(w.in(), crud.ListQuery{Page: 1, Size: 1, OrderBy: "student_elective_choices.created_at"}, electives.Filter{})
	must(t, err)
	if p.Total != 3 || len(p.Items) != 1 {
		t.Errorf("paging = %d of %d", len(p.Items), p.Total)
	}
	if len(p.Items[0].Offered) != 0 {
		t.Error("a list row carries the offered subjects")
	}
}

func TestTheRosterListsEveryStudentOfTheSectionIncludingThoseWhoHaveNotChosen(t *testing.T) {
	w := setup(t)
	w.create(t, w.s1, w.lang, "", w.french)
	// a student who has withdrawn, and one of another section, are not on it
	w.exec(t, `UPDATE student_enrollments SET status = 'withdrawn' WHERE student_id = $1::uuid`, w.s2)
	late := w.student(t, "S-9", &w.c9, &w.secA)
	r, err := w.svc.Roster(w.in(), w.year, w.c9, w.secA, w.lang)
	must(t, err)
	if r.Class.Name != "Class 9" || r.Section.Name != "A" || r.Year.Name != "2026-27" || r.Group.Name != "Second language" || names(r.Offered) != "French,Hindi,Sanskrit" || len(r.Students) != 2 {
		t.Fatalf("roster = %+v", r)
	}
	a, b := r.Students[0], r.Students[1]
	if a.Student.ID != w.s1 || a.ChoiceID == nil || names(a.Subjects) != "French" || !a.WithinLimits || a.Status == nil || *a.Status != "chosen" {
		t.Errorf("the student who chose = %+v", a)
	}
	if b.Student.ID != late || b.ChoiceID != nil || b.Status != nil || len(b.Subjects) != 0 || b.WithinLimits || len(b.Warnings) != 1 || b.Warnings[0].Code != "below_minimum" {
		t.Errorf("the student who has not = %+v", b)
	}
	_, err = w.svc.Roster(w.in(), w.year, w.c9, w.secA, w.skill)
	if err != nil {
		t.Errorf("skill is offered to 9-A: %v", err)
	}
	_, err = w.svc.Roster(w.in(), w.year, w.c10, w.secA, w.skill)
	fieldErr(t, "group not offered", err, "groupId", "not_offered")
	if _, err = w.svc.Roster(w.in(), w.year, w.c10, w.secB, w.lang); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("a class section that does not exist: %v", err)
	}
}

func TestABulkSaveIsAllOrNothingWithErrorsKeyedByStudent(t *testing.T) {
	w := setup(t)
	existing := w.create(t, w.s2, w.lang, "", w.hindi)
	save := func(allowCreate, allowDelete bool, rows ...electives.BulkRow) (electives.BulkResult, error) {
		return w.svc.Bulk(w.in(), w.f.Actor, electives.BulkInput{YearID: w.year, ClassID: w.c9, SectionID: w.secA, GroupID: w.lang, Rows: rows, AllowCreate: allowCreate, AllowDelete: allowDelete})
	}
	row := func(student string, subjects ...string) electives.BulkRow {
		return electives.BulkRow{StudentID: student, SubjectIDs: subjects}
	}

	// one good row and three bad ones: nothing is written, every problem is reported under its student
	_, err := save(true, true, row(w.s1, w.french), row(w.s2, w.hindi, w.french), row(w.s3, w.hindi), row(w.s1, w.hindi))
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("want field errors, got %v", err)
	}
	for _, want := range []struct{ field, code string }{
		{"rows[" + w.s2 + "].subjectIds", "above_maximum"},
		{"rows[" + w.s3 + "].studentId", "not_in_section"},
		{"rows[" + w.s1 + "].studentId", "duplicate_student"},
	} {
		found := false
		for _, f := range fe {
			found = found || (f.Field == want.field && f.Code == want.code)
		}
		if !found {
			t.Errorf("missing %s on %s in %v", want.code, want.field, fe)
		}
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choices WHERE deleted_at IS NULL`); n != 1 {
		t.Fatalf("a refused bulk save left %d choices (want only the existing one)", n)
	}
	got, _ := w.svc.Get(w.in(), existing.ID)
	if names(got.Subjects) != "Hindi" {
		t.Fatalf("the existing choice changed: %v", names(got.Subjects))
	}

	// a good save: one created, one replaced, one left as it is
	s5 := w.student(t, "S-5", &w.c9, &w.secA)
	res, err := save(true, true, row(w.s1, w.french), row(w.s2, w.sanskrit), row(s5))
	must(t, err)
	if res.Created != 1 || res.Updated != 1 || res.Unchanged != 1 || res.Removed != 0 || len(res.Items) != 3 {
		t.Fatalf("result = %+v", res)
	}
	got, _ = w.svc.Get(w.in(), existing.ID)
	if names(got.Subjects) != "Sanskrit" {
		t.Errorf("replaced = %v", names(got.Subjects))
	}
	res, err = save(true, true, row(w.s1, w.french), row(w.s2, w.sanskrit))
	must(t, err)
	if res.Unchanged != 2 || res.Created+res.Updated != 0 {
		t.Errorf("saving the same again = %+v", res)
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history`); n != 3 { // create s2, create s1, replace s2
		t.Errorf("history rows = %d", n)
	}

	// a row with no subjects removes the choice, when the caller may
	_, err = save(true, false, row(w.s1))
	fieldErr(t, "no delete right", err, "rows["+w.s1+"].subjectIds", "not_allowed")
	res, err = save(true, true, row(w.s1))
	must(t, err)
	if res.Removed != 1 {
		t.Fatalf("removed = %+v", res)
	}
	// adding needs the create right
	_, err = save(false, true, row(w.s1, w.hindi))
	fieldErr(t, "no create right", err, "rows["+w.s1+"].subjectIds", "not_allowed")
	_, err = save(true, true)
	fieldErr(t, "no rows", err, "rows", "required")
	_, err = save(true, true, row("nope", w.hindi))
	fieldErr(t, "bad id", err, "rows[nope].studentId", "invalid")
	if _, err := w.svc.Bulk(w.in(), w.f.Actor, electives.BulkInput{YearID: w.year, ClassID: w.c9, SectionID: w.secB, GroupID: w.lang, Rows: []electives.BulkRow{row(w.s1, w.hindi)}, AllowCreate: true}); err == nil {
		t.Error("a student of section A was saved into section B")
	} else {
		fieldErr(t, "wrong section", err, "rows["+w.s1+"].studentId", "not_in_section")
	}
}

func TestAClassSectionScopeNarrowsReadsAndRefusesWritesForOtherSections(t *testing.T) {
	w := setup(t)
	inA := w.create(t, w.s1, w.lang, "", w.hindi)
	inB := w.create(t, w.s3, w.lang, "", w.hindi)
	teacher := w.scoped(w.csmA)

	q, _ := crud.ParseList(1, 50, "", "", electives.SortKeys, electives.DefaultSort)
	p, err := w.svc.List(teacher, q, electives.Filter{})
	must(t, err)
	if p.Total != 1 || p.Items[0].ID != inA.ID {
		t.Fatalf("a section A teacher sees %+v", p.Items)
	}
	if p, err = w.svc.List(w.in(), q, electives.Filter{}); err != nil || p.Total != 2 {
		t.Fatalf("the whole school sees %d (%v)", p.Total, err)
	}
	if p, err = w.svc.List(w.scoped(), q, electives.Filter{}); err != nil || p.Total != 0 {
		t.Fatalf("an assignment to nothing sees %d (%v)", p.Total, err)
	}
	if _, err := w.svc.Get(teacher, inB.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get of another section: %v", err)
	}
	if _, err := w.svc.Get(teacher, inA.ID); err != nil {
		t.Errorf("get of their own: %v", err)
	}
	if _, err := w.svc.History(teacher, inB.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("history of another section: %v", err)
	}

	// writes for a student of another section look like writes for a student that does not exist
	_, err = w.svc.Create(teacher, w.f.Actor, electives.Input{StudentID: w.s3, YearID: w.year, GroupID: w.skill, SubjectIDs: []string{w.music, w.dance}})
	fieldErr(t, "create for another section", err, "studentId", "not_found")
	if _, err = w.svc.Replace(teacher, w.f.Actor, inB.ID, electives.Update{SubjectIDs: []string{w.french}}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("replace in another section: %v", err)
	}
	if err = w.svc.Delete(teacher, w.f.Actor, inB.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("delete in another section: %v", err)
	}
	if _, err = w.svc.Roster(teacher, w.year, w.c9, w.secB, w.lang); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("roster of another section: %v", err)
	}
	if _, err = w.svc.Bulk(teacher, w.f.Actor, electives.BulkInput{YearID: w.year, ClassID: w.c9, SectionID: w.secB, GroupID: w.lang, AllowCreate: true,
		Rows: []electives.BulkRow{{StudentID: w.s3, SubjectIDs: []string{w.french}}}}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bulk save of another section: %v", err)
	}
	if got, _ := w.svc.Get(w.in(), inB.ID); names(got.Subjects) != "Hindi" {
		t.Errorf("the other section's choice changed: %v", names(got.Subjects))
	}

	// and in their own section everything works
	made, err := w.svc.Create(teacher, w.f.Actor, electives.Input{StudentID: w.s2, YearID: w.year, GroupID: w.lang, SubjectIDs: []string{w.french}})
	must(t, err)
	if _, err = w.svc.Replace(teacher, w.f.Actor, made.ID, electives.Update{SubjectIDs: []string{w.sanskrit}}); err != nil {
		t.Errorf("replace in own section: %v", err)
	}
	r, err := w.svc.Roster(teacher, w.year, w.c9, w.secA, w.lang)
	must(t, err)
	if len(r.Students) != 2 {
		t.Errorf("own roster = %d students", len(r.Students))
	}
	if _, err = w.svc.Bulk(teacher, w.f.Actor, electives.BulkInput{YearID: w.year, ClassID: w.c9, SectionID: w.secA, GroupID: w.skill, AllowCreate: true,
		Rows: []electives.BulkRow{{StudentID: w.s1, SubjectIDs: []string{w.music, w.dance}}}}); err != nil {
		t.Errorf("bulk save of own section: %v", err)
	}
	if err = w.svc.Delete(teacher, w.f.Actor, made.ID); err != nil {
		t.Errorf("delete in own section: %v", err)
	}
}

func TestOneInstituteCannotSeeOrTouchAnothersChoices(t *testing.T) {
	w := setup(t)
	mine := w.create(t, w.s1, w.lang, "", w.hindi)
	bravo := modtest.In(w.f.Bravo)

	q, _ := crud.ParseList(1, 50, "", "", electives.SortKeys, electives.DefaultSort)
	if p, err := w.svc.List(bravo, q, electives.Filter{}); err != nil || p.Total != 0 {
		t.Fatalf("bravo sees %d (%v)", p.Total, err)
	}
	if _, err := w.svc.Get(bravo, mine.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := w.svc.Replace(bravo, w.f.Actor, mine.ID, electives.Update{SubjectIDs: []string{w.french}}); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("replace: %v", err)
	}
	if err := w.svc.Delete(bravo, w.f.Actor, mine.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
	if _, err := w.svc.History(bravo, mine.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("history: %v", err)
	}
	// alpha's ids do not exist in bravo: the student, the year and the group are all unknown there
	_, err := w.svc.Create(bravo, w.f.Actor, electives.Input{StudentID: w.s2, YearID: w.year, GroupID: w.lang, SubjectIDs: []string{w.hindi}})
	fieldErr(t, "alpha's student", err, "studentId", "not_found")
	if _, err := w.svc.Roster(bravo, w.year, w.c9, w.secA, w.lang); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("roster: %v", err)
	}
	if w.count(t, `SELECT count(*) FROM student_elective_choices WHERE tenant_id = $1`, w.f.Bravo.ID) != 0 {
		t.Error("bravo got rows")
	}
	// the database refuses a choice that mixes institutes, even for its owner
	bs := w.id(t, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth) VALUES ($1, 'B-1', 'B', 'B', 'female', DATE '2012-01-01') RETURNING id::text`, w.f.Bravo.ID)
	_, err = w.f.Admin.Exec(bg, `INSERT INTO student_elective_choices (tenant_id, student_id, year_id, group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid)`, w.f.Bravo.ID, bs, w.year, w.lang)
	if err == nil {
		t.Error("a choice mixed two institutes")
	}
}

func TestAnElectiveGroupInUseCannotBeDeleted(t *testing.T) {
	w := setup(t)
	groups := acaelectives.Service{DB: w.f.App}
	var inUse resource.InUseError
	if err := groups.Delete(w.in(), w.f.Actor, w.lang); !errors.As(err, &inUse) {
		t.Fatalf("a group with subjects mapped: %v", err)
	}
	empty := w.group(t, "EMPTY", "Empty", 0, 1, true)
	if err := groups.Delete(w.in(), w.f.Actor, empty); err != nil {
		t.Fatalf("an unused group: %v", err)
	}
	// the choice alone, with no subject mapped to the group any more, still holds the group
	w.create(t, w.s1, w.skill, "", w.music)
	w.exec(t, `UPDATE subject_class_mappings SET elective_group_id = NULL, is_elective = true WHERE elective_group_id = $1::uuid`, w.skill)
	if err := groups.Delete(w.in(), w.f.Actor, w.skill); !errors.As(err, &inUse) {
		t.Fatalf("a group students chose from: %v", err)
	}
}
