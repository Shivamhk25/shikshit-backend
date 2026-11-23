package admissions_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/admissions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
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

func fieldErr(t *testing.T, name string, err error, field, code string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Errorf("%s: want field errors, got %v", name, err)
		return
	}
	for _, e := range fe {
		if e.Field == field && (code == "" || e.Code == code) {
			return
		}
	}
	t.Errorf("%s: no %s error on %q: %v", name, code, field, fe)
}

type world struct {
	f                                        modtest.Fixture
	svc                                      admissions.Service
	class, year, secA, secB, unmapped, other string
	otherReg                                 string
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	a, b := modtest.In(f.Alpha), modtest.In(f.Bravo)
	now := time.Now().UTC()
	start, end := now.AddDate(0, -2, 0).Format("2006-01-02"), now.AddDate(0, 10, 0).Format("2006-01-02")
	y, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "Y", Name: "2026-27", StartDate: start, EndDate: end})
	must(t, err)
	c, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	oy, err := years.Service{DB: f.App}.Create(b, f.Actor, years.Input{Code: "Y", Name: "Other", StartDate: start, EndDate: end})
	must(t, err)
	oc, err := classes.Service{DB: f.App}.Create(b, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	w := world{f: f, svc: admissions.Service{DB: f.App}, class: c.ID, year: y.ID}
	for _, s := range []struct {
		tenant, code string
		dst          *string
	}{{f.Alpha.ID, "A", &w.secA}, {f.Alpha.ID, "B", &w.secB}, {f.Alpha.ID, "C", &w.unmapped}, {f.Bravo.ID, "A", &w.other}} {
		must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, $2, 'Section ' || $2) RETURNING id::text`, s.tenant, s.code).Scan(s.dst))
	}
	for _, m := range []struct{ tenant, year, class, section string }{{f.Alpha.ID, y.ID, c.ID, w.secA}, {f.Alpha.ID, y.ID, c.ID, w.secB}, {f.Bravo.ID, oy.ID, oc.ID, w.other}} {
		_, err = f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, m.tenant, m.year, m.class, m.section)
		must(t, err)
	}
	// an accepted registration in the other institute
	br, err := registrations.Service{DB: f.App}.Create(b, f.Actor, registrations.Input{AppliedOn: registrations.Today(now), FirstName: "Other", DateOfBirth: "2018-01-01",
		GuardianPhone: "9000000000", ClassID: oc.ID, YearID: oy.ID, Status: "accepted"})
	must(t, err)
	w.otherReg = br.ID
	return w
}

func (w world) register(t *testing.T, first string, mut func(*registrations.Input)) string {
	t.Helper()
	in := registrations.Input{AppliedOn: registrations.Today(time.Now()), FirstName: first, LastName: ptr("Rao"), DateOfBirth: "2018-06-01", Gender: ptr("female"),
		FatherName: ptr("Ravi Rao"), MotherName: ptr("Meera Rao"), GuardianPhone: "+91 98765 43210", ClassID: w.class, YearID: w.year, Status: "accepted"}
	if mut != nil {
		mut(&in)
	}
	r, err := registrations.Service{DB: w.f.App}.Create(modtest.In(w.f.Alpha), w.f.Actor, in)
	must(t, err)
	return r.ID
}

func (w world) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, w.f.Alpha.ID).Scan(&n))
	return n
}

func (w world) admit(t *testing.T, id string, mut func(*admissions.Input)) (admissions.Result, error) {
	in := admissions.Input{SectionID: w.secA}
	if mut != nil {
		mut(&in)
	}
	return w.svc.Admit(modtest.In(w.f.Alpha), w.f.Actor, id, in)
}

func TestAdmitCreatesTheStudentGuardiansAndEnrolment(t *testing.T) {
	w := setup(t)
	_, err := w.f.Admin.Exec(bg, `UPDATE academic_years SET is_current = true WHERE id::text = $1`, w.year)
	must(t, err)
	id := w.register(t, "Asha", func(i *registrations.Input) { i.GuardianName = ptr("Ravi Rao") })
	res, err := w.admit(t, id, func(i *admissions.Input) { i.ParentEmail = ptr("ravi@example.org") })
	must(t, err)
	s, e := res.Student, res.Enrollment
	if res.AlreadyAdmitted || s.RegistrationCode != "STU-000001" || s.FullName != "Asha Rao" || s.Gender != "female" || s.DateOfBirth != "2018-06-01" ||
		s.Status != "active" || s.ParentEmail == nil || *s.ParentEmail != "ravi@example.org" || s.AdmissionDate == nil || *s.AdmissionDate != registrations.Today(time.Now()) {
		t.Fatalf("student %+v", s)
	}
	if s.CurrentClassName == nil || *s.CurrentClassName != "Class 5" || s.CurrentSectionName == nil || *s.CurrentSectionName != "Section A" || s.CurrentRollNumber == nil || *s.CurrentRollNumber != "1" {
		t.Errorf("student class/section/roll %v %v %v", s.CurrentClassName, s.CurrentSectionName, s.CurrentRollNumber)
	}
	if e.StudentID != s.ID || e.YearID != w.year || e.ClassID != w.class || e.SectionID != w.secA || e.Status != "enrolled" || e.RollNumber == nil || *e.RollNumber != "1" {
		t.Errorf("enrolment %+v", e)
	}
	// father, mother and the named guardian (the same person as the father: one link, as father), the guardian is primary
	if len(res.Guardians) != 2 || res.Guardians[0].Relation != "father" || !res.Guardians[0].IsPrimary || res.Guardians[1].Relation != "mother" || res.Guardians[1].IsPrimary {
		t.Errorf("guardians %+v", res.Guardians)
	}
	for table, want := range map[string]int{"students": 1, "guardians": 2, "student_guardians": 2, "student_enrollments": 1} {
		if n := w.count(t, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}
	var linked *string
	must(t, w.f.Admin.QueryRow(bg, `SELECT admitted_student_id::text FROM student_registrations WHERE id::text = $1`, id).Scan(&linked))
	if linked == nil || *linked != s.ID {
		t.Errorf("registration link %v", linked)
	}
	for _, action := range []string{"admissions.admit", "students.create", "student_enrollments.create", "guardians.create", "student_guardians.create"} {
		if w.f.AuditCount(t, action) == 0 {
			t.Errorf("no audit row for %s", action)
		}
	}
}

func TestAdmitTwiceIsTheSameStudent(t *testing.T) {
	w := setup(t)
	id := w.register(t, "Asha", nil)
	first, err := w.admit(t, id, nil)
	must(t, err)
	again, err := w.admit(t, id, func(i *admissions.Input) {
		i.SectionID = w.secB
		i.RollNumber = ptr("77")
		i.StudentCode = ptr("OTHER-1")
	})
	must(t, err)
	if !again.AlreadyAdmitted || again.Student.ID != first.Student.ID || again.Student.RegistrationCode != "STU-000001" || again.Enrollment.ID != first.Enrollment.ID ||
		again.Enrollment.SectionID != w.secA || len(again.Guardians) != 2 {
		t.Errorf("second admit %+v", again)
	}
	for table, want := range map[string]int{"students": 1, "guardians": 2, "student_guardians": 2, "student_enrollments": 1} {
		if n := w.count(t, table); n != want {
			t.Errorf("%s = %d, want %d", table, n, want)
		}
	}
	if n := w.f.AuditCount(t, "admissions.admit"); n != 1 {
		t.Errorf("admit audit rows = %d, want 1", n)
	}
}

func TestSimultaneousAdmitsMakeOneStudent(t *testing.T) {
	w := setup(t)
	id := w.register(t, "Asha", nil)
	var wg sync.WaitGroup
	ids := make([]string, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := w.admit(t, id, nil)
			ids[i], errs[i] = r.Student.ID, err
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] || ids[0] == "" {
			t.Fatalf("call %d: %v %q (first %q)", i, errs[i], ids[i], ids[0])
		}
	}
	if n := w.count(t, "students"); n != 1 {
		t.Errorf("students = %d, want 1", n)
	}
}

func TestOnlyAnAcceptedRegistrationCanBeAdmitted(t *testing.T) {
	w := setup(t)
	for _, status := range []string{"submitted", "under_review", "rejected", "withdrawn"} {
		id := w.register(t, "Kid "+status, func(i *registrations.Input) { i.Status = status })
		if _, err := w.admit(t, id, nil); !errors.Is(err, admissions.ErrNotAccepted) {
			t.Errorf("%s: %v, want ErrNotAccepted", status, err)
		}
	}
	if _, err := w.admit(t, "00000000-0000-0000-0000-000000000001", nil); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if n := w.count(t, "students"); n != 0 {
		t.Errorf("students = %d, want 0", n)
	}
}

func TestSectionMustBeMappedToTheClassThatYear(t *testing.T) {
	w := setup(t)
	id := w.register(t, "Asha", nil)
	for name, c := range map[string]struct{ section, code string }{
		"none":             {"", "required"},
		"not a uuid":       {"nope", "invalid"},
		"unmapped section": {w.unmapped, "not_in_class"},
		"other institute":  {w.other, "not_in_class"},
		"unknown section":  {"00000000-0000-0000-0000-000000000001", "not_in_class"},
	} {
		_, err := w.admit(t, id, func(i *admissions.Input) { i.SectionID = c.section })
		fieldErr(t, name, err, "sectionId", c.code)
	}
	// a section mapped for another year does not count
	_, err := w.f.Admin.Exec(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y2', 'Next', '2030-04-01', '2031-03-31')`, w.f.Alpha.ID)
	must(t, err)
	var y2 string
	must(t, w.f.Admin.QueryRow(bg, `SELECT id::text FROM academic_years WHERE code = 'Y2' AND tenant_id = $1`, w.f.Alpha.ID).Scan(&y2))
	_, err = w.f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, w.f.Alpha.ID, y2, w.class, w.unmapped)
	must(t, err)
	_, err = w.admit(t, id, func(i *admissions.Input) { i.SectionID = w.unmapped })
	fieldErr(t, "mapped for another year", err, "sectionId", "not_in_class")
	if n := w.count(t, "students"); n != 0 {
		t.Errorf("students = %d, want 0", n)
	}
}

