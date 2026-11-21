package classes_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func ip(n int) *int { return &n }

func code(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || len(fe) == 0 {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe[0].Field, fe[0].Code
}

func TestClassLifecycleOrderingAndAgeRules(t *testing.T) {
	f := modtest.New(t)
	svc := classes.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	five, err := svc.Create(ctx, f.Actor, classes.Input{Code: "V", Name: "Class 5", DisplayOrder: 5, YoungestAgeMonths: ip(120), OldestAgeMonths: ip(144)})
	if err != nil || five.Status != "active" || *five.YoungestAgeMonths != 120 {
		t.Fatalf("%+v %v", five, err)
	}
	svc.Create(ctx, f.Actor, classes.Input{Code: "IV", Name: "Class 4", DisplayOrder: 4})
	svc.Create(ctx, f.Actor, classes.Input{Code: "VI", Name: "Class 6", DisplayOrder: 6})

	q, _ := crud.ParseList(1, 50, "", "", classes.SortKeys, classes.DefaultSort)
	p, _ := svc.List(ctx, q, classes.Filter{})
	if p.Total != 3 || p.Items[0].Code != "IV" || p.Items[2].Code != "VI" {
		t.Errorf("school's own order: %+v", p.Items)
	}

	_, err = svc.Create(ctx, f.Actor, classes.Input{Code: "X", Name: "x", YoungestAgeMonths: ip(200), OldestAgeMonths: ip(100)})
	if field, c := code(t, err); field != "oldestAgeMonths" || c != "before_youngest" {
		t.Errorf("%s/%s", field, c)
	}
	_, err = svc.Create(ctx, f.Actor, classes.Input{Code: "X", Name: "x", YoungestAgeMonths: ip(-1)})
	if _, c := code(t, err); c != "out_of_range" {
		t.Errorf("%v", err)
	}
	_, err = svc.Create(ctx, f.Actor, classes.Input{Code: "v", Name: "dup"})
	if field, c := code(t, err); field != "code" || c != "duplicate" {
		t.Errorf("%s/%s", field, c)
	}
	if _, err := svc.Update(ctx, f.Actor, five.ID, classes.Input{Code: "V", Name: "Fifth", DisplayOrder: 5}); err != nil {
		t.Errorf("update keeping its own code: %v", err)
	}
	if err := svc.Delete(ctx, f.Actor, five.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, five.ID); !errors.Is(err, classes.ErrNotFound) {
		t.Errorf("%v", err)
	}
	if _, err := svc.Get(modtest.In(f.Bravo), five.ID); !errors.Is(err, classes.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
}
