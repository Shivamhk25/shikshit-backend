package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestAdmissionsFollowThePermissionMatrix: the Registrar and the Admin run admissions; the Principal and the
// Accountant read only (403 on admit, preview and bulk); a user who may admit but not create students is refused on
// every write; a Teacher and a user with no role are refused everywhere; an anonymous call is 401. Admitting twice is
// the same student (201 then 200), reads are audited under the reader's identity, and the Admissions screen is in the
// navigation of those who may view it.
func TestAdmissionsFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody, gate := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken, s.signIn(t, "gate").AccessToken
	mk := func(token, path, body string) string {
		rec := s.call("POST", path, token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	class := mk(boss, "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	year := mk(boss, "/academic-years", `{"code":"Y","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	var section, unmapped string
	for code, dst := range map[string]*string{"A": &section, "B": &unmapped} {
		if err := s.adm.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, $2, 'Section ' || $2) RETURNING id::text`, s.tenantID, code).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, s.tenantID, year, class, section); err != nil {
		t.Fatal(err)
	}
	reg := func(first, status string) string {
		return mk(registrar, "/student-registrations", fmt.Sprintf(`{"appliedOn":"2026-05-01","firstName":%q,"lastName":"Rao","dateOfBirth":"2018-06-01","gender":"female","fatherName":"Ravi Rao","guardianName":"Meera Rao","guardianPhone":"9876543210","classId":%q,"yearId":%q,"status":%q}`, first, class, year, status))
	}
	r1, r2, r3, pending := reg("Asha", "accepted"), reg("Bina", "accepted"), reg("Chitra", "accepted"), reg("Dev", "under_review")
	admit := fmt.Sprintf(`{"sectionId":%q}`, section)

	// the queue: only accepted registrations, for everyone who may view it
	for name, tok := range map[string]string{"registrar": registrar, "admin": boss, "principal": principal, "accountant": accountant, "gatekeeper": gate} {
		rec := s.call("GET", "/admissions", tok, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":3`) || strings.Contains(rec.Body.String(), "Dev") || !strings.Contains(rec.Body.String(), `"admitted":false`) {
			t.Errorf("%s queue = %d %s", name, rec.Code, rec.Body)
		}
		if rec := s.call("GET", "/admissions/"+r1, tok, ""); rec.Code != 200 {
			t.Errorf("%s detail = %d", name, rec.Code)
		}
	}
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody} {
		for _, c := range [][3]string{{"GET", "/admissions", ""}, {"GET", "/admissions/" + r1, ""}, {"POST", "/admissions/" + r1 + "/admit", admit}, {"POST", "/admissions/preview", `{"items":[]}`}, {"POST", "/admissions/bulk", `{"items":[]}`}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	batch := fmt.Sprintf(`{"items":[{"registrationId":%q,"sectionId":%q}]}`, r1, section)
	for name, tok := range map[string]string{"principal": principal, "accountant": accountant, "gatekeeper (may not create students)": gate, "enroller (may create students, not admit)": s.signIn(t, "enrol").AccessToken} {
		for _, c := range [][3]string{{"POST", "/admissions/" + r1 + "/admit", admit}, {"POST", "/admissions/preview", batch}, {"POST", "/admissions/bulk", batch}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	if rec := s.call("GET", "/admissions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if n := countRows(t, s, "students"); n != 0 {
		t.Fatalf("refused calls created %d students", n)
	}

	// admit: 201 the first time, 200 and the same student the second time, by anyone who may admit
	rec := s.call("POST", "/admissions/"+r1+"/admit", registrar, admit)
	first := decode[admitBody](t, rec)
	if rec.Code != http.StatusCreated || first.AlreadyAdmitted || first.Student.RegistrationCode != "STU-000001" || first.Student.ID == "" || first.EnrollmentID == "" ||
		len(first.Guardians) != 2 {
		t.Errorf("registrar admit = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/admissions/"+r1+"/admit", boss, admit)
	again := decode[admitBody](t, rec)
	if rec.Code != http.StatusOK || !again.AlreadyAdmitted || again.Student.ID != first.Student.ID {
		t.Errorf("second admit = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/students/"+first.Student.ID, boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"STU-000001"`) {
		t.Errorf("the created student = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/student-enrollments?studentId="+first.Student.ID, boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rollNumber":"1"`) || !strings.Contains(rec.Body.String(), `"sectionName":"Section A"`) {
		t.Errorf("the created enrolment = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/admissions", principal, ""); !strings.Contains(rec.Body.String(), `"total":2`) {
		t.Errorf("queue after admit %s", rec.Body)
	}
	if rec := s.call("GET", "/admissions?state=admitted", principal, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), first.Student.ID) || !strings.Contains(rec.Body.String(), `"studentCode":"STU-000001"`) {
		t.Errorf("admitted list = %d %s", rec.Code, rec.Body)
	}

	// refusals and validation
	for name, c := range map[string]struct {
		path, body string
		code       int
		want       string
	}{
		"not accepted":      {"/admissions/" + pending + "/admit", admit, 409, "not_accepted"},
		"unknown":           {"/admissions/00000000-0000-0000-0000-000000000001/admit", admit, 404, ""},
		"no section":        {"/admissions/" + r2 + "/admit", `{}`, 400, "sectionId"},
		"unmapped section":  {"/admissions/" + r2 + "/admit", fmt.Sprintf(`{"sectionId":%q}`, unmapped), 400, "not_in_class"},
		"used roll":         {"/admissions/" + r2 + "/admit", fmt.Sprintf(`{"sectionId":%q,"rollNumber":"1"}`, section), 400, "rollNumber"},
		"used code":         {"/admissions/" + r2 + "/admit", fmt.Sprintf(`{"sectionId":%q,"studentCode":"stu-000001"}`, section), 400, "studentCode"},
		"bad email":         {"/admissions/" + r2 + "/admit", fmt.Sprintf(`{"sectionId":%q,"parentEmail":"x"}`, section), 400, "parentEmail"},
		"future date":       {"/admissions/" + r2 + "/admit", fmt.Sprintf(`{"sectionId":%q,"admissionDate":"2999-01-01"}`, section), 400, "admissionDate"},
		"empty batch":       {"/admissions/preview", `{"items":[]}`, 400, ""},
		"unknown field row": {"/admissions/preview", `{"items":[{"sectionId":"x"}]}`, 400, ""},
	} {
		rec := s.call("POST", c.path, registrar, c.body)
		if rec.Code != c.code || (c.want != "" && !strings.Contains(rec.Body.String(), c.want)) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for q, want := range map[string]int{"sort=password": 400, "state=weird": 400, "classId=notauuid": 400, "pageSize=1000": 400} {
		if rec := s.call("GET", "/admissions?"+q, boss, ""); rec.Code != want {
			t.Errorf("GET ?%s = %d, want %d", q, rec.Code, want)
		}
	}

	// preview, then commit: the preview saves nothing and says what the commit does
	pre := fmt.Sprintf(`{"items":[{"registrationId":%q,"sectionId":%q},{"registrationId":%q,"sectionId":%q},{"registrationId":%q,"sectionId":%q},{"registrationId":%q,"sectionId":%q}]}`,
		r2, section, r3, unmapped, pending, section, r1, section)
	before := countRows(t, s, "students")
	rec = s.call("POST", "/admissions/preview", registrar, pre)
	for _, want := range []string{`"outcome":"would_admit"`, `"outcome":"problem"`, "not_in_class", "not_accepted", `"outcome":"already_admitted"`, `"okCount":2`, `"problemCount":2`, `"registrationCode":"STU-000002"`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("preview lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if countRows(t, s, "students") != before {
		t.Error("a preview created students")
	}
	rec = s.call("POST", "/admissions/bulk", registrar, pre)
	for _, want := range []string{`"outcome":"admitted"`, `"outcome":"problem"`, `"outcome":"already_admitted"`, `"okCount":2`, `"problemCount":2`, `"registrationCode":"STU-000002"`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("bulk lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if countRows(t, s, "students") != before+1 {
		t.Errorf("bulk made %d students, want 1", countRows(t, s, "students")-before)
	}

	// audit: reads of the queue by the principal and the accountant are recorded under their own identity
	var reads int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('admissions.list', 'admissions.view')
		AND actor_id IN (SELECT id FROM users WHERE username IN ('pria', 'acct'))`).Scan(&reads); err != nil {
		t.Fatal(err)
	}
	if reads != 6 { // queue + detail for each (4), then the principal's two later queue reads
		t.Errorf("audited reads by principal/accountant = %d, want 6", reads)
	}
	var admits int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'admissions.admit'`).Scan(&admits); err != nil {
		t.Fatal(err)
	}
	if admits != 2 {
		t.Errorf("admit audit rows = %d, want 2 (refused, repeated and previewed admissions are not recorded)", admits)
	}

	for user, want := range map[string]bool{"clerk": true, "pria": true, "acct": true, "gate": true, "tina": false, "nobody": false} {
		a, _ := s.access(t, user)
		got := false
		for _, n := range a.Workspace.Navigation {
			got = got || n.ScreenID == "admissions"
		}
		if got != want {
			t.Errorf("%s sees the admissions screen = %v, want %v", user, got, want)
		}
	}
}

type admitBody struct {
	AlreadyAdmitted bool   `json:"alreadyAdmitted"`
	EnrollmentID    string `json:"enrollmentId"`
	Student         struct {
		ID               string `json:"id"`
		RegistrationCode string `json:"registrationCode"`
	} `json:"student"`
	Guardians []struct {
		Name string `json:"name"`
	} `json:"guardians"`
}

func countRows(t *testing.T, s accessStack, table string) int {
	t.Helper()
	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