func TestStudentCodeTypedOrAutomatic(t *testing.T) {
	w := setup(t)
	typed := w.register(t, "Typed", nil)
	res, err := w.admit(t, typed, func(i *admissions.Input) { i.StudentCode = ptr(" 2026/045 ") })
	must(t, err)
	if res.Student.RegistrationCode != "2026/045" {
		t.Errorf("typed code %q", res.Student.RegistrationCode)
	}
	// the next automatic code is not affected by a typed one
	auto := w.register(t, "Auto", nil)
	res, err = w.admit(t, auto, nil)
	must(t, err)
	if res.Student.RegistrationCode != "STU-000001" {
		t.Errorf("auto code %q", res.Student.RegistrationCode)
	}
	// a code a school typed by hand that looks like the next automatic one is skipped, not duplicated
	_, err = w.f.Admin.Exec(bg, `INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth) VALUES ($1, 'stu-000002', 'Hand', 'male', '2014-01-01')`, w.f.Alpha.ID)
	must(t, err)
	next := w.register(t, "Next", nil)
	res, err = w.admit(t, next, nil)
	must(t, err)
	if res.Student.RegistrationCode != "STU-000003" {
		t.Errorf("auto code after a typed STU-000002 = %q, want STU-000003", res.Student.RegistrationCode)
	}
	dup := w.register(t, "Dup", nil)
	_, err = w.admit(t, dup, func(i *admissions.Input) { i.StudentCode = ptr("2026/045") })
	fieldErr(t, "used code", err, "studentCode", "duplicate")
	_, err = w.admit(t, dup, func(i *admissions.Input) { i.StudentCode = ptr("STU-000001") })
	fieldErr(t, "used code, other case", err, "studentCode", "duplicate")
	_, err = w.admit(t, dup, func(i *admissions.Input) { i.StudentCode = ptr(fmt.Sprintf("%031d", 1)) })
	fieldErr(t, "too long", err, "studentCode", "")
}

