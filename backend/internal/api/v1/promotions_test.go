package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// TestPromotionsFollowThePermissionMatrix: the Registrar and the Admin prepare, commit, undo and discard; a user with
// only the promotions permissions (no right to create or edit students) prepares and discards but is refused on
// commit and undo; the Principal and the Accountant read only; a Teacher (class-section scope), a user with other
// permissions and a user with no role are refused everywhere; an anonymous call is 401. Committing twice is one set of
// enrolments, an undo that something changed is refused with reasons, every read is audited under the reader, and the
// Promotions screen is offered only to those who may view it institute-wide.
func TestPromotionsFollowThePermissionMatrix(t *testing.T) {
	s := newAccessStack(t)
	hash, _ := identity.HashPassword(password)
	var rid, uid string
	if err := s.adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'promoter', 'promoter') RETURNING id::text`, s.tenantID).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"stu.student_promotions.view", "stu.student_promotions.create", "stu.student_promotions.edit", "stu.student_promotions.delete"} {
		if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code) VALUES ($1, $2, $3)`, s.tenantID, rid, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, 'prom', 'prom', 'active', $2) RETURNING id::text`, s.tenantID, hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2, $3)`, s.tenantID, uid, rid); err != nil {
		t.Fatal(err)
	}

	boss, registrar := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken
	principal, accountant := s.signIn(t, "pria").AccessToken, s.signIn(t, "acct").AccessToken
	teacher, nobody, gate, prom := s.signIn(t, "tina").AccessToken, s.signIn(t, "nobody").AccessToken, s.signIn(t, "gate").AccessToken, s.signIn(t, "prom").AccessToken
	mk := func(token, path, body string) string {
		rec := s.call("POST", path, token, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	c5 := mk(boss, "/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	c6 := mk(boss, "/classes", `{"code":"VI","name":"Class 6","displayOrder":6}`)
	y1 := mk(boss, "/academic-years", `{"code":"Y1","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31"}`)
	y2 := mk(boss, "/academic-years", `{"code":"Y2","name":"2027-28","startDate":"2027-04-01","endDate":"2028-03-31"}`)
	var secA, secC string
	for code, dst := range map[string]*string{"A": &secA, "C": &secC} {
		if err := s.adm.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, $2, 'Section ' || $2) RETURNING id::text`, s.tenantID, code).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range [][3]string{{y1, c5, secA}, {y2, c6, secA}} {
		if _, err := s.adm.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, s.tenantID, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	var kids []string
	for i, name := range []string{"Asha", "Bina", "Chitra"} {
		kid := mk(boss, "/students", fmt.Sprintf(`{"registrationCode":"R-%d","firstName":%q,"gender":"female","dateOfBirth":"2015-06-01"}`, i, name))
		mk(boss, "/student-enrollments", fmt.Sprintf(`{"studentId":%q,"yearId":%q,"classId":%q,"sectionId":%q}`, kid, y1, c5, secA))
		kids = append(kids, kid)
	}
	newYear := func() int {
		var n int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM student_enrollments WHERE year_id::text = $1 AND deleted_at IS NULL`, y2).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	proposal := fmt.Sprintf("/promotions/proposal?fromYearId=%s&toYearId=%s&classId=%s&sectionId=%s", y1, y2, c5, secA)
	rows := func(decisions ...string) string {
		parts := make([]string, len(kids))
		for i, k := range kids {
			parts[i] = fmt.Sprintf(`{"studentId":%q,"decision":%q}`, k, decisions[i])
		}
		return strings.Join(parts, ",")
	}
	draft := fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q,"fromClassId":%q,"fromSectionId":%q,"rows":[%s]}`, y1, y2, c5, secA, rows("promote", "promote", "leave_out"))

	// who may read
	for name, tok := range map[string]string{"registrar": registrar, "admin": boss, "principal": principal, "accountant": accountant, "promoter": prom} {
		for _, path := range []string{"/promotions", proposal} {
			if rec := s.call("GET", path, tok, ""); rec.Code != 200 {
				t.Errorf("%s GET %s = %d %s", name, path, rec.Code, rec.Body)
			}
		}
	}
	// who is refused everything: a class-section scope, no role, other permissions only
	for name, tok := range map[string]string{"teacher": teacher, "nobody": nobody, "gatekeeper": gate} {
		for _, c := range [][3]string{{"GET", "/promotions", ""}, {"GET", proposal, ""}, {"POST", "/promotions", draft}} {
			if rec := s.call(c[0], c[1], tok, c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d, want 403", name, c[0], c[1], rec.Code)
			}
		}
	}
	// the principal and accountant read only
	for name, tok := range map[string]string{"principal": principal, "accountant": accountant} {
		if rec := s.call("POST", "/promotions", tok, draft); rec.Code != http.StatusForbidden {
			t.Errorf("%s create = %d, want 403", name, rec.Code)
		}
	}
	if rec := s.call("GET", "/promotions", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if n := countRows(t, s, "promotion_batches"); n != 0 {
		t.Fatalf("refused calls created %d batches", n)
	}

	// the proposal: the next class is the default, every student is listed
	rec := s.call("GET", proposal, registrar, "")
	for _, want := range []string{`"nextClassName":"Class 6"`, `"isLastClass":false`, `"existingBatchId":null`, `"decision":"promote"`, `"Asha`} {
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("proposal lacks %s: %d %s", want, rec.Code, rec.Body)
		}
	}

	// prepare: create, a duplicate is refused, validation names the row
	rec = s.call("POST", "/promotions", registrar, draft)
	batch := decode[promotionDetail](t, rec)
	if rec.Code != http.StatusCreated || batch.Batch.Status != "draft" || batch.Batch.StudentCount != 3 || batch.Batch.PromoteCount != 2 || batch.Batch.LeaveOutCount != 1 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id := batch.Batch.ID
	for name, c := range map[string]struct {
		method, path, body string
		code               int
		want               string
	}{
		"duplicate":        {"POST", "/promotions", draft, 400, "duplicate"},
		"empty rows":       {"POST", "/promotions", fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q,"fromClassId":%q,"fromSectionId":%q,"rows":[]}`, y1, y2, c5, secA), 400, ""},
		"same year":        {"POST", "/promotions", fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q,"fromClassId":%q,"fromSectionId":%q,"rows":[{"studentId":%q,"decision":"promote"}]}`, y1, y1, c5, secA, kids[0]), 400, "same_year"},
		"unknown decision": {"PUT", "/promotions/" + id, fmt.Sprintf(`{"rows":[{"studentId":%q,"decision":"skip"}]}`, kids[0]), 400, "decision"},
		"not a uuid":       {"PUT", "/promotions/" + id, `{"rows":[{"studentId":"x","decision":"promote"}]}`, 400, ""},
		"unknown batch":    {"GET", "/promotions/00000000-0000-0000-0000-000000000001", "", 404, ""},
	} {
		if rec := s.call(c.method, c.path, registrar, c.body); rec.Code != c.code || (c.want != "" && !strings.Contains(rec.Body.String(), c.want)) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	for q, want := range map[string]int{"sort=password": 400, "status=weird": 400, "classId=notauuid": 400, "pageSize=1000": 400} {
		if rec := s.call("GET", "/promotions?"+q, boss, ""); rec.Code != want {
			t.Errorf("GET ?%s = %d, want %d", q, rec.Code, want)
		}
	}
	// the promoter may save and preview
	if rec := s.call("PUT", "/promotions/"+id, prom, fmt.Sprintf(`{"rollMode":"keep","rows":[%s]}`, rows("promote", "repeat", "leave_out"))); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"rollMode":"keep"`) {
		t.Errorf("promoter save = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/promotions/"+id, registrar, fmt.Sprintf(`{"rows":[%s]}`, rows("promote", "promote", "leave_out"))); rec.Code != 200 {
		t.Errorf("registrar save = %d %s", rec.Code, rec.Body)
	}

	// preview saves nothing and says what the commit does
	before := countRows(t, s, "student_enrollments")
	rec = s.call("POST", "/promotions/"+id+"/preview", registrar, "")
	pre := decode[promotionOutcome](t, rec)
	if rec.Code != 200 || pre.EnrolledCount != 2 || pre.LeftOutCount != 1 || pre.ProblemCount != 0 || pre.Committed || pre.Batch.Status != "draft" {
		t.Errorf("preview = %d %s", rec.Code, rec.Body)
	}
	if countRows(t, s, "student_enrollments") != before {
		t.Error("a preview created enrolments")
	}

	// commit: refused for those who may not change students, once for real, then again
	for name, tok := range map[string]string{"promoter (no student rights)": prom, "principal": principal, "accountant": accountant, "teacher": teacher} {
		if rec := s.call("POST", "/promotions/"+id+"/commit", tok, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s commit = %d, want 403", name, rec.Code)
		}
	}
	if countRows(t, s, "student_enrollments") != before {
		t.Fatal("refused commits created enrolments")
	}
	rec = s.call("POST", "/promotions/"+id+"/commit", registrar, "")
	com := decode[promotionOutcome](t, rec)
	if rec.Code != 200 || !com.Committed || com.AlreadyCommitted || com.EnrolledCount != 2 || com.Batch.Status != "committed" || !com.Batch.CanUndo || com.Batch.UndoUntil == nil {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/promotions/"+id+"/commit", boss, "")
	again := decode[promotionOutcome](t, rec)
	if rec.Code != 200 || !again.AlreadyCommitted || again.Committed || countRows(t, s, "student_enrollments") != before+2 {
		t.Errorf("second commit = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/student-enrollments?yearId="+y2, boss, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"className":"Class 6"`) || !strings.Contains(rec.Body.String(), `"rollNumber":"1"`) {
		t.Errorf("the new enrolments = %d %s", rec.Code, rec.Body)
	}
	for _, c := range [][3]string{{"PUT", "/promotions/" + id, fmt.Sprintf(`{"rows":[%s]}`, rows("promote", "promote", "leave_out"))}, {"POST", "/promotions/" + id + "/preview", ""}, {"DELETE", "/promotions/" + id, ""}} {
		if rec := s.call(c[0], c[1], registrar, c[2]); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not_draft") {
			t.Errorf("%s %s after commit = %d %s", c[0], c[1], rec.Code, rec.Body)
		}
	}

	// undo: needs the student rights and a reason; refused when an enrolment changed
	for name, tok := range map[string]string{"promoter": prom, "principal": principal} {
		if rec := s.call("POST", "/promotions/"+id+"/undo", tok, `{"reason":"x"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s undo = %d, want 403", name, rec.Code)
		}
	}
	if rec := s.call("POST", "/promotions/"+id+"/undo", registrar, `{"reason":"  "}`); rec.Code != 400 {
		t.Errorf("undo without a reason = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/promotions/"+id+"/undo", registrar, `{"reason":"wrong class"}`)
	und := decode[promotionOutcome](t, rec)
	if rec.Code != 200 || !und.Undone || und.Batch.Status != "undone" || und.Batch.CanUndo {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body)
	}
	var live int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM student_enrollments WHERE year_id::text = $1 AND deleted_at IS NULL`, y2).Scan(&live); err != nil || live != 0 {
		t.Errorf("enrolments of the new year after undo: %d (%v)", live, err)
	}
	if rec := s.call("POST", "/promotions/"+id+"/undo", boss, `{"reason":"again"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"alreadyUndone":true`) {
		t.Errorf("second undo = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/promotions/"+id+"/commit", boss, ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"undone"`) {
		t.Errorf("commit of an undone batch = %d %s", rec.Code, rec.Body)
	}

	// a batch with a problem is refused whole, and a draft is discarded by whoever holds delete
	bad := fmt.Sprintf(`{"fromYearId":%q,"toYearId":%q,"fromClassId":%q,"fromSectionId":%q,"rows":[{"studentId":%q,"decision":"promote"},{"studentId":%q,"decision":"promote","toSectionId":%q}]}`, y1, y2, c5, secA, kids[0], kids[1], secC)
	rec = s.call("POST", "/promotions", registrar, bad)
	b2 := decode[promotionDetail](t, rec)
	if rec.Code != 201 {
		t.Fatalf("create bad = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/promotions/"+b2.Batch.ID+"/preview", registrar, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"problemCount":1`) || !strings.Contains(rec.Body.String(), "section_not_mapped") {
		t.Errorf("preview with a problem = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/promotions/"+b2.Batch.ID+"/commit", registrar, "")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "has_problems") || newYear() != 0 {
		t.Errorf("commit with a problem = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("DELETE", "/promotions/"+b2.Batch.ID, principal, ""); rec.Code != http.StatusForbidden {
		t.Errorf("principal discard = %d", rec.Code)
	}
	if rec := s.call("DELETE", "/promotions/"+b2.Batch.ID, prom, ""); rec.Code != http.StatusNoContent {
		t.Errorf("promoter discard = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/promotions/"+b2.Batch.ID, boss, ""); rec.Code != 404 {
		t.Errorf("discarded batch = %d", rec.Code)
	}

	// the class section can be promoted again; once a new enrolment is edited the undo is refused with the reason
	rec = s.call("POST", "/promotions", registrar, draft)
	b3 := decode[promotionDetail](t, rec)
	if rec.Code != 201 {
		t.Fatalf("create again = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/promotions/"+b3.Batch.ID+"/commit", registrar, ""); rec.Code != 200 {
		t.Fatalf("commit again = %d %s", rec.Code, rec.Body)
	}
	var enrolID string
	if err := s.adm.QueryRow(bg, `SELECT id::text FROM student_enrollments WHERE year_id::text = $1 AND student_id::text = $2 AND deleted_at IS NULL`, y2, kids[0]).Scan(&enrolID); err != nil {
		t.Fatal(err)
	}
	if rec := s.call("PUT", "/student-enrollments/"+enrolID, boss, fmt.Sprintf(`{"studentId":%q,"yearId":%q,"classId":%q,"sectionId":%q,"rollNumber":"77","status":"enrolled"}`, kids[0], y2, c6, secA)); rec.Code != 200 {
		t.Fatalf("edit the new enrolment = %d %s", rec.Code, rec.Body)
	}
	rec = s.call("POST", "/promotions/"+b3.Batch.ID+"/undo", registrar, `{"reason":"wrong class"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "cannot_undo") || !strings.Contains(rec.Body.String(), "enrolment_changed") || !strings.Contains(rec.Body.String(), kids[0]) {
		t.Errorf("undo after an edit = %d %s", rec.Code, rec.Body)
	}
	if newYear() != 2 {
		t.Error("a refused undo removed enrolments")
	}

	// audit: the principal's and accountant's reads are recorded under their own identity; each step is in the trail
	var reads int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('promotions.list', 'promotions.candidates', 'promotions.view')
		AND actor_id IN (SELECT id FROM users WHERE username IN ('pria', 'acct'))`).Scan(&reads); err != nil {
		t.Fatal(err)
	}
	if reads != 4 { // list and proposal for each
		t.Errorf("audited reads by principal/accountant = %d, want 4", reads)
	}
	for action, want := range map[string]int{"promotions.create": 3, "promotions.update": 2, "promotions.preview": 2, "promotions.commit": 2, "promotions.commit_refused": 1,
		"promotions.undo": 1, "promotions.undo_refused": 1, "promotions.discard": 1} {
		var n int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil || n != want {
			t.Errorf("audit rows %s = %d, want %d (%v)", action, n, want, err)
		}
	}

	for user, want := range map[string]bool{"clerk": true, "boss": true, "pria": true, "acct": true, "prom": true, "tina": false, "nobody": false, "gate": false} {
		a, _ := s.access(t, user)
		got := false
		for _, n := range a.Workspace.Navigation {
			got = got || n.ScreenID == "promotions"
		}
		if got != want {
			t.Errorf("%s sees the promotions screen = %v, want %v", user, got, want)
		}
	}
}

type promotionBatchBody struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`
	StudentCount  int     `json:"studentCount"`
	PromoteCount  int     `json:"promoteCount"`
	LeaveOutCount int     `json:"leaveOutCount"`
	CanUndo       bool    `json:"canUndo"`
	UndoUntil     *string `json:"undoUntil"`
}

type promotionDetail struct {
	Batch promotionBatchBody `json:"batch"`
}

type promotionOutcome struct {
	Batch            promotionBatchBody `json:"batch"`
	Committed        bool               `json:"committed"`
	AlreadyCommitted bool               `json:"alreadyCommitted"`
	Undone           bool               `json:"undone"`
	EnrolledCount    int                `json:"enrolledCount"`
	LeftOutCount     int                `json:"leftOutCount"`
	ProblemCount     int                `json:"problemCount"`
}
