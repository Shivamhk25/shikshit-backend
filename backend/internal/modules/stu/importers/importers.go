// Package importers registers the student data that can be imported (ONB-11): students, then their yearly enrollments.
// Each entity maps file columns to the module's own Input, so an imported row passes exactly the rules a typed one does.
package importers

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/stu/enrollments"
	"github.com/pie360/backend/internal/modules/stu/students"
	"github.com/pie360/backend/internal/platform/importer"
	"github.com/pie360/backend/internal/platform/tenancy"
)

const domain = "students"

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Entities returns the importable student entities.
func Entities(db tenancy.Beginner) []importer.Entity {
	return []importer.Entity{Students(db), Enrollments(db)}
}

// Students imports students; a row is matched by its registration code.
func Students(db tenancy.Beginner) importer.Entity {
	return importer.Importable[students.Student, students.Input]{
		EntityKey: "students", EntityTitle: "Students", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "registrationCode", Label: "Registration code", Required: true, Kind: "text", Example: "R-0001"},
			{Key: "firstName", Label: "First name", Required: true, Kind: "text", Example: "Asha"},
			{Key: "middleName", Label: "Middle name", Kind: "text"},
			{Key: "lastName", Label: "Last name", Kind: "text", Example: "Rao"},
			{Key: "gender", Label: "Gender", Required: true, Kind: "choice", Allowed: students.Genders, Example: "female"},
			{Key: "dateOfBirth", Label: "Date of birth", Required: true, Kind: "date", Example: "2015-06-01"},
			{Key: "admissionDate", Label: "Admission date", Kind: "date"},
			{Key: "mobile", Label: "Mobile", Kind: "text", Hint: "The number the school messages first."},
			{Key: "parentEmail", Label: "Parent email", Kind: "text"},
			{Key: "studentEmail", Label: "Student email", Kind: "text"},
			{Key: "apaarId", Label: "APAAR ID", Kind: "text", Hint: "12 digits."},
			{Key: "status", Label: "Status", Kind: "choice", Allowed: students.Statuses, Hint: "Blank means active."},
			{Key: "remarks", Label: "Remarks", Kind: "text"},
		},
		Store: students.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) students.Input {
			return students.Input{
				RegistrationCode: c.Text("registrationCode"), FirstName: c.Text("firstName"), MiddleName: c.Opt("middleName"), LastName: c.Opt("lastName"),
				Gender: lower(c.Text("gender")), DateOfBirth: c.Date("dateOfBirth"), AdmissionDate: opt(c.Date("admissionDate")),
				Mobile: c.Opt("mobile"), ParentEmail: c.Opt("parentEmail"), StudentEmail: c.Opt("studentEmail"), ApaarID: c.Opt("apaarId"),
				Status: lower(c.Text("status")), Remarks: c.Opt("remarks"),
			}
		},
		KeyOf:     func(i students.Input) string { return lower(i.RegistrationCode) },
		KeyColumn: "registrationCode", FindWhere: "lower(registration_code) = $1",
		ToInput: func(s students.Student) students.Input {
			return students.Input{RegistrationCode: s.RegistrationCode, FirstName: s.FirstName, MiddleName: s.MiddleName, LastName: s.LastName, Gender: s.Gender,
				DateOfBirth: s.DateOfBirth, AdmissionDate: s.AdmissionDate, Mobile: s.Mobile, ParentEmail: s.ParentEmail, StudentEmail: s.StudentEmail,
				ApaarID: s.ApaarID, Status: s.Status, Remarks: s.Remarks}
		},
		ID:    func(s students.Student) string { return s.ID },
		Stamp: func(s students.Student) time.Time { return s.UpdatedAt },
	}
}

