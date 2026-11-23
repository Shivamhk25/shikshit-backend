package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestRegistrationsFollowThePermissionMatrix: the Admin and the Registrar manage registrations, the Principal and the
// Accountant read only (403 on every write), a Teacher and a user with no role are refused, an anonymous call is 401,
// and every successful read is audited under the reader's own identity.
func TestRegistrationsFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken
	mk := func(token, path, body string) string {
		rec := s.call("POST", path, token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	class := mk(boss, "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	year := mk(boss, "/academic-years", `{"code":"Y","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	create := fmt.Sprintf(`{"appliedOn":"2026-05-01","firstName":"Asha","lastName":"Rao","dateOfBirth":"2018-06-01","guardianName":"Meera Rao","guardianPhone":"9876543210","classId":%q,"yearId":%q}`, class, year)
	update := strings.Replace(create, `"firstName":"Asha"`, `"firstName":"Aisha","status":"under_review"`, 1)

	id := mk(registrar, "/student-registrations", create)
	rec := s.call("GET", "/student-registrations/"+id, registrar, "")
	for _, want := range []string{`"status":"submitted"`, `"className":"Class 5"`, `"yearName":"2026-27"`, `"applicationNumber":"REG-000001"`, `"fullName":"Asha Rao"`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("registrar get lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if rec := s.call("PUT", "/student-registrations/"+id, registrar, update); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"under_review"`) || !strings.Contains(rec.Body.String(), `"REG-000001"`) {
		t.Errorf("registrar update = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/student-registrations", boss, create); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"REG-000002"`) {
		t.Errorf("admin create = %d %s", rec.Code, rec.Body)
	}
	// a client cannot choose the number or the enquiry link
	if rec := s.call("POST", "/student-registrations", boss, strings.Replace(create, `"firstName"`, `"applicationNumber":"REG-777","enquiryId":"00000000-0000-0000-0000-000000000001","firstName"`, 1)); rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"REG-000003"`) || strings.Contains(rec.Body.String(), "REG-777") || !strings.Contains(rec.Body.String(), `"enquiryId":null`) {
		t.Errorf("client-chosen number = %d %s", rec.Code, rec.Body)
	}

	reads := 0
	for name, tok := range map[string]string{"principal": principal, "accountant": accountant} {
		for _, p := range []string{"/student-registrations", "/student-registrations/" + id} {
			reads++
			if rec := s.call("GET", p, tok, ""); rec.Code != 200 {
				t.Errorf("%s GET %s = %d", name, p, rec.Code)
			}
		}
		for _, c := range [][3]string{{"POST", "/student-registrations", create}, {"PUT", "/student-registrations/" + id, update}, {"DELETE", "/student-registrations/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody} {
		for _, c := range [][3]string{{"GET", "/student-registrations", ""}, {"GET", "/student-registrations/" + id, ""}, {"POST", "/student-registrations", create}, {"PUT", "/student-registrations/" + id, update}, {"DELETE", "/student-registrations/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	if rec := s.call("GET", "/student-registrations", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	var byReaders, writes int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('student_registrations.view', 'student_registrations.list')
		AND actor_id IN (SELECT id FROM users WHERE username IN ('pria', 'acct'))`).Scan(&byReaders); err != nil {
		t.Fatal(err)
	}
	if byReaders != reads {
		t.Errorf("audited reads by principal/accountant = %d, want %d", byReaders, reads)
	}
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('student_registrations.create', 'student_registrations.update')`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 4 {
		t.Errorf("audited writes = %d, want 4 (refused writes are not recorded)", writes)
	}

	for user, want := range map[string]bool{"clerk": true, "pria": true, "acct": true, "tina": false, "nobody": false} {
		a, _ := s.access(t, user)
		got := false
		for _, n := range a.Workspace.Navigation {
			got = got || n.ScreenID == "registrations"
		}
		if got != want {
			t.Errorf("%s sees the registrations screen = %v, want %v", user, got, want)
		}
	}

	for name, c := range map[string]struct{ body, field string }{
		"empty":       {`{}`, ""},
		"bad phone":   {strings.Replace(create, "9876543210", "abc", 1), "guardianPhone"},
		"future dob":  {strings.Replace(create, "2018-06-01", "2999-01-01", 1), "dateOfBirth"},
		"future date": {strings.Replace(create, "2026-05-01", "2999-01-01", 1), "appliedOn"},
		"bad status":  {strings.Replace(create, `"firstName"`, `"status":"gone","firstName"`, 1), ""},
		"bad gender":  {strings.Replace(create, `"firstName"`, `"gender":"robot","firstName"`, 1), "gender"},
		"bad class":   {strings.Replace(create, class, "00000000-0000-0000-0000-000000000001", 1), "classId"},
		"no class":    {strings.Replace(create, fmt.Sprintf(`"classId":%q,`, class), "", 1), ""},
		"bad sibling": {strings.Replace(create, `"firstName"`, `"siblingStudentCode":"NOPE","firstName"`, 1), "siblingStudentCode"},
	} {
		rec := s.call("POST", "/student-registrations", registrar, c.body)
		if rec.Code != http.StatusBadRequest || (c.field != "" && !strings.Contains(rec.Body.String(), c.field)) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for q, want := range map[string]int{"sort=password": 400, "status=weird": 400, "classId=notauuid": 400, "yearId=notauuid": 400, "pageSize=1000": 400} {
		if rec := s.call("GET", "/student-registrations?"+q, boss, ""); rec.Code != want {
			t.Errorf("GET ?%s = %d, want %d", q, rec.Code, want)
		}
	}
	for q, total := range map[string]int{"": 3, "q=asha": 2, "q=aisha": 1, "q=nobody": 0, "q=REG-000002": 1, "status=under_review": 1, "status=submitted": 2,
		"classId=" + class: 3, "yearId=" + year: 3, "sort=applicant": 3} {
		rec := s.call("GET", "/student-registrations?"+q, registrar, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"total":%d`, total)) {
			t.Errorf("GET ?%s = %d %s, want total %d", q, rec.Code, rec.Body, total)
		}
	}
	if rec := s.call("DELETE", "/classes/"+class, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete a class used by a registration = %d, want 409", rec.Code)
	}
	if rec := s.call("DELETE", "/academic-years/"+year, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete a year used by a registration = %d, want 409", rec.Code)
	}
	if rec := s.call("DELETE", "/student-registrations/"+id, registrar, ""); rec.Code != http.StatusNoContent {
		t.Errorf("registrar delete = %d", rec.Code)
	}
	if rec := s.call("GET", "/student-registrations/"+id, registrar, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", rec.Code)
	}
}

// TestRegisteringAnEnquiryFollowsTheRules: the Registrar and the Admin may register an enquiry, which marks it
// converted; a second attempt and a closed enquiry are 409; the Principal and the Accountant (view only), a Teacher and
// a user who may create registrations but not see enquiries are refused; a missing detail is a 400.
func TestRegisteringAnEnquiryFollowsTheRules(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant, teacher := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken, s.signIn(t, "tina").AccessToken
	mk := func(token, path, body string) string {
		rec := s.call("POST", path, token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	class := mk(boss, "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	year := mk(boss, "/academic-years", `{"code":"Y","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	enquiry := func(code, extra string) string {
		return mk(registrar, "/enquiries", fmt.Sprintf(`{"code":%q,"enquirerName":"Meera Rao","phone":"9876543210","childName":"Asha Kumari Rao","source":"walk_in"%s}`, code, extra))
	}
	full := fmt.Sprintf(`,"dateOfBirth":"2018-06-01","classId":%q,"yearId":%q`, class, year)
	status := func(id string) string {
		var st string
		if err := s.adm.QueryRow(bg, `SELECT status FROM enquiries WHERE id::text = $1`, id).Scan(&st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	e1, e2, e3 := enquiry("E-1", full), enquiry("E-2", full), enquiry("E-3", full)

	for name, tok := range map[string]string{"principal": principal, "accountant": accountant, "teacher": teacher} {
		if rec := s.call("POST", "/enquiries/"+e1+"/register", tok, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s register = %d, want 403", name, rec.Code)
		}
	}
	if rec := s.call("POST", "/enquiries/"+e1+"/register", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous register = %d", rec.Code)
	}
	if status(e1) != "new" {
		t.Error("a refused call changed the enquiry")
	}

	rec := s.call("POST", "/enquiries/"+e1+"/register", registrar, "")
	for _, want := range []string{`"firstName":"Asha"`, `"lastName":"Kumari Rao"`, `"guardianName":"Meera Rao"`, `"guardianPhone":"9876543210"`, `"enquiryCode":"E-1"`, `"status":"submitted"`, `"applicationNumber":"REG-000001"`} {
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("register lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if status(e1) != "converted" {
		t.Errorf("enquiry status = %s", status(e1))
	}
	if rec := s.call("GET", "/enquiries/"+e1, registrar, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"converted"`) {
		t.Errorf("get enquiry = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries/"+e1+"/register", registrar, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already_converted") {
		t.Errorf("second register = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries/"+e2+"/register", boss, ""); rec.Code != http.StatusCreated {
		t.Errorf("admin register = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/enquiries/"+e3, registrar, fmt.Sprintf(`{"code":"E-3","enquirerName":"Meera Rao","phone":"9876543210","childName":"Asha","source":"walk_in","status":"closed"%s}`, full)); rec.Code != 200 {
		t.Fatalf("close enquiry = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries/"+e3+"/register", registrar, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "enquiry_closed") || status(e3) != "closed" {
		t.Errorf("register closed = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries/00000000-0000-0000-0000-000000000001/register", registrar, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown enquiry = %d", rec.Code)
	}
	// an enquiry without the details a registration needs: 400 naming them; the details can be sent with the call
	bare := enquiry("E-4", "")
	if rec := s.call("POST", "/enquiries/"+bare+"/register", registrar, ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "dateOfBirth") || !strings.Contains(rec.Body.String(), "classId") || status(bare) != "new" {
		t.Errorf("bare enquiry = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries/"+bare+"/register", registrar, fmt.Sprintf(`{"dateOfBirth":"2018-06-01","classId":%q,"yearId":%q,"gender":"female"}`, class, year)); rec.Code != http.StatusCreated ||
		!strings.Contains(rec.Body.String(), `"gender":"female"`) || status(bare) != "converted" {
		t.Errorf("bare enquiry with details = %d %s", rec.Code, rec.Body)
	}
	var created, converted int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FILTER (WHERE action = 'student_registrations.create'), count(*) FILTER (WHERE action = 'enquiries.convert') FROM audit_log`).Scan(&created, &converted); err != nil {
		t.Fatal(err)
	}
	if created != 3 || converted != 3 {
		t.Errorf("audit: %d registrations, %d conversions, want 3 and 3", created, converted)
	}

	// a role that may create registrations but not see enquiries cannot register an enquiry; nor can one whose
	// enquiry view is narrowed to a class section
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT tenant_id, id, 'adm.student_registrations.create', 'tenant' FROM roles WHERE code = 'teacher' AND tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `UPDATE users SET access_version = access_version + 1 WHERE tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	teacher = s.signIn(t, "tina").AccessToken
	e5 := enquiry("E-5", full)
	if rec := s.call("POST", "/enquiries/"+e5+"/register", teacher, ""); rec.Code != http.StatusForbidden || status(e5) != "new" {
		t.Errorf("create-only role = %d %s", rec.Code, rec.Body)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT tenant_id, id, 'adm.enquiry.view', 'class_section' FROM roles WHERE code = 'teacher' AND tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `UPDATE users SET access_version = access_version + 1 WHERE tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	teacher = s.signIn(t, "tina").AccessToken
	if rec := s.call("POST", "/enquiries/"+e5+"/register", teacher, ""); rec.Code != http.StatusForbidden || status(e5) != "new" {
		t.Errorf("narrow enquiry view = %d %s", rec.Code, rec.Body)
	}
}

// TestAnotherInstitutesRegistrationsAreInvisible: a registration written in another institute never shows in a list,
// cannot be read, changed or deleted by id, and its enquiry cannot be registered from here.
func TestAnotherInstitutesRegistrationsAreInvisible(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	var otherTenant, otherClass, otherYear, otherID, otherEnquiry string
	if err := s.adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ('rival', 'Rival School') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'V', 'Class 5') RETURNING id::text`, otherTenant).Scan(&otherClass); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date) VALUES ($1, 'Y', '2026-27', '2026-04-01', '2027-03-31') RETURNING id::text`, otherTenant).Scan(&otherYear); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO student_registrations (tenant_id, applied_on, first_name, date_of_birth, guardian_phone, class_id, year_id) VALUES ($1, '2026-05-01', 'Other Child', '2018-01-01', '9000000000', $2, $3) RETURNING id::text`,
		otherTenant, otherClass, otherYear).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO enquiries (tenant_id, code, enquirer_name, phone, child_name, source, class_id, year_id, date_of_birth) VALUES ($1, 'E-1', 'P', '9000000000', 'C', 'phone', $2, $3, '2018-01-01') RETURNING id::text`,
		otherTenant, otherClass, otherYear).Scan(&otherEnquiry); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("GET", "/student-registrations", boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":0`) || strings.Contains(rec.Body.String(), "Other Child") {
		t.Errorf("list shows another institute: %d %s", rec.Code, rec.Body)
	}
	body := fmt.Sprintf(`{"appliedOn":"2026-05-01","firstName":"Asha","dateOfBirth":"2018-06-01","guardianPhone":"9876543210","classId":%q,"yearId":%q}`, otherClass, otherYear)
	for _, c := range [][3]string{{"GET", "/student-registrations/" + otherID, ""}, {"PUT", "/student-registrations/" + otherID, body}, {"DELETE", "/student-registrations/" + otherID, ""}, {"POST", "/enquiries/" + otherEnquiry + "/register", ""}} {
		if rec := s.call(c[0], c[1], boss, c[2]); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c[0], c[1], rec.Code)
		}
	}
	if rec := s.call("POST", "/student-registrations", boss, body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "classId") {
		t.Errorf("another institute's class = %d %s", rec.Code, rec.Body)
	}
}

// TestRegistrationsRefuseAGrantNarrowedToAClassSection: registrations have no class-section link, so a role that holds
// the view permission only for a class section is refused (403) instead of being served every registration, and is
// not offered the screen.
func TestRegistrationsRefuseAGrantNarrowedToAClassSection(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT tenant_id, id, 'adm.student_registrations.view', 'class_section' FROM roles WHERE code = 'teacher' AND tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `UPDATE users SET access_version = access_version + 1 WHERE tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	teacher := s.signIn(t, "tina").AccessToken
	for _, p := range []string{"/student-registrations", "/student-registrations/00000000-0000-0000-0000-000000000001"} {
		if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
			t.Errorf("narrow grant GET %s = %d, want 403", p, rec.Code)
		}
	}
	a, _ := s.access(t, "tina")
	for _, n := range a.Workspace.Navigation {
		if n.ScreenID == "registrations" {
			t.Error("the registrations screen must not be offered to a class-section grant")
		}
	}
}
