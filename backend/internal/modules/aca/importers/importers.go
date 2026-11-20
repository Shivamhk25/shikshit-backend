// Package importers registers the academic master data that can be imported (ONB-10). Each entity is a thin mapping
// from file columns to the module's own Input, so an imported row passes exactly the rules a row typed on a screen does.
package importers

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/aca/divisions"
	"github.com/pie360/backend/internal/modules/aca/sections"
	"github.com/pie360/backend/internal/modules/aca/subjects"
	"github.com/pie360/backend/internal/platform/importer"
	"github.com/pie360/backend/internal/platform/tenancy"
)

const domain = "academic"

func status(c *importer.Cells) string {
	return strings.ToLower(c.Text("status"))
}

// Registry returns the importable academic entities.
func Registry(db tenancy.Beginner) *importer.Registry {
	return importer.NewRegistry(Divisions(db), Classes(db), Sections(db), Subjects(db))
}

var statusCol = importer.Column{Key: "status", Label: "Status", Kind: "choice", Allowed: []string{"active", "inactive"}, Hint: "Blank means active."}

// Divisions imports school divisions.
func Divisions(db tenancy.Beginner) importer.Entity {
	return importer.Importable[divisions.Division, divisions.Input]{
		EntityKey: "school_divisions", EntityTitle: "School divisions", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "PRI"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Primary"},
			{Key: "displayOrder", Label: "Display order", Kind: "integer", Example: "1"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: divisions.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) divisions.Input {
			return divisions.Input{Code: c.Text("code"), Name: c.Text("name"), DisplayOrder: c.Int("displayOrder", 0), Description: c.Opt("description"), Status: status(c)}
		},
		KeyOf:     func(i divisions.Input) string { return strings.ToLower(strings.TrimSpace(i.Code)) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(d divisions.Division) divisions.Input {
			return divisions.Input{Code: d.Code, Name: d.Name, DisplayOrder: d.DisplayOrder, Description: d.Description, Status: d.Status}
		},
		ID:    func(d divisions.Division) string { return d.ID },
		Stamp: func(d divisions.Division) time.Time { return d.UpdatedAt },
	}
}

// Classes imports classes; the division is matched by its code or name.
func Classes(db tenancy.Beginner) importer.Entity {
	return importer.Importable[classes.Class, classes.Input]{
		EntityKey: "classes", EntityTitle: "Classes", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "V"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Class 5"},
			{Key: "division", Label: "Division", Kind: "reference", Ref: "school_divisions", Hint: "Division code or name.", Example: "PRI"},
			{Key: "displayOrder", Label: "Display order", Kind: "integer", Example: "5"},
			{Key: "youngestAgeMonths", Label: "Youngest age (months)", Kind: "integer"},
			{Key: "oldestAgeMonths", Label: "Oldest age (months)", Kind: "integer"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: classes.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) classes.Input {
			in := classes.Input{Code: c.Text("code"), Name: c.Text("name"), DisplayOrder: c.Int("displayOrder", 0),
				YoungestAgeMonths: c.OptInt("youngestAgeMonths"), OldestAgeMonths: c.OptInt("oldestAgeMonths"), Description: c.Opt("description"), Status: status(c)}
			if id := c.Ref(ctx, tx, "division", "school_divisions", "division"); id != "" {
				in.DivisionID = &id
			}
			return in
		},
		KeyOf:     func(i classes.Input) string { return strings.ToLower(strings.TrimSpace(i.Code)) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		FieldColumn: map[string]string{"divisionId": "division"},
		ToInput: func(k classes.Class) classes.Input {
			return classes.Input{Code: k.Code, Name: k.Name, DivisionID: k.DivisionID, DisplayOrder: k.DisplayOrder,
				YoungestAgeMonths: k.YoungestAgeMonths, OldestAgeMonths: k.OldestAgeMonths, Description: k.Description, Status: k.Status}
		},
		ID:    func(k classes.Class) string { return k.ID },
		Stamp: func(k classes.Class) time.Time { return k.UpdatedAt },
	}
}

// Sections imports sections.
func Sections(db tenancy.Beginner) importer.Entity {
	return importer.Importable[sections.Section, sections.Input]{
		EntityKey: "sections", EntityTitle: "Sections", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "A"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Section A"},
			{Key: "shortName", Label: "Short name", Kind: "text", Example: "A"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: sections.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) sections.Input {
			return sections.Input{Code: c.Text("code"), Name: c.Text("name"), ShortName: c.Opt("shortName"), Description: c.Opt("description"), Status: status(c)}
		},
		KeyOf:     func(i sections.Input) string { return strings.ToLower(strings.TrimSpace(i.Code)) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(s sections.Section) sections.Input {
			return sections.Input{Code: s.Code, Name: s.Name, ShortName: s.ShortName, Description: s.Description, Status: s.Status}
		},
		ID:    func(s sections.Section) string { return s.ID },
		Stamp: func(s sections.Section) time.Time { return s.UpdatedAt },
	}
}

// Subjects imports subjects.
func Subjects(db tenancy.Beginner) importer.Entity {
	return importer.Importable[subjects.Subject, subjects.Input]{
		EntityKey: "subjects", EntityTitle: "Subjects", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "MATH"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Mathematics"},
			{Key: "shortName", Label: "Short name", Kind: "text", Example: "Maths"},
			{Key: "category", Label: "Category", Required: true, Kind: "choice", Allowed: subjects.Categories, Example: "core"},
			{Key: "area", Label: "Area", Required: true, Kind: "choice", Allowed: subjects.Areas, Example: "mathematics"},
			{Key: "color", Label: "Colour", Kind: "text", Example: "#2F5BFF"},
			{Key: "scholastic", Label: "Scholastic", Kind: "boolean", Hint: "yes or no. Blank means no."},
			{Key: "coScholastic", Label: "Co-scholastic", Kind: "boolean"},
			{Key: "nonScholastic", Label: "Non-scholastic", Kind: "boolean"},
			{Key: "credits", Label: "Credits", Kind: "number", Example: "4"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: subjects.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) subjects.Input {
			return subjects.Input{Code: c.Text("code"), Name: c.Text("name"), ShortName: c.Opt("shortName"), Color: c.Opt("color"),
				Category: c.Text("category"), Area: c.Text("area"),
				Scholastic: c.Bool("scholastic", false), CoScholastic: c.Bool("coScholastic", false), NonScholastic: c.Bool("nonScholastic", false),
				Credits: c.Float("credits", 0), Description: c.Opt("description"), Status: status(c)}
		},
		KeyOf:     func(i subjects.Input) string { return strings.ToLower(strings.TrimSpace(i.Code)) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(s subjects.Subject) subjects.Input {
			return subjects.Input{Code: s.Code, Name: s.Name, ShortName: s.ShortName, Color: s.Color, Category: s.Category, Area: s.Area,
				Scholastic: s.Scholastic, CoScholastic: s.CoScholastic, NonScholastic: s.NonScholastic, Credits: s.Credits, Description: s.Description, Status: s.Status}
		},
		ID:    func(s subjects.Subject) string { return s.ID },
		Stamp: func(s subjects.Subject) time.Time { return s.UpdatedAt },
	}
}
