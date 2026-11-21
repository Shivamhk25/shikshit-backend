// Package enrollments implements the yearly enrollment of a student (STU-02): which class and section the student is
// in for one academic year.
package enrollments

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/modules/aca/yearcheck"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the enrollment does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Statuses are the allowed enrollment states.
var Statuses = []string{"enrolled", "withdrawn", "completed"}

// Enrollment is an enrollment as the API shows it.
type Enrollment struct {
	ID               string    `json:"id"`
	StudentID        string    `json:"studentId"`
	RegistrationCode string    `json:"registrationCode"`
	StudentName      string    `json:"studentName"`
	YearID           string    `json:"yearId"`
	YearName         string    `json:"yearName"`
	ClassID          string    `json:"classId"`
	ClassName        string    `json:"className"`
	SectionID        string    `json:"sectionId"`
	SectionName      string    `json:"sectionName"`
	RollNumber       *string   `json:"rollNumber"`
	EnrollmentNumber *string   `json:"enrollmentNumber"`
	EnrollmentDate   *string   `json:"enrollmentDate"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	StudentID        string
	YearID           string
	ClassID          string
	SectionID        string
	RollNumber       *string
	EnrollmentNumber *string
	EnrollmentDate   *string
	Status           string
}

// Filter narrows a list.
type Filter struct{ StudentID, YearID, ClassID, SectionID, Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"studentName": "student_name", "className": "class_name", "sectionName": "section_name", "rollNumber": "roll_number", "createdAt": "created_at"}

// DefaultSort lists by class, section, then name.
const DefaultSort = "className,sectionName,studentName"

const t = "student_enrollments"

func sub(table, col, alias string) string {
	return "(SELECT " + col + " FROM " + table + " x WHERE x.tenant_id = " + t + ".tenant_id AND x.id = " + t + "." + alias + ")"
}

var columns = `id::text, student_id::text,
	` + sub("students", "x.registration_code", "student_id") + ` AS registration_code,
	` + sub("students", "btrim(x.first_name || ' ' || COALESCE(x.last_name, ''))", "student_id") + ` AS student_name,
	year_id::text, ` + sub("academic_years", "x.name", "year_id") + ` AS year_name,
	class_id::text, ` + sub("classes", "x.name", "class_id") + ` AS class_name,
	section_id::text, ` + sub("sections", "x.name", "section_id") + ` AS section_name,
	roll_number, enrollment_number, enrollment_date::text, status, created_at, updated_at`

func scan(r resource.Scanner) (Enrollment, error) {
	var e Enrollment
	err := r.Scan(&e.ID, &e.StudentID, &e.RegistrationCode, &e.StudentName, &e.YearID, &e.YearName, &e.ClassID, &e.ClassName, &e.SectionID, &e.SectionName,
		&e.RollNumber, &e.EnrollmentNumber, &e.EnrollmentDate, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// Normalise trims, defaults and validates one enrollment on its own; rules that need other records are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.StudentID, in.YearID, in.ClassID, in.SectionID = strings.TrimSpace(in.StudentID), strings.TrimSpace(in.YearID), strings.TrimSpace(in.ClassID), strings.TrimSpace(in.SectionID)
	in.RollNumber, in.EnrollmentNumber, in.EnrollmentDate = crud.TrimPtr(in.RollNumber), crud.TrimPtr(in.EnrollmentNumber), crud.TrimPtr(in.EnrollmentDate)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	if in.Status == "" {
		in.Status = "enrolled"
	}
	refs.Require(&errs, "studentId", "a student", in.StudentID)
	refs.Require(&errs, "yearId", "an academic year", in.YearID)
	refs.Require(&errs, "classId", "a class", in.ClassID)
	refs.Require(&errs, "sectionId", "a section", in.SectionID)
	errs.Optional("rollNumber", "roll number", in.RollNumber, 20)
	errs.Optional("enrollmentNumber", "enrollment number", in.EnrollmentNumber, 30)
	errs.Choice("status", in.Status, Statuses...)
	if in.EnrollmentDate != nil {
		if _, err := time.Parse("2006-01-02", *in.EnrollmentDate); err != nil {
			errs.Add("enrollmentDate", "invalid_date", "Enter the enrollment date as YYYY-MM-DD.")
		}
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Enrollment, Input]{
	Table: t, Action: "student_enrollments", ResourceType: "student_enrollment",
	Columns: columns, Scan: scan, ID: func(e Enrollment) string { return e.ID },
	WriteColumns: []string{"student_id", "year_id", "class_id", "section_id", "roll_number", "enrollment_number", "enrollment_date", "status"},
	Args: func(in Input) []any {
		return []any{in.StudentID, in.YearID, in.ClassID, in.SectionID, in.RollNumber, in.EnrollmentNumber, in.EnrollmentDate, in.Status}
	},
	SearchColumns: []string{"roll_number", "enrollment_number", sub("students", "x.registration_code", "student_id"), sub("students", "x.first_name || ' ' || COALESCE(x.last_name, '')", "student_id")},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		if _, _, err := yearcheck.Dates(ctx, tx, in.YearID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM students WHERE id::text = $1 AND deleted_at IS NULL`, in.StudentID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			fe.Add("studentId", "not_found", "This student does not exist.")
		}
		for _, r := range []struct{ field, table, id string }{{"classId", "classes", in.ClassID}, {"sectionId", "sections", in.SectionID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		if len(fe) == 0 {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM class_section_mappings
				WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL`, in.YearID, in.ClassID, in.SectionID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				fe.Add("sectionId", "not_in_class", "This section is not mapped to this class for this year. Map it first under Class sections.")
			}
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"student_enrollments_student_year_uq":           {Field: "studentId", Code: "duplicate", Message: "This student is already enrolled for this academic year."},
		"student_enrollments_roll_uq":                   {Field: "rollNumber", Code: "duplicate", Message: "Another student of this class and section already has this roll number."},
		"student_enrollments_number_uq":                 {Field: "enrollmentNumber", Code: "duplicate", Message: "Another enrollment already uses this number."},
		"student_enrollments_tenant_id_student_id_fkey": {Field: "studentId", Code: "not_found", Message: "This student does not exist."},
		"student_enrollments_tenant_id_year_id_fkey":    {Field: "yearId", Code: "not_found", Message: "This academic year does not exist."},
		"student_enrollments_tenant_id_class_id_fkey":   {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"student_enrollments_tenant_id_section_id_fkey": {Field: "sectionId", Code: "not_found", Message: "This section does not exist."},
	},
}

// Service reads and writes enrollments.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Enrollment, Input] { return s.store() }

func (s Service) store() resource.Store[Enrollment, Input] {
	return resource.Store[Enrollment, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of enrollments.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Enrollment], error) {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"student_id::text", f.StudentID}, {"year_id::text", f.YearID}, {"class_id::text", f.ClassID}, {"section_id::text", f.SectionID}, {"status", f.Status}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one enrollment.
func (s Service) Get(ctx context.Context, id string) (Enrollment, error) {
	return s.store().Get(ctx, id)
}

// Create adds an enrollment.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Enrollment, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces an enrollment's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Enrollment, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes an enrollment.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
