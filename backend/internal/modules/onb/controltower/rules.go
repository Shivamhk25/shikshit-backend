package controltower

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// NoSource is the note shown for a domain whose data does not exist yet. It reports not started, never a made-up number.
const NoSource = "No data source yet."

// min1 is a presence rule: at least one record, so Done is 0 or 1 of 1.
func min1(key, label string, n int) Check {
	if n > 0 {
		n = 1
	}
	return Check{Key: key, Label: label, Done: n, Total: 1}
}

// measureAll measures every domain that has a data source today. Others stay absent from the map and so count as
// "no source".
func measureAll(ctx context.Context, tx pgx.Tx) (map[string]Measure, error) {
	a, err := measureAcademic(ctx, tx)
	if err != nil {
		return nil, err
	}
	s, err := measureStudents(ctx, tx)
	if err != nil {
		return nil, err
	}
	return map[string]Measure{"academic": a, "students": s}, nil
}

// measureAcademic counts the academic structure: presence of the basics, and that every class is mapped to a section in
// the current year.
func measureAcademic(ctx context.Context, tx pgx.Tx) (Measure, error) {
	var years, current, terms, classes, sections, subjects, mappings, mappedClasses int
	err := tx.QueryRow(ctx, `
		WITH cy AS (SELECT id FROM academic_years WHERE is_current AND deleted_at IS NULL)
		SELECT
		  (SELECT count(*) FROM academic_years WHERE deleted_at IS NULL),
		  (SELECT count(*) FROM cy),
		  (SELECT count(*) FROM academic_terms WHERE deleted_at IS NULL AND year_id IN (SELECT id FROM cy)),
		  (SELECT count(*) FROM classes WHERE deleted_at IS NULL),
		  (SELECT count(*) FROM sections WHERE deleted_at IS NULL),
		  (SELECT count(*) FROM subjects WHERE deleted_at IS NULL),
		  (SELECT count(*) FROM class_section_mappings WHERE deleted_at IS NULL AND year_id IN (SELECT id FROM cy)),
		  (SELECT count(*) FROM classes c WHERE c.deleted_at IS NULL AND EXISTS (
		     SELECT 1 FROM class_section_mappings m WHERE m.class_id = c.id AND m.deleted_at IS NULL AND m.year_id IN (SELECT id FROM cy)))`).
		Scan(&years, &current, &terms, &classes, &sections, &subjects, &mappings, &mappedClasses)
	if err != nil {
		return Measure{}, err
	}
	return Measure{HasSource: true, Checks: []Check{
		min1("years", "An academic year exists", years),
		min1("current_year", "A current academic year is set", current),
		min1("terms", "The current year has terms", terms),
		min1("classes", "Classes exist", classes),
		min1("sections", "Sections exist", sections),
		min1("subjects", "Subjects exist", subjects),
		min1("mappings", "Sections are mapped to classes for the current year", mappings),
		{Key: "classes_mapped", Label: "Classes with a section in the current year", Done: mappedClasses, Total: classes},
	}}, nil
}

// measureStudents counts active students against the things each must have: an enrollment in the current year, a
// guardian, and an enrollment whose section is mapped to its class that year. Only counts leave this function.
func measureStudents(ctx context.Context, tx pgx.Tx) (Measure, error) {
	var students, enrolled, withGuardian, enrollments, mapped int
	err := tx.QueryRow(ctx, `
		WITH cy AS (SELECT id FROM academic_years WHERE is_current AND deleted_at IS NULL),
		     st AS (SELECT id FROM students WHERE deleted_at IS NULL AND status = 'active'),
		     en AS (SELECT student_id, class_id, section_id, year_id FROM student_enrollments
		            WHERE deleted_at IS NULL AND status = 'enrolled' AND year_id IN (SELECT id FROM cy) AND student_id IN (SELECT id FROM st))
		SELECT
		  (SELECT count(*) FROM st),
		  (SELECT count(*) FROM st WHERE id IN (SELECT student_id FROM en)),
		  (SELECT count(*) FROM st s WHERE EXISTS (
		     SELECT 1 FROM student_guardians sg JOIN guardians g ON g.id = sg.guardian_id AND g.deleted_at IS NULL
		     WHERE sg.student_id = s.id AND sg.deleted_at IS NULL)),
		  (SELECT count(*) FROM en),
		  (SELECT count(*) FROM en e WHERE EXISTS (
		     SELECT 1 FROM class_section_mappings m WHERE m.deleted_at IS NULL AND m.year_id = e.year_id AND m.class_id = e.class_id AND m.section_id = e.section_id))`).
		Scan(&students, &enrolled, &withGuardian, &enrollments, &mapped)
	if err != nil {
		return Measure{}, err
	}
	return Measure{HasSource: true, Checks: []Check{
		min1("students", "Students exist", students),
		{Key: "enrolled", Label: "Active students enrolled in the current year", Done: enrolled, Total: students},
		{Key: "guardians", Label: "Active students with a guardian", Done: withGuardian, Total: students},
		{Key: "enrollments_mapped", Label: "Enrollments in a section mapped to the class", Done: mapped, Total: enrollments},
	}}, nil
}
