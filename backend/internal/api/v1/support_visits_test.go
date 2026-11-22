package apiv1_test

import (
	"net/http"
	"strings"
	"testing"
)

type visitOut struct {
	VisitID       string `json:"visitId"`
	Code          string `json:"code"`
	InstituteCode string `json:"instituteCode"`
}

const visitReason = "Fees report shows the wrong total for class 5"

func (s usersStack) startVisit(t *testing.T, platformTok, tenantID string) visitOut {
	t.Helper()
	rec := s.do(http.MethodPost, console, "/platform/institutes/"+tenantID+"/visits", platformTok, `{"reason":"`+visitReason+`"}`)
	want(t, rec, http.StatusCreated, "")
	return decode[visitOut](t, rec)
}

func (s usersStack) redeem(t *testing.T, host, code string) string {
	t.Helper()
	rec := s.do(http.MethodPost, host, "/auth/support-visit", "", `{"code":"`+code+`"}`)
	want(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), `"refreshToken"`) {
		t.Fatalf("a visit must not get a refresh token: %s", rec.Body)
	}
	return decode[tokenBody](t, rec).AccessToken
}

func TestASupportVisitCanOnlyReadAndEverythingIsAudited(t *testing.T) {
	s := newUsersStack(t)
	ptok := s.platformToken(t)
	want(t, s.do(http.MethodPost, console, "/platform/institutes/"+s.tenant["alpha"]+"/visits", ptok, `{"reason":"too short"}`), http.StatusBadRequest, "validation_failed")

	v := s.startVisit(t, ptok, s.tenant["alpha"])
	if v.InstituteCode != "alpha" {
		t.Fatalf("visit = %+v", v)
	}
	tok := s.redeem(t, alphaHost, v.Code)
	// the code works once
	want(t, s.do(http.MethodPost, alphaHost, "/auth/support-visit", "", `{"code":"`+v.Code+`"}`), http.StatusUnauthorized, "invalid_visit_code")

	// the app gets a banner and only view permissions
	rec := s.do(http.MethodGet, alphaHost, "/me/access", tok, "")
	want(t, rec, http.StatusOK, "")
	body := rec.Body.String()
	if !strings.Contains(body, `"visit"`) || !strings.Contains(body, visitReason) || !strings.Contains(body, "Platform Ops") {
		t.Fatalf("no visit banner: %s", body)
	}
	if !strings.Contains(body, "iam.users.view") || strings.Contains(body, ".create") || strings.Contains(body, ".edit") || strings.Contains(body, ".delete") {
		t.Fatalf("a visit must hold view permissions only: %s", body)
	}

	// reads work, every change is refused before any handler runs
	want(t, s.do(http.MethodGet, alphaHost, "/users", tok, ""), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/users", tok, `{"username":"intruder","displayName":"X"}`), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPatch, alphaHost, "/users/"+s.ids["plain"], tok, `{"displayName":"X"}`), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPut, alphaHost, "/institute/appearance", tok, `{"accent":"#1A7A6D","preset":"modern"}`), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPut, alphaHost, "/me/preferences", tok, `{"themeMode":"dark"}`), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodGet, alphaHost, "/me/mfa", tok, ""), http.StatusForbidden, "read_only_visit")

	// the school's audit trail shows who, the reason, and each read
	var reads, enters int
	var meta, actor, source string
	if err := s.adm.QueryRow(bg, `SELECT count(*) FILTER (WHERE action = 'support.read' AND resource_id = 'ListUsers'),
			count(*) FILTER (WHERE action = 'support.visit_enter'),
			max(meta::text), max(actor_type), max(source)
		FROM audit_log WHERE tenant_id = $1 AND action IN ('support.read', 'support.visit_enter')`, s.tenant["alpha"]).Scan(&reads, &enters, &meta, &actor, &source); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || enters != 1 || !strings.Contains(meta, visitReason) || actor != "platform_user" || source != "impersonation" {
		t.Fatalf("audit: reads %d, enters %d, meta %s, actor %s, source %s", reads, enters, meta, actor, source)
	}

	// ending the visit stops the token at once
	want(t, s.do(http.MethodPost, alphaHost, "/support-visit/end", tok, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, alphaHost, "/users", tok, ""), http.StatusUnauthorized, "unauthenticated")
}

func TestAVisitBelongsToOneSchoolAndEnds(t *testing.T) {
	s := newUsersStack(t)
	ptok := s.platformToken(t)

	// a code for alpha does nothing on beta, and an alpha visit token is refused there
	v := s.startVisit(t, ptok, s.tenant["alpha"])
	want(t, s.do(http.MethodPost, betaHost, "/auth/support-visit", "", `{"code":"`+v.Code+`"}`), http.StatusUnauthorized, "invalid_visit_code")
	tok := s.redeem(t, alphaHost, v.Code)
	want(t, s.do(http.MethodGet, betaHost, "/me/access", tok, ""), http.StatusUnauthorized, "unauthenticated")
	// a visit token is not a platform token either
	want(t, s.do(http.MethodGet, console, "/platform/me", tok, ""), http.StatusUnauthorized, "unauthenticated")

	// expiry ends it
	if _, err := s.adm.Exec(bg, `UPDATE support_visits SET expires_at = now() - interval '1 second' WHERE id = $1::uuid`, v.VisitID); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", tok, ""), http.StatusUnauthorized, "unauthenticated")

	// a code not used in time cannot be used later
	late := s.startVisit(t, ptok, s.tenant["alpha"])
	if _, err := s.adm.Exec(bg, `UPDATE support_visits SET expires_at = now() - interval '1 second' WHERE id = $1::uuid`, late.VisitID); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/auth/support-visit", "", `{"code":"`+late.Code+`"}`), http.StatusUnauthorized, "invalid_visit_code")

	// only onboarding and live schools can be visited
	if _, err := s.adm.Exec(bg, `UPDATE tenants SET status = 'suspended' WHERE id = $1`, s.tenant["beta"]); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, console, "/platform/institutes/"+s.tenant["beta"]+"/visits", ptok, `{"reason":"`+visitReason+`"}`), http.StatusConflict, "not_visitable")

	// a school user cannot "end a visit", and visits cannot be started by school users
	admin := s.signIn(t, alphaHost, "admin", password)
	want(t, s.do(http.MethodPost, alphaHost, "/support-visit/end", admin, ""), http.StatusForbidden, "not_a_visit")
	want(t, s.do(http.MethodPost, console, "/platform/institutes/"+s.tenant["alpha"]+"/visits", admin, `{"reason":"`+visitReason+`"}`), http.StatusUnauthorized, "unauthenticated")
}
