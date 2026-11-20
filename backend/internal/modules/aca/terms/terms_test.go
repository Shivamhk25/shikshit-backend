package terms_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/terms"
	"github.com/pie360/backend/internal/modules/aca/years"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func setup(t *testing.T) (modtest.Fixture, terms.Service, years.Year) {
	f := modtest.New(t)
	y, err := years.Service{DB: f.App}.Create(modtest.In(f.Alpha), f.Actor, years.Input{Code: "2026-27", Name: "2026-27", StartDate: "2026-04-01", EndDate: "2027-03-31"})
	if err != nil {
		t.Fatal(err)
	}
	return f, terms.Service{DB: f.App}, y
}

func tm(year, code, start, end string) terms.Input {
	return terms.Input{YearID: year, Code: code, Title: "Term " + code, StartDate: start, EndDate: end}
}

func fieldOf(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || len(fe) == 0 {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe[0].Field, fe[0].Code
}

func TestCreateComputesDaysAndShowsTheYear(t *testing.T) {
	f, svc, y := setup(t)
	in := tm(y.ID, "T1", "2026-04-01", "2026-04-10")
	color := "#2F5BFF"
	in.Color = &color
	got, err := svc.Create(modtest.In(f.Alpha), f.Actor, in)
	if err != nil || got.Days != 10 || got.YearName != "2026-27" || *got.Color != color {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTermsMustLieInsideTheirYear(t *testing.T) {
	f, svc, y := setup(t)
	for _, c := range [][2]string{{"2026-03-31", "2026-06-01"}, {"2026-06-01", "2027-04-01"}, {"2025-01-01", "2030-01-01"}} {
		_, err := svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "X", c[0], c[1]))
		if _, code := fieldOf(t, err); code != "outside_year" {
			t.Errorf("%v: %v", c, err)
		}
	}
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "E", "2026-04-01", "2027-03-31")); err != nil {
		t.Errorf("a term covering the whole year: %v", err)
	}
}

func TestTermsInOneYearCannotOverlap(t *testing.T) {
	f, svc, y := setup(t)
	a, _ := svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "T1", "2026-04-01", "2026-09-30"))
	_, err := svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "T2", "2026-09-30", "2027-03-31"))
	if _, code := fieldOf(t, err); code != "overlaps_term" {
		t.Fatalf("%v", err)
	}
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "T2", "2026-10-01", "2027-03-31")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(modtest.In(f.Alpha), f.Actor, a.ID, tm(y.ID, "T1", "2026-04-01", "2026-09-30")); err != nil {
		t.Errorf("a term overlapping only itself: %v", err)
	}
}

func TestUnknownYearAndDuplicateCode(t *testing.T) {
	f, svc, y := setup(t)
	_, err := svc.Create(modtest.In(f.Alpha), f.Actor, tm("00000000-0000-0000-0000-000000000000", "T1", "2026-04-01", "2026-09-30"))
	if field, code := fieldOf(t, err); field != "yearId" || code != "not_found" {
		t.Errorf("%s/%s", field, code)
	}
	_, err = svc.Create(modtest.In(f.Alpha), f.Actor, tm("nope", "T1", "2026-04-01", "2026-09-30"))
	if field, code := fieldOf(t, err); field != "yearId" || code != "required" {
		t.Errorf("%s/%s", field, code)
	}
	svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "T1", "2026-04-01", "2026-06-30"))
	_, err = svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "t1", "2026-07-01", "2026-09-30"))
	if field, code := fieldOf(t, err); field != "code" || code != "duplicate" {
		t.Errorf("%s/%s", field, code)
	}
}

func TestAnotherInstituteCannotUseThisYear(t *testing.T) {
	f, svc, y := setup(t)
	_, err := svc.Create(modtest.In(f.Bravo), f.Actor, tm(y.ID, "T1", "2026-04-01", "2026-09-30"))
	if field, code := fieldOf(t, err); field != "yearId" || code != "not_found" {
		t.Fatalf("a term must not attach to another institute's year: %v", err)
	}
}

func TestValidationAndFilterByYear(t *testing.T) {
	f, svc, y := setup(t)
	_, err := svc.Create(modtest.In(f.Alpha), f.Actor, terms.Input{YearID: y.ID, Code: "T", Title: "t", StartDate: "2026-05-01", EndDate: "2026-04-01"})
	if _, code := fieldOf(t, err); code != "before_start" {
		t.Errorf("%v", err)
	}
	bad := "red"
	in := tm(y.ID, "T", "2026-04-01", "2026-05-01")
	in.Color = &bad
	if _, err := svc.Create(modtest.In(f.Alpha), f.Actor, in); err == nil {
		t.Error("an invalid colour was accepted")
	}
	svc.Create(modtest.In(f.Alpha), f.Actor, tm(y.ID, "T1", "2026-04-01", "2026-06-30"))
	q, _ := crud.ParseList(1, 50, "", "", terms.SortKeys, terms.DefaultSort)
	if p, _ := svc.List(modtest.In(f.Alpha), q, terms.Filter{YearID: y.ID}); p.Total != 1 {
		t.Errorf("filter by year: %d", p.Total)
	}
	if p, _ := svc.List(modtest.In(f.Alpha), q, terms.Filter{YearID: "00000000-0000-0000-0000-000000000000"}); p.Total != 0 {
		t.Errorf("filter by another year: %d", p.Total)
	}
}