func TestRollNumbers(t *testing.T) {
	w := setup(t)
	var rolls []string
	for _, name := range []string{"One", "Two", "Three"} {
		res, err := w.admit(t, w.register(t, name, func(i *registrations.Input) { i.DateOfBirth = "2018-0" + fmt.Sprint(len(rolls)+1) + "-01" }), nil)
		must(t, err)
		rolls = append(rolls, *res.Enrollment.RollNumber)
	}
	if fmt.Sprint(rolls) != "[1 2 3]" {
		t.Errorf("automatic rolls %v", rolls)
	}
	// typed rolls: a letter roll does not break the numbering, a used roll is refused, another section is separate
	res, err := w.admit(t, w.register(t, "Typed", nil), func(i *admissions.Input) { i.RollNumber = ptr("A-7"); i.SectionID = w.secB })
	must(t, err)
	if *res.Enrollment.RollNumber != "A-7" {
		t.Errorf("typed roll %v", *res.Enrollment.RollNumber)
	}
	res, err = w.admit(t, w.register(t, "InB", nil), func(i *admissions.Input) { i.SectionID = w.secB })
	must(t, err)
	if *res.Enrollment.RollNumber != "1" {
		t.Errorf("first numeric roll in section B = %v, want 1", *res.Enrollment.RollNumber)
	}
	_, err = w.admit(t, w.register(t, "Clash", nil), func(i *admissions.Input) { i.RollNumber = ptr("2") })
	fieldErr(t, "used roll", err, "rollNumber", "duplicate")
	_, err = w.admit(t, w.register(t, "Clash2", nil), func(i *admissions.Input) { i.RollNumber = ptr("a-7"); i.SectionID = w.secB })
	fieldErr(t, "used roll other case", err, "rollNumber", "duplicate")
	_, err = w.admit(t, w.register(t, "Long", nil), func(i *admissions.Input) { i.RollNumber = ptr("123456789012345678901") })
	fieldErr(t, "long roll", err, "rollNumber", "")
}

