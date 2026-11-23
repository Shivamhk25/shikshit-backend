package alumni_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/alumni"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/promotions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
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

// world: two years, classes 7 and 8 (8 is the last), one section, and the services that make alumni for real:
// promotions (graduation) and transfers.
type world struct {
	f              modtest.Fixture
	svc            alumni.Service
	promo          promotions.Service
	tr             transfers.Service
	y1, y2         string
	c7, c8, secA   string
	enrolAlreadyOn bool
}

func (w world) in() context.Context { return modtest.In(w.f.Alpha) }

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, svc: alumni.Service{DB: f.App}, promo: promotions.Service{DB: f.App}}
	ins := func(dst *string, q string, args ...any) {
		t.Helper()
		must(t, f.Admin.QueryRow(bg, q, args...).Scan(dst))
	}
	ins(&w.y1, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y1', '2025-26', '2025-04-01', '2026-03-31') RETURNING id::text`, f.Alpha.ID)
	ins(&w.y2, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y2', '2026-27', '2026-04-01', '2027-03-31') RETURNING id::text`, f.Alpha.ID)
	ins(&w.c7, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C7', 'Class 7', 7) RETURNING id::text`, f.Alpha.ID)
	ins(&w.c8, `INSERT INTO classes (tenant_id, code, name, display_order) VALUES ($1, 'C8', 'Class 8', 8) RETURNING id::text`, f.Alpha.ID)
	ins(&w.secA, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Alpha.ID)
	for _, m := range [][2]string{{w.y1, w.c7}, {w.y1, w.c8}, {w.y2, w.c7}, {w.y2, w.c8}} {
		_, err := f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, m[0], m[1], w.secA)
		must(t, err)
	}
	_, err := f.Admin.Exec(bg, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'stu'), ($1, 'wfl')`, f.Alpha.ID)
	must(t, err)
	reg := approvals.NewRegistry()
	must(t, transfers.Register(reg))
	w.tr = transfers.Service{DB: f.App, Approvals: approvals.Service{DB: f.App, Reg: reg, Access: authz.Service{DB: f.App}}}
	return w
}

// student adds a student of alpha, enrolled in the class for year 1 unless class is empty.
func (w world) student(t *testing.T, code, first, class, year string) string {
	t.Helper()
	s, err := students.Service{DB: w.f.App}.Create(w.in(), w.f.Actor, students.Input{RegistrationCode: code, FirstName: first, LastName: ptr("Rao"), Gender: "female", DateOfBirth: "2012-06-01"})
	must(t, err)
	if class != "" {
		_, err = enrollments.Service{DB: w.f.App}.Create(w.in(), w.f.Actor, enrollments.Input{StudentID: s.ID, YearID: year, ClassID: class, SectionID: w.secA, RollNumber: ptr(code)})
		must(t, err)
	}
	return s.ID
}

func (w world) graduate(t *testing.T) {
	t.Helper()
	p, err := w.promo.Candidates(w.in(), w.f.Actor, w.y1, w.y2, w.c8, w.secA)
	must(t, err)
	in := promotions.Input{FromYearID: w.y1, ToYearID: w.y2, FromClassID: w.c8, FromSectionID: w.secA}
	for _, c := range p.Candidates {
		in.Rows = append(in.Rows, promotions.RowInput{StudentID: c.StudentID, Decision: c.Decision, ToClassID: c.ToClassID})
	}
	d, err := w.promo.Create(w.in(), w.f.Actor, in)
	must(t, err)
	_, err = w.promo.Commit(w.in(), w.f.Actor, d.ID, nil)
	must(t, err)
}

// transfer requests, approves (by the owner role, as the approval engine would have) and completes a transfer.
func (w world) transfer(t *testing.T, student string, issuedOn *string) transfers.Transfer {
	t.Helper()
	x, err := w.tr.Create(w.in(), w.f.Actor, transfers.Input{StudentID: student, Reason: "health", ReasonNote: ptr("surgery abroad"), DestinationSchool: ptr("Delhi Public School"), LastAttendanceDate: ptr("2026-03-10")})
	must(t, err)
	_, err = w.f.Admin.Exec(bg, `UPDATE student_transfers SET status = 'approved', requested_on = DATE '2026-03-01' WHERE id = $1::uuid`, x.ID)
	must(t, err)
	res, err := w.tr.Complete(w.in(), w.f.Actor, x.ID, issuedOn)
	must(t, err)
	return res.Transfer
}

func (w world) list(t *testing.T, sort, search string, f alumni.Filter) crud.Page[alumni.Alumnus] {
	t.Helper()
	q, err := crud.ParseList(1, 50, sort, search, alumni.SortKeys, alumni.DefaultSort)
	must(t, err)
	p, err := w.svc.ListAudited(w.in(), w.f.Actor, q, f)
	must(t, err)
	return p
}

func byCode(p crud.Page[alumni.Alumnus]) map[string]alumni.Alumnus {
	m := map[string]alumni.Alumnus{}
	for _, a := range p.Items {
		m[a.RegistrationCode] = a
	}
	return m
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func today() string { return transfers.Today(time.Now()) }

func TestGraduatesAndTransferredStudentsAreListedWithWhereTheyLeftFrom(t *testing.T) {
	w := setup(t)
	g := w.student(t, "G-1", "Farid", w.c8, w.y1)
	tr := w.student(t, "T-1", "Tara", w.c7, w.y1)
	active := w.student(t, "A-1", "Anil", w.c7, w.y1)
	paused := w.student(t, "P-1", "Pia", w.c7, w.y1)
	_, err := w.f.Admin.Exec(bg, `UPDATE students SET status = 'paused' WHERE id = $1::uuid`, paused)
	must(t, err)
	_ = active

	w.graduate(t)
	x := w.transfer(t, tr, ptr("2026-03-12"))
	if x.TCNumber == nil {
		t.Fatal("no TC number")
	}

	p := w.list(t, "", "", alumni.Filter{})
	got := byCode(p)
	if p.Total != 2 || len(got) != 2 {
		t.Fatalf("list = %+v", p.Items)
	}
	gr := got["G-1"]
	if gr.ID != g || gr.Name != "Farid Rao" || gr.Reason != "graduated" || str(gr.ExitDate) != today() || str(gr.LastClassName) != "Class 8" || str(gr.LastSectionName) != "Section A" ||
		str(gr.YearName) != "2025-26" || gr.TCNumber != nil || gr.AdmissionDate != nil && *gr.AdmissionDate == "" {
		t.Errorf("graduate = %+v", gr)
	}
	tw := got["T-1"]
	if tw.ID != tr || tw.Reason != "transferred" || str(tw.ExitDate) != "2026-03-12" || str(tw.LastClassName) != "Class 7" || str(tw.YearName) != "2025-26" ||
		str(tw.TCNumber) != "TC-000001" || str(tw.DestinationSchool) != "Delhi Public School" || str(tw.LastAttendanceDate) != "2026-03-10" {
		t.Errorf("transferred = %+v", tw)
	}
	// why a child left is not in the list or the detail
	raw, _ := json.Marshal(p)
	one, err := w.svc.View(w.in(), w.f.Actor, tr)
	must(t, err)
	rawOne, _ := json.Marshal(one)
	for _, s := range []string{"surgery", "health", "Health"} {
		if strings.Contains(string(raw), s) || strings.Contains(string(rawOne), s) {
			t.Errorf("the transfer's reason %q reached the alumni list", s)
		}
	}
	if one.ID != tr || str(one.TCNumber) != "TC-000001" {
		t.Errorf("view = %+v", one)
	}
}

func TestUndoingAGraduationTakesTheStudentOffTheList(t *testing.T) {
	w := setup(t)
	w.student(t, "G-1", "Farid", w.c8, w.y1)
	w.graduate(t)
	if p := w.list(t, "", "", alumni.Filter{}); p.Total != 1 {
		t.Fatalf("after commit = %d", p.Total)
	}
	var batch string
	must(t, w.f.Admin.QueryRow(bg, `SELECT id::text FROM promotion_batches`).Scan(&batch))
	_, err := w.promo.Undo(w.in(), w.f.Actor, batch, "wrong class")
	must(t, err)
	if p := w.list(t, "", "", alumni.Filter{}); p.Total != 0 {
		t.Errorf("after undo = %d", p.Total)
	}
}

func TestAStatusSetByHandIsListedWithoutAnExitDate(t *testing.T) {
	w := setup(t)
	a := w.student(t, "M-1", "Manu", w.c7, w.y1)
	b := w.student(t, "M-2", "Mira", w.c8, w.y1)
	c := w.student(t, "M-3", "Maya", "", "")
	for id, st := range map[string]string{a: "alumni", b: "transferred", c: "alumni"} {
		_, err := w.f.Admin.Exec(bg, `UPDATE students SET status = $2 WHERE id = $1::uuid`, id, st)
		must(t, err)
	}
	got := byCode(w.list(t, "", "", alumni.Filter{}))
	if r := got["M-1"]; r.Reason != "other" || r.ExitDate != nil || str(r.LastClassName) != "Class 7" || r.TCNumber != nil {
		t.Errorf("alumni by hand = %+v", r)
	}
	if r := got["M-2"]; r.Reason != "transferred" || r.ExitDate != nil || str(r.LastClassName) != "Class 8" || r.TCNumber != nil {
		t.Errorf("transferred by hand = %+v", r)
	}
	if r := got["M-3"]; r.Reason != "other" || r.LastClassName != nil || r.YearName != nil {
		t.Errorf("no enrolment = %+v", r)
	}
}

func TestOnlyFormerStudentsAreListedAndRemovedOnesAreNot(t *testing.T) {
	w := setup(t)
	gone := w.student(t, "R-1", "Ravi", w.c7, w.y1)
	w.student(t, "A-1", "Anil", w.c7, w.y1)
	_, err := w.f.Admin.Exec(bg, `UPDATE students SET status = 'alumni', deleted_at = now() WHERE id = $1::uuid`, gone)
	must(t, err)
	if p := w.list(t, "", "", alumni.Filter{}); p.Total != 0 {
		t.Errorf("list = %+v", p.Items)
	}
	for _, id := range []string{gone, w.student(t, "A-2", "Asha", w.c7, w.y1)} {
		if _, err := w.svc.View(w.in(), w.f.Actor, id); !errors.Is(err, resource.ErrNotFound) {
			t.Errorf("view of %s = %v", id, err)
		}
	}
}

func TestFiltersSearchAndSorting(t *testing.T) {
	w := setup(t)
	w.student(t, "G-1", "Farid", w.c8, w.y1)
	t1 := w.student(t, "T-1", "Tara", w.c7, w.y1)
	w.student(t, "G-2", "Gita", w.c8, w.y1)
	hand := w.student(t, "M-1", "Manu", w.c7, w.y1)
	w.graduate(t)
	w.transfer(t, t1, ptr("2026-03-12"))
	_, err := w.f.Admin.Exec(bg, `UPDATE students SET status = 'alumni' WHERE id = $1::uuid`, hand)
	must(t, err)

	if p := w.list(t, "", "", alumni.Filter{}); p.Total != 4 {
		t.Fatalf("all = %d", p.Total)
	}
	for reason, n := range map[string]int{"graduated": 2, "transferred": 1, "other": 1} {
		if p := w.list(t, "", "", alumni.Filter{Reason: reason}); p.Total != n {
			t.Errorf("reason %s = %d, want %d", reason, p.Total, n)
		}
	}
	if p := w.list(t, "", "", alumni.Filter{ClassID: w.c8}); p.Total != 2 {
		t.Errorf("class 8 = %d", p.Total)
	}
	if p := w.list(t, "", "", alumni.Filter{ClassID: w.c7}); p.Total != 2 {
		t.Errorf("class 7 = %d", p.Total)
	}
	if p := w.list(t, "", "", alumni.Filter{YearID: w.y1}); p.Total != 4 {
		t.Errorf("year 1 = %d", p.Total)
	}
	if p := w.list(t, "", "", alumni.Filter{YearID: w.y2}); p.Total != 0 {
		t.Errorf("year 2 = %d", p.Total)
	}
	if p := w.list(t, "", "", alumni.Filter{Reason: "graduated", ClassID: w.c7}); p.Total != 0 {
		t.Errorf("graduated from class 7 = %d", p.Total)
	}
	for _, q := range []string{"gita", "G-2", "tc-000001"} {
		if p := w.list(t, "", q, alumni.Filter{}); p.Total != 1 {
			t.Errorf("search %q = %d", q, p.Total)
		}
	}
	// the most recent leavers first, students whose date is unknown last in either direction
	desc := w.list(t, "-exit", "", alumni.Filter{})
	asc := w.list(t, "exit", "", alumni.Filter{})
	if desc.Items[3].RegistrationCode != "M-1" && desc.Items[3].ExitDate != nil || str(desc.Items[0].ExitDate) != today() {
		t.Errorf("desc = %v", codes(desc))
	}
	if asc.Items[0].RegistrationCode != "T-1" {
		t.Errorf("asc = %v", codes(asc))
	}
	if n := codes(desc)[3]; n != "M-1" {
		t.Errorf("unknown exit date is not last when descending: %v", codes(desc))
	}
	if codes(asc)[0] == "M-1" {
		t.Errorf("unknown exit date is first when ascending: %v", codes(asc))
	}
	byName := w.list(t, "student", "", alumni.Filter{})
	if byName.Items[0].Name != "Farid Rao" {
		t.Errorf("by name = %v", codes(byName))
	}
	// paging keeps the total
	q, err := crud.ParseList(2, 3, "student", "", alumni.SortKeys, alumni.DefaultSort)
	must(t, err)
	pg, err := w.svc.ListAudited(w.in(), w.f.Actor, q, alumni.Filter{})
	must(t, err)
	if pg.Total != 4 || len(pg.Items) != 1 {
		t.Errorf("page 2 = %+v", pg)
	}
}

func codes(p crud.Page[alumni.Alumnus]) []string {
	var out []string
	for _, a := range p.Items {
		out = append(out, a.RegistrationCode)
	}
	return out
}

func TestReadsAreAuditedWithoutTheSearchTextAndNothingIsWritten(t *testing.T) {
	w := setup(t)
	s := w.student(t, "G-1", "Farid", w.c8, w.y1)
	w.graduate(t)
	var before int
	must(t, w.f.Admin.QueryRow(bg, `SELECT count(*) FROM students WHERE updated_at > now() - interval '1 second'`).Scan(&before))

	w.list(t, "", "farid", alumni.Filter{Reason: "graduated"})
	if _, err := w.svc.View(w.in(), w.f.Actor, s); err != nil {
		t.Fatal(err)
	}
	if n := w.f.AuditCount(t, "alumni.list"); n != 1 {
		t.Errorf("list audited %d times", n)
	}
	if n := w.f.AuditCount(t, "alumni.view"); n != 1 {
		t.Errorf("view audited %d times", n)
	}
	var meta string
	must(t, w.f.Admin.QueryRow(bg, `SELECT after::text FROM audit_log WHERE action = 'alumni.list'`).Scan(&meta))
	if strings.Contains(strings.ToLower(meta), "farid") || !strings.Contains(meta, `"searched": true`) && !strings.Contains(meta, `"searched":true`) || !strings.Contains(meta, "graduated") {
		t.Errorf("audit meta = %s", meta)
	}
	var st string
	must(t, w.f.Admin.QueryRow(bg, `SELECT status FROM students WHERE id = $1::uuid`, s).Scan(&st))
	if st != "alumni" {
		t.Errorf("student = %s", st)
	}
}

func TestAnotherInstituteSeesNoAlumni(t *testing.T) {
	w := setup(t)
	s := w.student(t, "G-1", "Farid", w.c8, w.y1)
	w.graduate(t)
	bravo := modtest.In(w.f.Bravo)
	q, err := crud.ParseList(1, 50, "", "", alumni.SortKeys, alumni.DefaultSort)
	must(t, err)
	p, err := w.svc.ListAudited(bravo, w.f.Actor, q, alumni.Filter{})
	must(t, err)
	if p.Total != 0 {
		t.Errorf("bravo sees %d", p.Total)
	}
	if _, err := w.svc.View(bravo, w.f.Actor, s); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("bravo view = %v", err)
	}
	if p := w.list(t, "", "", alumni.Filter{}); p.Total != 1 {
		t.Errorf("alpha lost its alumnus: %d", p.Total)
	}
}
