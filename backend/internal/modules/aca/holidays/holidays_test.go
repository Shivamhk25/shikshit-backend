package holidays_test

import (
	"errors"
	"os"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/events"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/holidays"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func codeOf(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || len(fe) == 0 {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe[0].Field, fe[0].Code
}

func year(t *testing.T, f modtest.Fixture) years.Year {
	y, err := years.Service{DB: f.App}.Create(modtest.In(f.Alpha), f.Actor, years.Input{Code: "2026-27", Name: "2026-27", StartDate: "2026-04-01", EndDate: "2027-03-31"})
	if err != nil {
		t.Fatal(err)
	}
	return y
}

func TestHolidayRules(t *testing.T) {
	f := modtest.New(t)
	y := year(t, f)
	svc := holidays.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	in := holidays.Input{YearID: y.ID, Title: "Diwali", HolidayType: "public", StartDate: "2026-11-08", EndDate: "2026-11-09"}
	h, err := svc.Create(ctx, f.Actor, in)
	if err != nil || h.YearName != "2026-27" || h.IsOptional || h.IsRecurringAnnual {
		t.Fatalf("%+v %v", h, err)
	}

	out := in
	out.StartDate, out.EndDate = "2028-01-26", "2028-01-26"
	_, err = svc.Create(ctx, f.Actor, out)
	if _, code := codeOf(t, err); code != "outside_year" {
		t.Errorf("a one-off holiday outside the year: %v", err)
	}
	out.IsRecurringAnnual = true
	if _, err := svc.Create(ctx, f.Actor, out); err != nil {
		t.Errorf("a holiday that recurs every year may be dated outside this year: %v", err)
	}

	for name, c := range map[string]struct {
		mod         func(*holidays.Input)
		field, code string
	}{
		"unknown type": {func(i *holidays.Input) { i.HolidayType = "random" }, "holidayType", "invalid_choice"},
		"end first":    {func(i *holidays.Input) { i.StartDate, i.EndDate = "2026-11-09", "2026-11-08" }, "endDate", "before_start"},
		"unknown year": {func(i *holidays.Input) { i.YearID = "00000000-0000-0000-0000-000000000000" }, "yearId", "not_found"},
		"no title":     {func(i *holidays.Input) { i.Title = " " }, "title", "required"},
	} {
		x := in
		c.mod(&x)
		_, err := svc.Create(ctx, f.Actor, x)
		if field, code := codeOf(t, err); field != c.field || code != c.code {
			t.Errorf("%s: %s/%s", name, field, code)
		}
	}
	// filters
	q, _ := crud.ParseList(1, 50, "", "", holidays.SortKeys, holidays.DefaultSort)
	if p, _ := svc.List(ctx, q, holidays.Filter{HolidayType: "public"}); p.Total != 2 {
		t.Errorf("type filter: %d", p.Total)
	}
	if p, _ := svc.List(ctx, q, holidays.Filter{YearID: "00000000-0000-0000-0000-000000000000"}); p.Total != 0 {
		t.Errorf("year filter: %d", p.Total)
	}
	if _, err := svc.Create(modtest.In(f.Bravo), f.Actor, in); err == nil {
		t.Error("a holiday attached to another institute's year")
	}
}

func TestCalendarEventRules(t *testing.T) {
	f := modtest.New(t)
	y := year(t, f)
	svc := events.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	in := events.Input{YearID: y.ID, Title: "Annual day", EventType: "event", StartDate: "2026-12-20", EndDate: "2026-12-20", IsAllDay: true}
	e, err := svc.Create(ctx, f.Actor, in)
	if err != nil || !e.IsAllDay || e.IsInstructionalDay || e.YearName != "2026-27" {
		t.Fatalf("%+v %v", e, err)
	}
	out := in
	out.StartDate, out.EndDate = "2027-03-30", "2027-04-02"
	if _, err := svc.Create(ctx, f.Actor, out); err == nil {
		t.Error("an event running past the end of the year was accepted")
	} else if _, code := codeOf(t, err); code != "outside_year" {
		t.Errorf("%v", err)
	}
	bad := in
	bad.EventType = "party"
	if _, err := svc.Create(ctx, f.Actor, bad); err == nil {
		t.Error("unknown event type accepted")
	}
	exam := in
	exam.Title, exam.EventType, exam.IsExaminationDay = "Unit test", "exam", true
	if _, err := svc.Create(ctx, f.Actor, exam); err != nil {
		t.Fatal(err)
	}
	q, _ := crud.ParseList(1, 50, "", "", events.SortKeys, events.DefaultSort)
	if p, _ := svc.List(ctx, q, events.Filter{EventType: "exam"}); p.Total != 1 || !p.Items[0].IsExaminationDay {
		t.Errorf("type filter: %+v", p)
	}
}