func TestGenderAndDuplicateChild(t *testing.T) {
	w := setup(t)
	noGender := w.register(t, "Neha", func(i *registrations.Input) { i.Gender = nil })
	_, err := w.admit(t, noGender, nil)
	fieldErr(t, "no gender", err, "gender", "required")
	_, err = w.admit(t, noGender, func(i *admissions.Input) { i.Gender = ptr("robot") })
	fieldErr(t, "bad gender", err, "gender", "")
	res, err := w.admit(t, noGender, func(i *admissions.Input) { i.Gender = ptr("Female") })
	must(t, err)
	if res.Student.Gender != "female" {
		t.Errorf("gender %q", res.Student.Gender)
	}

	// the same child registered again (same name and date of birth) is flagged, unless the registrar confirms
	again := w.register(t, "Neha", nil)
	_, err = w.admit(t, again, nil)
	fieldErr(t, "duplicate child", err, "confirmDuplicate", "possible_duplicate")
	if n := w.count(t, "students"); n != 1 {
		t.Errorf("students = %d, want 1", n)
	}
	res, err = w.admit(t, again, func(i *admissions.Input) { i.ConfirmDuplicate = true })
	must(t, err)
	if res.Student.RegistrationCode != "STU-000002" {
		t.Errorf("confirmed duplicate code %q", res.Student.RegistrationCode)
	}
	for name, mut := range map[string]func(*admissions.Input){
		"future admission date": func(i *admissions.Input) { i.AdmissionDate = ptr(time.Now().AddDate(0, 0, 3).Format("2006-01-02")) },
		"bad admission date":    func(i *admissions.Input) { i.AdmissionDate = ptr("1/1/2026") },
		"bad parent email":      func(i *admissions.Input) { i.ParentEmail = ptr("not an email") },
	} {
		_, err = w.admit(t, w.register(t, "Fresh "+name, func(i *registrations.Input) { i.DateOfBirth = "2017-02-02" }), mut)
		fieldErr(t, name, err, map[string]string{"future admission date": "admissionDate", "bad admission date": "admissionDate", "bad parent email": "parentEmail"}[name], "")
	}
}

func TestGuardiansAreReusedAndOptional(t *testing.T) {
	w := setup(t)
	a, err := w.admit(t, w.register(t, "Elder", nil), nil)
	must(t, err)
	b, err := w.admit(t, w.register(t, "Younger", func(i *registrations.Input) { i.DateOfBirth = "2020-01-01" }), nil)
	must(t, err)
	// the same parents (same names and phone) are the same guardians, linked to both children
	if n := w.count(t, "guardians"); n != 2 {
		t.Errorf("guardians = %d, want 2 (reused for the sibling)", n)
	}
	if a.Guardians[0].GuardianID != b.Guardians[0].GuardianID || len(b.Guardians) != 2 {
		t.Errorf("sibling guardians %+v vs %+v", a.Guardians, b.Guardians)
	}
	// only a guardian named: that person is the primary link, relation guardian
	g, err := w.admit(t, w.register(t, "Ward", func(i *registrations.Input) {
		i.DateOfBirth, i.FatherName, i.MotherName, i.GuardianName = "2019-03-03", nil, nil, ptr("Uncle Sam")
	}), nil)
	must(t, err)
	if len(g.Guardians) != 1 || g.Guardians[0].Relation != "guardian" || !g.Guardians[0].IsPrimary {
		t.Errorf("guardian only %+v", g.Guardians)
	}
	// the named guardian, who is not a parent, is the primary
	m, err := w.admit(t, w.register(t, "Mixed", func(i *registrations.Input) { i.DateOfBirth, i.GuardianName = "2016-03-03", ptr("Aunt Jo") }), nil)
	must(t, err)
	if len(m.Guardians) != 3 || m.Guardians[2].Name != "Aunt Jo" || !m.Guardians[2].IsPrimary || m.Guardians[0].IsPrimary {
		t.Errorf("named guardian primary %+v", m.Guardians)
	}
	// no names at all: the child is still admitted, with a note, and no guardian record
	before := w.count(t, "guardians")
	n, err := w.admit(t, w.register(t, "Nameless", func(i *registrations.Input) {
		i.DateOfBirth, i.FatherName, i.MotherName, i.GuardianName = "2015-05-05", nil, nil, nil
	}), nil)
	must(t, err)
	if len(n.Guardians) != 0 || len(n.Notes) != 1 || w.count(t, "guardians") != before {
		t.Errorf("no guardian names: %+v, guardians %d -> %d", n, before, w.count(t, "guardians"))
	}
}

