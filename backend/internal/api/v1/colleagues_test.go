package apiv1_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

type colleagueOut struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Label       string `json:"label"`
}

type colleaguePageOut struct {
	Items []colleagueOut `json:"items"`
	Page  struct{ Number, Size, Total int }
}

// addPerson inserts a user straight into the database for the lookup tests.
func (s usersStack) addPerson(t *testing.T, tenant, username, display, status, personType string) string {
	t.Helper()
	var id string
	if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash, person_type, person_id,
			email, phone)
		SELECT $1::uuid, $2::text, $3::text, $4::text, u.password_hash, NULLIF($5::text, ''), CASE WHEN $5::text = '' THEN NULL ELSE gen_random_uuid() END,
			$2::text || '@private.test', '9876500001'
		FROM users u WHERE u.username = 'plain' AND u.tenant_id = $6::uuid RETURNING id::text`,
		tenant, username, display, status, personType, s.tenant["alpha"]).Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.ids[username] = id
	return id
}

// colleagueStack is the users stack plus people in every state the lookup must treat differently.
func colleagueStack(t *testing.T) usersStack {
	t.Helper()
	s := newUsersStack(t)
	a := s.tenant["alpha"]
	s.addPerson(t, a, "priya", "Priya Rao", "active", "")
	s.addPerson(t, a, "pparent", "Priya Parent", "active", "guardian")
	s.addPerson(t, a, "pstudent", "Priya Student", "active", "student")
	s.addPerson(t, a, "pinvited", "Priya Invited", "invited", "")
	s.addPerson(t, a, "plocked", "Priya Locked", "locked", "")
	s.addPerson(t, a, "pdisabled", "Priya Disabled", "disabled", "")
	s.addPerson(t, a, "pdeleted", "Priya Deleted", "active", "")
	if _, err := s.adm.Exec(bg, `UPDATE users SET deleted_at = now() WHERE username = 'pdeleted'`); err != nil {
		t.Fatal(err)
	}
	// the same name in another institute
	s.addPerson(t, s.tenant["beta"], "pbeta", "Priya Beta", "active", "")
	return s
}

func (s usersStack) colleagues(t *testing.T, token, query string) colleaguePageOut {
	t.Helper()
	rec := s.do(http.MethodGet, alphaHost, "/colleagues"+query, token, "")
	want(t, rec, http.StatusOK, "")
	return decode[colleaguePageOut](t, rec)
}

func names(p colleaguePageOut) []string {
	out := []string{}
	for _, c := range p.Items {
		out = append(out, c.DisplayName)
	}
	return out
}

// Any signed-in staff account may look people up, with no permission; parent and student accounts and support visits
// may not; anonymous callers are refused.
func TestColleagueLookupWhoMayCall(t *testing.T) {
	s := colleagueStack(t)
	want(t, s.do(http.MethodGet, alphaHost, "/colleagues", "", ""), http.StatusUnauthorized, "")
	for _, who := range []string{"admin", "plain", "manager", "priya"} { // "plain" holds no role and no permission
		s.colleagues(t, s.signIn(t, alphaHost, who, password), "")
	}
	// plain cannot use the full user list: that is the gap this lookup closes
	want(t, s.do(http.MethodGet, alphaHost, "/users", s.signIn(t, alphaHost, "plain", password), ""), http.StatusForbidden, "")

	for _, who := range []string{"pparent", "pstudent"} {
		want(t, s.do(http.MethodGet, alphaHost, "/colleagues", s.signIn(t, alphaHost, who, password), ""), http.StatusForbidden, "permission_denied")
	}
	// an account linked to a guardian or student after it signed in loses the lookup at once
	tok := s.signIn(t, alphaHost, "priya", password)
	s.colleagues(t, tok, "")
	if _, err := s.adm.Exec(bg, `UPDATE users SET person_type = 'guardian', person_id = gen_random_uuid() WHERE username = 'priya'`); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/colleagues", tok, ""), http.StatusForbidden, "permission_denied")

	// a read-only support visit is not a person of the school
	v := s.startVisit(t, s.platformToken(t), s.tenant["alpha"])
	want(t, s.do(http.MethodGet, alphaHost, "/colleagues", s.redeem(t, alphaHost, v.Code), ""), http.StatusForbidden, "")
}

// Only active, live, non-parent, non-student users of the caller's own institute are returned.
func TestColleagueLookupReturnsOnlyActiveStaffOfTheInstitute(t *testing.T) {
	s := colleagueStack(t)
	tok := s.signIn(t, alphaHost, "plain", password)

	got := names(s.colleagues(t, tok, "?q=priya"))
	if strings.Join(got, "|") != "Priya Rao" {
		t.Fatalf("q=priya = %v, want only Priya Rao (not the parent, student, invited, locked, disabled, deleted or other-institute Priya)", got)
	}
	all := s.colleagues(t, tok, "")
	if strings.Join(names(all), "|") != "admin|manager|plain|Priya Rao|rolemgr" || all.Page.Total != 5 {
		t.Fatalf("everyone = %v (total %d)", names(all), all.Page.Total)
	}
	// the other institute sees its own people only
	rec := s.do(http.MethodGet, betaHost, "/colleagues?q=priya", s.signIn(t, betaHost, "betaadmin", password), "")
	want(t, rec, http.StatusOK, "")
	if got := names(decode[colleaguePageOut](t, rec)); strings.Join(got, "|") != "Priya Beta" {
		t.Fatalf("beta q=priya = %v", got)
	}
}

// The response is exactly id, displayName and label: no email, phone, username or status, whatever the person has on file.
func TestColleagueLookupNeverReturnsContactDetails(t *testing.T) {
	s := colleagueStack(t)
	rec := s.do(http.MethodGet, alphaHost, "/colleagues?q=priya", s.signIn(t, alphaHost, "plain", password), "")
	want(t, rec, http.StatusOK, "")
	body := rec.Body.String()
	for _, leak := range []string{"@private.test", "9876500001", "email", "phone", "username", "status", "personType", "priya@"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaks %q: %s", leak, body)
		}
	}
	var raw struct{ Items []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Items) != 1 {
		t.Fatalf("body = %s, %v", body, err)
	}
	keys := []string{}
	for k := range raw.Items[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "displayName,id,label" {
		t.Fatalf("fields = %v", keys)
	}
}

// The label is the role names (three at most) or "Staff".
func TestColleagueLookupLabels(t *testing.T) {
	s := colleagueStack(t)
	tok := s.signIn(t, alphaHost, "plain", password)
	label := func(q string) string {
		p := s.colleagues(t, tok, "?q="+q)
		if len(p.Items) != 1 {
			t.Fatalf("q=%s = %v", q, names(p))
		}
		return p.Items[0].Label
	}
	if got := label("Priya"); got != "Staff" {
		t.Fatalf("no role = %q", got)
	}
	grant := func(code string, extra string) {
		t.Helper()
		if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id, starts_at, ends_at)
			SELECT u.tenant_id, u.id, r.id, `+extra+` FROM users u JOIN roles r ON r.tenant_id = u.tenant_id AND r.code = $1
			WHERE u.username = 'priya'`, code); err != nil {
			t.Fatal(err)
		}
	}
	grant("principal", "now() - interval '1 day', NULL")
	if got := label("Priya"); got != "Principal" {
		t.Fatalf("one role = %q", got)
	}
	// a role that has ended, or has not started, is not shown
	grant("teacher", "now() - interval '2 day', now() - interval '1 day'")
	grant("accountant", "now() + interval '1 day', NULL")
	if got := label("Priya"); got != "Principal" {
		t.Fatalf("inactive roles shown: %q", got)
	}
	grant("registrar", "now() - interval '1 day', NULL")
	grant("librarian", "now() - interval '1 day', NULL")
	grant("nurse", "now() - interval '1 day', NULL")
	if got := label("Priya"); !strings.HasSuffix(got, ", +1 more") || strings.Count(got, ",") != 3 {
		t.Fatalf("four roles = %q, want three names and +1 more", got)
	}
}

