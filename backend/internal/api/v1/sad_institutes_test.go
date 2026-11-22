package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const console = "console.shikshit.test"

type instituteOut struct {
	ID              string   `json:"id"`
	Code            string   `json:"code"`
	Status          string   `json:"status"`
	PlanCode        *string  `json:"planCode"`
	Modules         []string `json:"modules"`
	WentLiveAt      *string  `json:"wentLiveAt"`
	AdminUsername   string   `json:"adminUsername"`
	AdminInviteLink string   `json:"adminInviteLink"`
}

func (s usersStack) platformToken(t *testing.T) string {
	t.Helper()
	s.addPlatformUser(t)
	tok, _, _ := s.platformSignIn(t, "")
	return tok
}

func newInstituteBody(code, plan, adminUser string) string {
	return fmt.Sprintf(`{"code":%q,"name":"Gamma Public School","planCode":%q,"admin":{"username":%q,"displayName":"Meena Rao","email":"meena@gamma.test"}}`, code, plan, adminUser)
}

func TestTheWizardCreatesAWholeInstitute(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)

	rec := s.do(http.MethodPost, console, "/platform/institutes", tok, newInstituteBody("Gamma", "standard", "meena"))
	want(t, rec, http.StatusCreated, "")
	got := decode[instituteOut](t, rec)
	if got.Code != "gamma" || got.Status != "onboarding" || got.PlanCode == nil || *got.PlanCode != "standard" {
		t.Fatalf("created = %+v", got)
	}
	if !strings.HasPrefix(got.AdminInviteLink, "/accept-invite?token=") || got.AdminUsername != "meena" {
		t.Fatalf("admin = %q %q", got.AdminUsername, got.AdminInviteLink)
	}
	var planModules int
	if err := s.adm.QueryRow(bg, `SELECT cardinality(modules) FROM plans WHERE code = 'standard'`).Scan(&planModules); err != nil {
		t.Fatal(err)
	}
	if len(got.Modules) != planModules {
		t.Fatalf("modules %v, plan has %d", got.Modules, planModules)
	}

	// the first user is an invited Admin of the new institute, with a pending invite, and roles exist
	var status string
	var isAdmin, invites, roles int
	if err := s.adm.QueryRow(bg, `SELECT u.status,
			(SELECT count(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = u.id AND r.code = 'admin'),
			(SELECT count(*) FROM user_invites i WHERE i.user_id = u.id AND i.used_at IS NULL),
			(SELECT count(*) FROM roles r WHERE r.tenant_id = u.tenant_id)
		FROM users u WHERE u.tenant_id = $1::uuid AND u.username = 'meena'`, got.ID).Scan(&status, &isAdmin, &invites, &roles); err != nil {
		t.Fatal(err)
	}
	if status != "invited" || isAdmin != 1 || invites != 1 || roles < 10 {
		t.Fatalf("admin user: status %s, admin %d, invites %d, roles %d", status, isAdmin, invites, roles)
	}

	// audit: one row, as the platform user, without the invite token
	var actorType, after string
	if err := s.adm.QueryRow(bg, `SELECT actor_type, after::text FROM audit_log WHERE tenant_id = $1::uuid AND action = 'institutes.create'`, got.ID).Scan(&actorType, &after); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(got.AdminInviteLink, "/accept-invite?token=")
	if actorType != "platform_user" || strings.Contains(after, token) {
		t.Fatalf("audit actor %s, after %s", actorType, after)
	}

	// the directory finds it
	rec = s.do(http.MethodGet, console, "/platform/institutes?q=gamma", tok, "")
	want(t, rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), got.ID) {
		t.Fatalf("directory: %s", rec.Body)
	}
	rec = s.do(http.MethodGet, console, "/platform/institutes?status=live", tok, "")
	want(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), got.ID) {
		t.Fatalf("status filter: an onboarding institute listed as live")
	}
}

