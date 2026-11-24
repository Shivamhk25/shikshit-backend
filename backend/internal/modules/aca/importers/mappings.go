package importers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/chapters"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classsections"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/subjectclasses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/textbooks"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

func refCol(key, label, ref, hint, example string, required bool) importer.Column {
	return importer.Column{Key: key, Label: label, Required: required, Kind: "reference", Ref: ref, Hint: hint, Example: example}
}

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ClassSections imports which sections a class has in an academic year. A row is identified by year, class and section.
func ClassSections(db tenancy.Beginner) importer.Entity {
	return importer.Importable[classsections.ClassSection, classsections.Input]{
		EntityKey: "class_section_mappings", EntityTitle: "Class sections", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			refCol("year", "Academic year", "academic_years", "Year code or name.", "2026-27", true),
			refCol("class", "Class", "classes", "Class code or name.", "V", true),
			refCol("section", "Section", "sections", "Section code or name.", "A", true),
			{Key: "capacity", Label: "Capacity", Kind: "integer", Hint: "1 to 1000.", Example: "40"},
			refCol("room", "Room", "rooms", "Room code or name.", "R101", false),
			{Key: "remarks", Label: "Remarks", Kind: "text"},
		},
		Store: classsections.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) classsections.Input {
			in := classsections.Input{
				YearID:    c.Ref(ctx, tx, "year", "academic_years", "academic year"),
				ClassID:   c.Ref(ctx, tx, "class", "classes", "class"),
				SectionID: c.Ref(ctx, tx, "section", "sections", "section"),
				Capacity:  c.OptInt("capacity"), Remarks: c.Opt("remarks"),
			}
			if id := c.Ref(ctx, tx, "room", "rooms", "room"); id != "" {
				in.RoomID = &id
			}
			return in
		},
		KeyOf:     func(i classsections.Input) string { return i.YearID + "|" + i.ClassID + "|" + i.SectionID },
		KeyColumn: "section", FindWhere: "(year_id::text || '|' || class_id::text || '|' || section_id::text) = $1",
		FieldColumn: map[string]string{"yearId": "year", "classId": "class", "sectionId": "section", "roomId": "room"},
		ToInput: func(m classsections.ClassSection) classsections.Input {
			return classsections.Input{YearID: m.YearID, ClassID: m.ClassID, SectionID: m.SectionID, Capacity: m.Capacity, RoomID: m.RoomID, Remarks: m.Remarks}
		},
		ID:    func(m classsections.ClassSection) string { return m.ID },
		Stamp: func(m classsections.ClassSection) time.Time { return m.UpdatedAt },
	}
}

// SubjectClasses imports which subjects a class studies in an academic year. A blank section means the whole class.
func SubjectClasses(db tenancy.Beginner) importer.Entity {
	return importer.Importable[subjectclasses.SubjectClass, subjectclasses.Input]{
		EntityKey: "subject_class_mappings", EntityTitle: "Class subjects", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			refCol("year", "Academic year", "academic_years", "Year code or name.", "2026-27", true),
			refCol("class", "Class", "classes", "Class code or name.", "V", true),
			refCol("subject", "Subject", "subjects", "Subject code or name.", "MATH", true),
			refCol("section", "Section", "sections", "Leave blank for the whole class. A section must already be mapped to the class.", "", false),
			{Key: "periodsPerWeek", Label: "Periods a week", Kind: "integer", Hint: "0 to 60.", Example: "6"},
			{Key: "isElective", Label: "Elective", Kind: "boolean", Hint: "Blank means no."},
			{Key: "isExamined", Label: "Examined", Kind: "boolean", Hint: "Blank means yes."},
			{Key: "isAssessed", Label: "Assessed", Kind: "boolean", Hint: "Blank means yes."},
			{Key: "isGradeOnly", Label: "Grade only", Kind: "boolean", Hint: "Blank means no."},
			{Key: "hasPractical", Label: "Has practical", Kind: "boolean", Hint: "Blank means no."},
			{Key: "onReportCard", Label: "On report card", Kind: "boolean", Hint: "Blank means yes."},
		},
		Store: subjectclasses.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) subjectclasses.Input {
			in := subjectclasses.Input{
				YearID:         c.Ref(ctx, tx, "year", "academic_years", "academic year"),
				ClassID:        c.Ref(ctx, tx, "class", "classes", "class"),
				SubjectID:      c.Ref(ctx, tx, "subject", "subjects", "subject"),
				PeriodsPerWeek: c.OptInt("periodsPerWeek"),
				IsElective:     c.Bool("isElective", false), IsExamined: c.Bool("isExamined", true), IsAssessed: c.Bool("isAssessed", true),
				IsGradeOnly: c.Bool("isGradeOnly", false), HasPractical: c.Bool("hasPractical", false), OnReportCard: c.Bool("onReportCard", true),
			}
			if id := c.Ref(ctx, tx, "section", "sections", "section"); id != "" {
				in.SectionID = &id
			}
			return in
		},
		KeyOf: func(i subjectclasses.Input) string {
			return i.YearID + "|" + i.ClassID + "|" + i.SubjectID + "|" + derefOr(i.SectionID)
		},
		KeyColumn:   "subject",
		FindWhere:   "(year_id::text || '|' || class_id::text || '|' || subject_id::text || '|' || COALESCE(section_id::text, '')) = $1",
		FieldColumn: map[string]string{"yearId": "year", "classId": "class", "subjectId": "subject", "sectionId": "section"},
		ToInput: func(m subjectclasses.SubjectClass) subjectclasses.Input {
			return subjectclasses.Input{YearID: m.YearID, ClassID: m.ClassID, SubjectID: m.SubjectID, SectionID: m.SectionID, PeriodsPerWeek: m.PeriodsPerWeek,
				IsElective: m.IsElective, ElectiveGroupID: m.ElectiveGroupID, IsExamined: m.IsExamined, IsAssessed: m.IsAssessed, IsGradeOnly: m.IsGradeOnly, HasPractical: m.HasPractical, OnReportCard: m.OnReportCard}
		},
		ID:    func(m subjectclasses.SubjectClass) string { return m.ID },
		Stamp: func(m subjectclasses.SubjectClass) time.Time { return m.UpdatedAt },
	}
}

