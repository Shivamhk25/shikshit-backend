package sections_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/sections"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func TestSectionLifecycleSearchAndRules(t *testing.T) {
	f := modtest.New(t)
	svc := sections.Service{DB: f.App}
	ctx := modtest.In(f.Alpha)
	short := "  A  "
	a, err := svc.Create(ctx, f.Actor, sections.Input{Code: "A", Name: "Section A", ShortName: &short})
	if err != nil || *a.ShortName != "A" {
		t.Fatalf("%+v %v", a, err)
	}
	svc.Create(ctx, f.Actor, sections.Input{Code: "B", Name: "Section B"})

	q, _ := crud.ParseList(1, 50, "", "section b", sections.SortKeys, sections.DefaultSort)
	if p, _ := svc.List(ctx, q, sections.Filter{}); p.Total != 1 || p.Items[0].Code != "B" {
		t.Errorf("search: %+v", p)
	}
	for name, in := range map[string]sections.Input{
		"no code":    {Name: "x"},
		"no name":    {Code: "Q"},
		"long short": {Code: "Q", Name: "x", ShortName: ptr(strings.Repeat("s", 21))},
		"duplicate":  {Code: "a", Name: "again"},
	} {
		var fe crud.FieldErrors
		if _, err := svc.Create(ctx, f.Actor, in); !errors.As(err, &fe) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, err := svc.Create(modtest.In(f.Bravo), f.Actor, sections.Input{Code: "A", Name: "other school"}); err != nil {
		t.Errorf("codes are per institute: %v", err)
	}
	if err := svc.Delete(ctx, f.Actor, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, f.Actor, sections.Input{Code: "A", Name: "reused"}); err != nil {
		t.Errorf("code reuse after delete: %v", err)
	}
	if f.AuditCount(t, "sections.create") != 4 {
		t.Errorf("audit rows: %d", f.AuditCount(t, "sections.create"))
	}
}

func ptr(s string) *string { return &s }
