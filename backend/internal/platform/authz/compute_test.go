package authz

import (
	"reflect"
	"testing"
)

var catalog = []string{
	"att.register.view", "att.register.edit",
	"fee.payments.view", "fee.payments.create",
	"onb.workspace.view",
}

func mods(m ...string) map[string]bool {
	out := map[string]bool{}
	for _, x := range m {
		out[x] = true
	}
	return out
}

func TestAdminWildcardExpandsToEveryEnabledPermission(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("att", "fee", "onb"), Grants: []Grant{{"*.*.*", ScopeTenant}}})
	if len(e.Permissions) != len(catalog) {
		t.Fatalf("permissions = %v", e.Permissions)
	}
	if !reflect.DeepEqual(e.Scopes["fee.payments"], []string{"tenant"}) {
		t.Errorf("scopes = %v", e.Scopes)
	}
}

func TestDisabledModuleRemovesPermissionsEvenForAdmin(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("att"), Grants: []Grant{{"*.*.*", ScopeTenant}}})
	if !reflect.DeepEqual(e.Permissions, []string{"att.register.edit", "att.register.view"}) {
		t.Fatalf("permissions = %v", e.Permissions)
	}
	if _, ok := e.Scopes["fee.payments"]; ok {
		t.Error("scope entry survived for a disabled module")
	}
}

func TestDenyOverrideBeatsEveryGrant(t *testing.T) {
	e := Compute(Inputs{
		Catalog: catalog, EnabledModules: mods("fee"),
		Grants:    []Grant{{"fee.*.*", ScopeTenant}},
		Overrides: []Override{{"fee.payments.create", "deny", ScopeTenant}, {"fee.payments.create", "allow", ScopeTenant}},
	})
	if e.Can("fee.payments.create") || !e.Can("fee.payments.view") {
		t.Fatalf("permissions = %v", e.Permissions)
	}
}

func TestDenyWildcardRemovesWholeResource(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("fee", "att"), Grants: []Grant{{"*.*.*", ScopeTenant}}, Overrides: []Override{{"fee", "deny", ScopeTenant}}})
	for _, p := range e.Permissions {
		if p[:3] == "fee" {
			t.Fatalf("%s survived a module-wide deny", p)
		}
	}
}

func TestAllowOverrideAddsAPermission(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("fee"), Overrides: []Override{{"fee.payments.view", "allow", ScopeTenant}}})
	if !reflect.DeepEqual(e.Permissions, []string{"fee.payments.view"}) {
		t.Fatalf("permissions = %v", e.Permissions)
	}
}

func TestNoGrantNoAccess(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("att", "fee", "onb")})
	if len(e.Permissions) != 0 || len(e.Scopes) != 0 {
		t.Fatalf("got %+v", e)
	}
	if e.Permissions == nil || e.Scopes == nil {
		t.Error("empty results must be empty, not nil, so they encode as [] and {}")
	}
}

func TestAssignmentScopesExpandToRecords(t *testing.T) {
	e := Compute(Inputs{
		Catalog: catalog, EnabledModules: mods("att"),
		Grants:      []Grant{{"att.*.*", "class_section"}},
		Assignments: []Assignment{{"class_section", "12"}, {"class_section", "14"}, {"campus", "main"}},
	})
	if got := e.Scopes["att.register"]; !reflect.DeepEqual(got, []string{"class_section:12", "class_section:14"}) {
		t.Fatalf("scopes = %v", e.Scopes)
	}
}

func TestAssignmentScopeWithoutAssignmentsMeansNoRecords(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("att"), Grants: []Grant{{"att.*.*", "class_section"}}})
	if !e.Can("att.register.view") {
		t.Fatal("the permission itself is still granted")
	}
	got, ok := e.Scopes["att.register"]
	if !ok || len(got) != 0 {
		t.Fatalf("want an empty (not missing) scope list, got %v present=%v", got, ok)
	}
}

func TestScopesUnionAcrossRoles(t *testing.T) {
	e := Compute(Inputs{
		Catalog: catalog, EnabledModules: mods("att"),
		Grants:      []Grant{{"att.*.view", ScopeTenant}, {"att.*.*", "class_section"}, {"att.register.edit", ScopeSelf}},
		Assignments: []Assignment{{"class_section", "7"}},
	})
	want := []string{"class_section:7", "self", "tenant"}
	if got := e.Scopes["att.register"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
}

func TestPermissionsAreSortedAndUnique(t *testing.T) {
	e := Compute(Inputs{Catalog: catalog, EnabledModules: mods("att", "fee", "onb"), Grants: []Grant{{"*.*.*", ScopeTenant}, {"fee", ScopeTenant}}})
	for i := 1; i < len(e.Permissions); i++ {
		if e.Permissions[i-1] >= e.Permissions[i] {
			t.Fatalf("not strictly sorted: %v", e.Permissions)
		}
	}
}

func TestScreensFollowPermissionAndModule(t *testing.T) {
	with := Compute(Inputs{Catalog: catalog, EnabledModules: mods("onb"), Grants: []Grant{{"onb.*.*", ScopeTenant}}})
	ids := func(ss []Screen) []string {
		out := []string{}
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(Visible(with, mods("onb"))); !reflect.DeepEqual(got, []string{"home", "onboarding", "profile"}) {
		t.Errorf("with permission: %v", got)
	}
	if got := ids(Visible(with, mods())); !reflect.DeepEqual(got, []string{"home", "profile"}) {
		t.Errorf("module disabled: %v", got)
	}
	none := Compute(Inputs{Catalog: catalog, EnabledModules: mods("onb")})
	if got := ids(Visible(none, mods("onb"))); !reflect.DeepEqual(got, []string{"home", "profile"}) {
		t.Errorf("no permission: %v", got)
	}
}

func TestMobileTabsAreCappedAtFive(t *testing.T) {
	var vis []Screen
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		vis = append(vis, Screen{ID: id, MobileTab: true})
	}
	vis = append(vis, Screen{ID: "web-only"})
	got := MobileTabs(vis)
	if len(got) != MaxMobileTabs || got[0] != "a" {
		t.Fatalf("tabs = %v", got)
	}
	if len(MobileTabs(nil)) != 0 || MobileTabs(nil) == nil {
		t.Error("no tabs must be an empty, non-nil slice")
	}
}

func TestAScreenForPersonalDataNeedsTheWholeInstituteScope(t *testing.T) {
	grant := func(scope string) Effective {
		return Compute(Inputs{
			Catalog:        []string{"stu.students.view", "stu.student_addresses.view"},
			EnabledModules: mods("stu"),
			Grants:         []Grant{{"stu.*.view", scope}},
			Assignments:    []Assignment{{"class_section", "12"}},
		})
	}
	ids := func(ss []Screen) []string {
		out := []string{}
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(Visible(grant(ScopeTenant), mods("stu"))); !reflect.DeepEqual(got, []string{"home", "students", "student_enrollments", "guardians", "student_guardians", "student_addresses", "profile"}) {
		t.Errorf("institute-wide: %v", got)
	}
	if got := ids(Visible(grant("class_section"), mods("stu"))); !reflect.DeepEqual(got, []string{"home", "profile"}) {
		t.Errorf("a class-section scope must not be offered the students screens: %v", got)
	}
}
