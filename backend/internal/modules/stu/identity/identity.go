// Package identity implements STU-01 Student Identity: find a student by name or code and read one identity card
// (codes, class and section, guardians, and the data an ID card prints). It reads students, enrollments, guardians and
// their links; it writes nothing except the audit trail.
//
// Personal data rules. Every search and every card view is audited, and the audit row never holds the text typed in a
// search, because that can be a child's name. A caller limited to class sections (a teacher) reaches only students
// enrolled in those sections, through the same condition the students list uses (package stuscope). Guardian phone
// numbers and emails, and the emergency number on the ID card, are masked (null) unless the caller passes
// contacts=true, which the API sets from the permission stu.student_contacts.view.
package identity

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/stuscope"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the student does not exist in this institute or is outside the caller's sections.
var ErrNotFound = resource.ErrNotFound

// Search limits.
const (
	MinQuery     = 2
	MaxQuery     = 100
	DefaultLimit = 10
	MaxLimit     = 25
)

// Match is one search result.
type Match struct {
	ID, RegistrationCode, FullName, Status string
	ClassName, SectionName                 *string
	HasPhoto                               bool
}

// Enrollment is the student's place in the current academic year.
type Enrollment struct {
	AcademicYear, ClassName, SectionName string
	RollNumber                           *string
}

// Guardian is a guardian linked to the student. Phone and Email are nil when contacts are masked.
type Guardian struct {
	ID, Name, Relation                   string
	IsPrimary, IsLegalCustody, CanPickup bool
	Phone, Email                         *string
}

// IDCard is what an ID card prints. EmergencyPhone is nil when contacts are masked.
type IDCard struct {
	InstituteName, StudentName, RegistrationCode, DateOfBirth string
	ClassSection, RollNumber, AcademicYear, ValidUntil        *string
	PrimaryGuardianName, EmergencyPhone                       *string
}

// Card is one student's identity card.
type Card struct {
	ID, RegistrationCode, FullName, Status, Gender, DateOfBirth string
	HasPhoto, ContactsVisible                                   bool
	Enrollment                                                  *Enrollment
	Guardians                                                   []Guardian
	IDCard                                                      IDCard
}

// Service reads identity cards.
type Service struct{ DB tenancy.Beginner }

// escapeLike makes user text match literally inside LIKE.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// current is the student's enrollment in the current academic year, when there is one.
const current = `LEFT JOIN LATERAL (
	SELECT y.name AS year_name, y.end_date, c.name AS class_name, sec.name AS section_name, e.roll_number
	FROM student_enrollments e
	JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id AND y.is_current AND y.deleted_at IS NULL
	JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id
	JOIN sections sec ON sec.tenant_id = e.tenant_id AND sec.id = e.section_id
	WHERE e.tenant_id = students.tenant_id AND e.student_id = students.id AND e.deleted_at IS NULL AND e.status = 'enrolled'
	LIMIT 1) cur ON true`

const fullName = `btrim(students.first_name || ' ' || COALESCE(students.middle_name || ' ', '') || COALESCE(students.last_name, ''))`

const hasPhoto = `EXISTS (SELECT 1 FROM files f WHERE f.tenant_id = students.tenant_id AND f.id = students.photo_file_id AND f.status = 'ready')`

// scopeSQL returns the extra WHERE condition (and its argument) for a caller limited to class sections. n is the
// number the placeholder takes.
func scopeSQL(ctx context.Context, n string) (string, []any) {
	c, ok := stuscope.Student("students", "id")(ctx)
	if !ok {
		return "", nil
	}
	return " AND " + strings.Replace(c.SQL, "?", n, 1), c.Args
}