// A failure at the last step undoes everything before it: the student, the guardians and links, the automatic code
// and the registration's link.
func TestAFailureRollsBackEverything(t *testing.T) {
	w := setup(t)
	id := w.register(t, "Asha", nil)
	_, err := w.f.Admin.Exec(bg, `CREATE FUNCTION boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'enrolment failed'; END $$;
		CREATE TRIGGER boom BEFORE INSERT ON student_enrollments FOR EACH ROW EXECUTE FUNCTION boom()`)
	must(t, err)
	if _, err := w.admit(t, id, nil); err == nil {
		t.Fatal("admit succeeded although the enrolment failed")
	}
	for _, table := range []string{"students", "guardians", "student_guardians", "student_enrollments", "student_code_counters"} {
		if n := w.count(t, table); n != 0 {
			t.Errorf("%s = %d after a failed admit, want 0", table, n)
		}
	}
	var linked *string
	must(t, w.f.Admin.QueryRow(bg, `SELECT admitted_student_id::text FROM student_registrations WHERE id::text = $1`, id).Scan(&linked))
	if linked != nil || w.f.AuditCount(t, "admissions.admit") != 0 || w.f.AuditCount(t, "students.create") != 0 {
		t.Errorf("link %v, audit rows kept", linked)
	}
	// once the fault is gone the same registration admits normally, with the first code
	_, err = w.f.Admin.Exec(bg, `DROP TRIGGER boom ON student_enrollments`)
	must(t, err)
	res, err := w.admit(t, id, nil)
	must(t, err)
	if res.Student.RegistrationCode != "STU-000001" {
		t.Errorf("code after rollback %q", res.Student.RegistrationCode)
	}
}

func TestOtherInstitutesAreInvisible(t *testing.T) {
	w := setup(t)
	if _, err := w.admit(t, w.otherReg, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("admit a registration of another institute: %v", err)
	}
	if _, err := w.svc.View(modtest.In(w.f.Alpha), w.f.Actor, w.otherReg); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("view a registration of another institute: %v", err)
	}
	p, err := w.svc.ListAudited(modtest.In(w.f.Alpha), w.f.Actor, crud.ListQuery{Page: 1, Size: 50, OrderBy: "applied_on"}, admissions.Filter{})
	must(t, err)
	if p.Total != 0 {
		t.Errorf("queue of alpha shows %d rows of bravo", p.Total)
	}
	rows, err := w.svc.Preview(modtest.In(w.f.Alpha), w.f.Actor, []admissions.Item{{RegistrationID: w.otherReg, Input: admissions.Input{SectionID: w.secA}}})
	must(t, err)
	if rows[0].Outcome != admissions.OutcomeProblem || rows[0].Problems[0].Code != "not_found" {
		t.Errorf("preview of another institute's registration: %+v", rows[0])
	}
	if n := w.count(t, "students"); n != 0 {
		t.Errorf("students = %d", n)
	}
}

type refuse struct{ reason string }

func (g refuse) Check(_ context.Context, _ pgx.Tx, _ string) (crud.FieldErrors, error) {
	return crud.FieldErrors{{Field: "fee", Code: "unpaid", Message: g.reason}}, nil
}

func TestAGateCanRefuseAnAdmission(t *testing.T) {
	w := setup(t)
	id := w.register(t, "Asha", nil)
	gated := admissions.Service{DB: w.f.App, Gates: []admissions.Gate{refuse{"The registration fee is not paid."}}}
	_, err := gated.Admit(modtest.In(w.f.Alpha), w.f.Actor, id, admissions.Input{SectionID: w.secA})
	var ne admissions.NotEligible
	if !errors.As(err, &ne) || len(ne.Problems) != 1 || ne.Problems[0].Message != "The registration fee is not paid." {
		t.Fatalf("gated admit: %v", err)
	}
	rows, err := gated.Preview(modtest.In(w.f.Alpha), w.f.Actor, []admissions.Item{{RegistrationID: id, Input: admissions.Input{SectionID: w.secA}}})
	must(t, err)
	if rows[0].Outcome != admissions.OutcomeProblem || rows[0].Problems[0].Code != "not_eligible" {
		t.Errorf("gated preview %+v", rows[0])
	}
	if n := w.count(t, "students"); n != 0 {
		t.Errorf("students = %d", n)
	}
}

