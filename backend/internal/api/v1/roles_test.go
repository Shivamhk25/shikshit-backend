package apiv1_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type roleOut struct {
	ID              string `json:"id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	IsSystem        bool   `json:"isSystem"`
	HolderCount     int    `json:"holderCount"`
	PermissionCount int    `json:"permissionCount"`
	Grants          []struct {
		Permission string `json:"permission"`
		Scope      string `json:"scope"`
	} `json:"grants"`
}

type itemsOut[T any] struct {
	Items []T `json:"items"`
}

func roleBody(name string, grants ...[2]string) string {
	type g struct {
		Permission string `json:"permission"`
		Scope      string `json:"scope"`
	}
	list := []g{}
	for _, x := range grants {
		list = append(list, g{x[0], x[1]})
	}
	b, _ := json.Marshal(map[string]any{"name": name, "grants": list})
	return string(b)
}

func rolesBody(ids ...string) string {
	b, _ := json.Marshal(map[string]any{"roleIds": append([]string{}, ids...)})
	return string(b)
}

func (s usersStack) roleByCode(t *testing.T, token, code string) roleOut {
	t.Helper()
	for _, r := range decode[itemsOut[roleOut]](t, s.do(http.MethodGet, alphaHost, "/roles", token, "")).Items {
		if r.Code == code {
			return r
		}
	}
	t.Fatalf("role %s not listed", code)
	return roleOut{}
}

func TestRolesCatalogAndList(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	plain := s.signIn(t, alphaHost, "plain", password)

	want(t, s.do(http.MethodGet, alphaHost, "/roles", plain, ""), http.StatusForbidden, "")
	cat := decode[itemsOut[struct{ Code, Module string }]](t, s.do(http.MethodGet, alphaHost, "/permissions/catalog", admin, ""))
	modules := map[string]bool{}
	for _, p := range cat.Items {
		modules[p.Module] = true
	}
	// Only the modules this institute has enabled are offered.
	if !modules["iam"] || !modules["stu"] || modules["fee"] || modules["aca"] {
		t.Fatalf("catalog modules = %v", modules)
	}
	a := s.roleByCode(t, admin, "admin")
	if !a.IsSystem || a.HolderCount != 1 || a.PermissionCount != len(cat.Items) {
		t.Fatalf("admin role = %+v, catalog %d", a, len(cat.Items))
	}
}

func TestRolesCreateEditDelete(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)

	rec := s.do(http.MethodPost, alphaHost, "/roles", admin, roleBody("Office clerk", [2]string{"stu.students.view", "tenant"}, [2]string{"stu.students.edit", "tenant"}))
	want(t, rec, http.StatusCreated, "")
	clerk := decode[roleOut](t, rec)
	if clerk.Code != "office_clerk" || clerk.IsSystem || len(clerk.Grants) != 2 {
		t.Fatalf("created = %+v", clerk)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/roles", admin, roleBody("OFFICE CLERK")), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/roles", admin, roleBody("Bad", [2]string{"fee.payments.view", "tenant"})), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/roles", admin, roleBody("Bad", [2]string{"stu.students.view", "everywhere"})), http.StatusBadRequest, "validation_failed")

	// Give the role to "plain": their access changes on the next request, and again when the role changes.
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", admin, rolesBody(clerk.ID)), http.StatusOK, "")
	plain := s.signIn(t, alphaHost, "plain", password)
	if body := s.do(http.MethodGet, alphaHost, "/me/access", plain, "").Body.String(); !strings.Contains(body, `"stu.students.edit"`) {
		t.Fatalf("plain access after role = %s", body)
	}
	want(t, s.do(http.MethodPatch, alphaHost, "/roles/"+clerk.ID, admin, roleBody("Office clerk", [2]string{"stu.students.view", "tenant"})), http.StatusOK, "")
	if body := s.do(http.MethodGet, alphaHost, "/me/access", plain, "").Body.String(); strings.Contains(body, `"stu.students.edit"`) || !strings.Contains(body, `"stu.students.view"`) {
		t.Fatalf("plain access after edit = %s", body)
	}

	// Built-in roles are read-only; a role somebody holds cannot be deleted.
	adminRole := s.roleByCode(t, admin, "admin")
	want(t, s.do(http.MethodPatch, alphaHost, "/roles/"+adminRole.ID, admin, roleBody("Boss")), http.StatusConflict, "system_role")
	want(t, s.do(http.MethodDelete, alphaHost, "/roles/"+adminRole.ID, admin, ""), http.StatusConflict, "system_role")
	want(t, s.do(http.MethodDelete, alphaHost, "/roles/"+clerk.ID, admin, ""), http.StatusConflict, "role_in_use")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", admin, rolesBody()), http.StatusOK, "")
	want(t, s.do(http.MethodDelete, alphaHost, "/roles/"+clerk.ID, admin, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, alphaHost, "/roles/"+clerk.ID, admin, ""), http.StatusNotFound, "")

	// Cloning the Admin role gives an editable copy with every permission.
	rec = s.do(http.MethodPost, alphaHost, "/roles/"+adminRole.ID+"/clone", admin, `{"name":"Deputy admin"}`)
	want(t, rec, http.StatusCreated, "")
	if c := decode[roleOut](t, rec); c.IsSystem || c.PermissionCount != adminRole.PermissionCount {
		t.Fatalf("clone = %+v, admin has %d", c, adminRole.PermissionCount)
	}

	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'roles.update' AND before::text LIKE '%stu.students.edit%' AND after::text NOT LIKE '%stu.students.edit%'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("roles.update audit with before/after = %d, %v", n, err)
	}
}

func TestRolesCannotGrantWhatYouDoNotHold(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	mgr := s.signIn(t, alphaHost, "rolemgr", password)

	// Not held at all, or held only for a class section and granted for the whole school: refused.
	want(t, s.do(http.MethodPost, alphaHost, "/roles", mgr, roleBody("Editors", [2]string{"stu.students.edit", "tenant"})), http.StatusForbidden, "cannot_grant")
	want(t, s.do(http.MethodPost, alphaHost, "/roles", mgr, roleBody("Viewers", [2]string{"stu.students.view", "tenant"})), http.StatusForbidden, "cannot_grant")
	// Within what they hold: allowed.
	rec := s.do(http.MethodPost, alphaHost, "/roles", mgr, roleBody("Section viewers", [2]string{"stu.students.view", "class_section"}))
	want(t, rec, http.StatusCreated, "")
	viewers := decode[roleOut](t, rec)

	// A role made by the Admin with more than the manager holds: the manager may rename it or remove grants, but
	// cannot add a grant they do not hold.
	clerk := decode[roleOut](t, s.do(http.MethodPost, alphaHost, "/roles", admin, roleBody("Clerk", [2]string{"stu.students.edit", "tenant"})))
	want(t, s.do(http.MethodPatch, alphaHost, "/roles/"+clerk.ID, mgr, roleBody("Office clerk", [2]string{"stu.students.edit", "tenant"})), http.StatusOK, "")
	want(t, s.do(http.MethodPatch, alphaHost, "/roles/"+clerk.ID, mgr, roleBody("Office clerk", [2]string{"stu.students.edit", "tenant"}, [2]string{"stu.students.delete", "tenant"})), http.StatusForbidden, "cannot_grant")
	want(t, s.do(http.MethodPatch, alphaHost, "/roles/"+viewers.ID, mgr, roleBody("Section viewers", [2]string{"stu.students.view", "tenant"})), http.StatusForbidden, "cannot_grant")

	// Giving roles: the manager cannot hand out the Admin role or a role wider than their own, to others or
	// to themselves; a role within their own access is fine.
	adminRole := s.roleByCode(t, admin, "admin")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", mgr, rolesBody(adminRole.ID)), http.StatusForbidden, "cannot_grant")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["rolemgr"]+"/roles", mgr, rolesBody(adminRole.ID)), http.StatusForbidden, "cannot_grant")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", mgr, rolesBody(clerk.ID)), http.StatusForbidden, "cannot_grant")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", mgr, rolesBody(viewers.ID)), http.StatusOK, "")
	got := decode[itemsOut[roleOut]](t, s.do(http.MethodGet, alphaHost, "/users/"+s.ids["plain"]+"/roles", mgr, ""))
	if len(got.Items) != 1 || got.Items[0].ID != viewers.ID {
		t.Fatalf("plain roles = %+v", got.Items)
	}
}

func TestRolesLastAdminKeepsTheRole(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	adminRole := s.roleByCode(t, admin, "admin")

	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["admin"]+"/roles", admin, rolesBody()), http.StatusConflict, "last_admin")
	// With a second active Admin it is allowed, and the Admin can give the Admin role.
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["plain"]+"/roles", admin, rolesBody(adminRole.ID)), http.StatusOK, "")
	want(t, s.do(http.MethodPut, alphaHost, "/users/"+s.ids["admin"]+"/roles", admin, rolesBody()), http.StatusOK, "")
	// The roles of another institute's user do not exist from here.
	want(t, s.do(http.MethodGet, alphaHost, "/users/"+s.ids["betaadmin"]+"/roles", s.signIn(t, alphaHost, "plain", password), ""), http.StatusNotFound, "")
}