// Search returns up to limit students whose name or registration code contains q, exact code first. The search is
// audited without q.
func (s Service) Search(ctx context.Context, a audit.Actor, q string, limit int) ([]Match, error) {
	q = strings.TrimSpace(q)
	var fe crud.FieldErrors
	if n := len([]rune(q)); n < MinQuery || n > MaxQuery {
		fe.Add("q", "invalid", "Type 2 to 100 characters of a name or code.")
		return nil, fe
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	out := []Match{}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := scopeSQL(ctx, "$4")
		params := append([]any{"%" + escapeLike(strings.ToLower(q)) + "%", strings.ToLower(q), limit}, args...)
		rows, err := tx.Query(ctx, `SELECT students.id::text, students.registration_code, `+fullName+`, students.status,
				cur.class_name, cur.section_name, `+hasPhoto+`
			FROM students `+current+`
			WHERE students.deleted_at IS NULL
			  AND (lower(students.registration_code) LIKE $1 OR lower(`+fullName+`) LIKE $1)`+extra+`
			ORDER BY (lower(students.registration_code) = $2) DESC, (lower(students.registration_code) LIKE $2 || '%') DESC,
			         lower(`+fullName+`), students.registration_code
			LIMIT $3`, params...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Match
			if err := rows.Scan(&m.ID, &m.RegistrationCode, &m.FullName, &m.Status, &m.ClassName, &m.SectionName, &m.HasPhoto); err != nil {
				return err
			}
			out = append(out, m)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_identity.search", ResourceType: "student",
			After: map[string]any{"searched": true, "results": len(out), "limit": limit}})
	})
	return out, err
}

// Get returns one identity card and audits the view. contacts says whether the caller may see phone numbers and emails.
func (s Service) Get(ctx context.Context, a audit.Actor, id string, contacts bool) (Card, error) {
	var c Card
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := scopeSQL(ctx, "$2")
		var (
			year, class, section, roll, endDate *string
			dob                                 string
		)
		err := tx.QueryRow(ctx, `SELECT students.id::text, students.registration_code, `+fullName+`, students.status, students.gender,
				students.date_of_birth::text, `+hasPhoto+`, cur.year_name, cur.class_name, cur.section_name, cur.roll_number, cur.end_date::text
			FROM students `+current+`
			WHERE students.id = $1::uuid AND students.deleted_at IS NULL`+extra, append([]any{id}, args...)...).
			Scan(&c.ID, &c.RegistrationCode, &c.FullName, &c.Status, &c.Gender, &dob, &c.HasPhoto, &year, &class, &section, &roll, &endDate)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		c.ContactsVisible = contacts
		c.Guardians = []Guardian{}
		if year != nil && class != nil && section != nil {
			c.Enrollment = &Enrollment{AcademicYear: *year, ClassName: *class, SectionName: *section, RollNumber: roll}
		}
		rows, err := tx.Query(ctx, `SELECT g.id::text, g.name, sg.relation, sg.is_primary, sg.is_legal_custody, sg.can_pickup, g.phone, g.email
			FROM student_guardians sg JOIN guardians g ON g.tenant_id = sg.tenant_id AND g.id = sg.guardian_id AND g.deleted_at IS NULL
			WHERE sg.student_id = $1::uuid AND sg.deleted_at IS NULL
			ORDER BY sg.is_primary DESC, lower(g.name), g.id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g Guardian
			var phone string
			if err := rows.Scan(&g.ID, &g.Name, &g.Relation, &g.IsPrimary, &g.IsLegalCustody, &g.CanPickup, &phone, &g.Email); err != nil {
				return err
			}
			if contacts {
				g.Phone = &phone
			} else {
				g.Email = nil
			}
			c.Guardians = append(c.Guardians, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		c.DateOfBirth = dob
		var institute string
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id = app_current_tenant()`).Scan(&institute); err != nil {
			return err
		}
		c.IDCard = IDCard{InstituteName: institute, StudentName: c.FullName, RegistrationCode: c.RegistrationCode, DateOfBirth: dob, AcademicYear: year, ValidUntil: endDate, RollNumber: roll}
		if class != nil && section != nil {
			cs := *class + " - " + *section
			c.IDCard.ClassSection = &cs
		}
		if len(c.Guardians) > 0 && c.Guardians[0].IsPrimary {
			name := c.Guardians[0].Name
			c.IDCard.PrimaryGuardianName = &name
			c.IDCard.EmergencyPhone = c.Guardians[0].Phone
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_identity.view", ResourceType: "student", ResourceID: id,
			After: map[string]any{"contactsShown": contacts}})
	})
	return c, err
}