func TestPreviewReportsProblemsAndWritesNothing(t *testing.T) {
	w := setup(t)
	ok1 := w.register(t, "Ok1", nil)
	ok2 := w.register(t, "Ok2", func(i *registrations.Input) { i.DateOfBirth = "2017-01-01" })
	noSection := w.register(t, "NoSection", func(i *registrations.Input) { i.DateOfBirth = "2017-02-01" })
	unmapped := w.register(t, "Unmapped", func(i *registrations.Input) { i.DateOfBirth = "2017-03-01" })
	pending := w.register(t, "Pending", func(i *registrations.Input) { i.DateOfBirth = "2017-04-01"; i.Status = "submitted" })
	clashA := w.register(t, "ClashA", func(i *registrations.Input) { i.DateOfBirth = "2017-05-01" })
	clashB := w.register(t, "ClashB", func(i *registrations.Input) { i.DateOfBirth = "2017-06-01" })
	codeA := w.register(t, "CodeA", func(i *registrations.Input) { i.DateOfBirth = "2017-07-01" })
	codeB := w.register(t, "CodeB", func(i *registrations.Input) { i.DateOfBirth = "2017-08-01" })
	already := w.register(t, "Already", func(i *registrations.Input) { i.DateOfBirth = "2017-09-01" })
	_, err := w.admit(t, already, func(i *admissions.Input) { i.RollNumber = ptr("50") })
	must(t, err)
	studentsBefore, countersBefore := w.count(t, "students"), w.count(t, "student_code_counters")

	in := func(id, section string, mut func(*admissions.Input)) admissions.Item {
		it := admissions.Item{RegistrationID: id, Input: admissions.Input{SectionID: section}}
		if mut != nil {
			mut(&it.Input)
		}
		return it
	}
	items := []admissions.Item{
		in(ok1, w.secA, nil), in(ok2, w.secA, nil), in(noSection, "", nil), in(unmapped, w.unmapped, nil), in(pending, w.secA, nil),
		in(clashA, w.secA, func(i *admissions.Input) { i.RollNumber = ptr("9") }), in(clashB, w.secA, func(i *admissions.Input) { i.RollNumber = ptr("9") }),
		in(codeA, w.secB, func(i *admissions.Input) { i.StudentCode = ptr("X-1") }), in(codeB, w.secB, func(i *admissions.Input) { i.StudentCode = ptr("x-1") }),
		in(already, w.secA, nil), in(ok1, w.secA, nil), in(w.otherReg, w.secA, nil),
	}
	rows, err := w.svc.Preview(modtest.In(w.f.Alpha), w.f.Actor, items)
	must(t, err)
	want := []struct{ outcome, field, code string }{
		{admissions.OutcomeOK, "", ""}, {admissions.OutcomeOK, "", ""}, {admissions.OutcomeProblem, "sectionId", "required"}, {admissions.OutcomeProblem, "sectionId", "not_in_class"},
		{admissions.OutcomeProblem, "registrationId", "not_accepted"}, {admissions.OutcomeOK, "", ""}, {admissions.OutcomeProblem, "rollNumber", "duplicate"},
		{admissions.OutcomeOK, "", ""}, {admissions.OutcomeProblem, "studentCode", "duplicate"}, {admissions.OutcomeAlreadyAdmitted, "", ""},
		{admissions.OutcomeProblem, "registrationId", "duplicate_in_request"}, {admissions.OutcomeProblem, "registrationId", "not_found"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d rows", len(rows))
	}
	for i, c := range want {
		r := rows[i]
		if r.Outcome != c.outcome || (c.field != "" && (len(r.Problems) == 0 || r.Problems[0].Field != c.field || r.Problems[0].Code != c.code)) {
			t.Errorf("row %d: %s %+v, want %s %s/%s", i, r.Outcome, r.Problems, c.outcome, c.field, c.code)
		}
	}
	// rows see the rows before them: the second automatic roll is the next one, not the same
	if *rows[0].Result.Enrollment.RollNumber != "51" || *rows[1].Result.Enrollment.RollNumber != "52" ||
		rows[0].Result.Student.RegistrationCode != "STU-000002" || rows[1].Result.Student.RegistrationCode != "STU-000003" {
		t.Errorf("preview numbers %s/%s %s/%s", rows[0].Result.Student.RegistrationCode, *rows[0].Result.Enrollment.RollNumber,
			rows[1].Result.Student.RegistrationCode, *rows[1].Result.Enrollment.RollNumber)
	}
	if w.count(t, "students") != studentsBefore || w.count(t, "student_code_counters") != countersBefore {
		t.Error("a preview wrote students or moved the code counter")
	}
	var counter int
	must(t, w.f.Admin.QueryRow(bg, `SELECT last_value FROM student_code_counters WHERE tenant_id = $1`, w.f.Alpha.ID).Scan(&counter))
	if counter != 1 {
		t.Errorf("code counter after preview = %d, want 1", counter)
	}
	if n := w.f.AuditCount(t, "admissions.admit"); n != 1 {
		t.Errorf("admit audit rows after preview = %d, want 1 (the earlier real one)", n)
	}

	// committing the valid rows gives exactly what the preview said
	good := []admissions.Item{items[0], items[1], items[5], items[7]}
	done, err := w.svc.AdmitMany(modtest.In(w.f.Alpha), w.f.Actor, good)
	must(t, err)
	for i, r := range done {
		if r.Outcome != admissions.OutcomeAdmitted || r.Result.Student.RegistrationCode == "" {
			t.Errorf("commit row %d: %+v", i, r)
		}
	}
	if done[0].Result.Student.RegistrationCode != "STU-000002" || *done[0].Result.Enrollment.RollNumber != "51" || *done[1].Result.Enrollment.RollNumber != "52" {
		t.Errorf("commit numbers differ from the preview: %s %s %s", done[0].Result.Student.RegistrationCode, *done[0].Result.Enrollment.RollNumber, *done[1].Result.Enrollment.RollNumber)
	}
	if done[3].Result.Student.RegistrationCode != "X-1" {
		t.Errorf("typed code %s", done[3].Result.Student.RegistrationCode)
	}
	if _, err := w.svc.Preview(modtest.In(w.f.Alpha), w.f.Actor, nil); !errors.Is(err, admissions.ErrTooMany) {
		t.Errorf("empty preview: %v", err)
	}
	if _, err := w.svc.AdmitMany(modtest.In(w.f.Alpha), w.f.Actor, make([]admissions.Item, admissions.MaxBatch+1)); !errors.Is(err, admissions.ErrTooMany) {
		t.Errorf("oversized bulk: %v", err)
	}
}

// One family's problem does not hold back the others, and each admission stands or falls on its own.
func TestBulkAdmitsTheValidOnesAndIsSafeToRepeat(t *testing.T) {
	w := setup(t)
	good1 := w.register(t, "Good1", nil)
	bad := w.register(t, "Bad", func(i *registrations.Input) { i.DateOfBirth = "2017-02-01" })
	good2 := w.register(t, "Good2", func(i *registrations.Input) { i.DateOfBirth = "2017-03-01" })
	items := []admissions.Item{
		{RegistrationID: good1, Input: admissions.Input{SectionID: w.secA}},
		{RegistrationID: bad, Input: admissions.Input{SectionID: w.unmapped}},
		{RegistrationID: good2, Input: admissions.Input{SectionID: w.secA}},
	}
	rows, err := w.svc.AdmitMany(modtest.In(w.f.Alpha), w.f.Actor, items)
	must(t, err)
	if rows[0].Outcome != admissions.OutcomeAdmitted || rows[1].Outcome != admissions.OutcomeProblem || rows[2].Outcome != admissions.OutcomeAdmitted {
		t.Fatalf("outcomes %s %s %s", rows[0].Outcome, rows[1].Outcome, rows[2].Outcome)
	}
	if w.count(t, "students") != 2 {
		t.Errorf("students = %d, want 2", w.count(t, "students"))
	}
	again, err := w.svc.AdmitMany(modtest.In(w.f.Alpha), w.f.Actor, items)
	must(t, err)
	if again[0].Outcome != admissions.OutcomeAlreadyAdmitted || again[2].Outcome != admissions.OutcomeAlreadyAdmitted || again[0].Result.Student.ID != rows[0].Result.Student.ID {
		t.Errorf("repeat %s %s", again[0].Outcome, again[2].Outcome)
	}
	if w.count(t, "students") != 2 {
		t.Errorf("students after repeat = %d, want 2", w.count(t, "students"))
	}

	// a failure inside one row's transaction undoes only that row
	_, err = w.f.Admin.Exec(bg, `CREATE FUNCTION boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.roll_number = '666' THEN RAISE EXCEPTION 'no'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER boom BEFORE INSERT ON student_enrollments FOR EACH ROW EXECUTE FUNCTION boom()`)
	must(t, err)
	f1 := w.register(t, "Fails", func(i *registrations.Input) { i.DateOfBirth = "2016-02-01" })
	f2 := w.register(t, "Fine", func(i *registrations.Input) { i.DateOfBirth = "2016-03-01" })
	rows, err = w.svc.AdmitMany(modtest.In(w.f.Alpha), w.f.Actor, []admissions.Item{
		{RegistrationID: f1, Input: admissions.Input{SectionID: w.secA, RollNumber: ptr("666")}},
		{RegistrationID: f2, Input: admissions.Input{SectionID: w.secA}},
	})
	must(t, err)
	if rows[0].Outcome != admissions.OutcomeProblem || rows[0].Problems[0].Code != "failed" || rows[1].Outcome != admissions.OutcomeAdmitted {
		t.Errorf("failing row %+v / %s", rows[0], rows[1].Outcome)
	}
	var linked *string
	must(t, w.f.Admin.QueryRow(bg, `SELECT admitted_student_id::text FROM student_registrations WHERE id::text = $1`, f1).Scan(&linked))
	if linked != nil {
		t.Error("the failed row is marked admitted")
	}
	if w.count(t, "students") != 3 {
		t.Errorf("students = %d, want 3", w.count(t, "students"))
	}
}

func TestQueueListsAcceptedRegistrationsNotYetAdmitted(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	r1 := w.register(t, "Asha", nil)
	_ = w.register(t, "Bina", func(i *registrations.Input) { i.DateOfBirth = "2017-01-01" })
	_ = w.register(t, "Chitra", func(i *registrations.Input) { i.DateOfBirth = "2017-02-01"; i.Status = "under_review" })
	_ = w.register(t, "Dev", func(i *registrations.Input) { i.DateOfBirth = "2017-03-01"; i.Status = "rejected" })
	list := func(f admissions.Filter) crud.Page[admissions.Admission] {
		q, err := crud.ParseList(1, 50, "applicant", "", admissions.SortKeys, admissions.DefaultSort)
		must(t, err)
		p, err := w.svc.ListAudited(a, w.f.Actor, q, f)
		must(t, err)
		return p
	}
	if p := list(admissions.Filter{}); p.Total != 2 || p.Items[0].FullName != "Asha Rao" || p.Items[0].Admitted {
		t.Errorf("queue %+v", p)
	}
	if p := list(admissions.Filter{ClassID: w.class, YearID: w.year}); p.Total != 2 {
		t.Errorf("filtered queue %d", p.Total)
	}
	if p := list(admissions.Filter{ClassID: "00000000-0000-0000-0000-000000000009"}); p.Total != 0 {
		t.Errorf("other class %d", p.Total)
	}
	if p := list(admissions.Filter{State: admissions.StateAdmitted}); p.Total != 0 {
		t.Errorf("admitted before any %d", p.Total)
	}
	res, err := w.admit(t, r1, func(i *admissions.Input) { i.SectionID = w.secB })
	must(t, err)
	if p := list(admissions.Filter{}); p.Total != 1 || p.Items[0].FullName != "Bina Rao" {
		t.Errorf("queue after admit %+v", p)
	}
	p := list(admissions.Filter{State: admissions.StateAdmitted})
	if p.Total != 1 || p.Items[0].StudentID == nil || *p.Items[0].StudentID != res.Student.ID || *p.Items[0].StudentCode != "STU-000001" ||
		*p.Items[0].EnrolledSection != "Section B" || *p.Items[0].RollNumber != "1" || p.Items[0].AdmittedAt == nil {
		t.Errorf("admitted list %+v", p.Items)
	}
	v, err := w.svc.View(a, w.f.Actor, r1)
	must(t, err)
	if !v.Admitted || v.Status != "accepted" {
		t.Errorf("view %+v", v)
	}
	if w.f.AuditCount(t, "admissions.list") == 0 || w.f.AuditCount(t, "admissions.view") == 0 {
		t.Error("reads of the queue are not audited")
	}
	// editing the registration afterwards cannot unlink the student
	_, err = w.f.Admin.Exec(bg, `UPDATE student_registrations SET admitted_student_id = NULL, admitted_at = NULL WHERE id::text = $1`, r1)
	must(t, err)
	v, err = w.svc.View(a, w.f.Actor, r1)
	must(t, err)
	if !v.Admitted {
		t.Error("the admission link was cleared by an update")
	}
}
