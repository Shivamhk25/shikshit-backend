package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
)

type roomBody struct {
	ID     string  `json:"id"`
	Code   *string `json:"code"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
}

type roomPageBody struct {
	Items []roomBody `json:"items"`
	Page  struct{ Number, Size, Total int }
}

type validationBody struct {
	Code   string `json:"code"`
	Errors []struct{ Field, Code string }
}

func (s accessStack) call(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, apiv1.BasePath+path, strings.NewReader(body))
	req.Host = "demo.shikshit.test"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Client", "web")
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

const goodRoom = `{"name":"Room 101","code":"R101","type":"classroom","capacity":40}`

func TestRoomsPermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, clerk, nobody := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken, s.signIn(t, "nobody").AccessToken

	// admin: full cycle
	rec := s.call("POST", "/rooms", boss, goodRoom)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create = %d %s", rec.Code, rec.Body)
	}
	id := decode[roomBody](t, rec).ID
	if rec = s.call("PUT", "/rooms/"+id, boss, `{"name":"Lab","capacity":20}`); rec.Code != 200 {
		t.Fatalf("admin update = %d %s", rec.Code, rec.Body)
	}

	// registrar holds aca.*.view only: can read, cannot write
	if rec = s.call("GET", "/rooms", clerk, ""); rec.Code != 200 {
		t.Fatalf("view-only list = %d %s", rec.Code, rec.Body)
	}
	if rec = s.call("GET", "/rooms/"+id, clerk, ""); rec.Code != 200 {
		t.Fatalf("view-only get = %d", rec.Code)
	}
	for name, c := range map[string]struct{ method, path, body string }{
		"create": {"POST", "/rooms", goodRoom},
		"edit":   {"PUT", "/rooms/" + id, `{"name":"x","capacity":1}`},
		"delete": {"DELETE", "/rooms/" + id, ""},
	} {
		rec = s.call(c.method, c.path, clerk, c.body)
		if rec.Code != http.StatusForbidden || decode[problemBody](t, rec).Code != "permission_denied" {
			t.Errorf("view-only %s = %d %s, want 403 permission_denied", name, rec.Code, rec.Body)
		}
	}
	if got := decode[roomBody](t, s.call("GET", "/rooms/"+id, boss, "")); got.Name != "Lab" {
		t.Errorf("a refused write must not change data: %+v", got)
	}

	// no role: nothing at all
	for _, c := range []struct{ method, path string }{{"GET", "/rooms"}, {"GET", "/rooms/" + id}, {"POST", "/rooms"}, {"DELETE", "/rooms/" + id}} {
		if rec = s.call(c.method, c.path, nobody, goodRoom); rec.Code != http.StatusForbidden {
			t.Errorf("no-role %s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}

	// anonymous
	if rec = s.call("GET", "/rooms", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}
}

func TestRoomsEndpointsDisappearWhenTheModuleIsOff(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	if _, err := s.adm.Exec(bg, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'aca'`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{{"GET", "/rooms"}, {"POST", "/rooms"}} {
		if rec := s.call(c.method, c.path, boss, goodRoom); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s with module off = %d, want 404 even for the admin", c.method, c.path, rec.Code)
		}
	}
}

func TestPermissionChangeAppliesOnTheNextRequestDespiteTheCache(t *testing.T) {
	s := newAccessStack(t)
	clerk := s.signIn(t, "clerk").AccessToken
	if rec := s.call("POST", "/rooms", clerk, goodRoom); rec.Code != http.StatusForbidden {
		t.Fatalf("before grant = %d", rec.Code)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, reason)
		SELECT tenant_id, id, 'aca.rooms.create', 'allow', 'test' FROM users WHERE username = 'clerk'`); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("POST", "/rooms", clerk, goodRoom); rec.Code != http.StatusCreated {
		t.Fatalf("after grant = %d %s: a stale cache must not hide a new grant", rec.Code, rec.Body)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, reason)
		SELECT tenant_id, id, 'aca.rooms.*', 'deny', 'test' FROM users WHERE username = 'clerk'`); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("GET", "/rooms", clerk, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("after deny = %d: a stale cache must not keep a revoked permission alive", rec.Code)
	}
}

