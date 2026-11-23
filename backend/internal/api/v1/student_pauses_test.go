package apiv1_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
)

const (
	pauView   = "stu.student_pauses.view"
	pauCreate = "stu.student_pauses.create"
	pauEdit   = "stu.student_pauses.edit"
)

type pauseOut struct {
	ID      string `json:"id"`
	Student struct {
		ID               string `json:"id"`
		RegistrationCode string `json:"registrationCode"`
	} `json:"student"`
	Status  string  `json:"status"`
	Reason  string  `json:"reason"`
	EndedOn *string `json:"endedOn"`
	Days    *int    `json:"days"`
	Figures struct {
		Owed   struct{ Status string } `json:"owed"`
		Missed struct{ Status string } `json:"missed"`
	} `json:"figures"`
}

type pausePageOut struct {
	Items []pauseOut `json:"items"`
	Page  struct{ Total int }
}

func (s usersStack) studentStatus(t *testing.T, id string) string {
	t.Helper()
	var st string
	if err := s.adm.QueryRow(bg, `SELECT status FROM students WHERE id = $1::uuid`, id).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func (s usersStack) newPause(t *testing.T, tok, student string) pauseOut {
	t.Helper()
	rec := s.do(http.MethodPost, alphaHost, "/student-pauses", tok, `{"studentId":"`+student+`","reason":"illness","expectedBack":"2099-01-01"}`)
	want(t, rec, http.StatusCreated, "")
	return decode[pauseOut](t, rec)
}

func TestAPauseIsRecordedChangedAndEndedThroughTheAPI(t *testing.T) {
	s := newUsersStack(t)
	reg := s.trUser(t, "registrar", map[string]string{pauView: "tenant", pauCreate: "tenant", pauEdit: "tenant"})
	st := s.student(t, "alpha", "A-1")

	x := s.newPause(t, reg, st)
	if x.Status != "active" || x.Student.RegistrationCode != "A-1" || x.Figures.Owed.Status != "not_tracked" || x.Figures.Missed.Status != "not_tracked" || x.Days == nil {
		t.Fatalf("paused = %+v", x)
	}
	if got := s.studentStatus(t, st); got != "paused" {
		t.Fatalf("student = %s", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses", reg, `{"studentId":"`+st+`","reason":"illness"}`), http.StatusConflict, "already_paused")
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses", reg, `{"studentId":"`+st+`"}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses", reg, ""), http.StatusBadRequest, "validation_failed")

	rec := s.do(http.MethodPut, alphaHost, "/student-pauses/"+x.ID, reg, `{"reason":"financial","expectedBack":"2099-02-01"}`)
	want(t, rec, http.StatusOK, "")
	if got := decode[pauseOut](t, rec); got.Reason != "financial" {
		t.Fatalf("updated = %+v", got)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/student-pauses/"+x.ID, reg, `{"reason":"other"}`), http.StatusBadRequest, "validation_failed")

	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses/"+x.ID+"/end", reg, `{"endedOn":"2099-03-01"}`), http.StatusBadRequest, "validation_failed")
	rec = s.do(http.MethodPost, alphaHost, "/student-pauses/"+x.ID+"/end", reg, ``)
	want(t, rec, http.StatusOK, "")
	if got := decode[pauseOut](t, rec); got.Status != "ended" || got.EndedOn == nil {
		t.Fatalf("ended = %+v", got)
	}
	if got := s.studentStatus(t, st); got != "active" {
		t.Fatalf("student after end = %s", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses/"+x.ID+"/end", reg, ``), http.StatusConflict, "not_active")
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses/"+x.ID+"/cancel", reg, ``), http.StatusConflict, "not_active")
	want(t, s.do(http.MethodPut, alphaHost, "/student-pauses/"+x.ID, reg, `{"reason":"illness"}`), http.StatusConflict, "not_active")

	// a pause recorded by mistake is cancelled, with a note
	y := s.newPause(t, reg, st)
	rec = s.do(http.MethodPost, alphaHost, "/student-pauses/"+y.ID+"/cancel", reg, `{"note":"wrong child"}`)
	want(t, rec, http.StatusOK, "")
	if got := decode[pauseOut](t, rec); got.Status != "cancelled" || got.Days != nil {
		t.Fatalf("cancelled = %+v", got)
	}
	if got := s.studentStatus(t, st); got != "active" {
		t.Fatalf("student after cancel = %s", got)
	}

	// a transferred student is refused
	other := s.student(t, "alpha", "A-2")
	if _, err := s.adm.Exec(bg, `UPDATE students SET status = 'transferred' WHERE id = $1::uuid`, other); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-pauses", reg, `{"studentId":"`+other+`","reason":"illness"}`), http.StatusBadRequest, "validation_failed")

	list := decode[pausePageOut](t, s.do(http.MethodGet, alphaHost, "/student-pauses?status=ended&q=a-1&sort=-from", reg, ""))
	if list.Page.Total != 1 || list.Items[0].ID != x.ID {
		t.Fatalf("list = %+v", list)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses?sort=reason", reg, ""), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses?status=lost", reg, ""), http.StatusBadRequest, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses/"+x.ID, reg, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses/00000000-0000-0000-0000-000000000000", reg, ""), http.StatusNotFound, "not_found")
	for _, action := range []string{"student_pauses.list", "student_pauses.view", "student_pauses.create", "student_pauses.end", "student_pauses.cancel", "student_pauses.update"} {
		var got int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&got); err != nil || got < 1 {
			t.Errorf("audit %s = %d (%v)", action, got, err)
		}
	}
}

func TestEveryPauseOperationChecksItsPermissionAndTheWholeSchoolScope(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	seed := s.newPause(t, admin, s.student(t, "alpha", "A-1"))
	id := seed.ID
	spare := s.student(t, "alpha", "A-2")

	type call struct{ name, method, path, body, perm string }
	calls := []call{
		{"list", http.MethodGet, "/student-pauses", "", pauView},
		{"get", http.MethodGet, "/student-pauses/" + id, "", pauView},
		{"create", http.MethodPost, "/student-pauses", `{"studentId":"` + spare + `","reason":"illness"}`, pauCreate},
		{"update", http.MethodPut, "/student-pauses/" + id, `{"reason":"financial"}`, pauEdit},
		{"end", http.MethodPost, "/student-pauses/" + id + "/end", "", pauEdit},
		{"cancel", http.MethodPost, "/student-pauses/" + id + "/cancel", "", pauEdit},
	}
	for _, c := range calls {
		if rec := s.do(c.method, alphaHost, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d", c.name, rec.Code)
		}
	}
	users := map[string]string{
		"nobody":   s.signIn(t, alphaHost, "plain", password),
		"viewer":   s.trUser(t, "viewer", map[string]string{pauView: "tenant"}),
		"creator":  s.trUser(t, "creator", map[string]string{pauCreate: "tenant"}),
		"editor":   s.trUser(t, "editor", map[string]string{pauEdit: "tenant"}),
		"narrowed": s.trUser(t, "narrowed", map[string]string{pauView: "class_section", pauCreate: "class_section", pauEdit: "class_section"}),
	}
	holds := map[string]string{"viewer": pauView, "creator": pauCreate, "editor": pauEdit}
	// reads and creates first, then the edits (end and cancel close the pause, so they run last and only once)
	for user, tok := range users {
		for _, c := range calls {
			if c.name == "end" || c.name == "cancel" {
				continue
			}
			rec := s.do(c.method, alphaHost, c.path, tok, c.body)
			allowed := holds[user] == c.perm
			switch {
			case !allowed && rec.Code != http.StatusForbidden:
				t.Errorf("%s calling %s = %d, want 403: %s", user, c.name, rec.Code, rec.Body)
			case allowed && rec.Code == http.StatusForbidden:
				t.Errorf("%s calling %s = 403: %s", user, c.name, rec.Body)
			}
		}
	}
	for user, tok := range users {
		for _, name := range []string{"end", "cancel"} {
			var c call
			for _, k := range calls {
				if k.name == name {
					c = k
				}
			}
			if holds[user] == pauEdit {
				continue
			}
			if rec := s.do(c.method, alphaHost, c.path, tok, c.body); rec.Code != http.StatusForbidden {
				t.Errorf("%s calling %s = %d, want 403", user, name, rec.Code)
			}
		}
	}
	if got := s.studentStatus(t, seed.Student.ID); got != "paused" {
		t.Errorf("a refused call changed the student: %s", got)
	}
	if rec := s.do(http.MethodPost, alphaHost, "/student-pauses/"+id+"/end", users["editor"], ""); rec.Code != http.StatusOK {
		t.Errorf("editor ending = %d %s", rec.Code, rec.Body)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses/"+id, users["narrowed"], ""), http.StatusForbidden, "permission_denied")
	if got := decode[pausePageOut](t, s.do(http.MethodGet, alphaHost, "/student-pauses", users["viewer"], "")); got.Page.Total < 1 {
		t.Errorf("viewer sees %d pauses", got.Page.Total)
	}
}

func TestAPauseIsNotVisibleToAnotherInstituteOrASupportVisit(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	betaAdmin := s.signIn(t, betaHost, "betaadmin", password)
	st := s.student(t, "alpha", "A-1")
	x := s.newPause(t, admin, st)

	for name, c := range map[string]struct{ method, path, body string }{
		"get":    {http.MethodGet, "/student-pauses/" + x.ID, ""},
		"update": {http.MethodPut, "/student-pauses/" + x.ID, `{"reason":"illness"}`},
		"end":    {http.MethodPost, "/student-pauses/" + x.ID + "/end", ""},
		"cancel": {http.MethodPost, "/student-pauses/" + x.ID + "/cancel", ""},
	} {
		if rec := s.do(c.method, betaHost, c.path, betaAdmin, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s of another institute's pause = %d %s", name, rec.Code, rec.Body)
		}
	}
	if got := decode[pausePageOut](t, s.do(http.MethodGet, betaHost, "/student-pauses", betaAdmin, "")); got.Page.Total != 0 {
		t.Errorf("beta sees %d pauses", got.Page.Total)
	}
	want(t, s.do(http.MethodPost, betaHost, "/student-pauses", betaAdmin, `{"studentId":"`+st+`","reason":"illness"}`), http.StatusBadRequest, "validation_failed")
	if got := s.studentStatus(t, st); got != "paused" {
		t.Errorf("alpha's student = %s", got)
	}

	v := s.startVisit(t, s.platformToken(t), s.tenant["alpha"])
	tok := s.redeem(t, alphaHost, v.Code)
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses", tok, ""), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodGet, alphaHost, "/student-pauses/"+x.ID, tok, ""), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodGet, alphaHost, "/alumni", tok, ""), http.StatusForbidden, "read_only_visit")
}

func TestBuiltInRolesAndThePausesAndAlumniScreens(t *testing.T) {
	s := newUsersStack(t)
	holds := func(role, perm string) bool {
		rows, err := s.adm.Query(bg, `SELECT rp.permission_code FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.tenant_id = $1 AND r.code = $2`, s.tenant["alpha"], role)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			if authz.MatchPermission(c, perm) {
				return true
			}
		}
		return false
	}
	for _, role := range []string{"accountant", "librarian", "transport_manager", "security_guard", "nurse", "counselor"} {
		if holds(role, pauView) {
			t.Errorf("%s must not read student pauses", role)
		}
		if !holds(role, aluView) || !holds(role, "stu.students.view") {
			t.Errorf("%s lost another student read", role)
		}
	}
	for _, role := range []string{"admin", "registrar", "principal"} {
		if !holds(role, pauView) || !holds(role, aluView) {
			t.Errorf("%s must read pauses and alumni", role)
		}
	}
	for _, p := range []string{pauCreate, pauEdit} {
		if !holds("registrar", p) || holds("principal", p) {
			t.Errorf("registrar must hold, and principal must not hold, %s", p)
		}
	}
	admin := s.signIn(t, alphaHost, "admin", password)
	narrow := s.trUser(t, "narrowed", map[string]string{pauView: "class_section", aluView: "class_section"})
	onlyPauses := s.trUser(t, "viewer", map[string]string{pauView: "tenant"})
	onlyAlumni := s.trUser(t, "alumniviewer", map[string]string{aluView: "tenant"})
	has := func(tok, screen string) bool {
		rec := s.do(http.MethodGet, alphaHost, "/me/access", tok, "")
		want(t, rec, http.StatusOK, "")
		return strings.Contains(rec.Body.String(), `"screenId":"`+screen+`"`)
	}
	if !has(admin, "student_pauses") || !has(admin, "alumni") || !has(onlyPauses, "student_pauses") || has(onlyPauses, "alumni") ||
		!has(onlyAlumni, "alumni") || has(onlyAlumni, "student_pauses") || has(narrow, "student_pauses") || has(narrow, "alumni") {
		t.Errorf("screens: admin=%v/%v pausesOnly=%v/%v alumniOnly=%v/%v narrowed=%v/%v", has(admin, "student_pauses"), has(admin, "alumni"), has(onlyPauses, "student_pauses"), has(onlyPauses, "alumni"),
			has(onlyAlumni, "student_pauses"), has(onlyAlumni, "alumni"), has(narrow, "student_pauses"), has(narrow, "alumni"))
	}
}