// Textbooks imports textbooks. The web address of the book is a link only; no file is uploaded.
func Textbooks(db tenancy.Beginner) importer.Entity {
	return importer.Importable[textbooks.Textbook, textbooks.Input]{
		EntityKey: "textbooks", EntityTitle: "Textbooks", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "NCERT-5-MATH"},
			{Key: "title", Label: "Title", Required: true, Kind: "text", Example: "Mathematics 5"},
			refCol("class", "Class", "classes", "Class code or name.", "V", true),
			refCol("subject", "Subject", "subjects", "Subject code or name.", "MATH", true),
			{Key: "publisher", Label: "Publisher", Kind: "text"},
			{Key: "author", Label: "Author", Kind: "text"},
			{Key: "isbn", Label: "ISBN", Kind: "text", Hint: "10 to 17 digits, hyphens or an X."},
			{Key: "bookUrl", Label: "Web link", Kind: "text", Hint: "Starts with http:// or https://."},
			{Key: "notes", Label: "Notes", Kind: "text"},
			statusCol,
		},
		Store: textbooks.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) textbooks.Input {
			return textbooks.Input{Code: c.Text("code"), Title: c.Text("title"),
				ClassID:   c.Ref(ctx, tx, "class", "classes", "class"),
				SubjectID: c.Ref(ctx, tx, "subject", "subjects", "subject"),
				Publisher: c.Opt("publisher"), Author: c.Opt("author"), Isbn: c.Opt("isbn"), BookUrl: c.Opt("bookUrl"), Notes: c.Opt("notes"), Status: status(c)}
		},
		KeyOf:     func(i textbooks.Input) string { return lower(i.Code) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		FieldColumn: map[string]string{"classId": "class", "subjectId": "subject"},
		ToInput: func(b textbooks.Textbook) textbooks.Input {
			return textbooks.Input{Code: b.Code, Title: b.Title, ClassID: b.ClassID, SubjectID: b.SubjectID, Publisher: b.Publisher, Author: b.Author,
				Isbn: b.Isbn, BookUrl: b.BookUrl, Notes: b.Notes, Status: b.Status}
		},
		ID:    func(b textbooks.Textbook) string { return b.ID },
		Stamp: func(b textbooks.Textbook) time.Time { return b.UpdatedAt },
	}
}

// Chapters imports textbook chapters. A chapter number is required in a file so a row can be matched again.
func Chapters(db tenancy.Beginner) importer.Entity {
	return importer.Importable[chapters.Chapter, chapters.Input]{
		EntityKey: "textbook_chapters", EntityTitle: "Textbook chapters", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			refCol("textbook", "Textbook", "textbooks", "Textbook code or title.", "NCERT-5-MATH", true),
			{Key: "chapterNo", Label: "Chapter number", Required: true, Kind: "text", Example: "1"},
			{Key: "title", Label: "Title", Required: true, Kind: "text", Example: "Numbers"},
			{Key: "sortOrder", Label: "Order", Kind: "integer", Hint: "Chapters are listed by this number. Blank means 0."},
			{Key: "periods", Label: "Periods", Kind: "integer", Hint: "0 to 200."},
			{Key: "fromPage", Label: "From page", Kind: "integer"},
			{Key: "toPage", Label: "To page", Kind: "integer"},
			{Key: "isOptional", Label: "Optional chapter", Kind: "boolean", Hint: "Optional chapters do not count towards the periods a book needs."},
			{Key: "summary", Label: "Summary", Kind: "text"},
		},
		Store: chapters.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) chapters.Input {
			return chapters.Input{TextbookID: c.Ref(ctx, tx, "textbook", "textbooks", "textbook"), ChapterNo: c.Opt("chapterNo"), Title: c.Text("title"),
				SortOrder: c.Int("sortOrder", 0), Periods: c.OptInt("periods"), FromPage: c.OptInt("fromPage"), ToPage: c.OptInt("toPage"),
				IsOptional: c.Bool("isOptional", false), Summary: c.Opt("summary")}
		},
		KeyOf:     func(i chapters.Input) string { return i.TextbookID + "|" + lower(derefOr(i.ChapterNo)) },
		KeyColumn: "chapterNo", FindWhere: "(textbook_id::text || '|' || lower(COALESCE(chapter_no, ''))) = $1",
		FieldColumn: map[string]string{"textbookId": "textbook"},
		ToInput: func(k chapters.Chapter) chapters.Input {
			return chapters.Input{TextbookID: k.TextbookID, SortOrder: k.SortOrder, ChapterNo: k.ChapterNo, Title: k.Title, Periods: k.Periods,
				FromPage: k.FromPage, ToPage: k.ToPage, IsOptional: k.IsOptional, Summary: k.Summary}
		},
		ID:    func(k chapters.Chapter) string { return k.ID },
		Stamp: func(k chapters.Chapter) time.Time { return k.UpdatedAt },
	}
}
