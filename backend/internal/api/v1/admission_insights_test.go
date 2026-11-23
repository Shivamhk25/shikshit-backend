package apiv1_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

type insightsOut struct {
	AsOf, TrendFrom, TrendTo string
	Enquiries                struct {
		Total    int
		ByStatus map[string]int
		BySource map[string]int
	}
	FollowUps     struct{ Due, DueToday, Overdue int }
	Registrations struct {
		Total, Admitted int
		ByStatus        map[string]int
	}
	Conversion map[string]*struct {
		Of, Converted int
		Rate          *float64
	}
	Trend []struct {
		Month                    string
		Enquiries, Registrations int
	}
}

type listTotal struct {
	Page struct{ Total int }
}

// addInsightsUser makes an active user with one custom role granting `permission` at `scope`.
func (s accessStack) addInsightsUser(t *testing.T, name, permission, scope string) string {
	t.Helper()
	hash, _ := identity.HashPassword(password)
	var uid, rid string
	if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, s.tenantID, name, hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, s.tenantID, name+"role").Scan(&rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2, $3, $4)`, s.tenantID, rid, permission, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, s.tenantID, uid, rid); err != nil {
		t.Fatal(err)
	}
	return name
}

// TestAdmissionInsightsPermissionAndCounts: the Admin, Registrar, Principal and Accountant (adm wildcards) may read the
// dashboard; a Teacher, a user with no role, a user holding only a class-section grant and an anonymous caller may
// not. A user holding only the insights permission reads the dashboard but not the lists behind it. The numbers equal
// the list endpoints' totals.
func TestAdmissionInsightsPermissionAndCounts(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken
	s.addInsightsUser(t, "classviewer", "adm.admission_insights.view", "class_section")
	s.addInsightsUser(t, "insightsonly", "adm.admission_insights.view", "tenant")
	s.addInsightsUser(t, "enquiryonly", "adm.enquiry.view", "tenant")
	scoped, only, enqOnly := s.signIn(t, "classviewer").AccessToken, s.signIn(t, "insightsonly").AccessToken, s.signIn(t, "enquiryonly").AccessToken

	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	for i, c := range []string{
		`{"enquirerName":"A","phone":"9876543210","childName":"C1","source":"walk_in","followUpDate":"` + yesterday + `"}`,
		`{"enquirerName":"B","phone":"9876543210","childName":"C2","source":"phone","status":"contacted","followUpDate":"2999-01-01"}`,
		`{"enquirerName":"C","phone":"9876543210","childName":"C3","source":"phone","status":"closed"}`,
	} {
		if rec := s.call("POST", "/enquiries", registrar, c); rec.Code != http.StatusCreated {
			t.Fatalf("enquiry %d = %d %s", i, rec.Code, rec.Body)
		}
	}

	for name, tok := range map[string]string{"admin": boss, "registrar": registrar, "principal": principal, "accountant": accountant, "insights only": only} {
		rec := s.call("GET", "/admission-insights", tok, "")
		if rec.Code != 200 {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
			continue
		}
		got := decode[insightsOut](t, rec)
		if got.Enquiries.Total != 3 || got.Enquiries.ByStatus["new"] != 1 || got.Enquiries.ByStatus["contacted"] != 1 || got.Enquiries.ByStatus["closed"] != 1 || got.Enquiries.ByStatus["visit_scheduled"] != 0 ||
			got.Enquiries.BySource["phone"] != 2 || got.Enquiries.BySource["referral"] != 0 || got.FollowUps.Due != 1 || got.FollowUps.Overdue != 1 || got.FollowUps.DueToday != 0 || len(got.Trend) != 12 {
			t.Errorf("%s body: %s", name, rec.Body)
		}
		if c := got.Conversion["enquiryToRegistration"]; c == nil || c.Of != 3 || c.Converted != 0 || c.Rate == nil || *c.Rate != 0 {
			t.Errorf("%s conversion: %s", name, rec.Body)
		}
		if c := got.Conversion["registrationToAccepted"]; c == nil || c.Of != 0 || c.Rate != nil {
			t.Errorf("%s registration conversion (no registrations: rate must be null): %s", name, rec.Body)
		}
		if c := got.Conversion["acceptedToAdmitted"]; c == nil || c.Of != 0 || c.Converted != 0 || c.Rate != nil || got.Registrations.Admitted != 0 {
			t.Errorf("%s: with nothing accepted, acceptedToAdmitted is 0 of 0 with a null rate: %s", name, rec.Body)
		}
	}
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody, "class-section grant": scoped, "enquiries only": enqOnly} {
		if rec := s.call("GET", "/admission-insights", tok, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, rec.Code)
		}
	}
	if rec := s.call("GET", "/admission-insights", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	// the insights permission does not open the lists behind it
	if rec := s.call("GET", "/enquiries", only, ""); rec.Code != http.StatusForbidden {
		t.Errorf("insights-only user reading enquiries = %d, want 403", rec.Code)
	}

	// same numbers as the list endpoints
	got := decode[insightsOut](t, s.call("GET", "/admission-insights", boss, ""))
	for _, c := range []struct {
		path string
		want int
	}{
		{"/enquiries?status=new", got.Enquiries.ByStatus["new"]}, {"/enquiries?status=closed", got.Enquiries.ByStatus["closed"]},
		{"/enquiries?source=phone", got.Enquiries.BySource["phone"]}, {"/enquiries?followUpDue=true", got.FollowUps.Due},
		{"/enquiries", got.Enquiries.Total}, {"/student-registrations", got.Registrations.Total},
		{"/admissions?state=admitted", got.Registrations.Admitted},
	} {
		if n := decode[listTotal](t, s.call("GET", c.path, boss, "")).Page.Total; n != c.want {
			t.Errorf("%s total = %d, dashboard says %d", c.path, n, c.want)
		}
	}

	// bad filters are 400 with a field
	for _, q := range []string{"from=2026-02-30", "from=2026-05-02&to=2026-05-01", "classId=nope", "yearId=1", "from=2000-01-01&to=2026-01-01"} {
		rec := s.call("GET", "/admission-insights?"+q, boss, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", q, rec.Code, rec.Body)
		}
	}
	// a range reaching into the past excludes today's enquiries
	rec := s.call("GET", fmt.Sprintf("/admission-insights?from=2020-01-01&to=2020-02-01"), boss, "")
	var raw map[string]json.RawMessage
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &raw) != nil || !strings.Contains(string(raw["enquiries"]), `"total":0`) {
		t.Errorf("old range = %d %s", rec.Code, rec.Body)
	}
	if got := decode[insightsOut](t, rec); len(got.Trend) != 2 || got.Trend[0].Month != "2020-01" || got.Trend[1].Month != "2020-02" {
		t.Errorf("old range trend = %+v", got.Trend)
	}
}

// TestAdmissionInsightsCountsAdmitted: admitting keeps a registration "accepted", so the dashboard's accepted count
// includes it; admitted equals the admissions list (state=admitted) and accepted minus admitted equals the queue.
func TestAdmissionInsightsCountsAdmitted(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	mk := func(token, path, body string) string {
		rec := s.call("POST", path, token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	class := mk(boss, "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	other := mk(boss, "/classes", `{"code":"VI","name":"Class 6","displayOrder":6}`)
	year := mk(boss, "/academic-years", `{"code":"Y","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	var section string
	if err := s.adm.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, s.tenantID).Scan(&section); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{class, other} {
		if _, err := s.adm.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, s.tenantID, year, c, section); err != nil {
			t.Fatal(err)
		}
	}
	reg := func(first, cls, status string) string {
		return mk(registrar, "/student-registrations", fmt.Sprintf(`{"appliedOn":"2026-05-01","firstName":%q,"lastName":"Rao","dateOfBirth":"2018-06-01","gender":"female","guardianName":"Meera Rao","guardianPhone":"9876543210","classId":%q,"yearId":%q,"status":%q}`, first, cls, year, status))
	}
	r1, r2, r3 := reg("Asha", class, "accepted"), reg("Bina", class, "accepted"), reg("Chitra", other, "accepted")
	reg("Dev", class, "under_review")
	for _, id := range []string{r1, r3} {
		if rec := s.call("POST", "/admissions/"+id+"/admit", registrar, fmt.Sprintf(`{"sectionId":%q}`, section)); rec.Code != http.StatusCreated {
			t.Fatalf("admit %s = %d %s", id, rec.Code, rec.Body)
		}
	}
	_ = r2
	for _, c := range []struct {
		query                       string
		accepted, admitted, waiting int
		rate                        float64
	}{
		{"", 3, 2, 1, 0.6667}, {"?classId=" + class, 2, 1, 1, 0.5}, {"?classId=" + other, 1, 1, 0, 1}, {"?yearId=" + year + "&from=2026-05-01&to=2026-05-31", 3, 2, 1, 0.6667},
	} {
		got := decode[insightsOut](t, s.call("GET", "/admission-insights"+c.query, boss, ""))
		ratio := got.Conversion["acceptedToAdmitted"]
		if got.Registrations.ByStatus["accepted"] != c.accepted || got.Registrations.Admitted != c.admitted || ratio == nil || ratio.Of != c.accepted || ratio.Converted != c.admitted || ratio.Rate == nil || *ratio.Rate != c.rate {
			t.Errorf("%q: %+v ratio %+v", c.query, got.Registrations, ratio)
		}
		filter := strings.TrimPrefix(strings.SplitN(c.query, "&from", 2)[0], "?")
		for state, want := range map[string]int{"admitted": c.admitted, "ready": c.waiting} {
			q := "state=" + state
			if filter != "" {
				q += "&" + filter
			}
			if n := decode[listTotal](t, s.call("GET", "/admissions?"+q, boss, "")).Page.Total; n != want {
				t.Errorf("%q admissions %s total = %d, want %d", c.query, state, n, want)
			}
		}
	}
}
