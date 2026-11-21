package years_test

import (
	"errors"
	"os"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/terms"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func yr(code, start, end string) years.Input {
	return years.Input{Code: code, Name: "Year " + code, StartDate: start, EndDate: end}
}

func fieldOf(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || len(fe) == 0 {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe[0].Field, fe[0].Code
}

func TestCreateAndValidation(t *testing.T) {
	f := modtest.New(t)
	svc := years.Service{DB: f.App}
	y, err := svc.Create(modtest.In(f.Alpha), f.Actor, yr("2026-27", "2026-04-01", "2027-03-31"))
	if err != nil || y.StartDate != "2026-04-01" || y.EndDate != "2027-03-31" || y.Status != "active" || y.IsCurrent {
		t.Fatalf("%+v %v", y, err)
	}
	for name, c := range map[string]struct {
		in          years.Input
		field, code string
	}{
		"no code":            {years.Input{Name: "x", StartDate: "2026-04-01", EndDate: "2027-03-31"}, "code", "required"},
		"bad code":           {years.Input{Code: "<b>", Name: "x", StartDate: "2026-04-01", EndDate: "2027-03-31"}, "code", "invalid"},
		"bad date":           {years.Input{Code: "a", Name: "x", StartDate: "01/04/2026", EndDate: "2027-03-31"}, "startDate", "invalid_date"},
		"impossible date":    {years.Input{Code: "a", Name: "x", StartDate: "2026-02-30", EndDate: "2027-03-31"}, "startDate", "invalid_date"},
		"end before start":   {years.Input{Code: "a", Name: "x", StartDate: "2027-04-01", EndDate: "2026-03-31"}, "endDate", "before_start"},
		"same day":           {years.Input{Code: "a", Name: "x", StartDate: "2026-04-01", EndDate: "2026-04-01"}, "endDate", "before_start"},
		"inactive + current": {years.Input{Code: "a", Name: "x", StartDate: "2028-04-01", EndDate: "2029-03-31", IsCurrent: true, Status: "inactive"}, "isCurrent", "inactive_year"},
	} {
		_, err := svc.Create(modtest.In(f.Alpha), f.Actor, c.in)
		if field, code := fieldOf(t, err); field != c.field || code != c.code {
			t.Errorf("%s: %s/%s, want %s/%s", name, field, code, c.field, c.code)
		}
	}
}

func TestYearsCannotOverlapButMayTouchTheNextOne(t *testing.T) {
	f := modtest.New(t)
	svc := years.Service{DB: f.App}
	a, _ := svc.Create(modtest.In(f.Alpha), f.Actor, yr("A", "2026-04-01", "2027-03-31"))
	for _, c := range []struct{ s, e string }{{"2027-03-31", "2028-03-30"}, {"2026-06-01", "2026-09-01"}, {"2025-01-01", "2026-04-01"}, {"2025-01-01", "2030-01-01"}} {
		if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, yr("X"+c.s, c.s, c.e)); err == nil {
			t.Errorf("%s..%s overlaps and was accepted", c.s, c.e)
		} else if _, code := fieldOf(t, err); code != "overlaps_year" {
			t.Errorf("%s..%s: %v", c.s, c.e, err)
		}
	}
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, yr("B", "2027-04-01", "2028-03-31")); err != nil {
		t.Errorf("the next year must be allowed: %v", err)
	}
	// editing a year may keep its own dates
	if _, err := svc.Update(modtest.In(f.Alpha), f.Actor, a.ID, yr("A", "2026-04-01", "2027-03-31")); err != nil {
		t.Errorf("a year overlapping only itself: %v", err)
	}
	// another institute may use the same dates
	if _, err := svc.Create(modtest.In(f.Bravo), f.Actor, yr("A", "2026-04-01", "2027-03-31")); err != nil {
		t.Errorf("overlap rule must be per institute: %v", err)
	}
}

func TestOnlyOneYearIsCurrent(t *testing.T) {
	f := modtest.New(t)
	svc := years.Service{DB: f.App}
	one := yr("A", "2026-04-01", "2027-03-31")
	one.IsCurrent = true
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, one); err != nil {
		t.Fatal(err)
	}
	two := yr("B", "2027-04-01", "2028-03-31")
	two.IsCurrent = true
	_, err := svc.Create(modtest.In(f.Alpha), f.Actor, two)
	if field, code := fieldOf(t, err); field != "isCurrent" || code != "another_current" {
		t.Fatalf("%s/%s", field, code)
	}
	two.IsCurrent = false
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, two); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteIsRefusedForTheCurrentYearAndForYearsWithTerms(t *testing.T) {
	f := modtest.New(t)
	ys, ts := years.Service{DB: f.App}, terms.Service{DB: f.App}
	cur := yr("A", "2026-04-01", "2027-03-31")
	cur.IsCurrent = true
	a, _ := ys.Create(modtest.In(f.Alpha), f.Actor, cur)
	b, _ := ys.Create(modtest.In(f.Alpha), f.Actor, yr("B", "2027-04-01", "2028-03-31"))

	var inUse resource.InUseError
	if err := ys.Delete(modtest.In(f.Alpha), f.Actor, a.ID); !errors.As(err, &inUse) {
		t.Fatalf("current year deleted: %v", err)
	}
	term, err := ts.Create(modtest.In(f.Alpha), f.Actor, terms.Input{YearID: b.ID, Code: "T1", Title: "Term 1", StartDate: "2027-04-01", EndDate: "2027-09-30"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ys.Delete(modtest.In(f.Alpha), f.Actor, b.ID); !errors.As(err, &inUse) {
		t.Fatalf("year with terms deleted: %v", err)
	}
	if err := ts.Delete(modtest.In(f.Alpha), f.Actor, term.ID); err != nil {
		t.Fatal(err)
	}
	if err := ys.Delete(modtest.In(f.Alpha), f.Actor, b.ID); err != nil {
		t.Fatalf("year without terms: %v", err)
	}
	// its code is free again
	if _, err := ys.Create(modtest.In(f.Alpha), f.Actor, yr("B", "2027-04-01", "2028-03-31")); err != nil {
		t.Errorf("code reuse: %v", err)
	}
	if f.AuditCount(t, "academic_years.delete") != 1 {
		t.Error("a refused delete must leave no audit row")
	}
}

func TestListFiltersSortAndIsolation(t *testing.T) {
	f := modtest.New(t)
	svc := years.Service{DB: f.App}
	cur := yr("B", "2027-04-01", "2028-03-31")
	cur.IsCurrent = true
	svc.Create(modtest.In(f.Alpha), f.Actor, yr("A", "2026-04-01", "2027-03-31"))
	svc.Create(modtest.In(f.Alpha), f.Actor, cur)
	svc.Create(modtest.In(f.Bravo), f.Actor, yr("Z", "2026-04-01", "2027-03-31"))

	q, _ := crud.ParseList(1, 50, "", "", years.SortKeys, years.DefaultSort)
	p, err := svc.List(modtest.In(f.Alpha), q, years.Filter{})
	if err != nil || p.Total != 2 || p.Items[0].Code != "B" {
		t.Fatalf("newest first and only this institute: %+v %v", p, err)
	}
	yes := true
	if p, _ = svc.List(modtest.In(f.Alpha), q, years.Filter{IsCurrent: &yes}); p.Total != 1 || p.Items[0].Code != "B" {
		t.Errorf("current filter: %+v", p)
	}
}
