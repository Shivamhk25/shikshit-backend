package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestStudentsFollowThePermissionMatrixAndScope: the Admin and the Registrar manage students, the Principal reads,
// a Teacher (whose grant is limited to class sections) is refused until the service can narrow the list, and a user
// with no role is refused.
func TestStudentsFollowThePermissionMatrixAndScope(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar, principal := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken, s.signIn(t, "pria").AccessToken
	teacher, nobody := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken
	mk := func(path, body string) string {
		rec := s.call("POST", path, boss, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	year := mk("/academic-years", `{"code":"Y","name":"Y","startDate":"2026-04-01","endDate":"2027-03-31","isCurrent":true}`)
	class := mk("/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	sec := mk("/sections", `{"code":"A","name":"Section A"}`)
	mk("/class-section-mappings", fmt.Sprintf(`{"yearId":%q,"classId":%q,"sectionId":%q}`, year, class, sec))

	create := `{"registrationCode":"R-1","firstName":"Asha","lastName":"Rao","gender":"female","dateOfBirth":"2015-06-01"}`
	update := `{"registrationCode":"R-1","firstName":"Asha","lastName":"Rao","gender":"female","dateOfBirth":"2015-06-01","status":"paused"}`
	id := mk("/students", create)
	enrol := fmt.Sprintf(`{"studentId":%q,"yearId":%q,"classId":%q,"sectionId":%q,"rollNumber":"1"}`, id, year, class, sec)
	eid := mk("/student-enrollments", enrol)

	if rec := s.call("PUT", "/students/"+id, boss, update); rec.Code != 200 {
		t.Errorf("admin update = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/students/"+id, boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"currentClassName":"Class 5"`) {
		t.Errorf("admin get = %d %s", rec.Code, rec.Body)
	}
	// the registrar manages students
	if rec := s.call("POST", "/students", registrar, strings.Replace(create, "R-1", "R-2", 1)); rec.Code != http.StatusCreated {
		t.Errorf("registrar create = %d %s", rec.Code, rec.Body)
	}
	// the principal reads only
	for _, p := range []string{"/students", "/students/" + id, "/student-enrollments", "/student-enrollments/" + eid} {
		if rec := s.call("GET", p, principal, ""); rec.Code != 200 {
			t.Errorf("principal GET %s = %d", p, rec.Code)
		}
	}
	for _, c := range [][3]string{{"POST", "/students", create}, {"PUT", "/students/" + id, update}, {"DELETE", "/students/" + id, ""},
		{"POST", "/student-enrollments", enrol}, {"DELETE", "/student-enrollments/" + eid, ""}} {
		if rec := s.call(c[0], c[1], principal, c[2]); rec.Code != http.StatusForbidden {
			t.Errorf("principal %s %s = %d, want 403", c[0], c[1], rec.Code)
		}
	}
	// a teacher holds stu.*.view only for class sections: no student data until the scope is applied
	for _, p := range []string{"/students", "/students/" + id, "/student-enrollments", "/student-enrollments/" + eid} {
		if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
			t.Errorf("teacher GET %s = %d, want 403", p, rec.Code)
		}
	}
	for _, c := range [][2]string{{"GET", "/students"}, {"POST", "/students"}, {"GET", "/student-enrollments"}} {
		if rec := s.call(c[0], c[1], nobody, create); rec.Code != http.StatusForbidden {
			t.Errorf("no role %s %s = %d, want 403", c[0], c[1], rec.Code)
		}
	}
	if rec := s.call("GET", "/students", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if rec := s.call("POST", "/students", boss, `{}`); rec.Code != 400 {
		t.Errorf("empty body = %d", rec.Code)
	}
	if rec := s.call("GET", "/students?sort=password", boss, ""); rec.Code != 400 {
		t.Errorf("bad sort = %d", rec.Code)
	}
	if rec := s.call("GET", "/students?status=weird", boss, ""); rec.Code != 400 {
		t.Errorf("bad status = %d", rec.Code)
	}
	if rec := s.call("DELETE", "/students/"+id, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete with enrollment = %d, want 409", rec.Code)
	}
	if rec := s.call("DELETE", "/student-enrollments/"+eid, boss, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete enrollment = %d", rec.Code)
	}
	if rec := s.call("DELETE", "/students/"+id, boss, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete student = %d", rec.Code)
	}
}
