package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
)

func (s accessStack) send(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, apiv1.BasePath+path, strings.NewReader(body))
	req.Host = "demo.shikshit.test"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

type towerBody struct {
	Total   int `json:"total"`
	Domains []struct {
		ID, Status string
		Blockers   []struct{ ID string }
	} `json:"domains"`
}

func TestControlTowerAccess(t *testing.T) {
	s := newAccessStack(t)
	admin := s.signIn(t, "boss").AccessToken
	rec := s.get("/onboarding/control-tower", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin get = %d %s", rec.Code, rec.Body)
	}
	got := decode[towerBody](t, rec)
	if got.Total == 0 || got.Domains[0].ID != "academic" {
		t.Errorf("tower = %+v", got)
	}

	// The Principal holds the whole resource through the template; the registrar holds none of it.
	if rec := s.get("/onboarding/control-tower", s.signIn(t, "pria").AccessToken); rec.Code != http.StatusOK {
		t.Errorf("principal get = %d %s", rec.Code, rec.Body)
	}
	clerk := s.signIn(t, "clerk").AccessToken
	if rec := s.get("/onboarding/control-tower", clerk); rec.Code != http.StatusForbidden {
		t.Errorf("registrar get = %d", rec.Code)
	}
	if rec := s.send(http.MethodPut, "/onboarding/control-tower/domains/academic", clerk, `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("registrar assign = %d", rec.Code)
	}
	if rec := s.get("/onboarding/control-tower", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous get = %d", rec.Code)
	}

	// Edit operations: validation, unknown domain, refused change with reasons.
	if rec := s.send(http.MethodPut, "/onboarding/control-tower/domains/academic", admin, `{"dueDate":"2026-12-31"}`); rec.Code != http.StatusOK {
		t.Errorf("assign = %d %s", rec.Code, rec.Body)
	}
	if rec := s.send(http.MethodPut, "/onboarding/control-tower/domains/nope", admin, `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown domain = %d", rec.Code)
	}
	if rec := s.send(http.MethodPost, "/onboarding/control-tower/domains/academic/state", admin, `{"to":"maybe"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad state = %d %s", rec.Code, rec.Body)
	}
	rec = s.send(http.MethodPost, "/onboarding/control-tower/domains/academic/state", admin, `{"to":"ready"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "readiness rule") {
		t.Errorf("ready with no data = %d %s", rec.Code, rec.Body)
	}
}

func TestControlTowerIsGoneWhenTheModuleIsOff(t *testing.T) {
	s := newAccessStack(t)
	admin := s.signIn(t, "boss").AccessToken
	if _, err := s.adm.Exec(bg, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'onb'`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	admin = s.signIn(t, "boss").AccessToken
	if rec := s.get("/onboarding/control-tower", admin); rec.Code != http.StatusNotFound {
		t.Errorf("tower with module off = %d", rec.Code)
	}
}
