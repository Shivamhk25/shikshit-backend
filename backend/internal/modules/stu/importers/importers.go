// Package importers registers the student data that can be imported (ONB-11): students, then their yearly enrollments.
// Each entity maps file columns to the module's own Input, so an imported row passes exactly the rules a typed one does.
package importers

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/stu/addresses"
	"github.com/pie360/backend/internal/modules/stu/enrollments"
	"github.com/pie360/backend/internal/modules/stu/guardians"
	"github.com/pie360/backend/internal/modules/stu/links"
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
	return []importer.Entity{Students(db), Enrollments(db), Guardians(db), StudentGuardians(db), Addresses(db)}
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

// Guardians imports parents and guardians; a row is identified by phone number and name, because two people can share
// a phone.
func Guardians(db tenancy.Beginner) importer.Entity {
	return importer.Importable[guardians.Guardian, guardians.Input]{
		EntityKey: "guardians", EntityTitle: "Guardians", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Meera Rao"},
			{Key: "phone", Label: "Phone", Required: true, Kind: "text", Example: "+91 98765 43210"},
			{Key: "email", Label: "Email", Kind: "text"},
		},
		Store: guardians.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) guardians.Input {
			return guardians.Input{Name: c.Text("name"), Phone: c.Text("phone"), Email: c.Opt("email")}
		},
		KeyOf:     func(i guardians.Input) string { return strings.TrimSpace(i.Phone) + "|" + lower(i.Name) },
		KeyColumn: "phone", FindWhere: "(phone || '|' || lower(btrim(name))) = $1",
		ToInput: func(g guardians.Guardian) guardians.Input {
			return guardians.Input{Name: g.Name, Phone: g.Phone, Email: g.Email}
		},
		ID:    func(g guardians.Guardian) string { return g.ID },
		Stamp: func(g guardians.Guardian) time.Time { return g.UpdatedAt },
	}
}

// StudentGuardians imports the links between students and guardians. The guardian is found by phone number, so
// import guardians first; a phone shared by two guardians is reported as ambiguous.
func StudentGuardians(db tenancy.Beginner) importer.Entity {
	return importer.Importable[links.Link, links.Input]{
		EntityKey: "student_guardians", EntityTitle: "Student guardians", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "student", Label: "Student", Required: true, Kind: "reference", Ref: "students", Hint: "Registration code.", Example: "R-0001"},
			{Key: "guardian", Label: "Guardian phone", Required: true, Kind: "reference", Ref: "guardians", Hint: "The phone number the guardian was imported with.", Example: "+91 98765 43210"},
			{Key: "relation", Label: "Relation", Required: true, Kind: "choice", Allowed: links.Relations, Example: "mother"},
			{Key: "isPrimary", Label: "Primary", Kind: "boolean", Hint: "A student has at most one primary guardian. Blank means no."},
			{Key: "isLegalCustody", Label: "Legal custody", Kind: "boolean", Hint: "Blank means no."},
			{Key: "canPickup", Label: "May collect the child", Kind: "boolean", Hint: "Blank means yes."},
		},
		Store: links.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) links.Input {
			return links.Input{StudentID: c.Ref(ctx, tx, "student", "students", "student"), GuardianID: c.Ref(ctx, tx, "guardian", "guardians", "guardian"),
				Relation: lower(c.Text("relation")), IsPrimary: c.Bool("isPrimary", false), IsLegalCustody: c.Bool("isLegalCustody", false), CanPickup: c.Bool("canPickup", true)}
		},
		KeyOf:     func(i links.Input) string { return i.StudentID + "|" + i.GuardianID },
		KeyColumn: "student", FindWhere: "(student_id::text || '|' || guardian_id::text) = $1",
		FieldColumn: map[string]string{"studentId": "student", "guardianId": "guardian"},
		ToInput: func(l links.Link) links.Input {
			return links.Input{StudentID: l.StudentID, GuardianID: l.GuardianID, Relation: l.Relation, IsPrimary: l.IsPrimary, IsLegalCustody: l.IsLegalCustody, CanPickup: l.CanPickup}
		},
		ID:    func(l links.Link) string { return l.ID },
		Stamp: func(l links.Link) time.Time { return l.UpdatedAt },
	}
}

// Addresses imports student addresses; a row is identified by the student and the kind of address.
func Addresses(db tenancy.Beginner) importer.Entity {
	return importer.Importable[addresses.Address, addresses.Input]{
		EntityKey: "student_addresses", EntityTitle: "Student addresses", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "student", Label: "Student", Required: true, Kind: "reference", Ref: "students", Hint: "Registration code.", Example: "R-0001"},
			{Key: "kind", Label: "Kind", Required: true, Kind: "choice", Allowed: addresses.Kinds, Example: "home"},
			{Key: "line1", Label: "Address line 1", Required: true, Kind: "text"},
			{Key: "line2", Label: "Address line 2", Kind: "text"},
			{Key: "city", Label: "City", Required: true, Kind: "text"},
			{Key: "state", Label: "State", Kind: "text"},
			{Key: "postcode", Label: "Postcode", Kind: "text"},
			{Key: "country", Label: "Country", Kind: "text", Hint: "Blank means India."},
		},
		Store: addresses.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) addresses.Input {
			return addresses.Input{StudentID: c.Ref(ctx, tx, "student", "students", "student"), Kind: lower(c.Text("kind")), Line1: c.Text("line1"), Line2: c.Opt("line2"),
				City: c.Text("city"), State: c.Opt("state"), Postcode: c.Opt("postcode"), Country: c.Text("country")}
		},
		KeyOf:     func(i addresses.Input) string { return i.StudentID + "|" + lower(i.Kind) },
		KeyColumn: "student", FindWhere: "(student_id::text || '|' || kind) = $1",
		FieldColumn: map[string]string{"studentId": "student"},
		ToInput: func(a addresses.Address) addresses.Input {
			return addresses.Input{StudentID: a.StudentID, Kind: a.Kind, Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, Postcode: a.Postcode, Country: a.Country}
		},
		ID:    func(a addresses.Address) string { return a.ID },
		Stamp: func(a addresses.Address) time.Time { return a.UpdatedAt },
	}
}

// Pack is the students setup pack (ONB-11): students first, then their enrollments, guardians and addresses. It is ready when at least one
// student exists and no active student lacks an enrollment in the current academic year.
func Pack() importer.Pack {
	return importer.Pack{
		Key: "students", Title: "Students", Domain: domain,
		Steps: []importer.PackStep{
			{Entity: "students", Title: "Students", Table: "students", Required: true},
			{Entity: "student_enrollments", Title: "Enrollments", Table: "student_enrollments", Requires: []string{"students"}},
			{Entity: "guardians", Title: "Guardians", Table: "guardians"},
			{Entity: "student_guardians", Title: "Student guardians", Table: "student_guardians", Requires: []string{"students", "guardians"}},
			{Entity: "student_addresses", Title: "Addresses", Table: "student_addresses", Requires: []string{"students"}},
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
