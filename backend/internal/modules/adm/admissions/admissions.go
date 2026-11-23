// Package admissions implements ADM-05 Admissions: an accepted registration becomes a student.
//
// The queue is the registrations whose status is "accepted" and that have not been admitted. Admitting one registration
// is a single transaction that creates the student, the guardians and their links, and the enrolment for the
// registration's year, class and chosen section, and then marks the registration as admitted (admitted_student_id).
// Nothing is kept if any step fails. Admitting the same registration again returns the same student and writes nothing.
//
// The students, guardians, links and enrolments are written through their own packages, so every rule those screens
// apply (code format, phone, section mapped to the class that year, roll number unique in the section) applies here
// too, and each record has its own audit row.
//
// Fee paid and entrance exam qualified are not checks yet: fees (FEE) and entrance exams (EXM) are not built. Each
// will register a Gate on Service.Gates; every gate runs before anything is written, in the preview as well as in the
// admit. Parent logins are not created here (see the ADM-05 notes in the pull request).
package admissions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/guardians"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/links"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the registration does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

var (
	// ErrNotAccepted means the registration is not in the accepted state, so it cannot be admitted. The API answers 409.
	ErrNotAccepted = errors.New("admissions: registration is not accepted")
	// ErrStudentRemoved means the registration was admitted but the student it became has since been removed. The API
	// answers 409; a person has to sort this out, the system does not create a second student.
	ErrStudentRemoved = errors.New("admissions: the admitted student was removed")
	// ErrTooMany means a bulk request has more registrations than MaxBatch.
	ErrTooMany = errors.New("admissions: too many registrations in one request")
)

// MaxBatch is the most registrations one preview or bulk request may carry.
const MaxBatch = 200

// NotEligible means a gate (fee, entrance exam, ...) refused the admission. The API answers 409.
type NotEligible struct{ Problems crud.FieldErrors }

func (e NotEligible) Error() string { return "admissions: not eligible: " + e.Problems.Error() }

// Gate is one eligibility rule beyond "the registration is accepted". FEE (registration fee paid) and EXM (entrance
// exam qualified, where an exam is mapped) add theirs to Service.Gates. A gate reads inside the admit transaction, so
// what it sees is what the admission commits against; it returns the reasons the registration may not be admitted, or
// none.
type Gate interface {
	Check(ctx context.Context, tx pgx.Tx, registrationID string) (crud.FieldErrors, error)
}

// Admission is a registration as the admissions queue shows it.
type Admission struct {
	ID                string     `json:"id"`
	ApplicationNumber string     `json:"applicationNumber"`
	AppliedOn         string     `json:"appliedOn"`
	FullName          string     `json:"fullName"`
	DateOfBirth       string     `json:"dateOfBirth"`
	Gender            *string    `json:"gender"`
	FatherName        *string    `json:"fatherName"`
	MotherName        *string    `json:"motherName"`
	GuardianName      *string    `json:"guardianName"`
	GuardianPhone     string     `json:"guardianPhone"`
	ClassID           string     `json:"classId"`
	ClassName         string     `json:"className"`
	SectionID         *string    `json:"sectionId"`
	SectionName       *string    `json:"sectionName"`
	YearID            string     `json:"yearId"`
	YearName          string     `json:"yearName"`
	Status            string     `json:"status"`
	Admitted          bool       `json:"admitted"`
	AdmittedAt        *time.Time `json:"admittedAt"`
	StudentID         *string    `json:"studentId"`
	StudentCode       *string    `json:"studentCode"`
	EnrolledSection   *string    `json:"enrolledSectionName"`
	RollNumber        *string    `json:"rollNumber"`
}

// States of the queue: "ready" (accepted, not admitted: the default) and "admitted".
const (
	StateReady    = "ready"
	StateAdmitted = "admitted"
)

// Filter narrows the queue.
type Filter struct{ State, ClassID, YearID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"applicationNumber": "application_number", "appliedOn": "applied_on", "applicant": "lower(first_name), lower(last_name)", "admittedAt": "admitted_at"}

// DefaultSort lists the oldest application first: the queue is worked in the order families applied.
const DefaultSort = "appliedOn"

const t = "student_registrations"

func sub(table, col, ref string) string {
	return "(SELECT " + col + " FROM " + table + " x WHERE x.tenant_id = " + t + ".tenant_id AND x.id = " + t + "." + ref + ")"
}

