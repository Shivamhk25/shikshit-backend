package identity_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
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

type env struct {
	f                       modtest.Fixture
	svc                     identity.Service
	year, class, secA, secB string
	mapA, mapB              string
}

func setup(t *testing.T) env {
	t.Helper()
	f := modtest.New(t)
	e := env{f: f, svc: identity.Service{DB: f.App}}
	now := time.Now().UTC()
	must(t, f.Admin.QueryRow(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', $2, $3, true) RETURNING id::text`,
		f.Alpha.ID, now.AddDate(-1, 0, 0), now.AddDate(0, 10, 0)).Scan(&e.year))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'V', 'Class 5') RETURNING id::text`, f.Alpha.ID).Scan(&e.class))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'A') RETURNING id::text`, f.Alpha.ID).Scan(&e.secA))
	must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'B', 'B') RETURNING id::text`, f.Alpha.ID).Scan(&e.secB))
	for sec, dst := range map[string]*string{e.secA: &e.mapA, e.secB: &e.mapB} {
		must(t, f.Admin.QueryRow(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`,
			f.Alpha.ID, e.year, e.class, sec).Scan(dst))
	}
	return e
}

func (e env) in() context.Context { return modtest.In(e.f.Alpha) }

// student adds a student of alpha; section "" means no enrollment.
func (e env) student(t *testing.T, code, first, last, section string) string {
	t.Helper()
	var id string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth) VALUES ($1, $2::text, $3::text, $4::text, 'female', DATE '2015-04-01') RETURNING id::text`,
		e.f.Alpha.ID, code, first, last).Scan(&id))
	roll := "7"
	if code != "S-1" {
		roll = code
	}
	if section != "" {
		_, err := e.f.Admin.Exec(bg, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`,
			e.f.Alpha.ID, id, e.year, e.class, section, roll)
		must(t, err)
	}
	return id
}

func (e env) guardian(t *testing.T, student, name, phone, relation string, primary bool) {
	t.Helper()
	var gid string
	must(t, e.f.Admin.QueryRow(bg, `INSERT INTO guardians (tenant_id, name, phone, email) VALUES ($1, $2::text, $3::text, $2::text || '@example.org') RETURNING id::text`, e.f.Alpha.ID, name, phone).Scan(&gid))
	_, err := e.f.Admin.Exec(bg, `INSERT INTO student_guardians (tenant_id, student_id, guardian_id, relation, is_primary) VALUES ($1, $2::uuid, $3::uuid, $4::text, $5)`, e.f.Alpha.ID, student, gid, relation, primary)
	must(t, err)
}

func TestSearchRanksCodeAndNameAndEscapesWildcards(t *testing.T) {
	e := setup(t)
	e.student(t, "S-1", "Asha", "Rao", e.secA)
	e.student(t, "S-10", "Bina", "Asha", e.secA)
	e.student(t, "S-2", "Chitra", "Mehta", "")
	gone := e.student(t, "S-3", "Asha", "Deleted", e.secA)
	_, err := e.f.Admin.Exec(bg, `UPDATE students SET deleted_at = now() WHERE id = $1::uuid`, gone)
	must(t, err)

	got, err := e.svc.Search(e.in(), e.f.Actor, "s-1", 0)
	must(t, err)
	if len(got) != 2 || got[0].RegistrationCode != "S-1" || got[1].RegistrationCode != "S-10" {
		t.Errorf("exact code must come first: %+v", got)
	}
	if got[0].ClassName == nil || *got[0].ClassName != "Class 5" || got[0].SectionName == nil || *got[0].SectionName != "A" {
		t.Errorf("class and section: %+v", got[0])
	}
	got, err = e.svc.Search(e.in(), e.f.Actor, "asha", 0)
	must(t, err)
	if len(got) != 2 {
		t.Errorf("name match across first and last name, deleted excluded: %+v", got)
	}
	got, err = e.svc.Search(e.in(), e.f.Actor, "mehta", 0)
	must(t, err)
	if len(got) != 1 || got[0].ClassName != nil {
		t.Errorf("a student with no enrollment has no class: %+v", got)
	}
	for _, q := range []string{"%%", "S_", `\\`} {
		if got, err = e.svc.Search(e.in(), e.f.Actor, q, 0); err != nil || len(got) != 0 {
			t.Errorf("%q must match literally, got %+v %v", q, got, err)
		}
	}
	if got, _ = e.svc.Search(e.in(), e.f.Actor, "asha", 1); len(got) != 1 {
		t.Errorf("limit 1 returned %d", len(got))
	}
	var fe crud.FieldErrors
	if _, err = e.svc.Search(e.in(), e.f.Actor, " a ", 0); !errors.As(err, &fe) {
		t.Errorf("one character must be refused, got %v", err)
	}
	if _, err = e.svc.Search(e.in(), e.f.Actor, strings.Repeat("x", 101), 0); !errors.As(err, &fe) {
		t.Errorf("101 characters must be refused, got %v", err)
	}
}