func TestWizardRulesAndAllOrNothing(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	count := func() int {
		var n int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM tenants`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for _, c := range []struct{ body, field string }{
		{newInstituteBody("alpha", "standard", "meena"), "code"},     // taken
		{newInstituteBody("admin", "standard", "meena"), "code"},     // reserved
		{newInstituteBody("Bad Code!", "standard", "meena"), "code"}, // pattern
		{newInstituteBody("gamma", "gold", "meena"), "planCode"},
		{newInstituteBody("gamma", "standard", "m"), "admin.username"},
	} {
		rec := s.do(http.MethodPost, console, "/platform/institutes", tok, c.body)
		want(t, rec, http.StatusBadRequest, "validation_failed")
		if !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Fatalf("want an error on %s: %s", c.field, rec.Body)
		}
	}
	// a failure half way (no Admin role to give) leaves nothing behind
	if _, err := s.adm.Exec(bg, `UPDATE role_templates SET is_active = false WHERE code = 'admin'`); err != nil {
		t.Fatal(err)
	}
	rec := s.do(http.MethodPost, console, "/platform/institutes", tok, newInstituteBody("gamma", "standard", "meena"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if after := count(); after != before {
		t.Fatalf("tenants %d -> %d: a failed wizard left an institute behind", before, after)
	}
}

func TestInstituteStatusFollowsItsLifecycle(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	id := decode[instituteOut](t, s.do(http.MethodPost, console, "/platform/institutes", tok, newInstituteBody("gamma", "standard", "meena"))).ID
	status := func(to string) *instituteOut {
		rec := s.do(http.MethodPost, console, "/platform/institutes/"+id+"/status", tok, `{"status":"`+to+`"}`)
		if rec.Code != http.StatusOK {
			want(t, rec, http.StatusConflict, "invalid_state")
			return nil
		}
		out := decode[instituteOut](t, rec)
		return &out
	}
	if status("suspended") != nil {
		t.Fatal("onboarding -> suspended must be refused")
	}
	live := status("live")
	if live == nil || live.WentLiveAt == nil {
		t.Fatalf("go live: %+v", live)
	}
	if status("archived") != nil {
		t.Fatal("live -> archived must be refused (suspend first)")
	}
	if status("suspended") == nil || status("live") == nil || status("suspended") == nil || status("archived") == nil {
		t.Fatal("suspend, reactivate, suspend and archive must work")
	}
	if status("live") != nil {
		t.Fatal("archived is final")
	}
	want(t, s.do(http.MethodPost, console, "/platform/institutes/00000000-0000-0000-0000-000000000000/status", tok, `{"status":"live"}`), http.StatusNotFound, "not_found")
}

func TestModulesAreChosenFromThePlansAndReachTheInstitute(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	alpha := s.tenant["alpha"]
	admin := s.signIn(t, alphaHost, "admin", password)

	want(t, s.do(http.MethodPut, console, "/platform/institutes/"+alpha+"/modules", tok, `{"modules":["stu"]}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPut, console, "/platform/institutes/"+alpha+"/modules", tok, `{"modules":["iam","zzz"]}`), http.StatusBadRequest, "validation_failed")

	version := func() int64 {
		var v int64
		if err := s.adm.QueryRow(bg, `SELECT access_version FROM users WHERE id = $1::uuid`, s.ids["plain"]).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version()
	rec := s.do(http.MethodPut, console, "/platform/institutes/"+alpha+"/modules", tok, `{"modules":["iam","aca"]}`)
	want(t, rec, http.StatusOK, "")
	if after := version(); after <= before {
		t.Fatalf("access version %d -> %d: cached permissions would keep the old modules", before, after)
	}
	if got := decode[instituteOut](t, rec).Modules; strings.Join(got, ",") != "aca,iam" {
		t.Fatalf("modules = %v", got)
	}
	// the institute's users see it on their next request: Students is gone, Academics arrived
	rec = s.do(http.MethodGet, alphaHost, "/me/access", admin, "")
	want(t, rec, http.StatusOK, "")
	body := rec.Body.String()
	if strings.Contains(body, `"stu"`) || !strings.Contains(body, `"aca"`) {
		t.Fatalf("access after module change: %s", body)
	}
	// beta is untouched
	var betaModules string
	if err := s.adm.QueryRow(bg, `SELECT string_agg(module_code, ',' ORDER BY module_code) FROM tenant_modules WHERE tenant_id = $1 AND enabled`, s.tenant["beta"]).Scan(&betaModules); err != nil {
		t.Fatal(err)
	}
	if betaModules != "iam,stu" {
		t.Fatalf("beta modules = %s", betaModules)
	}
}

func TestOnlyPlatformUsersReachTheConsole(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	for _, path := range []string{"/platform/institutes", "/platform/plans", "/platform/institutes/" + s.tenant["alpha"]} {
		want(t, s.do(http.MethodGet, console, path, admin, ""), http.StatusUnauthorized, "unauthenticated")
		want(t, s.do(http.MethodGet, alphaHost, path, admin, ""), http.StatusUnauthorized, "unauthenticated")
		want(t, s.do(http.MethodGet, console, path, "", ""), http.StatusUnauthorized, "unauthenticated")
	}
	want(t, s.do(http.MethodPost, console, "/platform/institutes", admin, newInstituteBody("gamma", "standard", "meena")), http.StatusUnauthorized, "unauthenticated")
}
