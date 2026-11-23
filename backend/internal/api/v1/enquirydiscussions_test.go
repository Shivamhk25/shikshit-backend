package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestEnquiryDiscussionsFollowThePermissionMatrix: the Admin and the Registrar manage discussions, the Principal and
// the Accountant read only (403 on every write), a Teacher and a user with no role are refused, an anonymous call is
// 401, and every successful read is audited under the reader's own identity.
func TestEnquiryDiscussionsFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken
	rec := s.call("POST", "/enquiries", registrar, `{"enquirerName":"Meera Rao","phone":"9876543210","childName":"Asha Rao","source":"walk_in","followUpDate":"2020-01-01"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup enquiry = %d %s", rec.Code, rec.Body)
	}
	enquiry := decode[idBody](t, rec).ID
	contacted := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	create := fmt.Sprintf(`{"enquiryId":%q,"contactedAt":%q,"channel":"call","summary":"Asked about fees","notes":"Will visit on Saturday","outcome":"interested","language":"hi"}`, enquiry, contacted)
	update := strings.Replace(create, `"channel":"call"`, `"channel":"whatsapp"`, 1)

	rec = s.call("POST", "/enquiry-discussions", registrar, create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registrar create = %d %s", rec.Code, rec.Body)
	}
	id := decode[idBody](t, rec).ID
	rec = s.call("GET", "/enquiry-discussions/"+id, registrar, "")
	for _, want := range []string{`"enquiryCode":"ENQ-000001"`, `"enquiryChildName":"Asha Rao"`, `"channel":"call"`, `"language":"hi"`, `"outcome":"interested"`, `"loggedByName":"clerk"`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("registrar get lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}
	if strings.Contains(rec.Body.String(), "updateEnquiryFollowUp") {
		t.Errorf("the write-only flag must not be returned: %s", rec.Body)
	}
	if rec := s.call("PUT", "/enquiry-discussions/"+id, registrar, update); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"channel":"whatsapp"`) {
		t.Errorf("registrar update = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/enquiry-discussions", boss, create); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"loggedByName":"boss"`) {
		t.Errorf("admin create = %d %s", rec.Code, rec.Body)
	}

	reads := 0
	for name, tok := range map[string]string{"principal": principal, "accountant": accountant} {
		for _, p := range []string{"/enquiry-discussions", "/enquiry-discussions/" + id} {
			reads++
			if rec := s.call("GET", p, tok, ""); rec.Code != 200 {
				t.Errorf("%s GET %s = %d", name, p, rec.Code)
			}
		}
		for _, c := range [][3]string{{"POST", "/enquiry-discussions", create}, {"PUT", "/enquiry-discussions/" + id, update}, {"DELETE", "/enquiry-discussions/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody} {
		for _, c := range [][3]string{{"GET", "/enquiry-discussions", ""}, {"GET", "/enquiry-discussions/" + id, ""}, {"POST", "/enquiry-discussions", create}, {"PUT", "/enquiry-discussions/" + id, update}, {"DELETE", "/enquiry-discussions/" + id, ""}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	if rec := s.call("GET", "/enquiry-discussions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	var byReaders int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('enquiry_discussions.view', 'enquiry_discussions.list')
		AND actor_id IN (SELECT id FROM users WHERE username IN ('pria', 'acct'))`).Scan(&byReaders); err != nil {
		t.Fatal(err)
	}
	if byReaders != reads {
		t.Errorf("audited reads by principal/accountant = %d, want %d", byReaders, reads)
	}
	var writes int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action LIKE 'enquiry_discussions.%' AND action NOT LIKE '%.view' AND action NOT LIKE '%.list'`).Scan(&writes); err != nil {
		t.Fatal(err)
	}
	if writes != 3 {
		t.Errorf("audited writes = %d, want 3 (refused writes are not recorded)", writes)
	}
	for user, want := range map[string]bool{"clerk": true, "pria": true, "acct": true, "tina": false, "nobody": false} {
		a, _ := s.access(t, user)
		got := false
		for _, n := range a.Workspace.Navigation {
			got = got || n.ScreenID == "enquiry_discussions"
		}
		if got != want {
			t.Errorf("%s sees the discussions screen = %v, want %v", user, got, want)
		}
	}

	// validation and contract errors
	for name, c := range map[string]struct{ body, field string }{
		"empty":                         {`{}`, ""},
		"no enquiry":                    {strings.Replace(create, enquiry, "", 1), ""},
		"unknown enquiry":               {strings.Replace(create, enquiry, "00000000-0000-0000-0000-000000000001", 1), "enquiryId"},
		"future contact":                {strings.Replace(create, contacted, time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339), 1), "contactedAt"},
		"bad channel":                   {strings.Replace(create, `"call"`, `"pigeon"`, 1), ""},
		"bad language":                  {strings.Replace(create, `"hi"`, `"klingon"`, 1), ""},
		"bad outcome":                   {strings.Replace(create, `"interested"`, `"won"`, 1), ""},
		"no summary":                    {strings.Replace(create, `Asked about fees`, `  `, 1), "summary"},
		"follow-up flag without a date": {strings.Replace(create, `"language"`, `"updateEnquiryFollowUp":true,"language"`, 1), "nextFollowUpDate"},
		"follow-up before the contact":  {strings.Replace(create, `"language"`, `"nextFollowUpDate":"2000-01-01","language"`, 1), "nextFollowUpDate"},
	} {
		rec := s.call("POST", "/enquiry-discussions", registrar, c.body)
		if rec.Code != http.StatusBadRequest || (c.field != "" && !strings.Contains(rec.Body.String(), c.field)) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for q, want := range map[string]int{"sort=password": 400, "channel=weird": 400, "outcome=weird": 400, "language=weird": 400, "enquiryId=notauuid": 400, "pageSize=1000": 400} {
		if rec := s.call("GET", "/enquiry-discussions?"+q, boss, ""); rec.Code != want {
			t.Errorf("GET ?%s = %d, want %d", q, rec.Code, want)
		}
	}
	for q, total := range map[string]int{"": 2, "q=fees": 2, "q=asha": 2, "q=nobody": 0, "channel=whatsapp": 1, "channel=call": 1, "language=hi": 2, "language=en": 0,
		"outcome=interested": 2, "outcome=not_reachable": 0, "enquiryId=" + enquiry: 2, "enquiryId=00000000-0000-0000-0000-000000000001": 0} {
		rec := s.call("GET", "/enquiry-discussions?"+q, registrar, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"total":%d`, total)) {
			t.Errorf("GET ?%s = %d %s, want total %d", q, rec.Code, rec.Body, total)
		}
	}
	// deleting the enquiry hides its notes; a registrar can delete a note
	if rec := s.call("DELETE", "/enquiry-discussions/"+id, registrar, ""); rec.Code != http.StatusNoContent {
		t.Errorf("registrar delete = %d", rec.Code)
	}
	if rec := s.call("GET", "/enquiry-discussions/"+id, registrar, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", rec.Code)
	}
	if rec := s.call("DELETE", "/enquiries/"+enquiry, registrar, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete enquiry = %d", rec.Code)
	}
	if rec := s.call("GET", "/enquiry-discussions", registrar, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("notes of a deleted enquiry are listed: %d %s", rec.Code, rec.Body)
	}
}

