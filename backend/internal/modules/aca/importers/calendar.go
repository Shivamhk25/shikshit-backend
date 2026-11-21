package importers

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/campuses"
	"github.com/pie360/backend/internal/modules/aca/holidays"
	"github.com/pie360/backend/internal/modules/aca/rooms"
	"github.com/pie360/backend/internal/modules/aca/terms"
	"github.com/pie360/backend/internal/modules/aca/years"
	"github.com/pie360/backend/internal/platform/importer"
	"github.com/pie360/backend/internal/platform/tenancy"
)

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Years imports academic years.
func Years(db tenancy.Beginner) importer.Entity {
	return importer.Importable[years.Year, years.Input]{
		EntityKey: "academic_years", EntityTitle: "Academic years", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "2026-27"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "2026-27"},
			{Key: "startDate", Label: "Start date", Required: true, Kind: "date", Hint: "2026-04-01 or 01/04/2026.", Example: "2026-04-01"},
			{Key: "endDate", Label: "End date", Required: true, Kind: "date", Example: "2027-03-31"},
			{Key: "isCurrent", Label: "Current year", Kind: "boolean", Hint: "yes or no. Only one year can be current."},
			{Key: "remarks", Label: "Remarks", Kind: "text"},
			statusCol,
		},
		Store: years.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) years.Input {
			return years.Input{Code: c.Text("code"), Name: c.Text("name"), StartDate: c.Date("startDate"), EndDate: c.Date("endDate"),
				IsCurrent: c.Bool("isCurrent", false), Remarks: c.Opt("remarks"), Status: status(c)}
		},
		KeyOf:     func(i years.Input) string { return lower(i.Code) },
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(y years.Year) years.Input {
			return years.Input{Code: y.Code, Name: y.Name, StartDate: y.StartDate, EndDate: y.EndDate, IsCurrent: y.IsCurrent, Remarks: y.Remarks, Status: y.Status}
		},
		ID:    func(y years.Year) string { return y.ID },
		Stamp: func(y years.Year) time.Time { return y.UpdatedAt },
	}
}

// Terms imports terms; the year is matched by its code or name and a term is identified by year and code.
func Terms(db tenancy.Beginner) importer.Entity {
	return importer.Importable[terms.Term, terms.Input]{
		EntityKey: "academic_terms", EntityTitle: "Terms", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "year", Label: "Academic year", Required: true, Kind: "reference", Ref: "academic_years", Hint: "Year code or name.", Example: "2026-27"},
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "T1"},
			{Key: "title", Label: "Title", Required: true, Kind: "text", Example: "Term 1"},
			{Key: "startDate", Label: "Start date", Required: true, Kind: "date", Example: "2026-04-01"},
			{Key: "endDate", Label: "End date", Required: true, Kind: "date", Example: "2026-09-30"},
			{Key: "color", Label: "Colour", Kind: "text", Example: "#2F5BFF"},
		},
		Store: terms.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) terms.Input {
			return terms.Input{YearID: c.Ref(ctx, tx, "year", "academic_years", "academic year"), Code: c.Text("code"), Title: c.Text("title"),
				StartDate: c.Date("startDate"), EndDate: c.Date("endDate"), Color: c.Opt("color")}
		},
		KeyOf:     func(i terms.Input) string { return i.YearID + "|" + lower(i.Code) },
		KeyColumn: "code", FindWhere: "(year_id::text || '|' || lower(code)) = $1",
		FieldColumn: map[string]string{"yearId": "year"},
		ToInput: func(t terms.Term) terms.Input {
			return terms.Input{YearID: t.YearID, Code: t.Code, Title: t.Title, StartDate: t.StartDate, EndDate: t.EndDate, Color: t.Color}
		},
		ID:    func(t terms.Term) string { return t.ID },
		Stamp: func(t terms.Term) time.Time { return t.UpdatedAt },
	}
}

