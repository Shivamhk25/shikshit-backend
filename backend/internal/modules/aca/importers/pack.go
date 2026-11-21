package importers

import "github.com/pie360/backend/internal/platform/importer"

// Packs returns the setup packs of the academic domain, by key.
func Packs() map[string]importer.Pack {
	return map[string]importer.Pack{"academic": Academic()}
}

// Academic is the academic structure pack (ONB-10). The order follows how a school is set up. Only years, classes,
// sections and subjects must have data for the pack to be ready; the other steps are recommended. A term or a holiday
// needs its year first, and the mapping steps need the masters they join. The pack is ready when the four required
// masters exist and every active class has a section and a subject mapped in the current year.
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
			{Entity: "class_section_mappings", Title: "Class sections", Table: "class_section_mappings", Requires: []string{"academic_years", "classes", "sections"}},
			{Entity: "subject_class_mappings", Title: "Class subjects", Table: "subject_class_mappings", Requires: []string{"academic_years", "classes", "subjects"}},
			{Entity: "textbooks", Title: "Textbooks", Table: "textbooks", Requires: []string{"classes", "subjects"}},
			{Entity: "textbook_chapters", Title: "Textbook chapters", Table: "textbook_chapters", Requires: []string{"textbooks"}},
		},
		Rules: []importer.PackRule{
			{Key: "current_year", Label: "A current academic year is set", SQL: `SELECT EXISTS (SELECT 1 FROM academic_years WHERE is_current AND deleted_at IS NULL)`},
			{Key: "has_classes", Label: "At least one class exists", SQL: `SELECT EXISTS (SELECT 1 FROM classes WHERE deleted_at IS NULL)`},
			{Key: "has_sections", Label: "At least one section exists", SQL: `SELECT EXISTS (SELECT 1 FROM sections WHERE deleted_at IS NULL)`},
			{Key: "has_subjects", Label: "At least one subject exists", SQL: `SELECT EXISTS (SELECT 1 FROM subjects WHERE deleted_at IS NULL)`},
			{Key: "class_mappings", Label: "Every active class has a section and subjects in the current year", SQL: `SELECT EXISTS (SELECT 1 FROM academic_years WHERE is_current AND deleted_at IS NULL)
				AND NOT EXISTS (
					SELECT 1 FROM classes c
					WHERE c.deleted_at IS NULL AND c.status = 'active'
					AND (NOT EXISTS (SELECT 1 FROM class_section_mappings m JOIN academic_years y ON y.tenant_id = m.tenant_id AND y.id = m.year_id
							WHERE m.tenant_id = c.tenant_id AND m.class_id = c.id AND m.deleted_at IS NULL AND y.is_current AND y.deleted_at IS NULL)
						OR NOT EXISTS (SELECT 1 FROM subject_class_mappings m JOIN academic_years y ON y.tenant_id = m.tenant_id AND y.id = m.year_id
							WHERE m.tenant_id = c.tenant_id AND m.class_id = c.id AND m.deleted_at IS NULL AND y.is_current AND y.deleted_at IS NULL)))`},
		},
	}
}
