package approvals

import "testing"

func TestRegisterRefusesBadTypes(t *testing.T) {
	ok := Type{Key: "fee.concession", Title: "Fee concession", Steps: []Step{{Name: "Review", Permission: "fee.concessions.approve"}}}
	cases := map[string]func(Type) Type{
		"bad key":      func(x Type) Type { x.Key = "Fee Concession"; return x },
		"empty key":    func(x Type) Type { x.Key = ""; return x },
		"no title":     func(x Type) Type { x.Title = ""; return x },
		"no steps":     func(x Type) Type { x.Steps = nil; return x },
		"no step name": func(x Type) Type { x.Steps = []Step{{Permission: "a.b.c"}}; return x },
		"no step perm": func(x Type) Type { x.Steps = []Step{{Name: "Review"}}; return x },
	}
	for name, mutate := range cases {
		if err := NewRegistry().Register(mutate(ok)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	r := NewRegistry()
	if err := r.Register(ok); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ok); err == nil {
		t.Error("a duplicate key was accepted")
	}
	// the registry keeps its own copy of the steps
	steps := []Step{{Name: "A", Permission: "a.b.c"}}
	r2 := NewRegistry()
	if err := r2.Register(Type{Key: "x", Title: "X", Steps: steps}); err != nil {
		t.Fatal(err)
	}
	steps[0].Permission = "changed.after.register"
	if got, _ := r2.Get("x"); got.Steps[0].Permission != "a.b.c" {
		t.Error("the registry shares the caller's slice")
	}
}
