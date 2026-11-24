// Package electives implements STU-04 Student electives: which elective subjects each student takes.
//
// A student has one choice per elective group and academic year: a set of subjects taken from the subjects the group
// offers to the student's class (and section) that year (subject_class_mappings.elective_group_id, ACA-31). The set is
// replaced as a whole and every change is kept in the history (who, when, before, after, and the change request behind
// it, STU-05).
//
// Rules, all checked inside the writing transaction:
//   - the student exists, is enrolled in a class for the year, and the caller may see that student (a class-section
//     scope narrows the students, see stuscope);
//   - the group is offered to the student's class and section that year, and every chosen subject is one of the
//     subjects it offers there, once;
//   - the number of subjects is within the group's minimum and maximum. When the group enforces its limits a choice
//     outside them is refused (400, field subjectIds, code below_minimum or above_maximum). When it does not enforce
//     them the limits are advisory: the choice is saved and the answer carries withinLimits=false and a warning;
//   - one live choice per student, group and year (409 choice_exists on a second create; PUT replaces).
//
// Electives are academic data, not sensitive personal data like transfers, so plain reads are not audited. Every write
// is.
package electives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/stuscope"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the choice does not exist in this institute, or the caller may not see its student.
var ErrNotFound = resource.ErrNotFound

// Statuses of a choice.
const (
	StatusChosen    = "chosen"
	StatusConfirmed = "confirmed"
)

// Statuses lists the allowed statuses.
var Statuses = []string{StatusChosen, StatusConfirmed}

// ConflictError means another record or the state of the choice stands in the way. The API answers 409 with Code as the
// problem code.
type ConflictError struct{ Code, Message string }

func (e ConflictError) Error() string { return "electives: " + e.Message }

// StudentRef names a student.
type StudentRef struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	RegistrationCode string `json:"registrationCode"`
}

// SubjectRef names a subject.
type SubjectRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// GroupRef is an elective group with the limits that apply to it.
type GroupRef struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Code          *string `json:"code"`
	MinSubjects   int     `json:"minSubjects"`
	MaxSubjects   int     `json:"maxSubjects"`
	EnforceLimits bool    `json:"enforceLimits"`
}

