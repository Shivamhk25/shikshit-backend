package importers

import "github.com/pie360/backend/internal/platform/importer"

// Packs returns the setup packs of the academic domain, by key.
func Packs() map[string]importer.Pack {
	return map[string]importer.Pack{"academic": Academic()}
}

// Academic is the academic structure pack (ONB-10). The order follows how a school is set up. Only years, classes,
// sections and subjects must have data for the pack to be ready; the other steps are recommended. A term or a holiday
// needs its year first. Class-section and subject-class mappings do not exist yet, so the rule that every class has a
// section and subjects stays pending and the pack is not reported ready until it can be checked.
func Academic() importer.Pack {
	return importer.Pack{
		Key: "academic", Title: "Academic structure", Domain: domain,
		Steps: []importer.PackStep{
			{Entity: "academic_years", Title: "Academic years", Table: "academic_years", Required: true},
			{Entity: "academic_terms", Title: "Terms", Table: "academic_terms", Requires: []string{"academic_years"}},
			{Entity: "holidays", Title: "Holidays", Table: "holidays", Requires: []string{"academic_years"}},
			{Entity: "campuses", Title: "Campuses", Table: "campuses"},
			{Entity: "school_divisions", Title: "School divisions", Table: "school_divisions"},
			{Entity: "classes", Title: "Classes", Table: "classes", Required: true},
			{Entity: "sections", Title: "Sections", Table: "sections", Required: true},
			{Entity: "subjects", Title: "Subjects", Table: "subjects", Required: true},
			{Entity: "rooms", Title: "Rooms", Table: "rooms"},
		},
		Rules: []importer.PackRule{
			{Key: "current_year", Label: "A current academic year is set", SQL: `SELECT EXISTS (SELECT 1 FROM academic_years WHERE is_current AND deleted_at IS NULL)`},
			{Key: "has_classes", Label: "At least one class exists", SQL: `SELECT EXISTS (SELECT 1 FROM classes WHERE deleted_at IS NULL)`},
			{Key: "has_sections", Label: "At least one section exists", SQL: `SELECT EXISTS (SELECT 1 FROM sections WHERE deleted_at IS NULL)`},
			{Key: "has_subjects", Label: "At least one subject exists", SQL: `SELECT EXISTS (SELECT 1 FROM subjects WHERE deleted_at IS NULL)`},
			{Key: "class_mappings", Label: "Every class has a section and mapped subjects", Pending: "Checked once class-section and subject-class mappings can be set up."},
		},
	}
}