// Search by name, case-insensitive and literal; q is empty or at least two characters; paging is stable and capped.
func TestColleagueLookupSearchAndPaging(t *testing.T) {
	s := colleagueStack(t)
	a := s.tenant["alpha"]
	for i := 1; i <= 7; i++ {
		s.addPerson(t, a, fmt.Sprintf("pg%d", i), fmt.Sprintf("Pager Person %d", i), "active", "")
	}
	s.addPerson(t, a, "twin1", "Twin Person", "active", "")
	s.addPerson(t, a, "twin2", "Twin Person", "active", "")
	s.addPerson(t, a, "pct", "Rate 100% Real", "active", "")
	tok := s.signIn(t, alphaHost, "plain", password)

	// case-insensitive contains
	if got := names(s.colleagues(t, tok, "?q=PRIYA%20ra")); strings.Join(got, "|") != "Priya Rao" {
		t.Fatalf("q=PRIYA ra = %v", got)
	}
	// % and _ are literal text, not wildcards
	if got := names(s.colleagues(t, tok, "?q=%25%25")); len(got) != 0 {
		t.Fatalf("q=%%%% matched %v", got)
	}
	if got := names(s.colleagues(t, tok, "?q=100%25")); strings.Join(got, "|") != "Rate 100% Real" {
		t.Fatalf("q=100%% = %v", got)
	}
	if got := names(s.colleagues(t, tok, "?q=Pager_Person")); len(got) != 0 {
		t.Fatalf("underscore acted as a wildcard: %v", got)
	}
	// a blank search is the same as none; one character is refused; two are fine
	if p := s.colleagues(t, tok, "?q=%20%20"); p.Page.Total != s.colleagues(t, tok, "").Page.Total {
		t.Fatalf("blank q differs from none")
	}
	want(t, s.do(http.MethodGet, alphaHost, "/colleagues?q=p", tok, ""), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, alphaHost, "/colleagues?q="+strings.Repeat("a", 101), tok, ""), http.StatusBadRequest, "validation_failed")
	s.colleagues(t, tok, "?q=pa")

	// paging: three at a time through seven, in name order, with the total and no repeats
	var walked []string
	for page := 1; page <= 3; page++ {
		p := s.colleagues(t, tok, fmt.Sprintf("?q=pager%%20person&pageSize=3&page=%d", page))
		if p.Page.Total != 7 || p.Page.Size != 3 || p.Page.Number != page {
			t.Fatalf("page %d info = %+v", page, p.Page)
		}
		walked = append(walked, names(p)...)
	}
	if want := "Pager Person 1|Pager Person 2|Pager Person 3|Pager Person 4|Pager Person 5|Pager Person 6|Pager Person 7"; strings.Join(walked, "|") != want {
		t.Fatalf("walked = %v", walked)
	}
	if p := s.colleagues(t, tok, "?q=pager&pageSize=3&page=4"); len(p.Items) != 0 || p.Page.Total != 0 {
		t.Fatalf("page past the end = %+v", p)
	}
	// equal names come in id order, every time
	twins := s.colleagues(t, tok, "?q=twin")
	if len(twins.Items) != 2 || twins.Items[0].ID >= twins.Items[1].ID || twins.Items[0].ID != s.colleagues(t, tok, "?q=twin").Items[0].ID {
		t.Fatalf("twins = %+v", twins.Items)
	}

	// the page size is capped at 50 and defaults to 25
	if p := s.colleagues(t, tok, ""); p.Page.Size != 25 {
		t.Fatalf("default size = %d", p.Page.Size)
	}
	if p := s.colleagues(t, tok, "?pageSize=50"); p.Page.Size != 50 {
		t.Fatalf("size 50 = %d", p.Page.Size)
	}
	for _, bad := range []string{"pageSize=51", "pageSize=200", "pageSize=0", "pageSize=-1", "page=0", "page=10001", "page=-3"} {
		want(t, s.do(http.MethodGet, alphaHost, "/colleagues?"+bad, tok, ""), http.StatusBadRequest, "validation_failed")
	}
}