var columns = `id::text, application_number, applied_on::text, concat_ws(' ', first_name, middle_name, last_name) AS full_name,
	date_of_birth::text, gender, father_name, mother_name, guardian_name, guardian_phone,
	class_id::text, ` + sub("classes", "x.name", "class_id") + ` AS class_name,
	section_id::text, ` + sub("sections", "x.name", "section_id") + ` AS section_name,
	year_id::text, ` + sub("academic_years", "x.name", "year_id") + ` AS year_name,
	status, admitted_student_id IS NOT NULL AS admitted, admitted_at, admitted_student_id::text,
	` + sub("students", "x.registration_code", "admitted_student_id") + ` AS student_code,
	(SELECT s.name FROM student_enrollments e JOIN sections s ON s.tenant_id = e.tenant_id AND s.id = e.section_id
		WHERE e.tenant_id = ` + t + `.tenant_id AND e.student_id = ` + t + `.admitted_student_id AND e.year_id = ` + t + `.year_id AND e.deleted_at IS NULL LIMIT 1) AS enrolled_section,
	(SELECT e.roll_number FROM student_enrollments e
		WHERE e.tenant_id = ` + t + `.tenant_id AND e.student_id = ` + t + `.admitted_student_id AND e.year_id = ` + t + `.year_id AND e.deleted_at IS NULL LIMIT 1) AS roll_number`

func scan(r resource.Scanner) (Admission, error) {
	var a Admission
	err := r.Scan(&a.ID, &a.ApplicationNumber, &a.AppliedOn, &a.FullName, &a.DateOfBirth, &a.Gender, &a.FatherName, &a.MotherName, &a.GuardianName, &a.GuardianPhone,
		&a.ClassID, &a.ClassName, &a.SectionID, &a.SectionName, &a.YearID, &a.YearName, &a.Status, &a.Admitted, &a.AdmittedAt, &a.StudentID, &a.StudentCode,
		&a.EnrolledSection, &a.RollNumber)
	return a, err
}

// spec reads registrations as the queue; the queue never writes through it (admitting is Admit).
var spec = resource.Spec[Admission, struct{}]{
	Table: t, Action: "admissions", ResourceType: "admission",
	Columns: columns, Scan: scan, ID: func(a Admission) string { return a.ID },
	SearchColumns: []string{"application_number", "concat_ws(' ', first_name, middle_name, last_name)", "guardian_name", "father_name", "mother_name", "guardian_phone"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads the queue and admits registrations.
type Service struct {
	DB tenancy.Beginner
	// Gates are the extra eligibility rules (fee paid, entrance exam qualified). None until FEE and EXM exist.
	Gates []Gate
}

func (s Service) store() resource.Store[Admission, struct{}] {
	return resource.Store[Admission, struct{}]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	conds := []resource.Cond{{SQL: "status = 'accepted'"}}
	if f.State == StateAdmitted {
		conds = []resource.Cond{{SQL: "admitted_student_id IS NOT NULL"}}
	} else {
		conds = append(conds, resource.Cond{SQL: "admitted_student_id IS NULL"})
	}
	for _, c := range []struct{ col, v string }{{"class_id::text", f.ClassID}, {"year_id::text", f.YearID}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// ListAudited returns one page of the queue (state "ready", the default) or of the admitted registrations. The read
// is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Admission], error) {
	if f.State == "" {
		f.State = StateReady
	}
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "state": f.State,
		"classId": f.ClassID, "yearId": f.YearID}, q, filterConds(f)...)
}

// View returns one registration as the queue shows it, whether or not it is admitted; the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Admission, error) {
	var out Admission
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "admissions.view", ResourceType: "admission", ResourceID: id})
	})
	return out, err
}

// Input is what the registrar decides at admission. The class and year are the registration's own.
type Input struct {
	// SectionID is required: the section must be mapped to the registration's class for its year.
	SectionID string
	// RollNumber is optional; blank gives the next number in the section (one more than the highest numeric roll).
	RollNumber *string
	// StudentCode is optional; blank gives the next automatic code (STU-000045). A typed code must be unused.
	StudentCode *string
	// Gender is needed only when the registration has none.
	Gender *string
	// AdmissionDate defaults to today in India; it may be earlier, never in the future.
	AdmissionDate *string
	// ParentEmail is saved on the student's record; the registration has no email.
	ParentEmail *string
	// ConfirmDuplicate admits even though a student with the same name and date of birth exists.
	ConfirmDuplicate bool
}