// Holidays imports holidays; one is identified by its year, title and first day.
func Holidays(db tenancy.Beginner) importer.Entity {
	return importer.Importable[holidays.Holiday, holidays.Input]{
		EntityKey: "holidays", EntityTitle: "Holidays", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "year", Label: "Academic year", Required: true, Kind: "reference", Ref: "academic_years", Hint: "Year code or name.", Example: "2026-27"},
			{Key: "title", Label: "Title", Required: true, Kind: "text", Example: "Diwali"},
			{Key: "holidayType", Label: "Type", Required: true, Kind: "choice", Allowed: holidays.Types, Example: "public"},
			{Key: "startDate", Label: "Start date", Required: true, Kind: "date", Example: "2026-11-08"},
			{Key: "endDate", Label: "End date", Required: true, Kind: "date", Example: "2026-11-10"},
			{Key: "isOptional", Label: "Optional holiday", Kind: "boolean"},
			{Key: "isRecurringAnnual", Label: "Repeats every year", Kind: "boolean"},
			{Key: "applicableFor", Label: "Applies to", Kind: "text"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: holidays.Service{DB: db}.Store(),
		Build: func(ctx context.Context, tx pgx.Tx, c *importer.Cells) holidays.Input {
			return holidays.Input{YearID: c.Ref(ctx, tx, "year", "academic_years", "academic year"), Title: c.Text("title"), HolidayType: strings.ToLower(c.Text("holidayType")),
				StartDate: c.Date("startDate"), EndDate: c.Date("endDate"), IsOptional: c.Bool("isOptional", false), IsRecurringAnnual: c.Bool("isRecurringAnnual", false),
				ApplicableFor: c.Opt("applicableFor"), Description: c.Opt("description"), Status: status(c)}
		},
		KeyOf:     func(i holidays.Input) string { return i.YearID + "|" + lower(i.Title) + "|" + i.StartDate },
		KeyColumn: "title", FindWhere: "(year_id::text || '|' || lower(title) || '|' || start_date::text) = $1",
		FieldColumn: map[string]string{"yearId": "year"},
		ToInput: func(h holidays.Holiday) holidays.Input {
			return holidays.Input{YearID: h.YearID, Title: h.Title, HolidayType: h.HolidayType, StartDate: h.StartDate, EndDate: h.EndDate, IsOptional: h.IsOptional,
				IsRecurringAnnual: h.IsRecurringAnnual, ApplicableFor: h.ApplicableFor, Description: h.Description, Status: h.Status}
		},
		ID:    func(h holidays.Holiday) string { return h.ID },
		Stamp: func(h holidays.Holiday) time.Time { return h.UpdatedAt },
	}
}

// Campuses imports campuses. A code is required in a file so a row can be matched again.
func Campuses(db tenancy.Beginner) importer.Entity {
	return importer.Importable[campuses.Campus, campuses.Input]{
		EntityKey: "campuses", EntityTitle: "Campuses", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "N"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "North campus"},
			{Key: "checkInRadiusM", Label: "Check-in radius (m)", Kind: "integer", Hint: "10 to 5000. Blank means 200.", Example: "200"},
			{Key: "latitude", Label: "Latitude", Kind: "number", Hint: "Give both latitude and longitude, or neither."},
			{Key: "longitude", Label: "Longitude", Kind: "number"},
			{Key: "staffDueIn", Label: "Staff due in", Kind: "text", Hint: "24-hour time like 08:30.", Example: "08:30"},
			{Key: "address", Label: "Address", Kind: "text"},
			{Key: "remarks", Label: "Remarks", Kind: "text"},
			statusCol,
		},
		Store: campuses.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) campuses.Input {
			if c.Text("code") == "" {
				c.Fail("code", "required", "Enter a code, so this campus can be matched when you import again.")
			}
			in := campuses.Input{Code: c.Opt("code"), Name: c.Text("name"), CheckInRadiusM: c.Int("checkInRadiusM", 200), StaffDueIn: c.Opt("staffDueIn"),
				Address: c.Opt("address"), Remarks: c.Opt("remarks"), Status: status(c)}
			if c.Text("latitude") != "" {
				v := c.Float("latitude", 0)
				in.Latitude = &v
			}
			if c.Text("longitude") != "" {
				v := c.Float("longitude", 0)
				in.Longitude = &v
			}
			return in
		},
		KeyOf: func(i campuses.Input) string {
			if i.Code == nil {
				return ""
			}
			return lower(*i.Code)
		},
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(k campuses.Campus) campuses.Input {
			return campuses.Input{Code: k.Code, Name: k.Name, CheckInRadiusM: k.CheckInRadiusM, Latitude: k.Latitude, Longitude: k.Longitude,
				StaffDueIn: k.StaffDueIn, Address: k.Address, Remarks: k.Remarks, Status: k.Status}
		},
		ID:    func(k campuses.Campus) string { return k.ID },
		Stamp: func(k campuses.Campus) time.Time { return k.UpdatedAt },
	}
}

