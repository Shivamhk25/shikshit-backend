package apiv1_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

const (
	elView   = "stu.student_electives.view"
	elCreate = "stu.student_electives.create"
	elEdit   = "stu.student_electives.edit"
	elDelete = "stu.student_electives.delete"
	chView   = "stu.student_elective_approvals.view"
	chCreate = "stu.student_elective_approvals.create"
	chEdit   = "stu.student_elective_approvals.edit"
)

type electiveOut struct {
	ID      string `json:"id"`
	Student struct {
		ID               string `json:"id"`
		RegistrationCode string `json:"registrationCode"`
	} `json:"student"`
	ClassName   *string `json:"className"`
	SectionName *string `json:"sectionName"`
	Group       struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"group"`
	Subjects []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"subjects"`
	Offered      []struct{ Name string } `json:"offered"`
	Status       string                  `json:"status"`
	WithinLimits bool                    `json:"withinLimits"`
}

type electivePageOut struct {
	Items []electiveOut `json:"items"`
	Page  struct{ Total int }
}

type electiveChangeOut struct {
	ID                string                `json:"id"`
	Status            string                `json:"status"`
	ApprovalRequestID *string               `json:"approvalRequestId"`
	FromSubject       struct{ Name string } `json:"fromSubject"`
	ToSubject         struct{ Name string } `json:"toSubject"`
	Decision          *struct {
		Outcome   string                 `json:"outcome"`
		DecidedBy *struct{ Name string } `json:"decidedBy"`
		Note      *string                `json:"note"`
	} `json:"decision"`
}

type electiveChangePageOut struct {
	Items []electiveChangeOut `json:"items"`
	Page  struct{ Total int }
}

// electiveWorld is institute alpha with Class 9 sections A and B, the groups "Second language" (Hindi, French; exactly
// one) and "Skill" (Music, Dance, Drama; Art for section B only; two, advisory), and one student in each section.
type electiveWorld struct {
	year, class, secA, secB, csmA, csmB string
	lang, skill                         string
	hindi, french, music, dance, art    string
	drama                               string
	sA, sB                              string
}

func (s usersStack) electiveWorld(t *testing.T) electiveWorld {
	t.Helper()
	alpha := s.tenant["alpha"]
	var w electiveWorld
	id := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := s.adm.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%v: %s", err, sql)
		}
		return v
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.adm.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%v: %s", err, sql)
		}
	}
	w.year = id(`INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', DATE '2026-04-01', DATE '2027-03-31', true) RETURNING id::text`, alpha)
	w.class = id(`INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'IX', 'Class 9') RETURNING id::text`, alpha)
	w.secA = id(`INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'A') RETURNING id::text`, alpha)
	w.secB = id(`INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'B', 'B') RETURNING id::text`, alpha)
	csm := func(sec string) string {
		return id(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`, alpha, w.year, w.class, sec)
	}
	w.csmA, w.csmB = csm(w.secA), csm(w.secB)
	w.lang = id(`INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects, enforce_limits) VALUES ($1, 'LANG', 'Second language', 1, 1, true) RETURNING id::text`, alpha)
	w.skill = id(`INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects, enforce_limits) VALUES ($1, 'SKILL', 'Skill', 2, 2, false) RETURNING id::text`, alpha)
	subject := func(code, name string) string {
		return id(`INSERT INTO subjects (tenant_id, code, name, category, area, scholastic, credits) VALUES ($1, $2::text, $3, 'elective', 'other', true, 1) RETURNING id::text`, alpha, code, name)
	}
	w.hindi, w.french, w.music, w.dance, w.art, w.drama = subject("HIN", "Hindi"), subject("FRE", "French"), subject("MUS", "Music"), subject("DAN", "Dance"), subject("ART", "Art"), subject("DRA", "Drama")
	offer := func(sub string, sec *string, group string) {
		exec(`INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, is_elective, elective_group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, true, $6::uuid)`,
			alpha, w.year, w.class, sub, sec, group)
	}
	offer(w.hindi, nil, w.lang)
	offer(w.french, nil, w.lang)
	offer(w.music, nil, w.skill)
	offer(w.dance, nil, w.skill)
	offer(w.drama, nil, w.skill)
	offer(w.art, &w.secB, w.skill)
	student := func(code, sec string) string {
		sid := s.student(t, "alpha", code)
		exec(`INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`, alpha, sid, w.year, w.class, sec, code)
		return sid
	}
	w.sA, w.sB = student("S-A", w.secA), student("S-B", w.secB)
	return w
}

// elUser adds an institute user of alpha who holds exactly the given grants (code -> scope) and is assigned to the given
// class-section mappings; it returns their access token.
func (s usersStack) elUser(t *testing.T, name string, grants map[string]string, assigned ...string) string {
	t.Helper()
	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	alpha := s.tenant["alpha"]
	var uid, rid string
	if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, alpha, name, hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	s.ids[name] = uid
	if err := s.adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, alpha, "r_"+name).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	for code, scope := range grants {
		if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, alpha, rid, code, scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, alpha, uid, rid); err != nil {
		t.Fatal(err)
	}
	for _, ref := range assigned {
		if _, err := s.adm.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', $3)`, alpha, uid, ref); err != nil {
			t.Fatal(err)
		}
	}
	return s.signIn(t, alphaHost, name, password)
}

