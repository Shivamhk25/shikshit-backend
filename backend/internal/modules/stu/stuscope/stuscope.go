// Package stuscope turns a caller's class-section limit (package scope) into the SQL conditions that narrow student
// data to the students enrolled in those class sections.
package stuscope

import (
	"context"
	"fmt"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/scope"
)

// enrolledIn is true when student <studentExpr> has a live, enrolled enrollment whose year, class and section match
// one of the class-section mappings in the bound list.
const enrolledIn = `EXISTS (SELECT 1 FROM student_enrollments se
	JOIN class_section_mappings csm ON csm.tenant_id = se.tenant_id AND csm.year_id = se.year_id AND csm.class_id = se.class_id AND csm.section_id = se.section_id AND csm.deleted_at IS NULL
	WHERE se.tenant_id = %[1]s.tenant_id AND se.student_id = %[2]s AND se.deleted_at IS NULL AND se.status = 'enrolled' AND csm.id::text = ANY(?))`

// Student returns a Restrict hook for a table whose rows belong to one student; table is the table name and
// studentCol the column holding the student id ("id" for students itself).
func Student(table, studentCol string) func(context.Context) (resource.Cond, bool) {
	sql := sprintf(enrolledIn, table, table+"."+studentCol)
	return func(ctx context.Context) (resource.Cond, bool) {
		ids, ok := scope.ClassSections(ctx)
		if !ok {
			return resource.Cond{}, false
		}
		return resource.Cond{SQL: sql, Args: []any{ids}}, true
	}
}

// Enrollment narrows enrollments to those that fall in the caller's class sections, whatever their status, so a
// teacher sees a withdrawn student of their section too.
func Enrollment(ctx context.Context) (resource.Cond, bool) {
	ids, ok := scope.ClassSections(ctx)
	if !ok {
		return resource.Cond{}, false
	}
	return resource.Cond{SQL: `EXISTS (SELECT 1 FROM class_section_mappings csm WHERE csm.tenant_id = student_enrollments.tenant_id
		AND csm.year_id = student_enrollments.year_id AND csm.class_id = student_enrollments.class_id AND csm.section_id = student_enrollments.section_id
		AND csm.deleted_at IS NULL AND csm.id::text = ANY(?))`, Args: []any{ids}}, true
}

// Guardian narrows guardians to those linked to a student in the caller's class sections.
func Guardian(ctx context.Context) (resource.Cond, bool) {
	ids, ok := scope.ClassSections(ctx)
	if !ok {
		return resource.Cond{}, false
	}
	return resource.Cond{SQL: `EXISTS (SELECT 1 FROM student_guardians sg WHERE sg.tenant_id = guardians.tenant_id AND sg.guardian_id = guardians.id
		AND sg.deleted_at IS NULL AND ` + sprintf(enrolledIn, "sg", "sg.student_id") + `)`, Args: []any{ids}}, true
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
