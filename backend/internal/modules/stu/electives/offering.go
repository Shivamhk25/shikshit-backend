package electives

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/scope"
)

// Placement is where a student sits in one academic year: the enrolment that decides which subjects are on offer.
type Placement struct {
	YearID, ClassID, SectionID, ClassName, SectionName string
}

// Offering is what one elective group offers to one class section in one year.
type Offering struct {
	Group     GroupRef
	Placement Placement
	// Subjects are the subjects the group offers there, by name.
	Subjects []SubjectRef
}

var india = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return l
	}
	return time.FixedZone("IST", 5*3600+1800)
}()

// Today is the date in India, in the form the API uses.
func Today(now time.Time) string { return now.In(india).Format(crud.DateLayout) }

// ChoiceRow is the identity of a live choice.
type ChoiceRow struct {
	ID, StudentID, YearID, GroupID, Status string
}

// LockChoice returns a live choice the caller may see, optionally taking its row lock so changes to one choice run one
// at a time. A choice of a student outside the caller's class sections is not found (ErrNotFound).
func LockChoice(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (ChoiceRow, error) {
	var r ChoiceRow
	args := []any{id}
	scope, args := Scoped(ctx, t, "student_id", args)
	sql := `SELECT id::text, student_id::text, year_id::text, group_id::text, status FROM student_elective_choices WHERE id::text = $1 AND deleted_at IS NULL` + scope
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	err := tx.QueryRow(ctx, sql, args...).Scan(&r.ID, &r.StudentID, &r.YearID, &r.GroupID, &r.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// studentVisible adds a not_found error on field unless the student exists and the caller's class-section limit, if any,
// covers them. Out-of-scope students look exactly like missing ones.
func studentVisible(ctx context.Context, tx pgx.Tx, fe *crud.FieldErrors, field, studentID string) error {
	if !refs.IsUUID(studentID) {
		return nil
	}
	args := []any{studentID}
	scope, args := Scoped(ctx, "students", "id", args)
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM students WHERE id::text = $1 AND deleted_at IS NULL`+scope+`)`, args...).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		fe.Add(field, "not_found", "This student was not found.")
	}
	return nil
}

// placementOf returns the student's live, enrolled placement in the year; a field error studentId "not_enrolled" if none.
func placementOf(ctx context.Context, tx pgx.Tx, studentID, yearID string) (Placement, error) {
	var p Placement
	err := tx.QueryRow(ctx, `SELECT e.year_id::text, e.class_id::text, e.section_id::text, c.name, sc.name
		FROM student_enrollments e
		JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id
		JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id
		WHERE e.student_id::text = $1 AND e.year_id::text = $2 AND e.status = 'enrolled' AND e.deleted_at IS NULL`, studentID, yearID).
		Scan(&p.YearID, &p.ClassID, &p.SectionID, &p.ClassName, &p.SectionName)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, crud.FieldErrors{{Field: "studentId", Code: "not_enrolled", Message: "This student is not enrolled in a class for this academic year."}}
	}
	return p, err
}

// LoadOffering reads the group and the subjects it offers to the placement's class and section. A mapping of a subject
// with a section applies to that section only and overrides the class-wide mapping of the same subject, so a subject is
// on offer in the group when the mapping that applies to the section names it. Errors are field errors on groupId:
// not_found (no such group), inactive, or not_offered (the group has no subject for this class section that year).
func LoadOffering(ctx context.Context, tx pgx.Tx, pl Placement, groupID string) (Offering, error) {
	off := Offering{Placement: pl, Group: GroupRef{ID: groupID}}
	var status string
	err := tx.QueryRow(ctx, `SELECT name, code, min_subjects, max_subjects, enforce_limits, status FROM elective_groups WHERE id::text = $1 AND deleted_at IS NULL`, groupID).
		Scan(&off.Group.Name, &off.Group.Code, &off.Group.MinSubjects, &off.Group.MaxSubjects, &off.Group.EnforceLimits, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return off, crud.FieldErrors{{Field: "groupId", Code: "not_found", Message: "This elective group does not exist."}}
	}
	if err != nil {
		return off, err
	}
	if status != "active" {
		return off, crud.FieldErrors{{Field: "groupId", Code: "inactive", Message: off.Group.Name + " is not in use."}}
	}
	rows, err := tx.Query(ctx, `WITH eff AS (
			SELECT DISTINCT ON (m.subject_id) m.subject_id, m.elective_group_id
			FROM subject_class_mappings m
			WHERE m.year_id::text = $1 AND m.class_id::text = $2 AND m.deleted_at IS NULL AND (m.section_id IS NULL OR m.section_id::text = $3)
			ORDER BY m.subject_id, (m.section_id IS NULL)
		)
		SELECT s.id::text, s.code, s.name FROM eff JOIN subjects s ON s.id = eff.subject_id AND s.deleted_at IS NULL
		WHERE eff.elective_group_id::text = $4 ORDER BY s.name, s.id`, pl.YearID, pl.ClassID, pl.SectionID, groupID)
	if err != nil {
		return off, err
	}
	defer rows.Close()
	off.Subjects = []SubjectRef{}
	for rows.Next() {
		var s SubjectRef
		if err := rows.Scan(&s.ID, &s.Code, &s.Name); err != nil {
			return off, err
		}
		off.Subjects = append(off.Subjects, s)
	}
	if err := rows.Err(); err != nil {
		return off, err
	}
	if len(off.Subjects) == 0 {
		return off, crud.FieldErrors{{Field: "groupId", Code: "not_offered", Message: fmt.Sprintf("%s is not offered to %s %s this year.", off.Group.Name, pl.ClassName, pl.SectionName)}}
	}
	return off, nil
}

// Has reports whether the group offers the subject there.
func (o Offering) Has(subjectID string) bool {
	for _, s := range o.Subjects {
		if s.ID == subjectID {
			return true
		}
	}
	return false
}

// Check validates a set of subject ids against the offering and returns the cleaned ids (trimmed, lower case, in the
// order given) and the warnings of a set that is outside advisory limits. field is the field the errors name. It refuses
// an empty set, an id that is not a UUID, a repeated subject, a subject the group does not offer to this class section,
// and, when the group enforces its limits, too few or too many subjects.
func (o Offering) Check(field string, ids []string) (clean []string, warnings []Warning, fe crud.FieldErrors) {
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case !refs.IsUUID(id):
			fe.Add(field, "invalid", "Choose subjects from the list.")
			continue
		case seen[id]:
			fe.Add(field, "duplicate", "A subject can be chosen only once.")
			continue
		case !o.Has(id):
			fe.Add(field, "not_in_group", fmt.Sprintf("A chosen subject is not part of %s for %s %s this year.", o.Group.Name, o.Placement.ClassName, o.Placement.SectionName))
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(ids) == 0 {
		fe.Add(field, "required", "Choose at least one subject. To remove the choice, delete it.")
	}
	if len(fe) > 0 {
		return clean, nil, fe
	}
	warnings = Warnings(o.Group, len(clean))
	if o.Group.EnforceLimits && len(warnings) > 0 {
		for _, w := range warnings {
			fe.Add(field, w.Code, w.Message)
		}
		return clean, nil, fe
	}
	return clean, warnings, nil
}

// InScope adds a not_found error on field unless the student is one the caller may see (see studentVisible); it is the
// check every write makes before touching a student's choices.
func InScope(ctx context.Context, tx pgx.Tx, fe *crud.FieldErrors, field, studentID string) error {
	return studentVisible(ctx, tx, fe, field, studentID)
}

// sectionInScope is true when the class section exists and the caller has no class-section limit or the section is one
// of theirs. It is used where a whole class section is addressed at once (roster, bulk save).
func sectionInScope(ctx context.Context, tx pgx.Tx, yearID, classID, sectionID string) (bool, error) {
	var mapping string
	err := tx.QueryRow(ctx, `SELECT id::text FROM class_section_mappings WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL`,
		yearID, classID, sectionID).Scan(&mapping)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ids, limited := scope.ClassSections(ctx)
	if !limited {
		return true, nil
	}
	for _, id := range ids {
		if id == mapping {
			return true, nil
		}
	}
	return false, nil
}
