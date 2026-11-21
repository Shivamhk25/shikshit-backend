// Package students implements STU-02 Students: a student's identity. Class and section come from the enrollment of an
// academic year (package enrollments), not from this record.
package students

import (
	"context"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/modules/stu/stuscope"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the student does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Genders and Statuses are the allowed values, stored as these codes.
var (
	Genders  = []string{"male", "female", "other"}
	Statuses = []string{"active", "paused", "transferred", "alumni"}
)

// MaxAgeYears is the oldest a student may be; it catches a mistyped century.
const MaxAgeYears = 30

// Student is a student as the API shows it.
type Student struct {
	ID                 string    `json:"id"`
	RegistrationCode   string    `json:"registrationCode"`
	FirstName          string    `json:"firstName"`
	MiddleName         *string   `json:"middleName"`
	LastName           *string   `json:"lastName"`
	FullName           string    `json:"fullName"`
	Gender             string    `json:"gender"`
	DateOfBirth        string    `json:"dateOfBirth"`
	AdmissionDate      *string   `json:"admissionDate"`
	Mobile             *string   `json:"mobile"`
	ParentEmail        *string   `json:"parentEmail"`
	StudentEmail       *string   `json:"studentEmail"`
	ApaarID            *string   `json:"apaarId"`
	Status             string    `json:"status"`
	Remarks            *string   `json:"remarks"`
	CurrentClassName   *string   `json:"currentClassName"`
	CurrentSectionName *string   `json:"currentSectionName"`
	CurrentRollNumber  *string   `json:"currentRollNumber"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	RegistrationCode string
	FirstName        string
	MiddleName       *string
	LastName         *string
	Gender           string
	DateOfBirth      string
	AdmissionDate    *string
	Mobile           *string
	ParentEmail      *string
	StudentEmail     *string
	ApaarID          *string
	Status           string
	Remarks          *string
}

// Filter narrows a list.
type Filter struct{ Status, Gender string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"registrationCode": "lower(registration_code)", "fullName": "lower(first_name), lower(last_name)", "dateOfBirth": "date_of_birth", "createdAt": "created_at"}

// DefaultSort lists students by name.
const DefaultSort = "fullName"

// The current class is the student's enrollment in the current academic year, when there is one.
const currentEnrollment = `(SELECT %s FROM student_enrollments e
	JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id AND y.is_current AND y.deleted_at IS NULL
	%s WHERE e.tenant_id = students.tenant_id AND e.student_id = students.id AND e.deleted_at IS NULL AND e.status = 'enrolled' LIMIT 1)`

func current(col, join string) string {
	return strings.Replace(strings.Replace(currentEnrollment, "%s", col, 1), "%s", join, 1)
}

var columns = `id::text, registration_code, first_name, middle_name, last_name,
	btrim(first_name || ' ' || COALESCE(middle_name || ' ', '') || COALESCE(last_name, '')) AS full_name,
	gender, date_of_birth::text, admission_date::text, mobile, parent_email, student_email, apaar_id, status, remarks,
	` + current("c.name", "JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id") + ` AS current_class,
	` + current("s.name", "JOIN sections s ON s.tenant_id = e.tenant_id AND s.id = e.section_id") + ` AS current_section,
	` + current("e.roll_number", "") + ` AS current_roll,
	created_at, updated_at`

func scan(r resource.Scanner) (Student, error) {
	var s Student
	err := r.Scan(&s.ID, &s.RegistrationCode, &s.FirstName, &s.MiddleName, &s.LastName, &s.FullName, &s.Gender, &s.DateOfBirth, &s.AdmissionDate,
		&s.Mobile, &s.ParentEmail, &s.StudentEmail, &s.ApaarID, &s.Status, &s.Remarks, &s.CurrentClassName, &s.CurrentSectionName, &s.CurrentRollNumber, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

var (
	apaarRE  = regexp.MustCompile(`^[0-9]{12}$`)
	mobileRE = regexp.MustCompile(`^[0-9+() -]{7,20}$`)
)

func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// Normalise trims, defaults and validates one student. today is passed in so the date rules are testable.
func Normalise(in Input, today time.Time) (Input, error) {
	var errs crud.FieldErrors
	in.RegistrationCode, in.FirstName = strings.TrimSpace(in.RegistrationCode), strings.TrimSpace(in.FirstName)
	in.Gender, in.Status = strings.ToLower(strings.TrimSpace(in.Gender)), strings.ToLower(strings.TrimSpace(in.Status))
	in.MiddleName, in.LastName, in.Remarks = crud.TrimPtr(in.MiddleName), crud.TrimPtr(in.LastName), crud.TrimPtr(in.Remarks)
	in.Mobile, in.ApaarID, in.AdmissionDate = crud.TrimPtr(in.Mobile), crud.TrimPtr(in.ApaarID), crud.TrimPtr(in.AdmissionDate)
	in.ParentEmail, in.StudentEmail = crud.TrimPtr(in.ParentEmail), crud.TrimPtr(in.StudentEmail)
	if in.Status == "" {
		in.Status = "active"
	}
	errs.Required("registrationCode", "a registration code", in.RegistrationCode, 30)
	errs.Required("firstName", "a first name", in.FirstName, 100)
	errs.Optional("middleName", "middle name", in.MiddleName, 100)
	errs.Optional("lastName", "last name", in.LastName, 100)
	errs.Optional("remarks", "remarks", in.Remarks, 500)
	errs.Choice("gender", in.Gender, Genders...)
	errs.Choice("status", in.Status, Statuses...)
	today = today.UTC().Truncate(24 * time.Hour)
	if dob, err := time.Parse("2006-01-02", in.DateOfBirth); err != nil {
		errs.Add("dateOfBirth", "invalid_date", "Enter the date of birth as YYYY-MM-DD.")
	} else if dob.After(today) {
		errs.Add("dateOfBirth", "in_future", "The date of birth cannot be in the future.")
	} else if dob.Before(today.AddDate(-MaxAgeYears, 0, 0)) {
		errs.Add("dateOfBirth", "too_old", "A student cannot be more than 30 years old. Check the year.")
	}
	if in.AdmissionDate != nil {
		if _, err := time.Parse("2006-01-02", *in.AdmissionDate); err != nil {
			errs.Add("admissionDate", "invalid_date", "Enter the admission date as YYYY-MM-DD.")
		}
	}
	if in.Mobile != nil && !mobileRE.MatchString(*in.Mobile) {
		errs.Add("mobile", "invalid", "A mobile number has 7 to 20 digits, and may include +, spaces, brackets or hyphens.")
	}
	for _, e := range []struct {
		field string
		v     *string
	}{{"parentEmail", in.ParentEmail}, {"studentEmail", in.StudentEmail}} {
		if e.v != nil && !validEmail(*e.v) {
			errs.Add(e.field, "invalid_email", "Enter a valid email address.")
		}
	}
	if in.ApaarID != nil && !apaarRE.MatchString(*in.ApaarID) {
		errs.Add("apaarId", "invalid", "An APAAR ID has 12 digits.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Student, Input]{
	Table: "students", Action: "students", ResourceType: "student",
	Columns: columns, Scan: scan, ID: func(s Student) string { return s.ID },
	WriteColumns: []string{"registration_code", "first_name", "middle_name", "last_name", "gender", "date_of_birth", "admission_date", "mobile", "parent_email", "student_email", "apaar_id", "status", "remarks"},
	Args: func(in Input) []any {
		return []any{in.RegistrationCode, in.FirstName, in.MiddleName, in.LastName, in.Gender, in.DateOfBirth, in.AdmissionDate, in.Mobile, in.ParentEmail, in.StudentEmail, in.ApaarID, in.Status, in.Remarks}
	},
	SearchColumns: []string{"registration_code", "first_name", "middle_name", "last_name", "mobile", "apaar_id"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: func(in Input) (Input, error) { return Normalise(in, time.Now()) },
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		return refs.Used(ctx, tx, id, `SELECT (SELECT count(*) FROM student_enrollments WHERE student_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM student_guardians WHERE student_id::text = $1 AND deleted_at IS NULL)
			+ (SELECT count(*) FROM student_addresses WHERE student_id::text = $1 AND deleted_at IS NULL)`,
			"This student still has enrollments, guardians or addresses. Remove them first.")
	},
	Restrict: stuscope.Student("students", "id"),
	Constraints: map[string]crud.FieldError{
		"students_tenant_code_uq":  {Field: "registrationCode", Code: "duplicate", Message: "Another student already uses this registration code."},
		"students_tenant_apaar_uq": {Field: "apaarId", Code: "duplicate", Message: "Another student already has this APAAR ID."},
	},
}

// Service reads and writes students.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Student, Input] { return s.store() }

func (s Service) store() resource.Store[Student, Input] {
	return resource.Store[Student, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of students.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Student], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"status", f.Status}, {"gender", f.Gender}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// Get returns one student.
func (s Service) Get(ctx context.Context, id string) (Student, error) { return s.store().Get(ctx, id) }

// ListAudited is List for the API: the read is recorded in the audit trail with the paging and filters used, and a
// flag saying whether a search was typed, never the search text, which can be a child's name.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Student], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "status": f.Status, "gender": f.Gender}, q, filterConds(f)...)
}

// View is Get for the API: the read is recorded in the audit trail.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Student, error) {
	return s.store().View(ctx, a, id)
}

// Create adds a student.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Student, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a student's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Student, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a student unless an enrollment still exists.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