// Item is one registration in a preview or a bulk admit, with its own decisions.
type Item struct {
	RegistrationID string
	Input
}

// GuardianLink is a guardian linked to the new student.
type GuardianLink struct {
	GuardianID string `json:"guardianId"`
	Name       string `json:"name"`
	Relation   string `json:"relation"`
	IsPrimary  bool   `json:"isPrimary"`
}

// Result is what an admission made (or, for AlreadyAdmitted, what an earlier admission made).
type Result struct {
	RegistrationID    string                 `json:"registrationId"`
	ApplicationNumber string                 `json:"applicationNumber"`
	AlreadyAdmitted   bool                   `json:"alreadyAdmitted"`
	Student           students.Student       `json:"student"`
	Enrollment        enrollments.Enrollment `json:"enrollment"`
	Guardians         []GuardianLink         `json:"guardians"`
	Notes             []string               `json:"notes"`
}

type regRow struct {
	id, number, first, phone, dob, classID, yearID, status string
	middle, last, gender, father, mother, guardian         *string
	admitted                                               *string
}

var rollRE = regexp.MustCompile(`^[0-9]{1,9}$`)

var india = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return l
	}
	return time.FixedZone("IST", 5*3600+1800)
}()

func trim(p *string) *string { return crud.TrimPtr(p) }

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// fieldMap renames a field of the students package's errors to the one the admit form shows.
func fieldMap(fe crud.FieldErrors) crud.FieldErrors {
	out := make(crud.FieldErrors, len(fe))
	for i, e := range fe {
		if e.Field == "registrationCode" {
			e.Field = "studentCode"
		}
		out[i] = e
	}
	return out
}