func TestRoomValidationReturnsFieldErrors(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	rec := s.call("POST", "/rooms", boss, `{"name":"","capacity":0,"type":"garage"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	v := decode[validationBody](t, rec)
	fields := map[string]string{}
	for _, e := range v.Errors {
		fields[e.Field] = e.Code
	}
	if v.Code != "validation_failed" || fields["name"] != "required" || fields["capacity"] != "out_of_range" || fields["type"] != "invalid_choice" {
		t.Errorf("%s", rec.Body)
	}
	// duplicate code
	s.call("POST", "/rooms", boss, goodRoom)
	rec = s.call("POST", "/rooms", boss, goodRoom)
	if v = decode[validationBody](t, rec); rec.Code != 400 || len(v.Errors) != 1 || v.Errors[0].Field != "code" || v.Errors[0].Code != "duplicate" {
		t.Errorf("duplicate code: %d %s", rec.Code, rec.Body)
	}
	// malformed JSON and a bad id are client errors, not server errors
	if rec = s.call("POST", "/rooms", boss, `{oops`); rec.Code != 400 {
		t.Errorf("malformed JSON = %d", rec.Code)
	}
	if rec = s.call("GET", "/rooms/not-a-uuid", boss, ""); rec.Code != 400 {
		t.Errorf("bad id = %d", rec.Code)
	}
	if rec = s.call("GET", "/rooms/00000000-0000-0000-0000-000000000000", boss, ""); rec.Code != 404 {
		t.Errorf("unknown id = %d", rec.Code)
	}
}

func TestRoomsListPagingAndRejectedSort(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	for _, n := range []string{"C", "A", "B"} {
		s.call("POST", "/rooms", boss, `{"name":"`+n+`","capacity":10}`)
	}
	page := decode[roomPageBody](t, s.call("GET", "/rooms?pageSize=2&page=1&sort=name", boss, ""))
	if len(page.Items) != 2 || page.Items[0].Name != "A" || page.Page.Total != 3 || page.Page.Size != 2 || page.Page.Number != 1 {
		t.Errorf("%+v", page)
	}
	for _, bad := range []string{"?sort=password", "?pageSize=999", "?page=0", "?pageSize=0", "?sort=%3Bdrop", "?status=weird"} {
		if rec := s.call("GET", "/rooms"+bad, boss, ""); rec.Code != 400 {
			t.Errorf("%s = %d, want 400", bad, rec.Code)
		}
	}
}

func TestRoomChangesAreAuditedWithTheWebClientAsSource(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	id := decode[roomBody](t, s.call("POST", "/rooms", boss, goodRoom)).ID
	s.call("DELETE", "/rooms/"+id, boss, "")
	var n int
	err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND source = 'web' AND actor_id IS NOT NULL AND action IN ('rooms.create','rooms.delete')`, id).Scan(&n)
	if err != nil || n != 2 {
		t.Fatalf("audit rows = %d (%v)", n, err)
	}
}

func TestRoomsAppearInNavigationOnlyForPermittedUsers(t *testing.T) {
	s := newAccessStack(t)
	has := func(user string) bool {
		got, _ := s.access(t, user)
		for _, n := range got.Workspace.Navigation {
			if n.ScreenID == "rooms" {
				return true
			}
		}
		return false
	}
	if !has("boss") || !has("clerk") || has("nobody") {
		t.Error("rooms must show for admin and registrar (aca.*.view) and not for a user without roles")
	}
}

// The permission table the middleware enforces must equal what the contract declares.
func TestOperationAccessMatchesTheContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi/openapi.bundled.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	table := apiv1.OperationAccess()
	seen := 0
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			op, ok := raw.(map[string]any) // path items also hold shared "parameters", which is not an operation
			if !ok {
				continue
			}
			perm, _ := op["x-permission"].(string)
			module, _ := op["x-module"].(string)
			id, _ := op["operationId"].(string)
			if perm == "" {
				continue
			}
			seen++
			name := strings.ToUpper(id[:1]) + id[1:]
			got, ok := table[name]
			if !ok {
				t.Errorf("%s %s declares %s but the middleware does not enforce it", strings.ToUpper(method), path, perm)
				continue
			}
			if got[0] != perm || got[1] != module {
				t.Errorf("%s: middleware %v, contract %s/%s", name, got, perm, module)
			}
		}
	}
	if seen != len(table) {
		t.Errorf("contract declares %d permissioned operations, middleware enforces %d", seen, len(table))
	}
}
