package importers_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/aca/classsections"
	"github.com/pie360/backend/internal/modules/aca/sections"
	"github.com/pie360/backend/internal/modules/aca/years"
	"github.com/pie360/backend/internal/modules/importreg"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/importer"
	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

type env struct {
	modtest.Fixture
	svc    importer.Service
	worker *jobs.Worker
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{Fixture: modtest.New(t)}
	if _, err := e.Admin.Exec(context.Background(), `UPDATE tenants SET settings = jsonb_set(settings, '{importApprovalRequired}', 'false'::jsonb) WHERE id = $1`, e.Alpha.ID); err != nil {
		t.Fatal(err)
	}
	e.svc = importer.Service{DB: e.App, Reg: importreg.Registry(e.App), Packs: importreg.Packs(), Now: time.Now}
	e.worker = &jobs.Worker{DB: e.App, Handlers: e.svc.Handlers()}
	// one current year, a class and two sections, one of them mapped
	a := modtest.In(e.Alpha)
	now := time.Now().UTC()
	y, err := years.Service{DB: e.App}.Create(a, e.Actor, years.Input{Code: "Y1", Name: "2026-27", StartDate: now.AddDate(0, -2, 0).Format("2006-01-02"), EndDate: now.AddDate(0, 10, 0).Format("2006-01-02"), IsCurrent: true})
	must(t, err)
	c, err := classes.Service{DB: e.App}.Create(a, e.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	s1, err := sections.Service{DB: e.App}.Create(a, e.Actor, sections.Input{Code: "A", Name: "A"})
	must(t, err)
	_, err = sections.Service{DB: e.App}.Create(a, e.Actor, sections.Input{Code: "B", Name: "B"})
	must(t, err)
	_, err = classsections.Service{DB: e.App}.Create(a, e.Actor, classsections.Input{YearID: y.ID, ClassID: c.ID, SectionID: s1.ID})
	must(t, err)
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 50; i++ {
		ran, err := e.worker.RunOnce(context.Background())
		must(t, err)
		if !ran {
			return
		}
	}
	t.Fatal("jobs never settled")
}

func (e *env) upload(t *testing.T, entity, strategy, mode, csv string) importer.Batch {
	t.Helper()
	b, err := e.svc.Upload(modtest.In(e.Alpha), e.Actor, importer.UploadInput{Entity: entity, Strategy: strategy, CommitMode: mode, FileName: "x.csv", Data: []byte(csv)})
	must(t, err)
	e.drain(t)
	return e.get(t, b.ID)
}

func (e *env) get(t *testing.T, id string) importer.Batch {
	t.Helper()
	b, err := e.svc.Get(modtest.In(e.Alpha), id)
	must(t, err)
	return b
}

func (e *env) commit(t *testing.T, id string) importer.Batch {
	t.Helper()
	_, err := e.svc.Commit(modtest.In(e.Alpha), e.Actor, id)
	must(t, err)
	e.drain(t)
	return e.get(t, id)
}

func (e *env) problems(t *testing.T, id string) map[int]string {
	t.Helper()
	page, err := e.svc.Rows(modtest.In(e.Alpha), id, "error", crud.ListQuery{Page: 1, Size: 100})
	must(t, err)
	out := map[int]string{}
	for _, r := range page.Items {
		if len(r.Problems) > 0 {
			out[r.RowNo] = r.Problems[0].Column + ":" + r.Problems[0].Code
		}
	}
	return out
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	must(t, e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE deleted_at IS NULL`).Scan(&n))
	return n
}

const header = "registrationCode,firstName,lastName,gender,dateOfBirth,mobile,apaarId,parentEmail\n"

func TestStudentsImportAndReportWhatIsWrongWithoutEchoingPersonalData(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "students", "", "skip_invalid", header+
		"R-1,Asha,Rao,female,2015-06-01,+91 98765 43210,123456789012,p@example.org\n"+
		"R-2,Ravi,Rao,Male,01/07/2014,,,\n"+
		"R-3,Mina,Rao,girl,2015-01-01,,,\n"+
		"R-4,Omar,Khan,male,2999-01-01,,,\n"+
		"R-5,Zed,Khan,male,2015-01-01,,123,\n"+
		"R-6,Kim,Lee,other,2015-01-01,,,not-an-email\n"+
		"r-1,Dup,Rao,female,2015-06-01,,,\n"+
		"R-8,,Rao,female,2015-06-01,,,\n")
	got := e.problems(t, b.ID)
	if b.ValidRows != 2 || got[4] != "gender:invalid_choice" || got[5] != "dateOfBirth:in_future" || got[6] != "apaarId:invalid" || got[7] != "parentEmail:invalid_email" || got[8] != "registrationCode:duplicate_in_file" {
		t.Fatalf("%+v %v", b, got)
	}
	if got[9] == "" {
		t.Fatalf("a missing first name must be flagged: %v", got)
	}
	// a problem names the column and a code, never the value that was wrong
	page, _ := e.svc.Rows(modtest.In(e.Alpha), b.ID, "error", crud.ListQuery{Page: 1, Size: 100})
	for _, r := range page.Items {
		for _, p := range r.Problems {
			for _, secret := range []string{"123", "not-an-email", "2999"} {
				if contains(p.Message, secret) {
					t.Errorf("message echoes the value %q: %q", secret, p.Message)
				}
			}
		}
	}
	e.commit(t, b.ID)
	if n := e.count(t, "students"); n != 2 {
		t.Fatalf("students: %d", n)
	}
	var dob string
	must(t, e.Admin.QueryRow(context.Background(), `SELECT date_of_birth::text FROM students WHERE registration_code = 'R-2'`).Scan(&dob))
	if dob != "2014-07-01" {
		t.Fatalf("day-first dates must be read as such: %s", dob)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestUpdatingStudentsKeepsBlankCells(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "students", "", "", header+"R-1,Asha,Rao,female,2015-06-01,9876543210,123456789012,p@example.org\n").ID)
	e.commit(t, e.upload(t, "students", "update", "", header+"R-1,Asha,Roy,female,2015-06-01,,,\n").ID)
	var last, mobile, apaar string
	must(t, e.Admin.QueryRow(context.Background(), `SELECT last_name, mobile, apaar_id FROM students WHERE registration_code = 'R-1'`).Scan(&last, &mobile, &apaar))
	if last != "Roy" || mobile != "9876543210" || apaar != "123456789012" {
		t.Fatalf("%q %q %q", last, mobile, apaar)
	}
}

func TestEnrollmentsImportByReferenceAndNeedAMappedSection(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "students", "", "", header+"R-1,Asha,Rao,female,2015-06-01,,,\nR-2,Ravi,Rao,male,2014-01-01,,,\nR-3,Mina,Rao,female,2015-01-01,,,\n").ID)
	b := e.upload(t, "student_enrollments", "", "skip_invalid",
		"student,year,class,section,rollNumber\nR-1,Y1,V,A,1\nR-2,2026-27,v,a,2\nR-3,Y1,V,B,3\nR-9,Y1,V,A,4\nR-1,Y1,V,A,5\n")
	got := e.problems(t, b.ID)
	if b.ValidRows != 2 || got[4] != "section:not_in_class" || got[5] != "student:not_found" || got[6] != "student:duplicate_in_file" {
		t.Fatalf("%+v %v", b, got)
	}
	e.commit(t, b.ID)
	if n := e.count(t, "student_enrollments"); n != 2 {
		t.Fatalf("enrollments: %d", n)
	}
}

func TestTheStudentsPackIsReadyOnlyWhenEveryActiveStudentIsEnrolled(t *testing.T) {
	e := setup(t)
	a := modtest.In(e.Alpha)
	ready := func() importer.PackStatus {
		st, err := e.svc.PackStatus(a, "students")
		must(t, err)
		return st
	}
	if st := ready(); st.Ready || len(st.Steps) != 2 || !st.Steps[0].Next {
		t.Fatalf("%+v", st)
	}
	e.commit(t, e.upload(t, "students", "", "", header+"R-1,Asha,Rao,female,2015-06-01,,,\n").ID)
	if st := ready(); st.Ready {
		t.Fatalf("an unenrolled active student: %+v", st)
	}
	e.commit(t, e.upload(t, "student_enrollments", "", "", "student,year,class,section\nR-1,Y1,V,A\n").ID)
	if st := ready(); !st.Ready {
		t.Fatalf("%+v", st)
	}
	// a transferred student needs no enrollment
	e.commit(t, e.upload(t, "students", "", "", "registrationCode,firstName,gender,dateOfBirth,status\nR-9,Gone,male,2014-01-01,transferred\n").ID)
	if st := ready(); !st.Ready {
		t.Fatalf("a transferred student must not block the pack: %+v", st)
	}
	e.commit(t, e.upload(t, "students", "", "", header+"R-2,Ravi,Rao,male,2014-01-01,,,\n").ID)
	if st := ready(); st.Ready {
		t.Fatalf("a new unenrolled student: %+v", st)
	}
}

func TestAnEnrollmentIsFoundAgainByStudentAndYear(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "students", "", "", header+"R-1,Asha,Rao,female,2015-06-01,,,\n").ID)
	e.commit(t, e.upload(t, "student_enrollments", "", "", "student,year,class,section,rollNumber,enrollmentNumber\nR-1,Y1,V,A,1,E-1\n").ID)
	if b := e.upload(t, "student_enrollments", "insert", "", "student,year,class,section\nR-1,Y1,V,A\n"); b.ErrorRows != 1 {
		t.Fatalf("add-only must flag an existing enrollment: %+v", b)
	}
	e.commit(t, e.upload(t, "student_enrollments", "update", "", "student,year,class,section,rollNumber\nR-1,Y1,V,A,9\n").ID)
	var roll, number string
	must(t, e.Admin.QueryRow(context.Background(), `SELECT roll_number, enrollment_number FROM student_enrollments`).Scan(&roll, &number))
	if roll != "9" || number != "E-1" || e.count(t, "student_enrollments") != 1 {
		t.Fatalf("%q %q", roll, number)
	}
}
