package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestMappingAndTextbookResourcesFollowThePermissionMatrix drives the four batch-2 resources over HTTP: the admin
// can do everything, a view-only user can read but never write, and a user with no role gets 403 everywhere.
func TestMappingAndTextbookResourcesFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, clerk, nobody := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken, s.signIn(t, "nobody").AccessToken
	mk := func(path, body string) string {
		rec := s.call("POST", path, boss, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	year := mk("/academic-years", `{"code":"Y","name":"Y","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	class := mk("/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	sec := mk("/sections", `{"code":"A","name":"Section A"}`)
	subj := mk("/subjects", `{"code":"MATH","name":"Mathematics","category":"core","area":"mathematics","credits":4}`)
	book := mk("/textbooks", fmt.Sprintf(`{"code":"B1","title":"Maths 5","classId":%q,"subjectId":%q}`, class, subj))

	cases := []struct{ name, path, create, update, listField string }{
		{"class section mappings", "/class-section-mappings",
			fmt.Sprintf(`{"yearId":%q,"classId":%q,"sectionId":%q,"capacity":40}`, year, class, sec),
			fmt.Sprintf(`{"yearId":%q,"classId":%q,"sectionId":%q,"capacity":35}`, year, class, sec), "capacity"},
		{"subject class mappings", "/subject-class-mappings",
			fmt.Sprintf(`{"yearId":%q,"classId":%q,"subjectId":%q,"periodsPerWeek":6}`, year, class, subj),
			fmt.Sprintf(`{"yearId":%q,"classId":%q,"subjectId":%q,"periodsPerWeek":5}`, year, class, subj), "periodsPerWeek"},
		{"textbooks", "/textbooks",
			fmt.Sprintf(`{"code":"B2","title":"Maths 5b","classId":%q,"subjectId":%q}`, class, subj),
			fmt.Sprintf(`{"code":"B2","title":"Maths 5c","classId":%q,"subjectId":%q}`, class, subj), "title"},
		{"textbook chapters", "/textbook-chapters",
			fmt.Sprintf(`{"textbookId":%q,"sortOrder":1,"title":"Numbers","periods":8}`, book),
			fmt.Sprintf(`{"textbookId":%q,"sortOrder":1,"title":"Numbers 2","periods":9}`, book), "periods"},
	}
	for _, c := range cases {
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
			"create": {"POST", c.path, c.create}, "edit": {"PUT", c.path + "/" + id, c.update}, "delete": {"DELETE", c.path + "/" + id, ""},
		} {
			if rec = s.call(call[0], call[1], clerk, call[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s: view-only %s = %d, want 403", c.name, what, rec.Code)
			}
		}
		for _, call := range [][2]string{{"GET", c.path}, {"GET", c.path + "/" + id}, {"POST", c.path}, {"PUT", c.path + "/" + id}, {"DELETE", c.path + "/" + id}} {
			if rec = s.call(call[0], call[1], nobody, c.create); rec.Code != http.StatusForbidden {
				t.Errorf("%s: no role %s %s = %d, want 403", c.name, call[0], call[1], rec.Code)
			}
		}
		if rec = s.call("GET", c.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous = %d", c.name, rec.Code)
		}
		if rec = s.call("POST", c.path, boss, `{}`); rec.Code != 400 {
			t.Errorf("%s: empty body = %d", c.name, rec.Code)
		}
		if rec = s.call("GET", c.path+"?sort=password", boss, ""); rec.Code != 400 {
			t.Errorf("%s: bad sort = %d", c.name, rec.Code)
		}
		if rec = s.call("DELETE", c.path+"/"+id, boss, ""); rec.Code != http.StatusNoContent {
			t.Errorf("%s: admin delete = %d", c.name, rec.Code)
		}
		if rec = s.call("GET", c.path+"/"+id, boss, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: get after delete = %d", c.name, rec.Code)
		}
	}

	// in-use deletes answer 409, not 500
	sc := mk("/class-section-mappings", cases[0].create)
	mk("/subject-class-mappings", fmt.Sprintf(`{"yearId":%q,"classId":%q,"subjectId":%q,"sectionId":%q}`, year, class, subj, sec))
	mk("/textbook-chapters", cases[3].create)
	for what, path := range map[string]string{
		"class section mapping": "/class-section-mappings/" + sc, "class": "/classes/" + class, "section": "/sections/" + sec,
		"subject": "/subjects/" + subj, "year": "/academic-years/" + year, "textbook": "/textbooks/" + book,
	} {
		if rec := s.call("DELETE", path, boss, ""); rec.Code != http.StatusConflict {
			t.Errorf("in-use %s delete = %d %s, want 409", what, rec.Code, rec.Body)
		}
	}
	// a section outside the class is a field error
	rec := s.call("POST", "/subject-class-mappings", boss, fmt.Sprintf(`{"yearId":%q,"classId":%q,"subjectId":%q,"sectionId":%q}`, year, class, subj, "00000000-0000-4000-8000-000000000000"))
	if rec.Code != 400 {
		t.Errorf("unmapped section = %d %s", rec.Code, rec.Body)
	}
}
