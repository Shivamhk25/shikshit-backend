// Package stutest tests students and their yearly enrollments together, because an enrollment points at a student and
// at the class sections of the academic structure.
package stutest_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/aca/classsections"
	"github.com/pie360/backend/internal/modules/aca/sections"
	"github.com/pie360/backend/internal/modules/aca/years"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/modules/stu/enrollments"
	"github.com/pie360/backend/internal/modules/stu/students"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func ptr[T any](v T) *T { return &v }

type world struct {
	f                 modtest.Fixture
	year, class, sec  string
	sec2              string
	otherClass, oYear string
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	a, b := modtest.In(f.Alpha), modtest.In(f.Bravo)
	now := time.Now().UTC()
	start, end := now.AddDate(0, -2, 0).Format("2006-01-02"), now.AddDate(0, 10, 0).Format("2006-01-02")
	y, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "Y", Name: "Current year", StartDate: start, EndDate: end, IsCurrent: true})
	must(t, err)
	c, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	s1, err := sections.Service{DB: f.App}.Create(a, f.Actor, sections.Input{Code: "A", Name: "A"})
	must(t, err)
	s2, err := sections.Service{DB: f.App}.Create(a, f.Actor, sections.Input{Code: "B", Name: "B"})
	must(t, err)
	_, err = classsections.Service{DB: f.App}.Create(a, f.Actor, classsections.Input{YearID: y.ID, ClassID: c.ID, SectionID: s1.ID})
	must(t, err)
	oy, err := years.Service{DB: f.App}.Create(b, f.Actor, years.Input{Code: "Y", Name: "Other", StartDate: start, EndDate: end})
	must(t, err)
	oc, err := classes.Service{DB: f.App}.Create(b, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	return world{f: f, year: y.ID, class: c.ID, sec: s1.ID, sec2: s2.ID, otherClass: oc.ID, oYear: oy.ID}
}

func stu(code string) students.Input {
	return students.Input{RegistrationCode: code, FirstName: "Asha", LastName: ptr("Rao"), Gender: "female", DateOfBirth: "2015-06-01"}
}

func fieldErr(t *testing.T, name string, err error, field string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Errorf("%s: want field errors, got %v", name, err)
		return
	}
	for _, e := range fe {
		if e.Field == field {
			return
		}
	}
	t.Errorf("%s: no error on %q: %v", name, field, fe)
}