// admit does one admission inside tx (which the caller owns and which is bound to the institute). created is false
// when the registration had already been admitted. Any error leaves nothing for the caller to keep: the caller rolls
// the transaction (or savepoint) back.
func (s Service) admit(ctx context.Context, tx pgx.Tx, actor audit.Actor, registrationID string, in Input) (res Result, created bool, err error) {
	var r regRow
	err = tx.QueryRow(ctx, `SELECT id::text, application_number, first_name, middle_name, last_name, gender, father_name, mother_name, guardian_name, guardian_phone,
			date_of_birth::text, class_id::text, year_id::text, status, admitted_student_id::text
		FROM student_registrations WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, registrationID).
		Scan(&r.id, &r.number, &r.first, &r.middle, &r.last, &r.gender, &r.father, &r.mother, &r.guardian, &r.phone, &r.dob, &r.classID, &r.yearID, &r.status, &r.admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, false, ErrNotFound
	}
	if err != nil {
		return res, false, err
	}
	// the row lock is what makes a second, simultaneous admit wait and then find the first one's student
	if r.admitted != nil {
		res, err = s.loadResult(ctx, tx, r)
		return res, false, err
	}
	if r.status != "accepted" {
		return res, false, ErrNotAccepted
	}
	for _, g := range s.Gates {
		fe, gerr := g.Check(ctx, tx, r.id)
		if gerr != nil {
			return res, false, gerr
		}
		if len(fe) > 0 {
			return res, false, NotEligible{Problems: fe}
		}
	}

	in.SectionID = strings.TrimSpace(in.SectionID)
	in.RollNumber, in.StudentCode, in.Gender = trim(in.RollNumber), trim(in.StudentCode), trim(in.Gender)
	in.AdmissionDate, in.ParentEmail = trim(in.AdmissionDate), trim(in.ParentEmail)
	var fe crud.FieldErrors

	today := registrations.Today(time.Now())
	admissionDate := today
	if in.AdmissionDate != nil {
		admissionDate = *in.AdmissionDate
		if d, derr := time.Parse(crud.DateLayout, admissionDate); derr != nil {
			fe.Add("admissionDate", "invalid_date", "Enter the admission date as YYYY-MM-DD.")
		} else if admissionDate > today {
			fe.Add("admissionDate", "in_future", "The admission date cannot be in the future.")
		} else if d.Year() < 2000 {
			fe.Add("admissionDate", "invalid_date", "Check the year of the admission date.")
		}
	}

	gender := ""
	switch {
	case r.gender != nil:
		gender = *r.gender
	case in.Gender != nil:
		gender = strings.ToLower(*in.Gender)
	default:
		fe.Add("gender", "required", "The registration has no gender. Choose one for the student.")
	}

	// section: required, and mapped to the registration's class for its year
	switch {
	case in.SectionID == "":
		fe.Add("sectionId", "required", "Choose a section.")
	case !uuidRE.MatchString(in.SectionID):
		fe.Add("sectionId", "invalid", "This is not a valid id.")
	default:
		var ok bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM class_section_mappings m
				JOIN sections s ON s.tenant_id = m.tenant_id AND s.id = m.section_id AND s.deleted_at IS NULL
				WHERE m.class_id::text = $1 AND m.year_id::text = $2 AND m.section_id::text = $3 AND m.deleted_at IS NULL)`, r.classID, r.yearID, in.SectionID).Scan(&ok); err != nil {
			return res, false, err
		}
		if !ok {
			fe.Add("sectionId", "not_in_class", "This section is not set up for the registration's class in that academic year. Map it first under Class sections.")
		}
	}

	// a typed roll number must be free in the section; blank is numbered later, under a lock
	if in.RollNumber != nil && len(fe) == 0 {
		var used bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM student_enrollments WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3
			AND lower(roll_number) = lower($4) AND deleted_at IS NULL)`, r.yearID, r.classID, in.SectionID, *in.RollNumber).Scan(&used); err != nil {
			return res, false, err
		}
		if used {
			fe.Add("rollNumber", "duplicate", "Another student of this class and section already has this roll number.")
		}
	}

	// the same child may already be a student (an earlier registration, an import)
	if !in.ConfirmDuplicate {
		var code *string
		err = tx.QueryRow(ctx, `SELECT registration_code FROM students WHERE lower(first_name) = lower($1) AND lower(COALESCE(last_name, '')) = lower($2)
			AND date_of_birth = $3::date AND deleted_at IS NULL LIMIT 1`, r.first, deref(r.last), r.dob).Scan(&code)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return res, false, err
		}
		if code != nil {
			fe.Add("confirmDuplicate", "possible_duplicate", "A student with this name and date of birth already exists ("+*code+"). If this is a different child, confirm to admit.")
		}
	}

	studentIn := students.Input{RegistrationCode: "STU-000000", FirstName: r.first, MiddleName: r.middle, LastName: r.last, Gender: gender, DateOfBirth: r.dob,
		AdmissionDate: &admissionDate, ParentEmail: in.ParentEmail, Status: "active"}
	if in.StudentCode != nil {
		studentIn.RegistrationCode = *in.StudentCode
	}
	// every field rule of the students screen, now, so the registrar sees all the problems at once
	if _, nerr := students.Normalise(studentIn, time.Now()); nerr != nil {
		var sfe crud.FieldErrors
		if errors.As(nerr, &sfe) {
			fe = append(fe, fieldMap(sfe)...)
		} else {
			return res, false, nerr
		}
	}
	if in.StudentCode != nil && !hasField(fe, "studentCode") {
		var used bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM students WHERE lower(registration_code) = lower($1) AND deleted_at IS NULL)`, *in.StudentCode).Scan(&used); err != nil {
			return res, false, err
		}
		if used {
			fe.Add("studentCode", "duplicate", "Another student already uses this student code.")
		}
	}
	if len(fe) > 0 {
		return res, false, fe
	}

	// ---- writes: nothing below is kept unless every step succeeds
	if in.StudentCode == nil {
		if err = tx.QueryRow(ctx, `SELECT next_student_code(app_current_tenant())`).Scan(&studentIn.RegistrationCode); err != nil {
			return res, false, err
		}
	}
	student, err := s.studentStore().CreateIn(ctx, tx, actor, studentIn)
	if err != nil {
		return res, false, mapStudentError(err)
	}

	glinks, notes, err := s.linkGuardians(ctx, tx, actor, r, student.ID)
	if err != nil {
		return res, false, err
	}

	roll := in.RollNumber
	if roll == nil {
		next, rerr := nextRoll(ctx, tx, r.yearID, r.classID, in.SectionID)
		if rerr != nil {
			return res, false, rerr
		}
		roll = &next
	}
	enrol, err := s.enrolmentStore().CreateIn(ctx, tx, actor, enrollments.Input{StudentID: student.ID, YearID: r.yearID, ClassID: r.classID, SectionID: in.SectionID,
		RollNumber: roll, EnrollmentDate: &admissionDate, Status: "enrolled"})
	if err != nil {
		return res, false, err
	}

	if _, err = tx.Exec(ctx, `UPDATE student_registrations SET admitted_student_id = $2::uuid, admitted_at = now(), admitted_by = NULLIF($3, '')::uuid, updated_by = NULLIF($3, '')::uuid WHERE id::text = $1`,
		r.id, student.ID, actor.UserID); err != nil {
		return res, false, err
	}
	if err = audit.Record(ctx, tx, actor, audit.Entry{Action: "admissions.admit", ResourceType: "admission", ResourceID: r.id,
		Before: map[string]any{"status": r.status, "admitted": false},
		After:  map[string]any{"applicationNumber": r.number, "studentId": student.ID, "studentCode": student.RegistrationCode, "enrollmentId": enrol.ID, "guardians": len(glinks)}}); err != nil {
		return res, false, err
	}
	// the student as the records now show it (class and section come from the enrolment)
	if student, err = s.studentStore().GetIn(ctx, tx, student.ID); err != nil {
		return res, false, err
	}
	return Result{RegistrationID: r.id, ApplicationNumber: r.number, Student: student, Enrollment: enrol, Guardians: glinks, Notes: notes}, true, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func hasField(fe crud.FieldErrors, field string) bool {
	for _, e := range fe {
		if e.Field == field {
			return true
		}
	}
	return false
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s Service) studentStore() resource.Store[students.Student, students.Input] {
	return students.Service{DB: s.DB}.Store()
}