func TestSearchAuditedWithoutTheTypedText(t *testing.T) {
	e := setup(t)
	e.student(t, "S-1", "Asha", "Rao", e.secA)
	_, err := e.svc.Search(e.in(), e.f.Actor, "Asha Rao", 0)
	must(t, err)
	if n := e.f.AuditCount(t, "student_identity.search"); n != 1 {
		t.Fatalf("search audit rows = %d", n)
	}
	var leaked int
	must(t, e.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'student_identity.search' AND (after::text ILIKE '%asha%' OR after::text ILIKE '%rao%')`).Scan(&leaked))
	if leaked != 0 {
		t.Error("the audit row holds the text typed")
	}
}

func TestCardMasksContactsAndFillsIDCard(t *testing.T) {
	e := setup(t)
	id := e.student(t, "S-1", "Asha", "Rao", e.secA)
	e.guardian(t, id, "Meera Rao", "9876543210", "mother", true)
	e.guardian(t, id, "Kiran Rao", "9123456780", "father", false)

	c, err := e.svc.Get(e.in(), e.f.Actor, id, true)
	must(t, err)
	if !c.ContactsVisible || len(c.Guardians) != 2 || c.Guardians[0].Name != "Meera Rao" || !c.Guardians[0].IsPrimary {
		t.Fatalf("primary guardian first: %+v", c.Guardians)
	}
	if c.Guardians[0].Phone == nil || *c.Guardians[0].Phone != "9876543210" || c.Guardians[1].Email == nil {
		t.Errorf("contacts must show: %+v", c.Guardians)
	}
	if c.Enrollment == nil || c.Enrollment.ClassName != "Class 5" || c.Enrollment.SectionName != "A" || c.Enrollment.AcademicYear != "2026-27" || *c.Enrollment.RollNumber != "7" {
		t.Errorf("enrollment: %+v", c.Enrollment)
	}
	i := c.IDCard
	if i.InstituteName != "alpha" || i.StudentName != "Asha Rao" || *i.ClassSection != "Class 5 - A" || *i.PrimaryGuardianName != "Meera Rao" || i.EmergencyPhone == nil || *i.EmergencyPhone != "9876543210" || i.ValidUntil == nil || i.DateOfBirth != "2015-04-01" {
		t.Errorf("id card: %+v", i)
	}

	m, err := e.svc.Get(e.in(), e.f.Actor, id, false)
	must(t, err)
	if m.ContactsVisible || len(m.Guardians) != 2 || m.Guardians[0].Name != "Meera Rao" {
		t.Fatalf("names stay visible: %+v", m.Guardians)
	}
	for _, g := range m.Guardians {
		if g.Phone != nil || g.Email != nil {
			t.Errorf("masked guardian leaks contact: %+v", g)
		}
	}
	if m.IDCard.EmergencyPhone != nil || m.IDCard.PrimaryGuardianName == nil {
		t.Errorf("masked id card: %+v", m.IDCard)
	}
	if n := e.f.AuditCount(t, "student_identity.view"); n != 2 {
		t.Errorf("view audit rows = %d, want 2", n)
	}
}

func TestCardWithoutEnrollmentOrGuardians(t *testing.T) {
	e := setup(t)
	id := e.student(t, "S-1", "Asha", "Rao", "")
	c, err := e.svc.Get(e.in(), e.f.Actor, id, true)
	must(t, err)
	if c.Enrollment != nil || len(c.Guardians) != 0 || c.IDCard.ClassSection != nil || c.IDCard.EmergencyPhone != nil || c.Guardians == nil {
		t.Errorf("empty card: %+v", c)
	}
}

func TestTenantIsolation(t *testing.T) {
	e := setup(t)
	id := e.student(t, "S-1", "Asha", "Rao", e.secA)
	other := modtest.In(e.f.Bravo)
	got, err := e.svc.Search(other, e.f.Actor, "asha", 0)
	must(t, err)
	if len(got) != 0 {
		t.Errorf("another institute found the student: %+v", got)
	}
	if _, err = e.svc.Get(other, e.f.Actor, id, true); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("another institute read the card: %v", err)
	}
}

func TestClassSectionScope(t *testing.T) {
	e := setup(t)
	inA := e.student(t, "S-1", "Asha", "Rao", e.secA)
	inB := e.student(t, "S-2", "Asha", "Shah", e.secB)
	noEnrol := e.student(t, "S-3", "Asha", "Verma", "")
	ctx := scope.WithClassSections(e.in(), []string{e.mapA})

	got, err := e.svc.Search(ctx, e.f.Actor, "asha", 0)
	must(t, err)
	if len(got) != 1 || got[0].ID != inA {
		t.Errorf("a section A teacher must find only section A: %+v", got)
	}
	if _, err = e.svc.Get(ctx, e.f.Actor, inA, true); err != nil {
		t.Errorf("own section: %v", err)
	}
	for _, id := range []string{inB, noEnrol} {
		if _, err = e.svc.Get(ctx, e.f.Actor, id, true); !errors.Is(err, identity.ErrNotFound) {
			t.Errorf("student outside the sections must be not found: %v", err)
		}
	}
	none := scope.WithClassSections(e.in(), []string{})
	if got, _ = e.svc.Search(none, e.f.Actor, "asha", 0); len(got) != 0 {
		t.Errorf("no assignment finds nothing: %+v", got)
	}
}