func (w electiveWorld) createBody(student string, subjects ...string) string {
	return `{"studentId":"` + student + `","yearId":"` + w.year + `","groupId":"` + w.lang + `","subjectIds":["` + strings.Join(subjects, `","`) + `"]}`
}

func (s usersStack) newElective(t *testing.T, tok string, w electiveWorld, student, group string, subjects ...string) electiveOut {
	t.Helper()
	body := `{"studentId":"` + student + `","yearId":"` + w.year + `","groupId":"` + group + `","subjectIds":["` + strings.Join(subjects, `","`) + `"]}`
	rec := s.do(http.MethodPost, alphaHost, "/student-electives", tok, body)
	want(t, rec, http.StatusCreated, "")
	return decode[electiveOut](t, rec)
}

func TestAnElectiveChoiceIsRecordedChangedAndRemovedThroughTheAPI(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", elDelete: "tenant"})

	c := s.newElective(t, reg, w, w.sA, w.lang, w.hindi)
	if c.Status != "chosen" || len(c.Subjects) != 1 || c.Subjects[0].Name != "Hindi" || c.Group.Name != "Second language" || !c.WithinLimits ||
		c.ClassName == nil || *c.ClassName != "Class 9" || c.SectionName == nil || *c.SectionName != "A" || c.Student.RegistrationCode != "S-A" {
		t.Fatalf("created = %+v", c)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-electives", reg, w.createBody(w.sA, w.french)), http.StatusConflict, "choice_exists")
	want(t, s.do(http.MethodPost, alphaHost, "/student-electives", reg, w.createBody(w.sB, w.hindi, w.french)), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/student-electives", reg, w.createBody(w.sB, w.music)), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/student-electives", reg, ""), http.StatusBadRequest, "validation_failed")
	rec := s.do(http.MethodPost, alphaHost, "/student-electives", reg, w.createBody(w.sB, w.music))
	if !strings.Contains(rec.Body.String(), `"field":"subjectIds"`) || !strings.Contains(rec.Body.String(), `"code":"not_in_group"`) {
		t.Fatalf("field error = %s", rec.Body)
	}

	rec = s.do(http.MethodGet, alphaHost, "/student-electives/"+c.ID, reg, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[electiveOut](t, rec); len(got.Offered) != 2 || got.Offered[0].Name != "French" {
		t.Fatalf("offered = %+v", got.Offered)
	}
	rec = s.do(http.MethodPut, alphaHost, "/student-electives/"+c.ID, reg, `{"subjectIds":["`+w.french+`"],"status":"confirmed"}`)
	want(t, rec, http.StatusOK, "")
	if got := decode[electiveOut](t, rec); got.Status != "confirmed" || got.Subjects[0].Name != "French" {
		t.Fatalf("replaced = %+v", got)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/student-electives/"+c.ID, reg, `{"subjectIds":[]}`), http.StatusBadRequest, "validation_failed")
	rec = s.do(http.MethodGet, alphaHost, "/student-electives/"+c.ID+"/history", reg, "")
	want(t, rec, http.StatusOK, "")
	if body := rec.Body.String(); !strings.Contains(body, `"action":"replace"`) || !strings.Contains(body, `"action":"create"`) || !strings.Contains(body, `"source":"staff"`) {
		t.Fatalf("history = %s", body)
	}
	rec = s.do(http.MethodGet, alphaHost, "/student-electives?groupId="+w.lang+"&status=confirmed&q=S-A", reg, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[electivePageOut](t, rec); got.Page.Total != 1 || got.Items[0].ID != c.ID {
		t.Fatalf("list = %+v", got)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives?status=nope", reg, ""), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives?sort=bogus", reg, ""), http.StatusBadRequest, "validation_failed")

	want(t, s.do(http.MethodDelete, alphaHost, "/student-electives/"+c.ID, reg, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+c.ID, reg, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, alphaHost, "/student-electives/"+c.ID, reg, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/not-a-uuid", reg, ""), http.StatusBadRequest, "")
}

func TestTheRosterAndTheBulkSaveWorkOverHTTP(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", elDelete: "tenant"})
	editOnly := s.elUser(t, "editor", map[string]string{elView: "tenant", elEdit: "tenant"})
	q := "?yearId=" + w.year + "&classId=" + w.class + "&sectionId=" + w.secA + "&groupId=" + w.lang

	rec := s.do(http.MethodGet, alphaHost, "/student-electives/roster"+q, reg, "")
	want(t, rec, http.StatusOK, "")
	if body := rec.Body.String(); !strings.Contains(body, `"choiceId":null`) || !strings.Contains(body, `"S-A"`) || strings.Contains(body, `"S-B"`) {
		t.Fatalf("roster = %s", body)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/roster", reg, ""), http.StatusBadRequest, "")

	body := `{"yearId":"` + w.year + `","classId":"` + w.class + `","sectionId":"` + w.secA + `","groupId":"` + w.lang + `","rows":[{"studentId":"` + w.sA + `","subjectIds":["` + w.french + `"]}]}`
	rec = s.do(http.MethodPut, alphaHost, "/student-electives/roster", reg, body)
	want(t, rec, http.StatusOK, "")
	if got := rec.Body.String(); !strings.Contains(got, `"created":1`) || !strings.Contains(got, `"result":"created"`) {
		t.Fatalf("bulk result = %s", got)
	}
	// a bad row refuses the whole save, with the error keyed by student
	bad := strings.Replace(body, `"rows":[`, `"rows":[{"studentId":"`+w.sB+`","subjectIds":["`+w.hindi+`"]},`, 1)
	rec = s.do(http.MethodPut, alphaHost, "/student-electives/roster", reg, bad)
	want(t, rec, http.StatusBadRequest, "validation_failed")
	if !strings.Contains(rec.Body.String(), `"field":"rows[`+w.sB+`].studentId"`) || !strings.Contains(rec.Body.String(), "not_in_section") {
		t.Fatalf("bulk errors = %s", rec.Body)
	}
	// edit without create may change an existing choice but not add one
	want(t, s.do(http.MethodPut, alphaHost, "/student-electives/roster", editOnly, strings.Replace(body, w.french, w.hindi, 1)), http.StatusOK, "")
	s2 := s.student(t, "alpha", "S-C")
	if _, err := s.adm.Exec(bg, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid)`, s.tenant["alpha"], s2, w.year, w.class, w.secA); err != nil {
		t.Fatal(err)
	}
	add := strings.Replace(body, `"rows":[`, `"rows":[{"studentId":"`+s2+`","subjectIds":["`+w.hindi+`"]},`, 1)
	rec = s.do(http.MethodPut, alphaHost, "/student-electives/roster", editOnly, add)
	want(t, rec, http.StatusBadRequest, "validation_failed")
	if !strings.Contains(rec.Body.String(), "not_allowed") {
		t.Fatalf("edit-only bulk = %s", rec.Body)
	}
}

func TestPermissionsGateEveryElectiveOperation(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", elDelete: "tenant", chView: "tenant", chCreate: "tenant"})
	viewer := s.elUser(t, "viewer", map[string]string{elView: "tenant", chView: "tenant"})
	nobody := s.elUser(t, "nobody", map[string]string{"stu.students.view": "tenant"})
	c := s.newElective(t, reg, w, w.sA, w.lang, w.hindi)
	q := "?yearId=" + w.year + "&classId=" + w.class + "&sectionId=" + w.secA + "&groupId=" + w.lang
	rosterBody := `{"yearId":"` + w.year + `","classId":"` + w.class + `","sectionId":"` + w.secA + `","groupId":"` + w.lang + `","rows":[{"studentId":"` + w.sA + `","subjectIds":["` + w.french + `"]}]}`
	changeBody := `{"choiceId":"` + c.ID + `","fromSubjectId":"` + w.hindi + `","toSubjectId":"` + w.french + `","reason":"prefers French"}`

	// someone who may only view reads, and nothing else
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives", viewer, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+c.ID, viewer, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+c.ID+"/history", viewer, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/roster"+q, viewer, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-elective-changes", viewer, ""), http.StatusOK, "")
	for name, rec := range map[string]func() int{
		"create": func() int {
			return s.do(http.MethodPost, alphaHost, "/student-electives", viewer, w.createBody(w.sB, w.hindi)).Code
		},
		"replace": func() int {
			return s.do(http.MethodPut, alphaHost, "/student-electives/"+c.ID, viewer, `{"subjectIds":["`+w.french+`"]}`).Code
		},
		"delete": func() int { return s.do(http.MethodDelete, alphaHost, "/student-electives/"+c.ID, viewer, "").Code },
		"bulk": func() int {
			return s.do(http.MethodPut, alphaHost, "/student-electives/roster", viewer, rosterBody).Code
		},
		"file change": func() int {
			return s.do(http.MethodPost, alphaHost, "/student-elective-changes", viewer, changeBody).Code
		},
	} {
		if got := rec(); got != http.StatusForbidden {
			t.Errorf("view-only %s = %d, want 403", name, got)
		}
	}
	// a user with no elective permission at all gets nothing
	for _, p := range []struct{ method, path string }{{http.MethodGet, "/student-electives"}, {http.MethodGet, "/student-electives/" + c.ID}, {http.MethodGet, "/student-elective-changes"},
		{http.MethodGet, "/student-electives/roster" + q}} {
		want(t, s.do(p.method, alphaHost, p.path, nobody, ""), http.StatusForbidden, "permission_denied")
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives", "", ""), http.StatusUnauthorized, "")
	// delete without the delete right, create without the create right
	noDelete := s.elUser(t, "nodelete", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant"})
	want(t, s.do(http.MethodDelete, alphaHost, "/student-electives/"+c.ID, noDelete, ""), http.StatusForbidden, "permission_denied")
	// a scope this resource cannot apply (a self scope) is refused rather than served unfiltered
	selfScoped := s.elUser(t, "selfscoped", map[string]string{elView: "self"})
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives", selfScoped, ""), http.StatusForbidden, "permission_denied")
	// the other institute does not see this one's choices
	beta := s.signIn(t, betaHost, "betaadmin", password)
	if got := decode[electivePageOut](t, s.do(http.MethodGet, betaHost, "/student-electives", beta, "")); got.Page.Total != 0 {
		t.Errorf("beta sees %d", got.Page.Total)
	}
	want(t, s.do(http.MethodGet, betaHost, "/student-electives/"+c.ID, beta, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPut, betaHost, "/student-electives/"+c.ID, beta, `{"subjectIds":["`+w.french+`"]}`), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, betaHost, "/student-electives/"+c.ID, beta, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, betaHost, "/student-electives", beta, w.createBody(w.sB, w.hindi)), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, betaHost, "/student-electives/roster"+q, beta, ""), http.StatusNotFound, "not_found")
}

func TestAClassTeacherWorksOnTheirSectionsOnly(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", elDelete: "tenant", chView: "tenant", chCreate: "tenant"})
	teacher := s.elUser(t, "teacher", map[string]string{elView: "class_section", elCreate: "class_section", elEdit: "class_section", chView: "class_section", chCreate: "class_section"}, w.csmA)
	inA := s.newElective(t, reg, w, w.sA, w.lang, w.hindi)
	inB := s.newElective(t, reg, w, w.sB, w.lang, w.hindi)
	rosterQ := func(sec string) string {
		return "?yearId=" + w.year + "&classId=" + w.class + "&sectionId=" + sec + "&groupId=" + w.lang
	}
	rosterBody := func(sec, student, subject string) string {
		return `{"yearId":"` + w.year + `","classId":"` + w.class + `","sectionId":"` + sec + `","groupId":"` + w.lang + `","rows":[{"studentId":"` + student + `","subjectIds":["` + subject + `"]}]}`
	}

	// reads
	if got := decode[electivePageOut](t, s.do(http.MethodGet, alphaHost, "/student-electives", teacher, "")); got.Page.Total != 1 || got.Items[0].ID != inA.ID {
		t.Fatalf("teacher's list = %+v", got)
	}
	if got := decode[electivePageOut](t, s.do(http.MethodGet, alphaHost, "/student-electives", reg, "")); got.Page.Total != 2 {
		t.Fatalf("registrar's list = %d", got.Page.Total)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inA.ID, teacher, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inB.ID, teacher, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inB.ID+"/history", teacher, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/roster"+rosterQ(w.secA), teacher, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-electives/roster"+rosterQ(w.secB), teacher, ""), http.StatusNotFound, "not_found")

	// writes: own section works, another section looks like it does not exist
	rec := s.do(http.MethodPut, alphaHost, "/student-electives/"+inA.ID, teacher, `{"subjectIds":["`+w.french+`"]}`)
	want(t, rec, http.StatusOK, "")
	want(t, s.do(http.MethodPut, alphaHost, "/student-electives/"+inB.ID, teacher, `{"subjectIds":["`+w.french+`"]}`), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, alphaHost, "/student-electives/"+inB.ID, teacher, ""), http.StatusForbidden, "permission_denied") // no delete right at all
	rec = s.do(http.MethodPost, alphaHost, "/student-electives", teacher, w.createBody(w.sB, w.hindi))
	want(t, rec, http.StatusBadRequest, "validation_failed")
	if !strings.Contains(rec.Body.String(), `"field":"studentId"`) || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("create for section B = %s", rec.Body)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/student-electives/roster", teacher, rosterBody(w.secA, w.sA, w.hindi)), http.StatusOK, "")
	want(t, s.do(http.MethodPut, alphaHost, "/student-electives/roster", teacher, rosterBody(w.secB, w.sB, w.french)), http.StatusNotFound, "not_found")
	if got := decode[electiveOut](t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inB.ID, reg, "")); got.Subjects[0].Name != "Hindi" {
		t.Errorf("section B's choice changed to %v", got.Subjects)
	}

	// change requests
	body := func(choice, from, to string) string {
		return `{"choiceId":"` + choice + `","fromSubjectId":"` + from + `","toSubjectId":"` + to + `","reason":"wants a change"}`
	}
	rec = s.do(http.MethodPost, alphaHost, "/student-elective-changes", teacher, body(inA.ID, w.hindi, w.french))
	want(t, rec, http.StatusCreated, "")
	mine := decode[electiveChangeOut](t, rec)
	want(t, s.do(http.MethodPost, alphaHost, "/student-elective-changes", teacher, body(inB.ID, w.hindi, w.french)), http.StatusBadRequest, "validation_failed")
	other := decode[electiveChangeOut](t, s.do(http.MethodPost, alphaHost, "/student-elective-changes", reg, body(inB.ID, w.hindi, w.french)))
	if got := decode[electiveChangePageOut](t, s.do(http.MethodGet, alphaHost, "/student-elective-changes", teacher, "")); got.Page.Total != 1 || got.Items[0].ID != mine.ID {
		t.Fatalf("teacher's requests = %+v", got)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-elective-changes/"+mine.ID, teacher, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-elective-changes/"+other.ID, teacher, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/student-elective-changes/"+other.ID+"/cancel", teacher, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/student-elective-changes/"+mine.ID+"/cancel", teacher, `{"comment":"sorry"}`), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/student-elective-changes/"+mine.ID+"/cancel", teacher, ""), http.StatusConflict, "not_cancellable")
}

func TestAChangeRequestIsApprovedOrRejectedInTheApprovalsInboxByTheRightApprover(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", chView: "tenant", chCreate: "tenant"})
	approver := s.elUser(t, "approver", map[string]string{chEdit: "tenant", chView: "tenant"})
	sectionA := s.elUser(t, "secta", map[string]string{chEdit: "class_section", chView: "class_section"}, w.csmA)
	inA := s.newElective(t, reg, w, w.sA, w.skill, w.music, w.dance)
	inB := s.newElective(t, reg, w, w.sB, w.skill, w.music, w.dance)
	file := func(choice, from, to string) electiveChangeOut {
		t.Helper()
		rec := s.do(http.MethodPost, alphaHost, "/student-elective-changes", reg, `{"choiceId":"`+choice+`","fromSubjectId":"`+from+`","toSubjectId":"`+to+`","wantedFrom":"2026-12-01","reason":"likes it"}`)
		want(t, rec, http.StatusCreated, "")
		return decode[electiveChangeOut](t, rec)
	}
	cA, cB := file(inA.ID, w.music, w.drama), file(inB.ID, w.music, w.art)
	if cA.ApprovalRequestID == nil || cA.Status != "requested" || cA.FromSubject.Name != "Music" || cA.ToSubject.Name != "Drama" {
		t.Fatalf("filed = %+v", cA)
	}
	if got := s.getApproval(t, alphaHost, reg, *cA.ApprovalRequestID); got.TypeKey != "student_elective_change" || got.SubjectID != cA.ID || got.CanDecide {
		t.Fatalf("requester's view = %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*cA.ApprovalRequestID+"/approve", reg, ""), http.StatusForbidden, "permission_denied")

	// the section A approver sees and decides A's request only
	if n := len(s.inbox(t, alphaHost, sectionA).Items); n != 1 {
		t.Fatalf("section approver's inbox = %d", n)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/"+*cB.ApprovalRequestID, sectionA, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*cB.ApprovalRequestID+"/approve", sectionA, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*cA.ApprovalRequestID+"/approve", sectionA, `{"comment":"fine"}`), http.StatusOK, "")
	got := decode[electiveOut](t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inA.ID, reg, ""))
	names := []string{}
	for _, sj := range got.Subjects {
		names = append(names, sj.Name)
	}
	if strings.Join(names, ",") != "Dance,Drama" {
		t.Fatalf("A's choice after approval = %v", names)
	}
	done := decode[electiveChangeOut](t, s.do(http.MethodGet, alphaHost, "/student-elective-changes/"+cA.ID, reg, ""))
	if done.Status != "approved" || done.Decision == nil || done.Decision.Outcome != "approved" || done.Decision.DecidedBy == nil || done.Decision.DecidedBy.Name != "secta" || done.Decision.Note == nil || *done.Decision.Note != "fine" {
		t.Fatalf("decision = %+v", done)
	}
	if h := s.do(http.MethodGet, alphaHost, "/student-electives/"+inA.ID+"/history", reg, ""); !strings.Contains(h.Body.String(), `"source":"change_request"`) || !strings.Contains(h.Body.String(), `"changeId":"`+cA.ID+`"`) {
		t.Fatalf("history = %s", h.Body)
	}

	// the whole-school approver rejects B's, with a reason; the choice stays
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*cB.ApprovalRequestID+"/reject", approver, ""), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*cB.ApprovalRequestID+"/reject", approver, `{"comment":"too late in the year"}`), http.StatusOK, "")
	rej := decode[electiveChangeOut](t, s.do(http.MethodGet, alphaHost, "/student-elective-changes/"+cB.ID, reg, ""))
	if rej.Status != "rejected" || rej.Decision == nil || rej.Decision.Note == nil || *rej.Decision.Note != "too late in the year" {
		t.Fatalf("rejected = %+v", rej)
	}
	if got := decode[electiveOut](t, s.do(http.MethodGet, alphaHost, "/student-electives/"+inB.ID, reg, "")); len(got.Subjects) != 2 || got.Subjects[0].Name != "Dance" || got.Subjects[1].Name != "Music" {
		t.Errorf("B's choice = %+v", got.Subjects)
	}
}

func TestAnApprovalThatNoLongerFitsAnswers409AndChangesNothing(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	reg := s.elUser(t, "registrar", map[string]string{elView: "tenant", elCreate: "tenant", elEdit: "tenant", chView: "tenant", chCreate: "tenant"})
	approver := s.elUser(t, "approver", map[string]string{chEdit: "tenant", chView: "tenant"})
	c := s.newElective(t, reg, w, w.sA, w.skill, w.music, w.dance)
	rec := s.do(http.MethodPost, alphaHost, "/student-elective-changes", reg, `{"choiceId":"`+c.ID+`","fromSubjectId":"`+w.music+`","toSubjectId":"`+w.drama+`","reason":"x"}`)
	want(t, rec, http.StatusCreated, "")
	ch := decode[electiveChangeOut](t, rec)
	// Drama stops being offered before the approver decides
	if _, err := s.adm.Exec(bg, `UPDATE subject_class_mappings SET deleted_at = now() WHERE subject_id = $1::uuid`, w.drama); err != nil {
		t.Fatal(err)
	}
	rec = s.do(http.MethodPost, alphaHost, "/approvals/"+*ch.ApprovalRequestID+"/approve", approver, "")
	want(t, rec, http.StatusConflict, "")
	if !strings.Contains(rec.Body.String(), "Reject the request") {
		t.Fatalf("answer = %s", rec.Body)
	}
	if got := decode[electiveChangeOut](t, s.do(http.MethodGet, alphaHost, "/student-elective-changes/"+ch.ID, reg, "")); got.Status != "requested" {
		t.Errorf("status = %s", got.Status)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*ch.ApprovalRequestID+"/reject", approver, `{"comment":"no longer offered"}`), http.StatusOK, "")
}

func TestTheElectiveScreensAreOfferedToTheRightUsersAndRoleTemplatesGrantTheRightPermissions(t *testing.T) {
	s := newUsersStack(t)
	screens := func(tok string) string {
		rec := s.do(http.MethodGet, alphaHost, "/me/access", tok, "")
		want(t, rec, http.StatusOK, "")
		out := ""
		for _, id := range []string{"student_electives", "elective_changes"} {
			if strings.Contains(rec.Body.String(), `"screenId":"`+id+`"`) {
				out += id + " "
			}
		}
		return strings.TrimSpace(out)
	}
	admin := s.signIn(t, alphaHost, "admin", password)
	whole := s.elUser(t, "whole", map[string]string{elView: "tenant", chView: "tenant"})
	teacher := s.elUser(t, "teacher", map[string]string{elView: "class_section", chView: "class_section"}, "00000000-0000-0000-0000-000000000001")
	selfOnly := s.elUser(t, "selfonly", map[string]string{elView: "self", chView: "self"})
	onlyChanges := s.elUser(t, "onlychanges", map[string]string{chView: "tenant"})
	for name, c := range map[string]struct{ tok, want string }{
		"admin": {admin, "student_electives elective_changes"}, "whole school": {whole, "student_electives elective_changes"},
		"class teacher": {teacher, "student_electives elective_changes"}, "self scope": {selfOnly, ""}, "only changes": {onlyChanges, "elective_changes"},
	} {
		if got := screens(c.tok); got != c.want {
			t.Errorf("%s sees %q, want %q", name, got, c.want)
		}
	}

	// the role templates: a teacher records choices and files requests for their sections; deciding stays with the principal
	grants := func(role string) []string {
		rows, err := s.adm.Query(bg, `SELECT permission_code FROM role_template_permissions WHERE template_code = $1`, role)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	holds := func(role, perm string) bool {
		for _, g := range grants(role) {
			if authz.MatchPermission(g, perm) {
				return true
			}
		}
		return false
	}
	for _, p := range []string{elView, elCreate, elEdit, chView, chCreate} {
		if !holds("teacher", p) {
			t.Errorf("teacher must hold %s", p)
		}
	}
	for _, p := range []string{elDelete, chEdit, "stu.student_elective_approvals.delete"} {
		if holds("teacher", p) {
			t.Errorf("teacher must not hold %s", p)
		}
	}
	for _, role := range []string{"principal", "registrar", "admin"} {
		for _, p := range []string{elView, elCreate, elEdit, elDelete, chView, chCreate, chEdit} {
			if !holds(role, p) {
				t.Errorf("%s must hold %s", role, p)
			}
		}
	}
	for _, role := range []string{"accountant", "librarian", "counselor"} {
		if holds(role, elCreate) || holds(role, chEdit) {
			t.Errorf("%s must not change electives", role)
		}
	}
	// the teacher's elective grants are class-section grants, never the whole school
	var scope string
	if err := s.adm.QueryRow(bg, `SELECT scope FROM role_template_permissions WHERE template_code = 'teacher' AND permission_code = $1`, elCreate).Scan(&scope); err != nil || scope != "class_section" {
		t.Errorf("teacher's create scope = %q (%v)", scope, err)
	}
}

func TestASubjectOfAClassCanBeOfferedInAnElectiveGroup(t *testing.T) {
	s := newUsersStack(t)
	w := s.electiveWorld(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	body := func(subject, group string, elective bool) string {
		g := ""
		if group != "" {
			g = `,"electiveGroupId":"` + group + `"`
		}
		return `{"yearId":"` + w.year + `","classId":"` + w.class + `","subjectId":"` + subject + `","isElective":` + map[bool]string{true: "true", false: "false"}[elective] + g + `}`
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'aca')`, s.tenant["alpha"]); err != nil {
		t.Fatal(err)
	}
	var sub, otherGroup string
	if err := s.adm.QueryRow(bg, `INSERT INTO subjects (tenant_id, code, name, category, area, credits) VALUES ($1, 'SAN', 'Sanskrit', 'elective', 'languages', 1) RETURNING id::text`, s.tenant["alpha"]).Scan(&sub); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects) VALUES ($1, 'LANG', 'Other', 1, 1) RETURNING id::text`, s.tenant["beta"]).Scan(&otherGroup); err != nil {
		t.Fatal(err)
	}

	rec := s.do(http.MethodPost, alphaHost, "/subject-class-mappings", admin, body(sub, w.lang, true))
	want(t, rec, http.StatusCreated, "")
	if got := rec.Body.String(); !strings.Contains(got, `"electiveGroupId":"`+w.lang+`"`) || !strings.Contains(got, `"electiveGroupName":"Second language"`) {
		t.Fatalf("mapping = %s", got)
	}
	// without a group the answer says so; a group needs an elective subject; an unknown group, or another institute's, is refused
	rec = s.do(http.MethodPost, alphaHost, "/subject-class-mappings", admin, body(w.art, "", true))
	want(t, rec, http.StatusCreated, "")
	if !strings.Contains(rec.Body.String(), `"electiveGroupId":null`) {
		t.Fatalf("mapping without a group = %s", rec.Body)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/subject-class-mappings", admin, body(w.music, w.lang, false)), http.StatusBadRequest, "validation_failed")
	rec = s.do(http.MethodPost, alphaHost, "/subject-class-mappings", admin, body(w.dance, "00000000-0000-0000-0000-000000000001", true))
	want(t, rec, http.StatusBadRequest, "validation_failed")
	rec = s.do(http.MethodPost, alphaHost, "/subject-class-mappings", admin, body(w.hindi, otherGroup, true))
	want(t, rec, http.StatusBadRequest, "validation_failed")
	if !strings.Contains(rec.Body.String(), `"field":"electiveGroupId"`) {
		t.Fatalf("other institute's group = %s", rec.Body)
	}
	// a group still holding subjects cannot be deleted
	want(t, s.do(http.MethodDelete, alphaHost, "/elective-groups/"+w.lang, admin, ""), http.StatusConflict, "in_use")
}
