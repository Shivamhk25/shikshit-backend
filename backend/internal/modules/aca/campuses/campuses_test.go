package campuses_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/campuses"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func f64(v float64) *float64 { return &v }
func sp(s string) *string    { return &s }

func TestCampusRules(t *testing.T) {
	f := modtest.New(t)
	svc := campuses.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	ok, err := svc.Create(ctx, f.Actor, campuses.Input{Name: "Main", CheckInRadiusM: 200, Latitude: f64(28.6139), Longitude: f64(77.209), StaffDueIn: sp("08:30")})
	if err != nil || *ok.Latitude != 28.6139 || *ok.StaffDueIn != "08:30" {
		t.Fatalf("%+v %v", ok, err)
	}
	if _, err := svc.Create(ctx, f.Actor, campuses.Input{Name: "No map", CheckInRadiusM: 50}); err != nil {
		t.Errorf("a campus without a position: %v", err)
	}
	for name, in := range map[string]campuses.Input{
		"lat without long": {Name: "x", CheckInRadiusM: 50, Latitude: f64(10)},
		"long without lat": {Name: "x", CheckInRadiusM: 50, Longitude: f64(10)},
		"lat too big":      {Name: "x", CheckInRadiusM: 50, Latitude: f64(91), Longitude: f64(0)},
		"long too big":     {Name: "x", CheckInRadiusM: 50, Latitude: f64(0), Longitude: f64(181)},
		"radius too small": {Name: "x", CheckInRadiusM: 5},
		"radius too big":   {Name: "x", CheckInRadiusM: 5001},
		"bad time":         {Name: "x", CheckInRadiusM: 50, StaffDueIn: sp("8:30am")},
		"time out of day":  {Name: "x", CheckInRadiusM: 50, StaffDueIn: sp("25:00")},
		"no name":          {CheckInRadiusM: 50},
	} {
		var fe crud.FieldErrors
		if _, err := svc.Create(ctx, f.Actor, in); !errors.As(err, &fe) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestCampusCodesAreOptionalButUniqueWhenGiven(t *testing.T) {
	f := modtest.New(t)
	svc := campuses.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	for i := 0; i < 2; i++ {
		if _, err := svc.Create(ctx, f.Actor, campuses.Input{Name: "no code", CheckInRadiusM: 50}); err != nil {
			t.Fatalf("campuses without a code never collide: %v", err)
		}
	}
	svc.Create(ctx, f.Actor, campuses.Input{Code: sp("N"), Name: "North", CheckInRadiusM: 50})
	var fe crud.FieldErrors
	if _, err := svc.Create(ctx, f.Actor, campuses.Input{Code: sp("n"), Name: "again", CheckInRadiusM: 50}); !errors.As(err, &fe) || fe[0].Code != "duplicate" {
		t.Errorf("%v", err)
	}
	if _, err := svc.Create(modtest.In(f.Bravo), f.Actor, campuses.Input{Code: sp("N"), Name: "other school", CheckInRadiusM: 50}); err != nil {
		t.Errorf("codes are per institute: %v", err)
	}
}