// Warning is a rule a saved choice does not meet because the group does not enforce its limits.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Choice is a student's elective choice as the API shows it.
type Choice struct {
	ID          string       `json:"id"`
	Student     StudentRef   `json:"student"`
	YearID      string       `json:"yearId"`
	YearName    string       `json:"yearName"`
	ClassID     *string      `json:"classId"`
	ClassName   *string      `json:"className"`
	SectionID   *string      `json:"sectionId"`
	SectionName *string      `json:"sectionName"`
	Group       GroupRef     `json:"group"`
	Subjects    []SubjectRef `json:"subjects"`
	// Offered is what the student may choose from: filled only when one choice is read.
	Offered      []SubjectRef `json:"offered"`
	Status       string       `json:"status"`
	WithinLimits bool         `json:"withinLimits"`
	Warnings     []Warning    `json:"warnings"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// Filter narrows a list.
type Filter struct{ YearID, ClassID, SectionID, GroupID, StudentID, Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"student": studentName, "class": className, "section": sectionName, "group": groupNameExpr, "status": t + ".status", "createdAt": t + ".created_at"}

// DefaultSort lists by student name.
const DefaultSort = "student"

const t = "student_elective_choices"

const studentName = `(SELECT btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')) FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`
const studentCode = `(SELECT s.registration_code FROM students s WHERE s.tenant_id = ` + t + `.tenant_id AND s.id = ` + t + `.student_id)`

// enrol is a value of the student's enrolment in the choice's year (their class and section that year).
func enrol(col, join string) string {
	return `(SELECT ` + col + ` FROM student_enrollments e ` + join + ` WHERE e.tenant_id = ` + t + `.tenant_id AND e.student_id = ` + t + `.student_id AND e.year_id = ` + t + `.year_id AND e.deleted_at IS NULL LIMIT 1)`
}

var (
	classID     = enrol("e.class_id", "")
	sectionID   = enrol("e.section_id", "")
	className   = enrol("c.name", "JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id")
	sectionName = enrol("sc.name", "JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id")
)

const groupNameExpr = `(SELECT g.name FROM elective_groups g WHERE g.tenant_id = ` + t + `.tenant_id AND g.id = ` + t + `.group_id)`

var columns = `id::text, student_id::text, ` + studentName + `, ` + studentCode + `, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = ` + t + `.tenant_id AND y.id = ` + t + `.year_id),
	` + classID + `::text, ` + className + `, ` + sectionID + `::text, ` + sectionName + `,
	group_id::text,
	(SELECT json_build_object('name', g.name, 'code', g.code, 'min', g.min_subjects, 'max', g.max_subjects, 'enforce', g.enforce_limits)
	   FROM elective_groups g WHERE g.tenant_id = ` + t + `.tenant_id AND g.id = ` + t + `.group_id),
	(SELECT COALESCE(json_agg(json_build_object('id', s.id, 'code', s.code, 'name', s.name) ORDER BY s.name, s.id), '[]'::json)
	   FROM student_elective_choice_subjects cs JOIN subjects s ON s.tenant_id = cs.tenant_id AND s.id = cs.subject_id
	  WHERE cs.tenant_id = ` + t + `.tenant_id AND cs.choice_id = ` + t + `.id),
	status, created_at, updated_at`

func scan(r resource.Scanner) (Choice, error) {
	var c Choice
	var group, subjects []byte
	err := r.Scan(&c.ID, &c.Student.ID, &c.Student.Name, &c.Student.RegistrationCode, &c.YearID, &c.YearName, &c.ClassID, &c.ClassName, &c.SectionID, &c.SectionName,
		&c.Group.ID, &group, &subjects, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	var g struct {
		Name    string  `json:"name"`
		Code    *string `json:"code"`
		Min     int     `json:"min"`
		Max     int     `json:"max"`
		Enforce bool    `json:"enforce"`
	}
	if err := json.Unmarshal(group, &g); err != nil {
		return c, fmt.Errorf("electives: group of choice %s: %w", c.ID, err)
	}
	c.Group.Name, c.Group.Code, c.Group.MinSubjects, c.Group.MaxSubjects, c.Group.EnforceLimits = g.Name, g.Code, g.Min, g.Max, g.Enforce
	if err := json.Unmarshal(subjects, &c.Subjects); err != nil {
		return c, fmt.Errorf("electives: subjects of choice %s: %w", c.ID, err)
	}
	c.Offered = []SubjectRef{}
	c.Warnings = Warnings(c.Group, len(c.Subjects))
	c.WithinLimits = len(c.Warnings) == 0
	return c, nil
}

// Warnings returns what a set of n subjects fails of the group's limits. Whether that refuses the choice or only
// warns is decided where it is written (enforce_limits); reading always reports it.
func Warnings(g GroupRef, n int) []Warning {
	out := []Warning{}
	if n < g.MinSubjects {
		out = append(out, Warning{"below_minimum", fmt.Sprintf("%s needs at least %d subject%s; %d chosen.", g.Name, g.MinSubjects, plural(g.MinSubjects), n)})
	}
	if n > g.MaxSubjects {
		out = append(out, Warning{"above_maximum", fmt.Sprintf("%s allows at most %d subject%s; %d chosen.", g.Name, g.MaxSubjects, plural(g.MaxSubjects), n)})
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

var spec = resource.Spec[Choice, struct{}]{
	Table: t, Action: "student_electives", ResourceType: "student_elective",
	Columns: columns, Scan: scan, ID: func(c Choice) string { return c.ID },
	SearchColumns: []string{studentName, studentCode},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads and writes student elective choices.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Choice, struct{}] {
	return resource.Store[Choice, struct{}]{DB: s.DB, Spec: spec}
}

// Scoped adds the caller's class-section limit, when there is one, to a query that already filters the table table on
// studentCol; args are the arguments so far and the new ones are appended. It returns the SQL to add (with a leading
// " AND ") and the grown arguments.
func Scoped(ctx context.Context, table, studentCol string, args []any) (string, []any) {
	c, ok := stuscope.Student(table, studentCol)(ctx)
	if !ok {
		return "", args
	}
	sql := c.SQL
	for _, a := range c.Args {
		args = append(args, a)
		sql = strings.Replace(sql, "?", fmt.Sprintf("$%d", len(args)), 1)
	}
	return " AND " + sql, args
}

// List returns one page of choices, narrowed to the students the caller may see.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Choice], error) {
	var conds []resource.Cond
	add := func(sql string, v string) {
		if v != "" {
			conds = append(conds, resource.Cond{SQL: sql, Args: []any{v}})
		}
	}
	add(t+".year_id::text = ?", f.YearID)
	add(t+".group_id::text = ?", f.GroupID)
	add(t+".student_id::text = ?", f.StudentID)
	add(t+".status = ?", f.Status)
	add(classID+"::text = ?", f.ClassID)
	add(sectionID+"::text = ?", f.SectionID)
	if c, ok := stuscope.Student(t, "student_id")(ctx); ok {
		conds = append(conds, c)
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one choice with the subjects it could be changed to (Offered). A choice of a student the caller may not
// see is not found.
func (s Service) Get(ctx context.Context, id string) (Choice, error) {
	var c Choice
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := LockChoice(ctx, tx, id, false); err != nil {
			return err
		}
		var err error
		if c, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		pl, err := placementOf(ctx, tx, c.Student.ID, c.YearID)
		var pfe crud.FieldErrors
		if errors.As(err, &pfe) {
			return nil // the student is no longer in a class that year: nothing is on offer
		}
		if err != nil {
			return err
		}
		off, err := LoadOffering(ctx, tx, pl, c.Group.ID)
		var fe crud.FieldErrors
		if errors.As(err, &fe) {
			return nil
		}
		if err != nil {
			return err
		}
		c.Offered = off.Subjects
		return nil
	})
	return c, err
}
