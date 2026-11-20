package electives_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/electives"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func TestElectiveGroupLimits(t *testing.T) {
	f := modtest.New(t)
	svc := electives.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	g, err := svc.Create(ctx, f.Actor, electives.Input{Name: "Languages", MinSubjects: 1, MaxSubjects: 2, EnforceLimits: true})
	if err != nil || !g.EnforceLimits || g.Status != "active" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := svc.Create(ctx, f.Actor, electives.Input{Name: "Exactly one", MinSubjects: 1, MaxSubjects: 1}); err != nil {
		t.Errorf("min equal to max: %v", err)
	}
	for name, in := range map[string]electives.Input{
		"min above max": {Name: "x", MinSubjects: 3, MaxSubjects: 2},
		"max zero":      {Name: "x", MinSubjects: 0, MaxSubjects: 0},
		"max too big":   {Name: "x", MinSubjects: 0, MaxSubjects: 51},
		"no name":       {MinSubjects: 0, MaxSubjects: 1},
	} {
		var fe crud.FieldErrors
		if _, err := svc.Create(ctx, f.Actor, in); !errors.As(err, &fe) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, err := svc.Get(modtest.In(f.Bravo), g.ID); !errors.Is(err, electives.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
}