func TestStudentRules(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := students.Service{DB: w.f.App}
	in := stu("  R-001 ")
	in.MiddleName, in.Mobile, in.ParentEmail, in.ApaarID = ptr(" K "), ptr("+91 98765 43210"), ptr("parent@example.org"), ptr("123456789012")
	s, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if s.RegistrationCode != "R-001" || s.FullName != "Asha K Rao" || s.Status != "active" || s.CurrentClassName != nil {
		t.Fatalf("%+v", s)
	}
	for name, c := range map[string]struct {
		mut   func(*students.Input)
		field string
	}{
		"duplicate code":  {func(i *students.Input) { i.RegistrationCode = "r-001" }, "registrationCode"},
		"duplicate apaar": {func(i *students.Input) { i.RegistrationCode = "R-2"; i.ApaarID = ptr("123456789012") }, "apaarId"},
		"no first name":   {func(i *students.Input) { i.RegistrationCode = "R-3"; i.FirstName = " " }, "firstName"},
		"bad gender":      {func(i *students.Input) { i.RegistrationCode = "R-4"; i.Gender = "x" }, "gender"},
		"bad status":      {func(i *students.Input) { i.RegistrationCode = "R-5"; i.Status = "gone" }, "status"},
		"future dob": {func(i *students.Input) {
			i.RegistrationCode = "R-6"
			i.DateOfBirth = time.Now().AddDate(0, 0, 2).Format("2006-01-02")
		}, "dateOfBirth"},
		"too old":       {func(i *students.Input) { i.RegistrationCode = "R-7"; i.DateOfBirth = "1980-01-01" }, "dateOfBirth"},
		"bad dob":       {func(i *students.Input) { i.RegistrationCode = "R-8"; i.DateOfBirth = "01/06/2015" }, "dateOfBirth"},
		"bad mobile":    {func(i *students.Input) { i.RegistrationCode = "R-9"; i.Mobile = ptr("abc") }, "mobile"},
		"bad email":     {func(i *students.Input) { i.RegistrationCode = "R-10"; i.ParentEmail = ptr("not an email") }, "parentEmail"},
		"display email": {func(i *students.Input) { i.RegistrationCode = "R-11"; i.StudentEmail = ptr("Asha <a@example.org>") }, "studentEmail"},
		"short apaar":   {func(i *students.Input) { i.RegistrationCode = "R-12"; i.ApaarID = ptr("123") }, "apaarId"},
		"bad admission": {func(i *students.Input) { i.RegistrationCode = "R-13"; i.AdmissionDate = ptr("soon") }, "admissionDate"},
		"long code":     {func(i *students.Input) { i.RegistrationCode = strings.Repeat("x", 31) }, "registrationCode"},
	} {
		x := stu("Z")
		c.mut(&x)
		_, err := svc.Create(a, w.f.Actor, x)
		fieldErr(t, name, err, c.field)
	}
	if _, err := svc.Create(modtest.In(w.f.Bravo), w.f.Actor, stu("R-001")); err != nil {
		t.Errorf("codes are per institute: %v", err)
	}
	if _, err := svc.Get(modtest.In(w.f.Bravo), s.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	q, _ := crud.ParseList(1, 50, "", "rao", students.SortKeys, students.DefaultSort)
	if p, err := svc.List(a, q, students.Filter{Gender: "female"}); err != nil || p.Total != 1 {
		t.Errorf("search: %+v %v", p, err)
	}
	if p, err := svc.List(a, q, students.Filter{Status: "alumni"}); err != nil || p.Total != 0 {
		t.Errorf("status filter: %+v %v", p, err)
	}
	if w.f.AuditCount(t, "students.create") != 2 { // one in each institute
		t.Errorf("audit: %d", w.f.AuditCount(t, "students.create"))
	}
	must(t, svc.Delete(a, w.f.Actor, s.ID))
	if _, err := svc.Create(a, w.f.Actor, stu("R-001")); err != nil {
		t.Errorf("code reuse after delete: %v", err)
	}
}

func TestEnrollmentsPlaceAStudentInAMappedSectionOncePerYear(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	ss, es := students.Service{DB: w.f.App}, enrollments.Service{DB: w.f.App}
	s1, err := ss.Create(a, w.f.Actor, stu("R-1"))
	must(t, err)
	s2, err := ss.Create(a, w.f.Actor, stu("R-2"))
	must(t, err)
	in := enrollments.Input{StudentID: s1.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec, RollNumber: ptr("7"), EnrollmentNumber: ptr("E-1"), EnrollmentDate: ptr("2026-04-01")}
	e, err := es.Create(a, w.f.Actor, in)
	must(t, err)
	if e.StudentName != "Asha Rao" || e.ClassName != "Class 5" || e.SectionName != "A" || e.Status != "enrolled" {
		t.Fatalf("%+v", e)
	}
	got, err := ss.Get(a, s1.ID)
	must(t, err)
	if got.CurrentClassName == nil || *got.CurrentClassName != "Class 5" || *got.CurrentSectionName != "A" || *got.CurrentRollNumber != "7" {
		t.Fatalf("the student's current class comes from the current year's enrollment: %+v", got)
	}
	for name, c := range map[string]struct {
		in    enrollments.Input
		field string
	}{
		"same year twice":    {enrollments.Input{StudentID: s1.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec}, "studentId"},
		"same roll number":   {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec, RollNumber: ptr("7")}, "rollNumber"},
		"same enrol number":  {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec, EnrollmentNumber: ptr("e-1")}, "enrollmentNumber"},
		"section not mapped": {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec2}, "sectionId"},
		"other institute":    {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.otherClass, SectionID: w.sec}, "classId"},
		"unknown student":    {enrollments.Input{StudentID: "00000000-0000-4000-8000-000000000000", YearID: w.year, ClassID: w.class, SectionID: w.sec}, "studentId"},
		"bad status":         {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec, Status: "left"}, "status"},
		"bad date":           {enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec, EnrollmentDate: ptr("x")}, "enrollmentDate"},
		"no student":         {enrollments.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec}, "studentId"},
	} {
		_, err := es.Create(a, w.f.Actor, c.in)
		fieldErr(t, name, err, c.field)
	}
	// the roll number may repeat in another section of the class, once that section is mapped
	_, err = classsections.Service{DB: w.f.App}.Create(a, w.f.Actor, classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec2})
	must(t, err)
	if _, err := es.Create(a, w.f.Actor, enrollments.Input{StudentID: s2.ID, YearID: w.year, ClassID: w.class, SectionID: w.sec2, RollNumber: ptr("7")}); err != nil {
		t.Errorf("roll numbers are per section: %v", err)
	}
	// a student with an enrollment cannot be deleted; a withdrawn one no longer shows a current class
	var inUse resource.InUseError
	if err := ss.Delete(a, w.f.Actor, s1.ID); !errors.As(err, &inUse) {
		t.Errorf("delete with enrollment: %v", err)
	}
	in.Status = "withdrawn"
	_, err = es.Update(a, w.f.Actor, e.ID, in)
	must(t, err)
	if got, _ = ss.Get(a, s1.ID); got.CurrentClassName != nil {
		t.Errorf("withdrawn students have no current class: %+v", got)
	}
	if _, err := es.Get(modtest.In(w.f.Bravo), e.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	must(t, es.Delete(a, w.f.Actor, e.ID))
	must(t, ss.Delete(a, w.f.Actor, s1.ID))
}
