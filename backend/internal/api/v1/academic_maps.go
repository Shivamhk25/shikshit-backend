package apiv1

import (
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/sections"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/subjects"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/terms"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// The handwritten half of the generated resource handlers (see scripts/gen_resources.py): converting between the
// API types and the domain types, and reading each resource's own list filters.

func apiDate(s string) openapi_types.Date {
	t, _ := time.Parse(crud.DateLayout, s) // the service only returns valid dates
	return openapi_types.Date{Time: t}
}

func domainDate(d openapi_types.Date) string { return d.Format(crud.DateLayout) }

func strPtr[T ~string](p *T) *string {
	if p == nil {
		return nil
	}
	s := string(*p)
	return &s
}

func str[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func apiDatePtr(s *string) *openapi_types.Date {
	if s == nil {
		return nil
	}
	d := apiDate(*s)
	return &d
}

func domainDatePtr(d *openapi_types.Date) *string {
	if d == nil {
		return nil
	}
	s := domainDate(*d)
	return &s
}

func uuidPtr(s *string) *openapi_types.UUID {
	if s == nil {
		return nil
	}
	u := mustUUID(*s)
	return &u
}

func uuidStrPtr(u *openapi_types.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func enumPtr[T ~string](s *string) *T {
	if s == nil {
		return nil
	}
	v := T(*s)
	return &v
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// ---- academic years

func toGenAcademicYear(y years.Year) gen.AcademicYear {
	return gen.AcademicYear{
		Id: mustUUID(y.ID), Code: y.Code, Name: y.Name, StartDate: apiDate(y.StartDate), EndDate: apiDate(y.EndDate),
		IsCurrent: y.IsCurrent, Remarks: y.Remarks, Status: gen.AcademicYearStatus(y.Status), CreatedAt: y.CreatedAt, UpdatedAt: y.UpdatedAt,
	}
}

func academicYearInput(b gen.AcademicYearInput) years.Input {
	return years.Input{
		Code: b.Code, Name: b.Name, StartDate: domainDate(b.StartDate), EndDate: domainDate(b.EndDate),
		IsCurrent: boolOr(b.IsCurrent, false), Remarks: b.Remarks, Status: str(b.Status),
	}
}

func academicYearFilter(p gen.ListAcademicYearsParams) (years.Filter, crud.FieldErrors) {
	var errs crud.FieldErrors
	f := years.Filter{Status: str(p.Status), IsCurrent: p.IsCurrent}
	oneOf(&errs, "status", f.Status, "active", "inactive")
	return f, errs
}

// ---- academic terms

func toGenAcademicTerm(t terms.Term) gen.AcademicTerm {
	return gen.AcademicTerm{
		Id: mustUUID(t.ID), YearId: mustUUID(t.YearID), YearName: t.YearName, Code: t.Code, Title: t.Title,
		StartDate: apiDate(t.StartDate), EndDate: apiDate(t.EndDate), Days: t.Days, Color: t.Color, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func academicTermInput(b gen.AcademicTermInput) terms.Input {
	return terms.Input{YearID: b.YearId.String(), Code: b.Code, Title: b.Title, StartDate: domainDate(b.StartDate), EndDate: domainDate(b.EndDate), Color: b.Color}
}

func academicTermFilter(p gen.ListAcademicTermsParams) (terms.Filter, crud.FieldErrors) {
	f := terms.Filter{}
	if p.YearId != nil {
		f.YearID = p.YearId.String()
	}
	return f, nil
}

// ---- classes

func toGenSchoolClass(c classes.Class) gen.SchoolClass {
	return gen.SchoolClass{
		Id: mustUUID(c.ID), Code: c.Code, Name: c.Name, DivisionId: uuidPtr(c.DivisionID), DivisionName: c.DivisionName, DisplayOrder: c.DisplayOrder, YoungestAgeMonths: c.YoungestAgeMonths,
		OldestAgeMonths: c.OldestAgeMonths, Description: c.Description, Status: gen.SchoolClassStatus(c.Status), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func schoolClassInput(b gen.SchoolClassInput) classes.Input {
	return classes.Input{
		Code: b.Code, Name: b.Name, DivisionID: uuidStrPtr(b.DivisionId), DisplayOrder: intOr(b.DisplayOrder, 0), YoungestAgeMonths: b.YoungestAgeMonths,
		OldestAgeMonths: b.OldestAgeMonths, Description: b.Description, Status: str(b.Status),
	}
}

func schoolClassFilter(p gen.ListSchoolClassesParams) (classes.Filter, crud.FieldErrors) {
	var errs crud.FieldErrors
	f := classes.Filter{Status: str(p.Status)}
	oneOf(&errs, "status", f.Status, "active", "inactive")
	return f, errs
}

// ---- sections

func toGenSection(s sections.Section) gen.Section {
	return gen.Section{
		Id: mustUUID(s.ID), Code: s.Code, Name: s.Name, ShortName: s.ShortName, Description: s.Description,
		Status: gen.SectionStatus(s.Status), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func sectionInput(b gen.SectionInput) sections.Input {
	return sections.Input{Code: b.Code, Name: b.Name, ShortName: b.ShortName, Description: b.Description, Status: str(b.Status)}
}

func sectionFilter(p gen.ListSectionsParams) (sections.Filter, crud.FieldErrors) {
	var errs crud.FieldErrors
	f := sections.Filter{Status: str(p.Status)}
	oneOf(&errs, "status", f.Status, "active", "inactive")
	return f, errs
}

// ---- subjects

func toGenSubject(s subjects.Subject) gen.Subject {
	return gen.Subject{
		Id: mustUUID(s.ID), Code: s.Code, Name: s.Name, ShortName: s.ShortName, Color: s.Color, Category: gen.SubjectCategory(s.Category), Area: gen.SubjectArea(s.Area),
		Scholastic: s.Scholastic, CoScholastic: s.CoScholastic, NonScholastic: s.NonScholastic, Credits: s.Credits,
		Description: s.Description, Status: gen.SubjectStatus(s.Status), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

func subjectInput(b gen.SubjectInput) subjects.Input {
	return subjects.Input{
		Code: b.Code, Name: b.Name, ShortName: b.ShortName, Color: b.Color, Category: string(b.Category), Area: string(b.Area),
		Scholastic: boolOr(b.Scholastic, false), CoScholastic: boolOr(b.CoScholastic, false), NonScholastic: boolOr(b.NonScholastic, false),
		Credits: b.Credits, Description: b.Description, Status: str(b.Status),
	}
}

func subjectFilter(p gen.ListSubjectsParams) (subjects.Filter, crud.FieldErrors) {
	var errs crud.FieldErrors
	f := subjects.Filter{Status: str(p.Status)}
	if p.Category != nil {
		f.Category = *p.Category
	}
	oneOf(&errs, "status", f.Status, "active", "inactive")
	return f, errs
}
