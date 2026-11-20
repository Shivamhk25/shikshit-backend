package apiv1_test

import (
	"net/http"
	"strings"
	"testing"
)

type idBody struct {
	ID string `json:"id"`
}

type resourceCase struct {
	name, path     string
	create, update string
	listField      string // a field present on each item
}

var academicCases = []resourceCase{
	{"academic years", "/academic-years", `{"code":"2026-27","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`, `{"code":"2026-27","name":"Renamed","startDate":"2026-04-01","endDate":"2027-03-31"}`, "startDate"},
	{"classes", "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`, `{"code":"V","name":"Fifth","displayOrder":5}`, "displayOrder"},
	{"sections", "/sections", `{"code":"A","name":"Section A"}`, `{"code":"A","name":"Section Alpha"}`, "code"},
	{"subjects", "/subjects", `{"code":"MATH","name":"Mathematics","category":"Core","area":"Academic","credits":4}`, `{"code":"MATH","name":"Maths","category":"Core","area":"Academic","credits":3.5}`, "credits"},
}

func TestAcademicResourcesFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, clerk, nobody := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken, s.signIn(t, "nobody").AccessToken
	for _, c := range academicCases {
		rec := s.call("POST", c.path, boss, c.create)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: admin create = %d %s", c.name, rec.Code, rec.Body)
		}
		id := decode[idBody](t, rec).ID
		if rec = s.call("PUT", c.path+"/"+id, boss, c.update); rec.Code != 200 {
			t.Errorf("%s: admin update = %d %s", c.name, rec.Code, rec.Body)
		}
		if rec = s.call("GET", c.path, clerk, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"`+c.listField+`"`) {
			t.Errorf("%s: view-only list = %d %s", c.name, rec.Code, rec.Body)
		}
		if rec = s.call("GET", c.path+"/"+id, clerk, ""); rec.Code != 200 {
			t.Errorf("%s: view-only get = %d", c.name, rec.Code)
		}
		for what, call := range map[string][3]string{
			"create": {"POST", c.path, c.create},
			"edit":   {"PUT", c.path + "/" + id, c.update},
			"delete": {"DELETE", c.path + "/" + id, ""},
		} {
			if rec = s.call(call[0], call[1], clerk, call[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s: view-only %s = %d, want 403", c.name, what, rec.Code)
			}
		}
		for _, call := range [][2]string{{"GET", c.path}, {"POST", c.path}, {"DELETE", c.path + "/" + id}} {
			if rec = s.call(call[0], call[1], nobody, c.create); rec.Code != http.StatusForbidden {
				t.Errorf("%s: no role %s %s = %d, want 403", c.name, call[0], call[1], rec.Code)
			}
		}
		if rec = s.call("GET", c.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous = %d", c.name, rec.Code)
		}
		if rec = s.call("DELETE", c.path+"/"+id, boss, ""); rec.Code != http.StatusNoContent {
			t.Errorf("%s: admin delete = %d", c.name, rec.Code)
		}
		if rec = s.call("GET", c.path+"/"+id, boss, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: get after delete = %d", c.name, rec.Code)
		}
	}
}

func TestAcademicResourcesReturnFieldErrorsAndReuseDeletedCodes(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	for _, c := range academicCases {
		if rec := s.call("POST", c.path, boss, `{}`); rec.Code != 400 {
			t.Errorf("%s: empty body = %d", c.name, rec.Code)
		}
		id := decode[idBody](t, s.call("POST", c.path, boss, c.create)).ID
		rec := s.call("POST", c.path, boss, c.create)
		v := decode[validationBody](t, rec)
		wantField, wantCode := "code", "duplicate"
		if c.name == "academic years" { // the same dates are reported first: the overlap check runs before the insert
			wantField, wantCode = "startDate", "overlaps_year"
		}
		if rec.Code != 400 || len(v.Errors) == 0 || v.Errors[0].Field != wantField || v.Errors[0].Code != wantCode {
			t.Errorf("%s: duplicate = %d %s", c.name, rec.Code, rec.Body)
		}
		s.call("DELETE", c.path+"/"+id, boss, "")
		if rec = s.call("POST", c.path, boss, c.create); rec.Code != 201 {
			t.Errorf("%s: code reuse after delete = %d %s", c.name, rec.Code, rec.Body)
		}
		if rec = s.call("GET", c.path+"?sort=password", boss, ""); rec.Code != 400 {
			t.Errorf("%s: bad sort = %d", c.name, rec.Code)
		}
		if rec = s.call("GET", c.path+"?status=weird", boss, ""); rec.Code != 400 {
			t.Errorf("%s: bad status = %d", c.name, rec.Code)
		}
	}
}

func TestYearsAndTermsOverHTTP(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken

	rec := s.call("POST", "/academic-years", boss, `{"code":"Y1","name":"Y1","startDate":"2026-04-01","endDate":"2027-03-31","isCurrent":true}`)
	if rec.Code != 201 {
		t.Fatalf("year = %d %s", rec.Code, rec.Body)
	}
	year := decode[idBody](t, rec).ID

	// overlap and a second current year are field errors
	rec = s.call("POST", "/academic-years", boss, `{"code":"Y2","name":"Y2","startDate":"2026-06-01","endDate":"2027-01-01"}`)
	if v := decode[validationBody](t, rec); rec.Code != 400 || v.Errors[0].Code != "overlaps_year" {
		t.Errorf("overlap: %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/academic-years", boss, `{"code":"Y3","name":"Y3","startDate":"2027-04-01","endDate":"2028-03-31","isCurrent":true}`)
	if v := decode[validationBody](t, rec); rec.Code != 400 || v.Errors[0].Code != "another_current" {
		t.Errorf("second current: %d %s", rec.Code, rec.Body)
	}

	// terms
	rec = s.call("POST", "/academic-terms", boss, `{"yearId":"`+year+`","code":"T1","title":"Term 1","startDate":"2026-04-01","endDate":"2026-09-30","color":"#2F5BFF"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"days":183`) || !strings.Contains(rec.Body.String(), `"yearName":"Y1"`) {
		t.Fatalf("term = %d %s", rec.Code, rec.Body)
	}
	term := decode[idBody](t, rec).ID
	rec = s.call("POST", "/academic-terms", boss, `{"yearId":"`+year+`","code":"T2","title":"Term 2","startDate":"2026-09-01","endDate":"2027-03-31"}`)
	if v := decode[validationBody](t, rec); rec.Code != 400 || v.Errors[0].Code != "overlaps_term" {
		t.Errorf("term overlap: %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/academic-terms", boss, `{"yearId":"`+year+`","code":"T3","title":"Term 3","startDate":"2027-04-01","endDate":"2027-06-01"}`)
	if v := decode[validationBody](t, rec); rec.Code != 400 || v.Errors[0].Code != "outside_year" {
		t.Errorf("outside year: %d %s", rec.Code, rec.Body)
	}
	if rec = s.call("GET", "/academic-terms?yearId="+year, boss, ""); !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Errorf("filter by year: %s", rec.Body)
	}
	if rec = s.call("GET", "/academic-terms?yearId=not-a-uuid", boss, ""); rec.Code != 400 {
		t.Errorf("bad yearId = %d", rec.Code)
	}

	// the current year, and a year with terms, cannot be deleted: 409 with a reason
	if rec = s.call("DELETE", "/academic-years/"+year, boss, ""); rec.Code != http.StatusConflict || decode[problemBody](t, rec).Code != "in_use" {
		t.Errorf("delete current year = %d %s", rec.Code, rec.Body)
	}
	other := decode[idBody](t, s.call("POST", "/academic-years", boss, `{"code":"Y4","name":"Y4","startDate":"2027-04-01","endDate":"2028-03-31"}`)).ID
	s.call("POST", "/academic-terms", boss, `{"yearId":"`+other+`","code":"T1","title":"T","startDate":"2027-04-01","endDate":"2027-06-01"}`)
	if rec = s.call("DELETE", "/academic-years/"+other, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete year with terms = %d", rec.Code)
	}
	if rec = s.call("DELETE", "/academic-terms/"+term, boss, ""); rec.Code != 204 {
		t.Errorf("delete term = %d", rec.Code)
	}
}

func TestModulesOffHidesTheAcademicEndpoints(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	if _, err := s.adm.Exec(bg, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'aca'`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/academic-years", "/academic-terms", "/classes", "/sections", "/subjects", "/rooms"} {
		if rec := s.call("GET", p, boss, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s with the module off = %d", p, rec.Code)
		}
	}
}