// Rooms imports rooms. A code is required in a file so a row can be matched again.
func Rooms(db tenancy.Beginner) importer.Entity {
	return importer.Importable[rooms.Room, rooms.Input]{
		EntityKey: "rooms", EntityTitle: "Rooms", EntityDomain: domain, Version: 1,
		Cols: []importer.Column{
			{Key: "code", Label: "Code", Required: true, Kind: "text", Example: "R101"},
			{Key: "name", Label: "Name", Required: true, Kind: "text", Example: "Room 101"},
			{Key: "type", Label: "Type", Kind: "choice", Allowed: rooms.Types, Example: "classroom"},
			{Key: "capacity", Label: "Capacity", Kind: "integer", Hint: "1 to 10000. Blank means 30.", Example: "30"},
			{Key: "building", Label: "Building", Kind: "text"},
			{Key: "floor", Label: "Floor", Kind: "text"},
			{Key: "availableForTimetable", Label: "Used in timetable", Kind: "boolean", Hint: "Blank means yes."},
			{Key: "displayOrder", Label: "Display order", Kind: "integer"},
			{Key: "description", Label: "Description", Kind: "text"},
			statusCol,
		},
		Store: rooms.Service{DB: db}.Store(),
		Build: func(_ context.Context, _ pgx.Tx, c *importer.Cells) rooms.Input {
			if c.Text("code") == "" {
				c.Fail("code", "required", "Enter a code, so this room can be matched when you import again.")
			}
			in := rooms.Input{Code: c.Opt("code"), Name: c.Text("name"), Capacity: c.Int("capacity", 30), Building: c.Opt("building"), Floor: c.Opt("floor"),
				DisplayOrder: c.Int("displayOrder", 0), Description: c.Opt("description"), Status: status(c)}
			if t := c.Opt("type"); t != nil {
				l := strings.ToLower(*t)
				in.Type = &l
			}
			if c.Text("availableForTimetable") != "" {
				v := c.Bool("availableForTimetable", true)
				in.AvailableForTimetable = &v
			}
			return in
		},
		KeyOf: func(i rooms.Input) string {
			if i.Code == nil {
				return ""
			}
			return lower(*i.Code)
		},
		KeyColumn: "code", FindWhere: "lower(code) = $1",
		ToInput: func(r rooms.Room) rooms.Input {
			av := r.AvailableForTimetable
			return rooms.Input{Code: r.Code, Name: r.Name, Type: r.Type, Capacity: r.Capacity, Building: r.Building, Floor: r.Floor,
				AvailableForTimetable: &av, DisplayOrder: r.DisplayOrder, Description: r.Description, Status: r.Status}
		},
		ID:    func(r rooms.Room) string { return r.ID },
		Stamp: func(r rooms.Room) time.Time { return r.UpdatedAt },
	}
}
