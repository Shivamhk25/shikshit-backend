package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestEnquiriesFollowThePermissionMatrix: the Admin and the Registrar manage enquiries, the Principal and the
// Accountant read only (403 on every write), a Teacher and a user with no role are refused, an anonymous call is
// 401, and every successful read is audited under the reader's own identity.
func TestEnquiriesFollowThePermissionMatrix(t *testing.T) {
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
	create := fmt.Sprintf(`{"enquirerName":"Meera Rao","phone":"9876543210","email":"meera@example.org","childName":"Asha Rao","dateOfBirth":"2021-06-01","classId":%q,"yearId":%q,"source":"walk_in","followUpDate":"2020-01-01"}`, class, year)
	update := strings.Replace(create, `"source":"walk_in"`, `"source":"phone","status":"contacted"`, 1)

	id := mk(registrar, "/enquiries", create)
	rec := s.call("GET", "/enquiries/"+id, registrar, "")
	for _, want := range []string{`"status":"new"`, `"className":"Class 5"`, `"yearName":"2026-27"`, `"followUpDue":true`, `"source":"walk_in"`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("registrar get lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if rec := s.call("PUT", "/enquiries/"+id, registrar, update); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"contacted"`) {
		t.Errorf("registrar update = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries", boss, create); rec.Code != http.StatusCreated {
		t.Errorf("admin create = %d %s", rec.Code, rec.Body)
	}
	// numbering is automatic (A-20): the registrar's enquiry is number 1 and the admin's number 2; a code sent by a client
	// is ignored, and replacing an enquiry never changes its number
	if rec := s.call("GET", "/enquiries/"+id, registrar, ""); !strings.Contains(rec.Body.String(), `"code":"ENQ-000001"`) {
		t.Errorf("first enquiry is not ENQ-000001: %s", rec.Body)
	}
	if rec := s.call("POST", "/enquiries", registrar, strings.Replace(create, `{`, `{"code":"MINE-1",`, 1)); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"code":"ENQ-000003"`) {
		t.Errorf("a code sent by the client must be ignored: %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/enquiries/"+id, registrar, strings.Replace(update, `{`, `{"code":"MINE-2",`, 1)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"code":"ENQ-000001"`) {
		t.Errorf("update must keep the number: %d %s", rec.Code, rec.Body)
	}

	// principal and accountant read only
	reads := 0
	for name, tok := range map[string]string{"principal": principal, "accountant": accountant} {
		for _, p := range []string{"/enquiries", "/enquiries/" + id} {
			reads++
			if rec := s.call("GET", p, tok, ""); rec.Code != 200 {
				t.Errorf("%s GET %s = %d", name, p, rec.Code)
			}
		}
		for _, c := range [][3]string{{"POST", "/enquiries", create}, {"PUT", "/enquiries/" + id, update}, {"DELETE", "/enquiries/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	// teacher and no-role users are refused everywhere; anonymous is 401
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody} {
		for _, c := range [][3]string{{"GET", "/enquiries", ""}, {"GET", "/enquiries/" + id, ""}, {"POST", "/enquiries", create}, {"PUT", "/enquiries/" + id, update}, {"DELETE", "/enquiries/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	if rec := s.call("GET", "/enquiries", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	// reads by the principal and the accountant were audited under their own identity; refused reads were not
	var byReaders int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('enquiries.view', 'enquiries.list')
		AND actor_id IN (SELECT id FROM users WHERE username IN ('pria', 'acct'))`).Scan(&byReaders); err != nil {
		t.Fatal(err)
	}
	if byReaders != reads {
		t.Errorf("audited reads by principal/accountant = %d, want %d", byReaders, reads)
	}
	var writes int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('enquiries.create', 'enquiries.update')`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 5 {
		t.Errorf("audited writes = %d, want 5 (refused writes are not recorded)", writes)
	}

	// the screen is offered to the roles that may view it, and to nobody else
	for user, want := range map[string]bool{"clerk": true, "pria": true, "acct": true, "tina": false, "nobody": false} {
		a, _ := s.access(t, user)
		got := false
		for _, n := range a.Workspace.Navigation {
			got = got || n.ScreenID == "enquiries"
		}
		if got != want {
			t.Errorf("%s sees the enquiries screen = %v, want %v", user, got, want)
		}
	}

	// validation and contract errors
	for name, c := range map[string]struct{ body, field string }{
		"empty":      {`{}`, ""},
		"bad phone":  {strings.Replace(create, "9876543210", "abc", 1), "phone"},
		"future dob": {strings.Replace(create, "2021-06-01", "2999-01-01", 1), "dateOfBirth"},
		"bad source": {strings.Replace(create, "walk_in", "pigeon", 1), ""},
		"bad class":  {strings.Replace(create, class, "00000000-0000-0000-0000-000000000001", 1), "classId"},
		"bad email":  {strings.Replace(create, "meera@example.org", "nope", 1), "email"},
		"bad status": {strings.Replace(create, `"source":"walk_in"`, `"source":"walk_in","status":"gone"`, 1), ""},
	} {
		rec := s.call("POST", "/enquiries", registrar, c.body)
		if rec.Code != http.StatusBadRequest || (c.field != "" && !strings.Contains(rec.Body.String(), c.field)) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for q, want := range map[string]int{"sort=password": 400, "status=weird": 400, "source=weird": 400, "followUpDue=maybe": 400, "classId=notauuid": 400, "pageSize=1000": 400} {
		if rec := s.call("GET", "/enquiries?"+q, boss, ""); rec.Code != want {
			t.Errorf("GET ?%s = %d, want %d", q, rec.Code, want)
		}
	}

	// filters and search through the API
	for q, total := range map[string]int{"": 3, "q=meera": 3, "q=ENQ-000002": 1, "q=nobody": 0, "status=contacted": 1, "status=new": 2, "source=walk_in": 2, "source=phone": 1,
		"classId=" + class: 3, "followUpDue=true": 3, "followUpDue=false": 0} {
		rec := s.call("GET", "/enquiries?"+q, registrar, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"total":%d`, total)) {
			t.Errorf("GET ?%s = %d %s, want total %d", q, rec.Code, rec.Body, total)
		}
	}
	if rec := s.call("DELETE", "/classes/"+class, boss, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete a class used by an enquiry = %d, want 409", rec.Code)
	}
	if rec := s.call("DELETE", "/enquiries/"+id, registrar, ""); rec.Code != http.StatusNoContent {
		t.Errorf("registrar delete = %d", rec.Code)
	}
	if rec := s.call("GET", "/enquiries/"+id, registrar, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", rec.Code)
	}
}

// TestAnotherInstitutesEnquiriesAreInvisible: an enquiry written in another institute never shows in a list, cannot be
// read, changed or deleted by id, and cannot be used as a reference.
func TestAnotherInstitutesEnquiriesAreInvisible(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	var otherTenant, otherID, otherClass string
	if err := s.adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ('rival', 'Rival School') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'V', 'Class 5') RETURNING id::text`, otherTenant).Scan(&otherClass); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO enquiries (tenant_id, code, enquirer_name, phone, child_name, source) VALUES ($1, 'ENQ-000001', 'Other Parent', '9000000000', 'Other Child', 'phone') RETURNING id::text`, otherTenant).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("GET", "/enquiries", boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":0`) || strings.Contains(rec.Body.String(), "Other Child") {
		t.Errorf("list shows another institute: %d %s", rec.Code, rec.Body)
	}
	body := `{"enquirerName":"Meera","phone":"9876543210","childName":"Asha","source":"phone"}`
	for _, c := range [][3]string{{"GET", "/enquiries/" + otherID, ""}, {"PUT", "/enquiries/" + otherID, body}, {"DELETE", "/enquiries/" + otherID, ""}} {
		if rec := s.call(c[0], c[1], boss, c[2]); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c[0], c[1], rec.Code)
		}
	}
	// numbering is per institute (this one's first enquiry is also ENQ-000001), and another institute's class is not a valid reference
	if rec := s.call("POST", "/enquiries", boss, body); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"code":"ENQ-000001"`) {
		t.Errorf("first enquiry of this institute = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiries", boss, strings.Replace(body, `{`, `{"classId":"`+otherClass+`",`, 1)); rec.Code != http.StatusBadRequest {
		t.Errorf("another institute's class = %d %s", rec.Code, rec.Body)
	}
}

// TestEnquiriesRefuseAGrantNarrowedToAClassSection: enquiries have no class-section link, so a role that holds the view
// permission only for a class section is refused (403) instead of being served every enquiry, and is not offered the screen.
func TestEnquiriesRefuseAGrantNarrowedToAClassSection(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT tenant_id, id, 'adm.enquiry.view', 'class_section' FROM roles WHERE code = 'teacher' AND tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `UPDATE users SET access_version = access_version + 1 WHERE tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	teacher := s.signIn(t, "tina").AccessToken
	for _, p := range []string{"/enquiries", "/enquiries/00000000-0000-0000-0000-000000000001"} {
		if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
			t.Errorf("narrow grant GET %s = %d, want 403", p, rec.Code)
		}
	}
	a, _ := s.access(t, "tina")
	for _, n := range a.Workspace.Navigation {
		if n.ScreenID == "enquiries" {
			t.Error("the enquiries screen must not be offered to a class-section grant")
		}
	}
}