func (s Service) enrolmentStore() resource.Store[enrollments.Enrollment, enrollments.Input] {
	return enrollments.Service{DB: s.DB}.Store()
}

// mapStudentError names the student code the way the admit form does.
func mapStudentError(err error) error {
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return fieldMap(fe)
	}
	return err
}

// nextRoll gives the next roll number of a section for the year: one more than the highest purely numeric roll. A lock
// keyed by the section makes two admissions into the same section take turns, so they never get the same number.
func nextRoll(ctx context.Context, tx pgx.Tx, yearID, classID, sectionID string) (string, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "roll|"+tenantID(ctx)+"|"+yearID+"|"+classID+"|"+sectionID); err != nil {
		return "", err
	}
	var n int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(roll_number::bigint), 0) + 1 FROM student_enrollments
		WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL AND roll_number ~ $4`,
		yearID, classID, sectionID, rollRE.String()).Scan(&n)
	return fmt.Sprint(n), err
}

func tenantID(ctx context.Context) string {
	if t, ok := tenancy.FromContext(ctx); ok {
		return t.ID
	}
	return ""
}

// linkGuardians creates (or reuses) the guardians named on the registration and links them to the student. The
// registration has one phone number; it is used for each of them. A guardian who already exists with the same name and
// phone (a sibling's parent) is linked, not created again. The primary guardian is the named guardian, else the father,
// else the mother. A registration with no parent or guardian name creates no guardian, and says so in the notes.
func (s Service) linkGuardians(ctx context.Context, tx pgx.Tx, actor audit.Actor, r regRow, studentID string) ([]GuardianLink, []string, error) {
	type person struct{ name, relation string }
	var people []person
	seen := map[string]bool{}
	for _, p := range []person{{deref(r.father), "father"}, {deref(r.mother), "mother"}, {deref(r.guardian), "guardian"}} {
		p.name = strings.Join(strings.Fields(p.name), " ")
		if p.name == "" || seen[norm(p.name)] {
			continue
		}
		seen[norm(p.name)] = true
		people = append(people, p)
	}
	if len(people) == 0 {
		return []GuardianLink{}, []string{"The registration names no parent or guardian, so no guardian was added. Add one on the student's record."}, nil
	}
	primary := 0
	if g := norm(deref(r.guardian)); g != "" {
		for i, p := range people {
			if norm(p.name) == g {
				primary = i
			}
		}
	}
	gs, ls := guardians.Service{DB: s.DB}.Store(), links.Service{DB: s.DB}.Store()
	out := make([]GuardianLink, 0, len(people))
	for i, p := range people {
		var gid string
		err := tx.QueryRow(ctx, `SELECT id::text FROM guardians WHERE phone = $1 AND lower(btrim(name)) = lower(btrim($2)) AND deleted_at IS NULL LIMIT 1`, r.phone, p.name).Scan(&gid)
		if errors.Is(err, pgx.ErrNoRows) {
			g, cerr := gs.CreateIn(ctx, tx, actor, guardians.Input{Name: p.name, Phone: r.phone})
			if cerr != nil {
				return nil, nil, cerr
			}
			gid = g.ID
		} else if err != nil {
			return nil, nil, err
		}
		if _, err := ls.CreateIn(ctx, tx, actor, links.Input{StudentID: studentID, GuardianID: gid, Relation: p.relation, IsPrimary: i == primary}); err != nil {
			return nil, nil, err
		}
		out = append(out, GuardianLink{GuardianID: gid, Name: p.name, Relation: p.relation, IsPrimary: i == primary})
	}
	return out, []string{}, nil
}

// loadResult describes an admission already made.
func (s Service) loadResult(ctx context.Context, tx pgx.Tx, r regRow) (Result, error) {
	res := Result{RegistrationID: r.id, ApplicationNumber: r.number, AlreadyAdmitted: true, Guardians: []GuardianLink{}, Notes: []string{}}
	student, err := s.studentStore().GetIn(ctx, tx, *r.admitted)
	if errors.Is(err, resource.ErrNotFound) {
		return res, ErrStudentRemoved
	}
	if err != nil {
		return res, err
	}
	res.Student = student
	enrol, _, err := s.enrolmentStore().FindIn(ctx, tx, `student_id::text = $1 AND year_id::text = $2`, student.ID, r.yearID)
	if err != nil {
		return res, err
	}
	res.Enrollment = enrol
	rows, err := tx.Query(ctx, `SELECT g.id::text, g.name, l.relation, l.is_primary FROM student_guardians l
		JOIN guardians g ON g.tenant_id = l.tenant_id AND g.id = l.guardian_id AND g.deleted_at IS NULL
		WHERE l.student_id::text = $1 AND l.deleted_at IS NULL ORDER BY l.is_primary DESC, l.created_at, g.name`, student.ID)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var g GuardianLink
		if err := rows.Scan(&g.GuardianID, &g.Name, &g.Relation, &g.IsPrimary); err != nil {
			return res, err
		}
		res.Guardians = append(res.Guardians, g)
	}
	return res, rows.Err()
}

// Admit admits one registration in one transaction. Admitting a registration that is already admitted returns the same
// student with AlreadyAdmitted set and writes nothing, whatever the new input says. Errors: ErrNotFound,
// ErrNotAccepted, NotEligible, crud.FieldErrors (nothing written), ErrStudentRemoved.
func (s Service) Admit(ctx context.Context, actor audit.Actor, registrationID string, in Input) (Result, error) {
	var out Result
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		out, _, err = s.admit(ctx, tx, actor, registrationID, in)
		return err
	})
	if err != nil {
		var zero Result
		return zero, mapStudentError(err)
	}
	return out, nil
}

// Row outcomes of a preview or a bulk admit.
const (
	OutcomeOK              = "would_admit"      // preview: would be admitted
	OutcomeAdmitted        = "admitted"         // bulk: admitted now
	OutcomeAlreadyAdmitted = "already_admitted" // admitted before; nothing written
	OutcomeProblem         = "problem"          // not admitted; Problems says why
)

// Row is the outcome for one registration of a preview or a bulk admit.
type Row struct {
	RegistrationID    string
	ApplicationNumber string
	Outcome           string
	Problems          crud.FieldErrors
	// Result is set for admitted rows, and for rows a preview says would be admitted (what the admission would be).
	Result *Result
}

// problemsOf turns an admit error into the problems to show on a row. ok is false for an error that is not about this
// registration (the database failing), which the caller treats as a failure of the call.
func problemsOf(err error) (crud.FieldErrors, bool) {
	var fe crud.FieldErrors
	var ne NotEligible
	switch {
	case errors.As(err, &fe):
		return fe, true
	case errors.As(err, &ne):
		out := make(crud.FieldErrors, len(ne.Problems))
		for i, p := range ne.Problems {
			p.Code = "not_eligible"
			out[i] = p
		}
		return out, true
	case errors.Is(err, ErrNotFound):
		return crud.FieldErrors{{Field: "registrationId", Code: "not_found", Message: "This registration does not exist."}}, true
	case errors.Is(err, ErrNotAccepted):
		return crud.FieldErrors{{Field: "registrationId", Code: "not_accepted", Message: "Only an accepted registration can be admitted."}}, true
	case errors.Is(err, ErrStudentRemoved):
		return crud.FieldErrors{{Field: "registrationId", Code: "student_removed", Message: "This registration was admitted, but its student has been removed. Ask an administrator."}}, true
	}
	return nil, false
}

var errPreviewDone = errors.New("admissions: preview rolled back")

func checkBatch(items []Item) error {
	if len(items) == 0 || len(items) > MaxBatch {
		return ErrTooMany
	}
	return nil
}

// Preview says what admitting these registrations would do, row by row, without keeping anything. It runs the real
// admission for each row inside one transaction that is rolled back at the end, each row in its own savepoint, so a
// row sees what the rows before it would have created (the next roll number, a code typed twice) and the answer is
// the one the commit gives. A registration listed twice is a problem on its second row.
func (s Service) Preview(ctx context.Context, actor audit.Actor, items []Item) ([]Row, error) {
	if err := checkBatch(items); err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(items))
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		listed := map[string]bool{}
		for _, it := range items {
			row := Row{RegistrationID: it.RegistrationID}
			if listed[it.RegistrationID] {
				row.Outcome = OutcomeProblem
				row.Problems = crud.FieldErrors{{Field: "registrationId", Code: "duplicate_in_request", Message: "This registration is listed twice."}}
				rows = append(rows, row)
				continue
			}
			listed[it.RegistrationID] = true
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			res, created, aerr := s.admit(ctx, sp, actor, it.RegistrationID, it.Input)
			if aerr != nil {
				_ = sp.Rollback(ctx)
				fe, ok := problemsOf(mapStudentError(aerr))
				if !ok {
					return aerr
				}
				row.Outcome, row.Problems = OutcomeProblem, fe
				rows = append(rows, row)
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			row.ApplicationNumber, row.Result = res.ApplicationNumber, &res
			row.Outcome = OutcomeOK
			if !created {
				row.Outcome = OutcomeAlreadyAdmitted
			}
			rows = append(rows, row)
		}
		return errPreviewDone
	})
	if err != nil && !errors.Is(err, errPreviewDone) {
		return nil, err
	}
	return rows, nil
}

// AdmitMany admits each registration in its own transaction and reports each one. It is not all-or-nothing on
// purpose: the registrations are independent families, so one bad row (a duplicate roll number, a section not mapped)
// must not hold back the rest, and a long batch must not hold one transaction and its row locks open. Every admission
// is atomic on its own, and the whole call is safe to repeat: a registration already admitted is reported as such.
// Run Preview first to see the problems before committing.
func (s Service) AdmitMany(ctx context.Context, actor audit.Actor, items []Item) ([]Row, error) {
	if err := checkBatch(items); err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(items))
	listed := map[string]bool{}
	for _, it := range items {
		row := Row{RegistrationID: it.RegistrationID}
		if listed[it.RegistrationID] {
			row.Outcome = OutcomeProblem
			row.Problems = crud.FieldErrors{{Field: "registrationId", Code: "duplicate_in_request", Message: "This registration is listed twice."}}
			rows = append(rows, row)
			continue
		}
		listed[it.RegistrationID] = true
		var res Result
		var created bool
		err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
			res, created, err = s.admit(ctx, tx, actor, it.RegistrationID, it.Input)
			return err
		})
		if err != nil {
			fe, ok := problemsOf(mapStudentError(err))
			if !ok {
				fe = crud.FieldErrors{{Field: "registrationId", Code: "failed", Message: "This registration could not be admitted. Try it again."}}
			}
			row.Outcome, row.Problems = OutcomeProblem, fe
			rows = append(rows, row)
			continue
		}
		row.ApplicationNumber, row.Result = res.ApplicationNumber, &res
		row.Outcome = OutcomeAdmitted
		if !created {
			row.Outcome = OutcomeAlreadyAdmitted
		}
		rows = append(rows, row)
	}
	return rows, nil
}
