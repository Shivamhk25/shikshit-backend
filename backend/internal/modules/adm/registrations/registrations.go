// Package registrations implements ADM-06 Student Registrations: a family applies for a place for a child. The record
// keeps the child, the parents, the class and year applied for, and where the application stands. Admission and
// enrolment into the students register are later steps (ADM-05, ADM-07). A registration can be created from an
// enquiry (ADM-03), see CreateFromEnquiry.
package registrations

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the registration does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Genders are the codes the students register uses; a registration may leave the gender blank until admission.
// Statuses are where an application stands: submitted (received), under_review (being looked at), accepted (a place is
// offered), rejected (no place) and withdrawn (the family pulled out). Becoming a student is ADM-05/07, not a status.
var (
	Genders  = students.Genders
	Statuses = []string{"submitted", "under_review", "accepted", "rejected", "withdrawn"}
)

// Registration is a registration as the API shows it. ApplicationNumber and EnquiryID are given by the system.
type Registration struct {
	ID                 string    `json:"id"`
	ApplicationNumber  string    `json:"applicationNumber"`
	AppliedOn          string    `json:"appliedOn"`
	FirstName          string    `json:"firstName"`
	MiddleName         *string   `json:"middleName"`
	LastName           *string   `json:"lastName"`
	FullName           string    `json:"fullName"`
	DateOfBirth        string    `json:"dateOfBirth"`
	Gender             *string   `json:"gender"`
	FatherName         *string   `json:"fatherName"`
	MotherName         *string   `json:"motherName"`
	GuardianName       *string   `json:"guardianName"`
	GuardianPhone      string    `json:"guardianPhone"`
	SiblingStudentCode *string   `json:"siblingStudentCode"`
	ClassID            string    `json:"classId"`
	ClassCode          string    `json:"classCode"`
	ClassName          string    `json:"className"`
	SectionID          *string   `json:"sectionId"`
	SectionName        *string   `json:"sectionName"`
	YearID             string    `json:"yearId"`
	YearName           string    `json:"yearName"`
	Status             string    `json:"status"`
	EnquiryID          *string   `json:"enquiryId"`
	EnquiryCode        *string   `json:"enquiryCode"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// Input is what create and replace accept. EnquiryID is set only by CreateFromEnquiry; the database keeps it fixed
// on later updates, and the application number is always given by the database.
type Input struct {
	AppliedOn          string
	FirstName          string
	MiddleName         *string
	LastName           *string
	DateOfBirth        string
	Gender             *string
	FatherName         *string
	MotherName         *string
	GuardianName       *string
	GuardianPhone      string
	SiblingStudentCode *string
	ClassID            string
	SectionID          *string
	YearID             string
	Status             string
	EnquiryID          *string
}

// Filter narrows a list.
type Filter struct{ Status, ClassID, YearID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"applicationNumber": "application_number", "appliedOn": "applied_on", "applicant": "lower(first_name), lower(last_name)",
	"dateOfBirth": "date_of_birth", "status": "status", "createdAt": "created_at"}

// DefaultSort lists the newest applications first.
const DefaultSort = "-appliedOn"

const columns = `id::text, application_number, applied_on::text, first_name, middle_name, last_name,
	concat_ws(' ', first_name, middle_name, last_name) AS full_name, date_of_birth::text, gender, father_name, mother_name,
	guardian_name, guardian_phone, sibling_student_code, class_id::text,
	(SELECT c.code FROM classes c WHERE c.tenant_id = student_registrations.tenant_id AND c.id = student_registrations.class_id) AS class_code,
	(SELECT c.name FROM classes c WHERE c.tenant_id = student_registrations.tenant_id AND c.id = student_registrations.class_id) AS class_name,
	section_id::text,
	(SELECT s.name FROM sections s WHERE s.tenant_id = student_registrations.tenant_id AND s.id = student_registrations.section_id) AS section_name,
	year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = student_registrations.tenant_id AND y.id = student_registrations.year_id) AS year_name,
	status, enquiry_id::text,
	(SELECT e.code FROM enquiries e WHERE e.tenant_id = student_registrations.tenant_id AND e.id = student_registrations.enquiry_id) AS enquiry_code,
	created_at, updated_at`

func scan(r resource.Scanner) (Registration, error) {
	var g Registration
	err := r.Scan(&g.ID, &g.ApplicationNumber, &g.AppliedOn, &g.FirstName, &g.MiddleName, &g.LastName, &g.FullName, &g.DateOfBirth, &g.Gender,
		&g.FatherName, &g.MotherName, &g.GuardianName, &g.GuardianPhone, &g.SiblingStudentCode, &g.ClassID, &g.ClassCode, &g.ClassName,
		&g.SectionID, &g.SectionName, &g.YearID, &g.YearName, &g.Status, &g.EnquiryID, &g.EnquiryCode, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// phoneRE is the phone rule every other record uses (guardians, students, enquiries).
var phoneRE = regexp.MustCompile(`^[0-9+() -]{7,20}$`)

// india is the calendar the school keeps; "today" for an application date is today there.
var india = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return l
	}
	return time.FixedZone("IST", 5*3600+1800)
}()

// Today is the date in India, in the form the API uses.
func Today(now time.Time) string { return now.In(india).Format(crud.DateLayout) }

// Normalise trims, defaults and validates one registration on its own, as of now; rules that need other records are in
// the spec's Check.
func Normalise(in Input) (Input, error) { return normalise(in, time.Now()) }

// NormaliseAt is Normalise with the time of "now" given, so the date rules can be tested at their edges.
func NormaliseAt(in Input, now time.Time) (Input, error) { return normalise(in, now) }

func normalise(in Input, now time.Time) (Input, error) {
	var errs crud.FieldErrors
	in.AppliedOn, in.FirstName, in.DateOfBirth = strings.TrimSpace(in.AppliedOn), strings.TrimSpace(in.FirstName), strings.TrimSpace(in.DateOfBirth)
	in.GuardianPhone, in.ClassID, in.YearID = strings.TrimSpace(in.GuardianPhone), strings.TrimSpace(in.ClassID), strings.TrimSpace(in.YearID)
	in.Status = strings.ToLower(strings.TrimSpace(in.Status))
	in.MiddleName, in.LastName, in.Gender = crud.TrimPtr(in.MiddleName), crud.TrimPtr(in.LastName), crud.TrimPtr(in.Gender)
	in.FatherName, in.MotherName, in.GuardianName = crud.TrimPtr(in.FatherName), crud.TrimPtr(in.MotherName), crud.TrimPtr(in.GuardianName)
	in.SiblingStudentCode, in.SectionID, in.EnquiryID = crud.TrimPtr(in.SiblingStudentCode), crud.TrimPtr(in.SectionID), crud.TrimPtr(in.EnquiryID)
	if in.Gender != nil {
		g := strings.ToLower(*in.Gender)
		in.Gender = &g
		errs.Choice("gender", g, Genders...)
	}
	if in.Status == "" {
		in.Status = "submitted"
	}
	errs.Required("firstName", "a first name", in.FirstName, 100)
	errs.Optional("middleName", "middle name", in.MiddleName, 100)
	errs.Optional("lastName", "last name", in.LastName, 100)
	errs.Optional("fatherName", "father's name", in.FatherName, 150)
	errs.Optional("motherName", "mother's name", in.MotherName, 150)
	errs.Optional("guardianName", "guardian's name", in.GuardianName, 150)
	errs.Optional("siblingStudentCode", "sibling's student code", in.SiblingStudentCode, 30)
	errs.Choice("status", in.Status, Statuses...)
	if in.GuardianPhone == "" {
		errs.Add("guardianPhone", "required", "Enter a phone number we can reach the family on.")
	} else if !phoneRE.MatchString(in.GuardianPhone) {
		errs.Add("guardianPhone", "invalid", "A phone number has 7 to 20 digits, and may include +, spaces, brackets or hyphens.")
	}
	today := now.In(india)
	if d, err := time.Parse(crud.DateLayout, in.AppliedOn); err != nil {
		errs.Add("appliedOn", "invalid_date", "Enter the application date as YYYY-MM-DD.")
	} else if in.AppliedOn > today.Format(crud.DateLayout) {
		errs.Add("appliedOn", "in_future", "The application date cannot be in the future.")
	} else if d.Year() < 2000 {
		errs.Add("appliedOn", "invalid_date", "Check the year of the application date.")
	}
	day := now.UTC().Truncate(24 * time.Hour)
	if dob, err := time.Parse(crud.DateLayout, in.DateOfBirth); err != nil {
		errs.Add("dateOfBirth", "invalid_date", "Enter the date of birth as YYYY-MM-DD.")
	} else if dob.After(day) {
		errs.Add("dateOfBirth", "in_future", "The date of birth cannot be in the future.")
	} else if dob.Before(day.AddDate(-students.MaxAgeYears, 0, 0)) {
		errs.Add("dateOfBirth", "too_old", "A child cannot be more than 30 years old. Check the year.")
	}
	for _, r := range []struct {
		field string
		v     string
	}{{"classId", in.ClassID}, {"yearId", in.YearID}} {
		if r.v == "" {
			errs.Add(r.field, "required", "Choose "+map[string]string{"classId": "a class", "yearId": "an academic year"}[r.field]+".")
		} else if !refs.IsUUID(r.v) {
			errs.Add(r.field, "invalid", "This is not a valid id.")
		}
	}
	if in.SectionID != nil && !refs.IsUUID(*in.SectionID) {
		errs.Add("sectionId", "invalid", "This is not a valid id.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Registration, Input]{
	Table: "student_registrations", Action: "student_registrations", ResourceType: "student_registration",
	Columns: columns, Scan: scan, ID: func(g Registration) string { return g.ID },
	WriteColumns: []string{"applied_on", "first_name", "middle_name", "last_name", "date_of_birth", "gender", "father_name", "mother_name", "guardian_name",
		"guardian_phone", "sibling_student_code", "class_id", "section_id", "year_id", "status", "enquiry_id"},
	Args: func(in Input) []any {
		return []any{in.AppliedOn, in.FirstName, in.MiddleName, in.LastName, in.DateOfBirth, in.Gender, in.FatherName, in.MotherName, in.GuardianName,
			in.GuardianPhone, in.SiblingStudentCode, in.ClassID, in.SectionID, in.YearID, in.Status, in.EnquiryID}
	},
	SearchColumns: []string{"application_number", "concat_ws(' ', first_name, middle_name, last_name)", "guardian_name", "father_name", "mother_name", "guardian_phone"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		for _, r := range []struct{ field, table, id string }{{"classId", "classes", in.ClassID}, {"yearId", "academic_years", in.YearID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		if in.SectionID != nil {
			if err := refs.Live(ctx, tx, &fe, "sectionId", "sections", *in.SectionID); err != nil {
				return err
			}
			if len(fe) == 0 {
				var n int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM class_section_mappings WHERE class_id::text = $1 AND year_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL`,
					in.ClassID, in.YearID, *in.SectionID).Scan(&n); err != nil {
					return err
				}
				if n == 0 {
					fe.Add("sectionId", "not_in_class", "This section is not set up for the chosen class in that academic year.")
				}
			}
		}
		if in.SiblingStudentCode != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM students WHERE lower(registration_code) = lower($1) AND deleted_at IS NULL`, *in.SiblingStudentCode).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				fe.Add("siblingStudentCode", "not_found", "No student has this code.")
			}
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"student_registrations_enquiry_uq":                {Field: "enquiryId", Code: "already_converted", Message: "This enquiry already has a registration."},
		"student_registrations_tenant_id_class_id_fkey":   {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"student_registrations_tenant_id_section_id_fkey": {Field: "sectionId", Code: "not_found", Message: "This section does not exist."},
		"student_registrations_tenant_id_year_id_fkey":    {Field: "yearId", Code: "not_found", Message: "This academic year does not exist."},
	},
}

// Service reads and writes registrations.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Registration, Input] {
	return resource.Store[Registration, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"status", f.Status}, {"class_id::text", f.ClassID}, {"year_id::text", f.YearID}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// List returns one page of registrations.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Registration], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

// ListAudited is List for the API: the read is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Registration], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "",
		"status": f.Status, "classId": f.ClassID, "yearId": f.YearID}, q, filterConds(f)...)
}

// Get returns one registration.
func (s Service) Get(ctx context.Context, id string) (Registration, error) {
	return s.store().Get(ctx, id)
}

// View is Get for the API: the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Registration, error) {
	return s.store().View(ctx, a, id)
}

// Create adds a registration; the database gives it the next application number.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Registration, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a registration's fields. The application number and the enquiry link never change.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Registration, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a registration. Its application number is not reused.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
