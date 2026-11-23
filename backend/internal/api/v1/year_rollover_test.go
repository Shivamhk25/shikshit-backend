package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

type rolloverResult struct {
	Step          string `json:"step"`
	DryRun        bool   `json:"dryRun"`
	CreatedCount  int    `json:"createdCount"`
	ExistingCount int    `json:"existingCount"`
	ConflictCount int    `json:"conflictCount"`
	SkippedCount  int    `json:"skippedCount"`
	RunID         string `json:"runId"`
	Blockers      []string
	Items         []struct{ Label, Status, Reason string }
}

type rolloverChecklist struct {
	From struct {
		ID, Name  string
		IsCurrent bool
	} `json:"from"`
	To struct {
		ID, Name  string
		IsCurrent bool
	} `json:"to"`
	Steps []struct {
		ID           string
		Irreversible bool
		SourceCount  int
		TargetCount  int
		LastRun      *struct{ CreatedCount int }
	}
	Shift struct {
		Mode  string
		Years int
	}
}

// TestYearRolloverFollowsThePermissionMatrix: the Admin does everything. A user with only `view` reads the checklist
// and the history; with `create` as well, previews and runs the copy steps and creates the new year, but needs `edit`
// to make the new year current. Everyone else (Registrar, Principal, Accountant, Teacher, a user with no role, a grant
// that is not institute-wide) is refused on every operation; an anonymous call is 401. Making the year current needs
// `confirm`, is refused while the new year has no terms or class sections, and every run is audited under the user.
func TestYearRolloverFollowsThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	hash, _ := identity.HashPassword(password)
	mkUser := func(name string, scope string, perms ...string) string {
		t.Helper()
		var rid, uid string
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(s.adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, s.tenantID, name).Scan(&rid))
		for _, p := range perms {
			_, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2, $3, $4)`, s.tenantID, rid, p, scope)
			must(err)
		}
		must(s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, s.tenantID, name, hash).Scan(&uid))
		_, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, s.tenantID, uid, rid)
		must(err)
		return s.signIn(t, name).AccessToken
	}
	viewer := mkUser("rview", "tenant", "ten.year_rollover.view")
	roller := mkUser("rroll", "tenant", "ten.year_rollover.view", "ten.year_rollover.create")
	switcher := mkUser("rswitch", "tenant", "ten.year_rollover.view", "ten.year_rollover.create", "ten.year_rollover.edit")
	narrow := mkUser("rnarrow", "self", "ten.year_rollover.view", "ten.year_rollover.create", "ten.year_rollover.edit")
	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken

	mk := func(path, body string) string {
		t.Helper()
		rec := s.call("POST", path, boss, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	a := mk("/academic-years", `{"code":"Y1","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	b := mk("/academic-years", `{"code":"Y2","name":"2027-28","startDate":"2027-04-01","endDate":"2028-03-31"}`)
	c5 := mk("/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	var secA string
	if err := s.adm.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, s.tenantID).Scan(&secA); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date) VALUES ('%s', '%s', 'T1', 'Term 1', '2026-04-01', '2026-09-30')`, s.tenantID, a),
		fmt.Sprintf(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ('%s', '%s', '%s', '%s')`, s.tenantID, a, c5, secA),
		fmt.Sprintf(`UPDATE academic_years SET is_current = true WHERE id = '%s'`, a),
	} {
		if _, err := s.adm.Exec(bg, q); err != nil {
			t.Fatal(err)
		}
	}

	pair := fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q}`, a, b)
	confirmed := fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q,"confirm":true}`, a, b)
	checklist := fmt.Sprintf("/year-rollover/checklist?fromYearId=%s&toYearId=%s", a, b)
	newYear := fmt.Sprintf(`{"fromYearId":%q,"code":"Y9","name":"2031-32","startDate":"2031-04-01","endDate":"2032-03-31"}`, a)
	ops := [][3]string{{"GET", checklist, ""}, {"GET", "/year-rollover/history", ""}, {"POST", "/year-rollover/years", newYear},
		{"POST", "/year-rollover/steps/terms/preview", pair}, {"POST", "/year-rollover/steps/terms/run", pair}}

	// who is refused everything: other roles, no role, a grant that is not institute-wide
	for name, tok := range map[string]string{"registrar": registrar, "principal": principal, "accountant": accountant, "teacher": teacher, "nobody": nobody, "narrow scope": narrow} {
		for _, o := range ops {
			if rec := s.call(o[0], o[1], tok, o[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, o[0], o[1], rec.Code)
			}
		}
	}
	for _, o := range ops {
		if rec := s.call(o[0], o[1], "", o[2]); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d", o[0], o[1], rec.Code)
		}
	}
	// view only: reads, nothing else
	for _, o := range ops[:2] {
		if rec := s.call(o[0], o[1], viewer, o[2]); rec.Code != 200 {
			t.Errorf("viewer %s %s = %d %s", o[0], o[1], rec.Code, rec.Body)
		}
	}
	for _, o := range ops[2:] {
		if rec := s.call(o[0], o[1], viewer, o[2]); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s = %d, want 403", o[0], o[1], rec.Code)
		}
	}
	var rows int
	_ = s.adm.QueryRow(bg, `SELECT (SELECT count(*) FROM year_rollover_runs) + (SELECT count(*) FROM academic_terms WHERE year_id::text = $1)`, b).Scan(&rows)
	if rows != 0 {
		t.Fatalf("refused calls changed %d rows", rows)
	}

	// the checklist
	cl := decode[rolloverChecklist](t, s.call("GET", checklist, boss, ""))
	if len(cl.Steps) != 6 || cl.From.ID != a || !cl.From.IsCurrent || cl.To.IsCurrent || cl.Shift.Mode != "years" || cl.Shift.Years != 1 {
		t.Fatalf("checklist = %+v", cl)
	}
	if cl.Steps[0].ID != "terms" || cl.Steps[0].SourceCount != 1 || cl.Steps[5].ID != "make_current" || !cl.Steps[5].Irreversible || cl.Steps[0].Irreversible {
		t.Errorf("steps = %+v", cl.Steps)
	}
	for name, c := range map[string]struct {
		method, path, body, code string
		status                   int
	}{
		"same year":     {"GET", fmt.Sprintf("/year-rollover/checklist?fromYearId=%s&toYearId=%s", a, a), "", "same_year", 400},
		"before":        {"GET", fmt.Sprintf("/year-rollover/checklist?fromYearId=%s&toYearId=%s", b, a), "", "not_after", 400},
		"missing param": {"GET", "/year-rollover/checklist?fromYearId=" + a, "", "", 400},
		"unknown step":  {"POST", "/year-rollover/steps/nope/preview", pair, "", 400},
		"overlap":       {"POST", "/year-rollover/years", fmt.Sprintf(`{"fromYearId":%q,"code":"Z","name":"Z","startDate":"2027-01-01","endDate":"2027-12-31"}`, a), "overlaps_year", 400},
		"not after":     {"POST", "/year-rollover/years", fmt.Sprintf(`{"fromYearId":%q,"code":"Z","name":"Z","startDate":"2025-04-01","endDate":"2026-03-31"}`, a), "not_after", 400},
	} {
		rec := s.call(c.method, c.path, boss, c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}

	// preview changes nothing; the run does what the preview said
	prev := decode[rolloverResult](t, s.call("POST", "/year-rollover/steps/terms/preview", roller, pair))
	if !prev.DryRun || prev.CreatedCount != 1 || prev.RunID != "" {
		t.Fatalf("preview = %+v", prev)
	}
	if n := countRows(t, s, "year_rollover_runs"); n != 0 {
		t.Fatalf("preview wrote %d log rows", n)
	}
	rec := s.call("POST", "/year-rollover/steps/terms/run", roller, pair)
	run := decode[rolloverResult](t, rec)
	if rec.Code != 200 || run.DryRun || run.CreatedCount != prev.CreatedCount || run.RunID == "" {
		t.Fatalf("run = %d %s", rec.Code, rec.Body)
	}
	again := decode[rolloverResult](t, s.call("POST", "/year-rollover/steps/terms/run", roller, pair))
	if again.CreatedCount != 0 || again.ExistingCount != 1 {
		t.Errorf("second run = %+v", again)
	}

	// make current: the step needs edit, confirm, and a new year that is ready
	if rec := s.call("POST", "/year-rollover/steps/make_current/run", roller, confirmed); rec.Code != http.StatusForbidden {
		t.Errorf("create-only user ran make_current = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/year-rollover/steps/make_current/preview", roller, pair); rec.Code != 200 {
		t.Errorf("create-only user previewed make_current = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/year-rollover/steps/make_current/run", switcher, pair); rec.Code != 400 || !strings.Contains(rec.Body.String(), `"confirm"`) {
		t.Errorf("unconfirmed = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/year-rollover/steps/make_current/run", switcher, confirmed)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "blocked") || !strings.Contains(rec.Body.String(), "class sections") {
		t.Errorf("blocked run = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/year-rollover/steps/class_sections/run", roller, pair); rec.Code != 200 {
		t.Fatalf("class sections = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/year-rollover/steps/make_current/run", switcher, confirmed)
	if rec.Code != 200 || decode[rolloverResult](t, rec).CreatedCount != 1 {
		t.Fatalf("make current = %d %s", rec.Code, rec.Body)
	}
	var current string
	if err := s.adm.QueryRow(bg, `SELECT id::text FROM academic_years WHERE is_current AND deleted_at IS NULL`).Scan(&current); err != nil || current != b {
		t.Errorf("current year = %s (%v), want %s", current, err, b)
	}

	// the new year created in the wizard is not current, and everything is audited under the actor
	rec = s.call("POST", "/year-rollover/years", roller, newYear)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"isCurrent":false`) {
		t.Errorf("create year = %d %s", rec.Code, rec.Body)
	}
	var audited int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log a JOIN users u ON u.id = a.actor_id WHERE u.username = 'rswitch' AND a.action = 'year_rollover.make_current'`).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("make_current audit rows under rswitch = %d (%v)", audited, err)
	}
	var failed int
	_ = s.adm.QueryRow(bg, `SELECT count(*) FROM year_rollover_runs WHERE status = 'failed'`).Scan(&failed)
	if failed != 1 {
		t.Errorf("failed runs = %d, want the one blocked attempt", failed)
	}
	hist := s.call("GET", "/year-rollover/history?sort=ranAt", viewer, "")
	for _, want := range []string{`"step":"make_current"`, `"status":"failed"`, `"status":"completed"`, `"runByName":"rswitch"`, `"fromYearName":"2026-27"`} {
		if hist.Code != 200 || !strings.Contains(hist.Body.String(), want) {
			t.Errorf("history lacks %s: %d %s", want, hist.Code, hist.Body)
		}
	}
	if rec := s.call("GET", "/year-rollover/history?sort=nope", viewer, ""); rec.Code != 400 {
		t.Errorf("bad sort = %d", rec.Code)
	}

	// the screen is offered only to those who may view it institute-wide
	for name, c := range map[string]struct {
		user string
		want bool
	}{"boss": {"boss", true}, "clerk": {"clerk", false}, "pria": {"pria", false}, "rview": {"rview", true}, "rnarrow": {"rnarrow", false}} {
		got, _ := s.access(t, c.user)
		has := false
		for _, n := range got.Workspace.Navigation {
			if n.ScreenID == "year_rollover" {
				has = true
			}
		}
		if has != c.want {
			t.Errorf("%s sees year_rollover = %v, want %v", name, has, c.want)
		}
	}
}