// The delegation check accepts exactly the people the lookup returns (except the caller, who cannot delegate to
// themselves): every person in the institute is tried and the two answers must agree.
func TestDelegationAcceptsExactlyTheLookup(t *testing.T) {
	s := colleagueStack(t)
	mgr := s.signIn(t, alphaHost, "manager", password)
	offered := map[string]bool{}
	for _, c := range s.colleagues(t, mgr, "?pageSize=50").Items {
		offered[c.ID] = true
	}
	tried := 0
	for username, id := range s.ids {
		if username == "manager" {
			continue // self
		}
		tried++
		rec := s.do(http.MethodPost, alphaHost, "/approvals/delegations", mgr, s.delegationBody(username, 1))
		if offered[id] {
			want(t, rec, http.StatusCreated, "")
		} else {
			want(t, rec, http.StatusBadRequest, "validation_failed")
			if !strings.Contains(rec.Body.String(), "delegateId") {
				t.Errorf("%s: refusal does not name delegateId: %s", username, rec.Body)
			}
		}
	}
	if tried < 12 {
		t.Fatalf("only %d people tried", tried)
	}
}

// An enquiry can be newly assigned only to the people the lookup returns; an assignee who was locked later stays
// on the enquiry, and editing other fields still works.
func TestEnquiryAssigneeIsAColleague(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	person := func(username, status, personType string) string {
		t.Helper()
		var id string
		if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash, person_type, person_id)
			SELECT tenant_id, $1::text, $1::text, $2::text, password_hash, NULLIF($3::text, ''), CASE WHEN $3::text = '' THEN NULL ELSE gen_random_uuid() END
			FROM users WHERE username = 'boss' RETURNING id::text`, username, status, personType).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	staff, locked := person("staff1", "active", ""), person("staff2", "active", "")
	parent, student := person("par", "active", "guardian"), person("stu", "active", "student")
	invited, disabled, locked2 := person("inv", "invited", ""), person("dis", "disabled", ""), person("lck", "locked", "")
	body := func(code, assignee string) string {
		return fmt.Sprintf(`{"code":%q,"enquirerName":"Meera Rao","phone":"9876543210","childName":"Asha Rao","source":"walk_in","assignedToId":%q}`, code, assignee)
	}
	cases := []struct {
		who string
		ok  bool
	}{{staff, true}, {locked, true}, {parent, false}, {student, false}, {invited, false}, {disabled, false}, {locked2, false},
		{"not-a-uuid", false}, {"00000000-0000-4000-8000-000000000000", false}}
	for n, c := range cases {
		rec := s.call("POST", "/enquiries", boss, body(fmt.Sprintf("E-%d", n), c.who))
		switch {
		case c.ok && rec.Code != http.StatusCreated:
			t.Errorf("assign to %s = %d %s, want 201", c.who, rec.Code, rec.Body)
		case !c.ok && rec.Code != http.StatusBadRequest:
			t.Errorf("assign to %s = %d %s, want 400", c.who, rec.Code, rec.Body)
		}
	}
	// the assignee is locked later: the enquiry keeps them and can still be edited, but nobody else can be given a locked person
	rec := s.call("POST", "/enquiries", boss, body("E-KEEP", locked))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id := decode[idBody](t, rec).ID
	if _, err := s.adm.Exec(bg, `UPDATE users SET status = 'locked' WHERE id = $1::uuid`, locked); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("PUT", "/enquiries/"+id, boss, strings.Replace(body("E-KEEP", locked), "Meera Rao", "Meera R. Rao", 1)); rec.Code != http.StatusOK {
		t.Errorf("editing with the unchanged, now locked assignee = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/enquiries/"+id, boss, body("E-KEEP", locked2)); rec.Code != http.StatusBadRequest {
		t.Errorf("re-assigning to a locked person = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/enquiries/"+id, boss, body("E-KEEP", staff)); rec.Code != http.StatusOK {
		t.Errorf("re-assigning to staff = %d %s", rec.Code, rec.Body)
	}
}
