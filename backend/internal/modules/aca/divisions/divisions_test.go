package divisions_test

import (
	"errors"
	"os"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/divisions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func fieldOf(t *testing.T, err error) (string, string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || len(fe) == 0 {
		t.Fatalf("want field errors, got %v", err)
	}
	return fe[0].Field, fe[0].Code
}

func TestDivisionsAndTheClassesThatBelongToThem(t *testing.T) {
	f := modtest.New(t)
	ctx := modtest.In(f.Alpha)
	ds, cs := divisions.Service{DB: f.App}, classes.Service{DB: f.App}

	d, err := ds.Create(ctx, f.Actor, divisions.Input{Code: "PRI", Name: "Primary", DisplayOrder: 1})
	if err != nil || d.Status != "active" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := ds.Create(ctx, f.Actor, divisions.Input{Code: "pri", Name: "again"}); err == nil {
		t.Error("duplicate code accepted")
	} else if field, code := fieldOf(t, err); field != "code" || code != "duplicate" {
		t.Errorf("%s/%s", field, code)
	}

	c, err := cs.Create(ctx, f.Actor, classes.Input{Code: "V", Name: "Class 5", DivisionID: &d.ID})
	if err != nil || c.DivisionName == nil || *c.DivisionName != "Primary" {
		t.Fatalf("class shows its division: %+v %v", c, err)
	}

	var inUse resource.InUseError
	if err := ds.Delete(ctx, f.Actor, d.ID); !errors.As(err, &inUse) {
		t.Fatalf("a division with classes was deleted: %v", err)
	}
	if err := cs.Delete(ctx, f.Actor, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := ds.Delete(ctx, f.Actor, d.ID); err != nil {
		t.Fatalf("once its classes are gone: %v", err)
	}
}

func TestAClassCannotUseAnotherInstitutesDivision(t *testing.T) {
	f := modtest.New(t)
	d, _ := divisions.Service{DB: f.App}.Create(modtest.In(f.Alpha), f.Actor, divisions.Input{Code: "PRI", Name: "Primary"})
	_, err := classes.Service{DB: f.App}.Create(modtest.In(f.Bravo), f.Actor, classes.Input{Code: "V", Name: "Class 5", DivisionID: &d.ID})
	if field, code := fieldOf(t, err); field != "divisionId" || code != "not_found" {
		t.Fatalf("%s/%s: %v", field, code, err)
	}
	bad := "not-a-uuid"
	_, err = classes.Service{DB: f.App}.Create(modtest.In(f.Alpha), f.Actor, classes.Input{Code: "V", Name: "x", DivisionID: &bad})
	if field, _ := fieldOf(t, err); field != "divisionId" {
		t.Errorf("%v", err)
	}
}
