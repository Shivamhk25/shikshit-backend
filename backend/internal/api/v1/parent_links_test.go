package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// parentFixture adds, in alpha: students asha, ravi (siblings) and meera; guardian "Sunil" linked to asha and ravi,
// guardian "Kavya" linked to meera; and accounts "parent1" and "parent2" with no person link. In beta: a student and
// a guardian linked to them.
type parentFixture struct {
	student, guardian, link map[string]string // name -> id ("asha", "sunil", "sunil-ravi", ...)
}

func (s usersStack) parentFixture(t *testing.T) parentFixture {
	t.Helper()
	f := parentFixture{student: map[string]string{}, guardian: map[string]string{}, link: map[string]string{}}
	one := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := s.adm.QueryRow(bg, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	student := func(tenant, name, code string) {
		f.student[name] = one(`INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth)
			VALUES ($1, $2::text, $3::text, 'female', DATE '2015-04-01') RETURNING id::text`, s.tenant[tenant], code, name)
	}
	guardian := func(tenant, name, phone string) {
		f.guardian[name] = one(`INSERT INTO guardians (tenant_id, name, phone) VALUES ($1, $2::text, $3::text) RETURNING id::text`,
			s.tenant[tenant], name, phone)
	}
	link := func(tenant, g, st string) {
		f.link[g+"-"+st] = one(`INSERT INTO student_guardians (tenant_id, student_id, guardian_id, relation) VALUES ($1, $2::uuid, $3::uuid, 'father') RETURNING id::text`,
			s.tenant[tenant], f.student[st], f.guardian[g])
	}
	student("alpha", "asha", "A-1")
	student("alpha", "ravi", "A-2")
	student("alpha", "meera", "A-3")
	guardian("alpha", "sunil", "9800000001")
	guardian("alpha", "kavya", "9800000002")
	link("alpha", "sunil", "asha")
	link("alpha", "sunil", "ravi")
	link("alpha", "kavya", "meera")
	student("beta", "bina", "B-1")
	guardian("beta", "bharat", "9800000003")
	link("beta", "bharat", "bina")

	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"parent1", "parent2"} {
		s.ids[u] = one(`INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`,
			s.tenant["alpha"], u, hash)
	}
	return f
}

type parentAccountOut struct {
	UserID   string `json:"userId"`
	Children []struct {
		LinkID      string `json:"linkId"`
		StudentName string `json:"studentName"`
		NoAccess    bool   `json:"noAccess"`
	} `json:"children"`
}

type myChildrenOut struct {
	Items []struct {
		Name     string `json:"name"`
		Relation string `json:"relation"`
	} `json:"items"`
}

func (s usersStack) myChildren(t *testing.T, token string) []string {
	t.Helper()
	rec := s.do(http.MethodGet, alphaHost, "/me/children", token, "")
	want(t, rec, http.StatusOK, "")
	var names []string
	for _, c := range decode[myChildrenOut](t, rec).Items {
		names = append(names, c.Name)
	}
	return names
}

func linkBody(userID, guardianID string) string {
	return fmt.Sprintf(`{"userId":%q,"guardianId":%q}`, userID, guardianID)
}

