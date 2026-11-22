package authz

import "testing"

func TestCanGrant(t *testing.T) {
	catalog := []string{"fee.payments.view", "fee.payments.create", "stu.students.view", "stu.students.edit", "att.register.edit"}
	admin := Compute(Inputs{Catalog: catalog, EnabledModules: mods("fee", "stu", "att"), Grants: []Grant{{"*.*.*", ScopeTenant}}})
	teacher := Compute(Inputs{Catalog: catalog, EnabledModules: mods("fee", "stu", "att"),
		Grants:      []Grant{{"stu.students.view", "class_section"}, {"att.register.edit", ScopeSelf}},
		Assignments: []Assignment{{"class_section", "cs1"}}})
	unassigned := Compute(Inputs{Catalog: catalog, EnabledModules: mods("stu"), Grants: []Grant{{"stu.students.view", "class_section"}}})

	for _, tc := range []struct {
		name  string
		e     Effective
		code  string
		scope string
		want  bool
	}{
		{"admin grants anything at any scope", admin, "fee.payments.create", ScopeTenant, true},
		{"admin grants a narrow scope", admin, "stu.students.view", "class_section", true},
		{"not held", teacher, "fee.payments.view", ScopeTenant, false},
		{"held narrowly, granted narrowly", teacher, "stu.students.view", "class_section", true},
		{"held narrowly, granted widely", teacher, "stu.students.view", ScopeTenant, false},
		{"held narrowly, granted at another narrow kind", teacher, "stu.students.view", "own_children", false},
		{"view held, edit not", teacher, "stu.students.edit", "class_section", false},
		{"self covers self only", teacher, "att.register.edit", ScopeSelf, true},
		{"self does not cover tenant", teacher, "att.register.edit", ScopeTenant, false},
		{"held at a scope with nothing assigned: refused", unassigned, "stu.students.view", "class_section", false},
		{"disabled module is not held", Compute(Inputs{Catalog: catalog, EnabledModules: mods("stu"), Grants: []Grant{{"*.*.*", ScopeTenant}}}), "fee.payments.view", ScopeTenant, false},
	} {
		if got := tc.e.CanGrant(tc.code, tc.scope); got != tc.want {
			t.Errorf("%s: CanGrant(%s, %s) = %v, want %v", tc.name, tc.code, tc.scope, got, tc.want)
		}
	}
}
