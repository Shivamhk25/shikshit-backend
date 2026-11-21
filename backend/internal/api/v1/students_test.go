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
	// every read above was audited under the reader's own identity, and the refused ones were not
	var views, byPrincipal int
	if err := s.adm.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE actor_id = (SELECT id FROM users WHERE username = 'pria')) FROM audit_log WHERE action IN ('students.view', 'students.list', 'student_enrollments.view', 'student_enrollments.list')`).Scan(&views, &byPrincipal); err != nil {
		t.Fatal(err)
	}
	if byPrincipal != 4 {
		t.Errorf("principal reads audited = %d, want 4", byPrincipal)
	}
	// a teacher holds stu.*.view only for class sections: no student data until the scope is applied
	for _, p := range []string{"/students", "/students/" + id, "/student-enrollments", "/student-enrollments/" + eid} {
		if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
			t.Errorf("teacher GET %s = %d, want 403", p, rec.Code)
		}
	}
	var teacherReads int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE actor_id = (SELECT id FROM users WHERE username = 'tina')`).Scan(&teacherReads); err != nil || teacherReads != 0 {
		t.Errorf("a refused read must not look like a read in the trail: %d %v", teacherReads, err)
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

// TestGuardiansLinksAndAddressesFollowTheSameRules: the Admin and Registrar manage them, the Principal reads, a
// Teacher (class-section scope) and a user with no role are refused, and every successful read is audited.
func TestGuardiansLinksAndAddressesFollowTheSameRules(t *testing.T) {
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
	student := mk("/students", `{"registrationCode":"R-1","firstName":"Asha","gender":"female","dateOfBirth":"2015-06-01"}`)
	guardian := mk("/guardians", `{"name":"Meera Rao","phone":"9876543210"}`)

	cases := []struct{ name, path, create, update string }{
		{"guardians", "/guardians", `{"name":"Raj Rao","phone":"9000000001"}`, `{"name":"Raj K Rao","phone":"9000000001"}`},
		{"student guardians", "/student-guardians",
			fmt.Sprintf(`{"studentId":%q,"guardianId":%q,"relation":"mother","isPrimary":true}`, student, guardian),
			fmt.Sprintf(`{"studentId":%q,"guardianId":%q,"relation":"mother","isPrimary":true,"canPickup":false}`, student, guardian)},
		{"student addresses", "/student-addresses",
			fmt.Sprintf(`{"studentId":%q,"kind":"home","line1":"12 Park Road","city":"Pune","country":"India"}`, student),
			fmt.Sprintf(`{"studentId":%q,"kind":"home","line1":"14 Park Road","city":"Pune","country":"India"}`, student)},
	}
	reads := 0
	for _, c := range cases {
		id := mk(c.path, c.create)
		if rec := s.call("PUT", c.path+"/"+id, boss, c.update); rec.Code != 200 {
			t.Errorf("%s: admin update = %d %s", c.name, rec.Code, rec.Body)
		}
		for _, p := range []string{c.path, c.path + "/" + id} {
			if rec := s.call("GET", p, principal, ""); rec.Code != 200 {
				t.Errorf("%s: principal GET %s = %d", c.name, p, rec.Code)
			}
			reads++
			if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
				t.Errorf("%s: teacher GET %s = %d, want 403", c.name, p, rec.Code)
			}
			if rec := s.call("GET", p, nobody, ""); rec.Code != http.StatusForbidden {
				t.Errorf("%s: no role GET %s = %d, want 403", c.name, p, rec.Code)
			}
		}
		for what, call := range map[string][3]string{"create": {"POST", c.path, c.create}, "edit": {"PUT", c.path + "/" + id, c.update}, "delete": {"DELETE", c.path + "/" + id, ""}} {
			if rec := s.call(call[0], call[1], principal, call[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s: principal %s = %d, want 403", c.name, what, rec.Code)
			}
		}
		if rec := s.call("PUT", c.path+"/"+id, registrar, c.update); rec.Code != 200 {
			t.Errorf("%s: registrar update = %d %s", c.name, rec.Code, rec.Body)
		}
		if rec := s.call("POST", c.path, boss, `{}`); rec.Code != 400 {
			t.Errorf("%s: empty body = %d", c.name, rec.Code)
		}
		if rec := s.call("GET", c.path+"?sort=password", boss, ""); rec.Code != 400 {
			t.Errorf("%s: bad sort = %d", c.name, rec.Code)
		}
		if rec := s.call("GET", c.path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: anonymous = %d", c.name, rec.Code)
		}
		if rec := s.call("DELETE", c.path+"/"+id, boss, ""); rec.Code != http.StatusNoContent {
			t.Errorf("%s: admin delete = %d", c.name, rec.Code)
		}
	}
	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE actor_id = (SELECT id FROM users WHERE username = 'pria') AND action ~ '\.(view|list)$'`).Scan(&n); err != nil || n != reads {
		t.Errorf("audited principal reads = %d, want %d (%v)", n, reads, err)
	}
	// a guardian that is linked cannot be deleted: 409, not 500
	mk("/student-guardians", cases[1].create)
	if rec := s.call("DELETE", "/guardians/"+guardian, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("linked guardian delete = %d, want 409", rec.Code)
	}
}