func TestAParentSeesOnlyTheChildrenOfTheirGuardianRecord(t *testing.T) {
	s := newUsersStack(t)
	f := s.parentFixture(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	parent := s.signIn(t, alphaHost, "parent1", password)

	if got := s.myChildren(t, parent); len(got) != 0 {
		t.Fatalf("before any link the account sees %v", got)
	}

	rec := s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["sunil"]))
	want(t, rec, http.StatusOK, "")
	if acc := decode[parentAccountOut](t, rec); len(acc.Children) != 2 {
		t.Fatalf("linked account shows %d children, want 2: %s", len(acc.Children), rec.Body)
	}
	// Another family's account must not widen what this one sees.
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent2"], f.guardian["kavya"])), http.StatusOK, "")
	if got := fmt.Sprint(s.myChildren(t, parent)); got != "[asha ravi]" {
		t.Fatalf("parent sees %s, want [asha ravi] (never meera, never another institute's child)", got)
	}
	// Only a guardian link counts: an account linked as a student to the same id sees nothing.
	if _, err := s.adm.Exec(bg, `UPDATE users SET person_type = 'student', person_id = $2::uuid WHERE id = $1::uuid`, s.ids["plain"], f.guardian["sunil"]); err != nil {
		t.Fatal(err)
	}
	if got := s.myChildren(t, s.signIn(t, alphaHost, "plain", password)); len(got) != 0 {
		t.Fatalf("a student-type account sees %v", got)
	}

	// Custody: the school stops this guardian's account seeing ravi; ravi stays on the guardian's file.
	var before int64
	if err := s.adm.QueryRow(bg, `SELECT access_version FROM users WHERE id = $1::uuid`, s.ids["parent1"]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec = s.do(http.MethodPut, alphaHost, "/parent-links/children/"+f.link["sunil-ravi"]+"/access", admin, `{"noAccess":true}`)
	want(t, rec, http.StatusOK, "")
	if got := fmt.Sprint(s.myChildren(t, parent)); got != "[asha]" {
		t.Fatalf("after no access the parent sees %s, want [asha]", got)
	}
	var after int64
	if err := s.adm.QueryRow(bg, `SELECT access_version FROM users WHERE id = $1::uuid`, s.ids["parent1"]).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("access version %d -> %d: the parent's apps would not refetch", before, after)
	}
	rec = s.do(http.MethodGet, alphaHost, "/parent-links?q=sunil", admin, "")
	want(t, rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), `"noAccess":true`) || !strings.Contains(rec.Body.String(), "ravi") {
		t.Fatalf("the screen must still list ravi, marked no access: %s", rec.Body)
	}

	// Access comes back when the school allows it again.
	want(t, s.do(http.MethodPut, alphaHost, "/parent-links/children/"+f.link["sunil-ravi"]+"/access", admin, `{"noAccess":false}`), http.StatusOK, "")
	if got := fmt.Sprint(s.myChildren(t, parent)); got != "[asha ravi]" {
		t.Fatalf("after allowing again the parent sees %s", got)
	}

	// A removed link, a deleted student and a deleted guardian never count.
	if _, err := s.adm.Exec(bg, `UPDATE student_guardians SET deleted_at = now() WHERE id = $1::uuid`, f.link["sunil-asha"]); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(s.myChildren(t, parent)); got != "[ravi]" {
		t.Fatalf("after the asha link is removed the parent sees %s", got)
	}
	if _, err := s.adm.Exec(bg, `UPDATE students SET deleted_at = now() WHERE id = $1::uuid`, f.student["ravi"]); err != nil {
		t.Fatal(err)
	}
	if got := s.myChildren(t, parent); len(got) != 0 {
		t.Fatalf("after ravi is deleted the parent sees %v", got)
	}
}

func TestADeletedGuardianRecordGivesNoChildren(t *testing.T) {
	s := newUsersStack(t)
	f := s.parentFixture(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	parent := s.signIn(t, alphaHost, "parent1", password)
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["kavya"])), http.StatusOK, "")
	if got := fmt.Sprint(s.myChildren(t, parent)); got != "[meera]" {
		t.Fatalf("parent sees %s", got)
	}
	if _, err := s.adm.Exec(bg, `UPDATE guardians SET deleted_at = now() WHERE id = $1::uuid`, f.guardian["kavya"]); err != nil {
		t.Fatal(err)
	}
	if got := s.myChildren(t, parent); len(got) != 0 {
		t.Fatalf("with the guardian deleted the parent sees %v", got)
	}
}