// Enrollments imports yearly enrollments; a row is identified by the student and the academic year.
func Enrollments(db tenancy.Beginner) importer.Entity {
	return importer.Importable[enrollments.Enrollment, enrollments.Input]{
		EntityKey: "student_enrollments", EntityTitle: "Student enrollments", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "student", Label: "Student", Required: true, Kind: "reference", Ref: "students", Hint: "Registration code.", Example: "R-0001"},
			{Key: "year", Label: "Academic year", Required: true, Kind: "reference", Ref: "academic_years", Hint: "Year code or name.", Example: "2026-27"},
			{Key: "class", Label: "Class", Required: true, Kind: "reference", Ref: "classes", Hint: "Class code or name.", Example: "V"},
			{Key: "section", Label: "Section", Required: true, Kind: "reference", Ref: "sections", Hint: "Must already be mapped to the class for that year.", Example: "A"},
			{Key: "rollNumber", Label: "Roll number", Kind: "text"},
			{Key: "enrollmentNumber", Label: "Enrollment number", Kind: "text"},
			{Key: "enrollmentDate", Label: "Enrollment date", Kind: "date"},
			{Key: "status", Label: "Status", Kind: "choice", Allowed: enrollments.Statuses, Hint: "Blank means enrolled."},
		},
		Store: enrollments.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) enrollments.Input {
			return enrollments.Input{
				StudentID: c.Ref(ctx, tx, "student", "students", "student"), YearID: c.Ref(ctx, tx, "year", "academic_years", "academic year"),
				ClassID: c.Ref(ctx, tx, "class", "classes", "class"), SectionID: c.Ref(ctx, tx, "section", "sections", "section"),
				RollNumber: c.Opt("rollNumber"), EnrollmentNumber: c.Opt("enrollmentNumber"), EnrollmentDate: opt(c.Date("enrollmentDate")),
				Status: lower(c.Text("status")),
			}
		},
		KeyOf:     func(i enrollments.Input) string { return i.StudentID + "|" + i.YearID },
		KeyColumn: "student", FindWhere: "(student_id::text || '|' || year_id::text) = $1",
		FieldColumn: map[string]string{"studentId": "student", "yearId": "year", "classId": "class", "sectionId": "section"},
		ToInput: func(e enrollments.Enrollment) enrollments.Input {
			return enrollments.Input{StudentID: e.StudentID, YearID: e.YearID, ClassID: e.ClassID, SectionID: e.SectionID, RollNumber: e.RollNumber,
				EnrollmentNumber: e.EnrollmentNumber, EnrollmentDate: e.EnrollmentDate, Status: e.Status}
		},
		ID:    func(e enrollments.Enrollment) string { return e.ID },
		Stamp: func(e enrollments.Enrollment) time.Time { return e.UpdatedAt },
	}
}

// Pack is the students setup pack (ONB-11): students first, then their enrollments. It is ready when at least one
// student exists and no active student lacks an enrollment in the current academic year.
func Pack() importer.Pack {
	return importer.Pack{
		Key: "students", Title: "Students", Domain: domain,
		Steps: []importer.PackStep{
			{Entity: "students", Title: "Students", Table: "students", Required: true},
			{Entity: "student_enrollments", Title: "Enrollments", Table: "student_enrollments", Requires: []string{"students"}},
		},
		Rules: []importer.PackRule{
			{Key: "has_students", Label: "At least one student exists", SQL: `SELECT EXISTS (SELECT 1 FROM students WHERE deleted_at IS NULL)`},
			{Key: "all_enrolled", Label: "Every active student is enrolled in the current academic year", SQL: `SELECT EXISTS (SELECT 1 FROM academic_years WHERE is_current AND deleted_at IS NULL)
				AND NOT EXISTS (
					SELECT 1 FROM students s
					WHERE s.deleted_at IS NULL AND s.status = 'active'
					AND NOT EXISTS (SELECT 1 FROM student_enrollments e JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id
						WHERE e.tenant_id = s.tenant_id AND e.student_id = s.id AND e.deleted_at IS NULL AND e.status = 'enrolled' AND y.is_current AND y.deleted_at IS NULL))`},
		},
	}
}
