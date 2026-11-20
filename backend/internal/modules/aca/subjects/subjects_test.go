package subjects_test

import (
	"errors"
	"os"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/subjects"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func valid(code string) subjects.Input {
	return subjects.Input{Code: code, Name: "Mathematics", Category: "Core", Area: "Academic", Scholastic: true, Credits: 4}
}

func TestSubjectLifecycleAndRules(t *testing.T) {
	f := modtest.New(t)
	svc := subjects.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	color := "#2F5BFF"
	in := valid("MATH")
	in.Color = &color
	in.Credits = 3.54
	s, err := svc.Create(ctx, f.Actor, in)
	if err != nil || s.Credits != 3.5 || !s.Scholastic || s.CoScholastic || *s.Color != color {
		t.Fatalf("credits are kept to one decimal: %+v %v", s, err)
	}
	for name, mod := range map[string]func(*subjects.Input){
		"no category":    func(i *subjects.Input) { i.Category = " " },
		"no area":        func(i *subjects.Input) { i.Area = "" },
		"negative":       func(i *subjects.Input) { i.Credits = -1 },
		"too many":       func(i *subjects.Input) { i.Credits = 100 },
		"bad colour":     func(i *subjects.Input) { c := "blue"; i.Color = &c },
		"duplicate code": func(i *subjects.Input) { i.Code = "math" },
	} {
		in := valid("PHY")
		mod(&in)
		var fe crud.FieldErrors
		if _, err := svc.Create(ctx, f.Actor, in); !errors.As(err, &fe) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	svc.Create(ctx, f.Actor, valid("ART"))
	q, _ := crud.ParseList(1, 50, "-credits,name", "", subjects.SortKeys, subjects.DefaultSort)
	if p, _ := svc.List(ctx, q, subjects.Filter{}); p.Total != 2 || p.Items[0].Code != "ART" {
		t.Errorf("sort by credits desc: %+v", p.Items)
	}
	q, _ = crud.ParseList(1, 50, "", "", subjects.SortKeys, subjects.DefaultSort)
	if p, _ := svc.List(ctx, q, subjects.Filter{Category: "CORE"}); p.Total != 2 {
		t.Errorf("category filter is case-insensitive: %d", p.Total)
	}
	if p, _ := svc.List(ctx, q, subjects.Filter{Category: "other"}); p.Total != 0 {
		t.Errorf("category filter: %d", p.Total)
	}
	if _, err := svc.Get(modtest.In(f.Bravo), s.ID); !errors.Is(err, subjects.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	upd, err := svc.Update(ctx, f.Actor, s.ID, func() subjects.Input { i := valid("MATH"); i.Name = "Maths"; return i }())
	if err != nil || upd.Name != "Maths" {
		t.Errorf("%+v %v", upd, err)
	}
}