func TestParentLinkRules(t *testing.T) {
	s := newUsersStack(t)
	f := s.parentFixture(t)
	admin := s.signIn(t, alphaHost, "admin", password)

	// Only staff with the permission manage links; a parent cannot link themselves to a child.
	plain := s.signIn(t, alphaHost, "plain", password)
	want(t, s.do(http.MethodGet, alphaHost, "/parent-links", plain, ""), http.StatusForbidden, "permission_denied")
	parent := s.signIn(t, alphaHost, "parent1", password)
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", parent, linkBody(s.ids["parent1"], f.guardian["kavya"])), http.StatusForbidden, "permission_denied")

	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["sunil"])), http.StatusOK, "")
	// linking again to the same guardian changes nothing
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["sunil"])), http.StatusOK, "")

	// One account per guardian.
	rec := s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent2"], f.guardian["sunil"]))
	want(t, rec, http.StatusBadRequest, "validation_failed")
	if !strings.Contains(rec.Body.String(), "guardianId") || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("want a duplicate error on guardianId: %s", rec.Body)
	}
	// The users screen hits the same rule.
	rec = s.do(http.MethodPatch, alphaHost, "/users/"+s.ids["parent2"], admin, fmt.Sprintf(`{"personType":"guardian","personId":%q}`, f.guardian["sunil"]))
	want(t, rec, http.StatusBadRequest, "validation_failed")

	// An account that belongs to a student cannot become a parent's account.
	if _, err := s.adm.Exec(bg, `UPDATE users SET person_type = 'student', person_id = $2::uuid WHERE id = $1::uuid`, s.ids["plain"], f.student["meera"]); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["plain"], f.guardian["kavya"])), http.StatusConflict, "student_account")

	// Another institute's guardian and an unknown account are not found.
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent2"], f.guardian["bharat"])), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["betaadmin"], f.guardian["kavya"])), http.StatusBadRequest, "validation_failed")

	// Unlink: the account stays and sees nothing; unlinking an account that is not a parent's is 404.
	want(t, s.do(http.MethodDelete, alphaHost, "/parent-links/"+s.ids["parent1"], admin, ""), http.StatusNoContent, "")
	if got := s.myChildren(t, parent); len(got) != 0 {
		t.Fatalf("after unlink the account sees %v", got)
	}
	want(t, s.do(http.MethodDelete, alphaHost, "/parent-links/"+s.ids["parent1"], admin, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, alphaHost, "/parent-links/"+s.ids["plain"], admin, ""), http.StatusNotFound, "not_found")
	// now another account may take the guardian
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent2"], f.guardian["sunil"])), http.StatusOK, "")

	want(t, s.do(http.MethodPut, alphaHost, "/parent-links/children/"+f.link["bharat-bina"]+"/access", admin, `{"noAccess":true}`), http.StatusNotFound, "not_found")
}

func TestParentLinksAreIsolatedPerInstitute(t *testing.T) {
	s := newUsersStack(t)
	f := s.parentFixture(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["sunil"])), http.StatusOK, "")

	beta := s.signIn(t, betaHost, "betaadmin", password)
	rec := s.do(http.MethodGet, betaHost, "/parent-links", beta, "")
	want(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), s.ids["parent1"]) {
		t.Fatalf("beta sees alpha's parent account: %s", rec.Body)
	}
	want(t, s.do(http.MethodPut, betaHost, "/parent-links/children/"+f.link["sunil-asha"]+"/access", beta, `{"noAccess":true}`), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, betaHost, "/parent-links/"+s.ids["parent1"], beta, ""), http.StatusNotFound, "not_found")
}

func TestAParentSeesNoChildWhileStudentsIsSwitchedOff(t *testing.T) {
	s := newUsersStack(t)
	f := s.parentFixture(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	want(t, s.do(http.MethodPost, alphaHost, "/parent-links", admin, linkBody(s.ids["parent1"], f.guardian["sunil"])), http.StatusOK, "")
	if _, err := s.adm.Exec(bg, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'stu'`, s.tenant["alpha"]); err != nil {
		t.Fatal(err)
	}
	parent := s.signIn(t, alphaHost, "parent1", password)
	if got := s.myChildren(t, parent); len(got) != 0 {
		t.Fatalf("with Students off the parent sees %v", got)
	}
}
