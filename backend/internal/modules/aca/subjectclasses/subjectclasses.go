// Package subjectclasses implements ACA-31 Subject Class Mappings: which subjects a class studies in an academic year.
package subjectclasses

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/yearcheck"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the mapping does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// SubjectClass is a mapping as the API shows it. A nil section means the whole class.
type SubjectClass struct {
	ID             string  `json:"id"`
	YearID         string  `json:"yearId"`
	YearName       string  `json:"yearName"`
	ClassID        string  `json:"classId"`
	ClassCode      string  `json:"classCode"`
	ClassName      string  `json:"className"`
	SubjectID      string  `json:"subjectId"`
	SubjectCode    string  `json:"subjectCode"`
	SubjectName    string  `json:"subjectName"`
	SubjectArea    string  `json:"subjectArea"`
	SectionID      *string `json:"sectionId"`
	SectionName    *string `json:"sectionName"`
	PeriodsPerWeek *int    `json:"periodsPerWeek"`
	IsElective     bool    `json:"isElective"`
	// ElectiveGroupID names the elective group (ACA-10) this subject is offered in; only an elective mapping has one.
	ElectiveGroupID   *string   `json:"electiveGroupId"`
	ElectiveGroupName *string   `json:"electiveGroupName"`
	IsExamined        bool      `json:"isExamined"`
	IsAssessed        bool      `json:"isAssessed"`
	IsGradeOnly       bool      `json:"isGradeOnly"`
	HasPractical      bool      `json:"hasPractical"`
	OnReportCard      bool      `json:"onReportCard"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	YearID          string
	ClassID         string
	SubjectID       string
	SectionID       *string
	PeriodsPerWeek  *int
	IsElective      bool
	ElectiveGroupID *string
	IsExamined      bool
	IsAssessed      bool
	IsGradeOnly     bool
	HasPractical    bool
	OnReportCard    bool
}

// Filter narrows a list.
type Filter struct{ YearID, ClassID, SubjectID, SectionID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"className": "class_name", "subjectName": "subject_name", "periodsPerWeek": "periods_per_week", "createdAt": "created_at"}

// DefaultSort lists classes in name order, then their subjects.
const DefaultSort = "className,subjectName"

const columns = `id::text, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = subject_class_mappings.tenant_id AND y.id = subject_class_mappings.year_id) AS year_name,
	class_id::text,
	(SELECT c.code FROM classes c WHERE c.tenant_id = subject_class_mappings.tenant_id AND c.id = subject_class_mappings.class_id) AS class_code,
	(SELECT c.name FROM classes c WHERE c.tenant_id = subject_class_mappings.tenant_id AND c.id = subject_class_mappings.class_id) AS class_name,
	subject_id::text,
	(SELECT s.code FROM subjects s WHERE s.tenant_id = subject_class_mappings.tenant_id AND s.id = subject_class_mappings.subject_id) AS subject_code,
	(SELECT s.name FROM subjects s WHERE s.tenant_id = subject_class_mappings.tenant_id AND s.id = subject_class_mappings.subject_id) AS subject_name,
	(SELECT s.area FROM subjects s WHERE s.tenant_id = subject_class_mappings.tenant_id AND s.id = subject_class_mappings.subject_id) AS subject_area,
	section_id::text,
	(SELECT s.name FROM sections s WHERE s.tenant_id = subject_class_mappings.tenant_id AND s.id = subject_class_mappings.section_id) AS section_name,
	periods_per_week, is_elective, elective_group_id::text,
	(SELECT g.name FROM elective_groups g WHERE g.tenant_id = subject_class_mappings.tenant_id AND g.id = subject_class_mappings.elective_group_id) AS elective_group_name,
	is_examined, is_assessed, is_grade_only, has_practical, on_report_card, created_at, updated_at`

func scan(r resource.Scanner) (SubjectClass, error) {
	var m SubjectClass
	err := r.Scan(&m.ID, &m.YearID, &m.YearName, &m.ClassID, &m.ClassCode, &m.ClassName, &m.SubjectID, &m.SubjectCode, &m.SubjectName, &m.SubjectArea, &m.SectionID, &m.SectionName,
		&m.PeriodsPerWeek, &m.IsElective, &m.ElectiveGroupID, &m.ElectiveGroupName, &m.IsExamined, &m.IsAssessed, &m.IsGradeOnly, &m.HasPractical, &m.OnReportCard, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// Normalise trims and validates one mapping on its own; rules that need other records are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.YearID, in.ClassID, in.SubjectID = strings.TrimSpace(in.YearID), strings.TrimSpace(in.ClassID), strings.TrimSpace(in.SubjectID)
	in.SectionID, in.ElectiveGroupID = crud.TrimPtr(in.SectionID), crud.TrimPtr(in.ElectiveGroupID)
	refs.Require(&errs, "yearId", "an academic year", in.YearID)
	refs.Require(&errs, "classId", "a class", in.ClassID)
	refs.Require(&errs, "subjectId", "a subject", in.SubjectID)
	if in.SectionID != nil && !refs.IsUUID(*in.SectionID) {
		errs.Add("sectionId", "invalid", "Choose a section from the list.")
	}
	if in.ElectiveGroupID != nil {
		if !refs.IsUUID(*in.ElectiveGroupID) {
			errs.Add("electiveGroupId", "invalid", "Choose an elective group from the list.")
		} else if !in.IsElective {
			errs.Add("electiveGroupId", "not_elective", "Only an elective subject can belong to an elective group. Mark the subject as elective, or leave the group empty.")
		}
	}
	if in.PeriodsPerWeek != nil && (*in.PeriodsPerWeek < 0 || *in.PeriodsPerWeek > 60) {
		errs.Add("periodsPerWeek", "out_of_range", "Periods a week must be between 0 and 60.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[SubjectClass, Input]{
	Table: "subject_class_mappings", Action: "subject_class_mappings", ResourceType: "subject_class_mapping",
	Columns: columns, Scan: scan, ID: func(m SubjectClass) string { return m.ID },
	WriteColumns: []string{"year_id", "class_id", "subject_id", "section_id", "periods_per_week", "is_elective", "elective_group_id", "is_examined", "is_assessed", "is_grade_only", "has_practical", "on_report_card"},
	Args: func(in Input) []any {
		return []any{in.YearID, in.ClassID, in.SubjectID, in.SectionID, in.PeriodsPerWeek, in.IsElective, in.ElectiveGroupID, in.IsExamined, in.IsAssessed, in.IsGradeOnly, in.HasPractical, in.OnReportCard}
	},
	SearchColumns: []string{
		"(SELECT c.name FROM classes c WHERE c.tenant_id = subject_class_mappings.tenant_id AND c.id = subject_class_mappings.class_id)",
		"(SELECT s.name FROM subjects s WHERE s.tenant_id = subject_class_mappings.tenant_id AND s.id = subject_class_mappings.subject_id)",
	},
	Sorts: SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		if _, _, err := yearcheck.Dates(ctx, tx, in.YearID); err != nil {
			return err
		}
		for _, r := range []struct{ field, table, id string }{{"classId", "classes", in.ClassID}, {"subjectId", "subjects", in.SubjectID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		if in.ElectiveGroupID != nil && refs.IsUUID(*in.ElectiveGroupID) {
			if err := refs.Live(ctx, tx, &fe, "electiveGroupId", "elective_groups", *in.ElectiveGroupID); err != nil {
				return err
			}
		}
		if in.SectionID != nil && len(fe) == 0 {
			// a subject can be set for one section only when that section is part of the class that year
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM class_section_mappings
				WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL`, in.YearID, in.ClassID, *in.SectionID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				fe.Add("sectionId", "not_in_class", "This section is not mapped to this class for this year. Map it first, or leave the section empty for the whole class.")
			}
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"subject_class_mappings_uq":                        {Field: "subjectId", Code: "duplicate", Message: "This subject is already mapped to this class (and section) for this year."},
		"subject_class_mappings_tenant_id_year_id_fkey":    {Field: "yearId", Code: "not_found", Message: "This academic year does not exist."},
		"subject_class_mappings_tenant_id_class_id_fkey":   {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"subject_class_mappings_tenant_id_subject_id_fkey": {Field: "subjectId", Code: "not_found", Message: "This subject does not exist."},
		"subject_class_mappings_elective_group_fk":         {Field: "electiveGroupId", Code: "not_found", Message: "This elective group does not exist."},
		"subject_class_mappings_group_needs_elective_ck":   {Field: "electiveGroupId", Code: "not_elective", Message: "Only an elective subject can belong to an elective group."},
		"subject_class_mappings_tenant_id_section_id_fkey": {Field: "sectionId", Code: "not_found", Message: "This section does not exist."},
	},
}

// Service reads and writes subject-class mappings.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[SubjectClass, Input] { return s.store() }

func (s Service) store() resource.Store[SubjectClass, Input] {
	return resource.Store[SubjectClass, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of mappings.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[SubjectClass], error) {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"year_id", f.YearID}, {"class_id", f.ClassID}, {"subject_id", f.SubjectID}, {"section_id", f.SectionID}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + "::text = ?", Args: []any{c.v}})
		}
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one mapping.
func (s Service) Get(ctx context.Context, id string) (SubjectClass, error) {
	return s.store().Get(ctx, id)
}

// Create adds a mapping.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (SubjectClass, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a mapping's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (SubjectClass, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a mapping.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