// TestADiscussionCanMoveTheEnquiryFollowUpDate: with updateEnquiryFollowUp the enquiry's follow-up date follows the note
// in the same save; without it, it does not.
func TestADiscussionCanMoveTheEnquiryFollowUpDate(t *testing.T) {
	s := newAccessStack(t)
	registrar := s.signIn(t, "clerk").AccessToken
	rec := s.call("POST", "/enquiries", registrar, `{"enquirerName":"Meera Rao","phone":"9876543210","childName":"Asha Rao","source":"walk_in","followUpDate":"2030-01-01"}`)
	enquiry := decode[idBody](t, rec).ID
	next := time.Now().AddDate(0, 0, 4).Format("2006-01-02")
	body := func(flag string) string {
		return fmt.Sprintf(`{"enquiryId":%q,"contactedAt":%q,"channel":"visit","summary":"Visited","outcome":"visit_scheduled","language":"en","nextFollowUpDate":%q%s}`,
			enquiry, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), next, flag)
	}
	if rec := s.call("POST", "/enquiry-discussions", registrar, body("")); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"nextFollowUpDate":"`+next+`"`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/enquiries/"+enquiry, registrar, ""); !strings.Contains(rec.Body.String(), `"followUpDate":"2030-01-01"`) {
		t.Errorf("the enquiry date must not move without the flag: %s", rec.Body)
	}
	if rec := s.call("POST", "/enquiry-discussions", registrar, body(`,"updateEnquiryFollowUp":true`)); rec.Code != http.StatusCreated {
		t.Fatalf("create with flag = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/enquiries/"+enquiry, registrar, ""); !strings.Contains(rec.Body.String(), `"followUpDate":"`+next+`"`) {
		t.Errorf("the enquiry date must follow the note: %s", rec.Body)
	}
}

// TestAnotherInstitutesDiscussionsAreInvisible: a note written in another institute never shows in a list, cannot be
// read, changed or deleted by id, and its enquiry cannot be used as a reference.
func TestAnotherInstitutesDiscussionsAreInvisible(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	var otherTenant, otherEnquiry, otherID string
	if err := s.adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ('rival', 'Rival School') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO enquiries (tenant_id, enquirer_name, phone, child_name, source) VALUES ($1, 'Other Parent', '9000000000', 'Other Child', 'phone') RETURNING id::text`, otherTenant).Scan(&otherEnquiry); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO enquiry_discussions (tenant_id, enquiry_id, contacted_at, channel, summary, outcome, language)
		VALUES ($1, $2, now(), 'call', 'Other note', 'interested', 'en') RETURNING id::text`, otherTenant, otherEnquiry).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("GET", "/enquiry-discussions", boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":0`) || strings.Contains(rec.Body.String(), "Other") {
		t.Errorf("list shows another institute: %d %s", rec.Code, rec.Body)
	}
	body := fmt.Sprintf(`{"enquiryId":%q,"contactedAt":%q,"channel":"call","summary":"x","outcome":"other","language":"en"}`, otherEnquiry, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339))
	for _, c := range [][3]string{{"GET", "/enquiry-discussions/" + otherID, ""}, {"PUT", "/enquiry-discussions/" + otherID, body}, {"DELETE", "/enquiry-discussions/" + otherID, ""}} {
		if rec := s.call(c[0], c[1], boss, c[2]); rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 404 (or 400 for a foreign enquiry)", c[0], c[1], rec.Code)
		}
	}
	if rec := s.call("POST", "/enquiry-discussions", boss, body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "enquiryId") {
		t.Errorf("another institute's enquiry as a reference = %d %s", rec.Code, rec.Body)
	}
	// and nothing of it was changed
	var summary string
	if err := s.adm.QueryRow(bg, `SELECT summary FROM enquiry_discussions WHERE id = $1`, otherID).Scan(&summary); err != nil || summary != "Other note" {
		t.Errorf("the other institute's note changed: %q %v", summary, err)
	}
}

// TestDiscussionsRefuseAGrantNarrowedToAClassSection: discussions have no class-section link, so a role that holds the
// view permission only for a class section is refused (403) and is not offered the screen.
func TestDiscussionsRefuseAGrantNarrowedToAClassSection(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope)
		SELECT tenant_id, id, 'adm.enquiry_discussions.view', 'class_section' FROM roles WHERE code = 'teacher' AND tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `UPDATE users SET access_version = access_version + 1 WHERE tenant_id = $1`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	teacher := s.signIn(t, "tina").AccessToken
	for _, p := range []string{"/enquiry-discussions", "/enquiry-discussions/00000000-0000-0000-0000-000000000001"} {
		if rec := s.call("GET", p, teacher, ""); rec.Code != http.StatusForbidden {
			t.Errorf("narrow grant GET %s = %d, want 403", p, rec.Code)
		}
	}
	a, _ := s.access(t, "tina")
	for _, n := range a.Workspace.Navigation {
		if n.ScreenID == "enquiry_discussions" {
			t.Error("the discussions screen must not be offered to a class-section grant")
		}
	}
}
