package apiv1_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

type transferOut struct {
	ID      string `json:"id"`
	Student struct {
		ID               string `json:"id"`
		RegistrationCode string `json:"registrationCode"`
	} `json:"student"`
	Status            string  `json:"status"`
	Reason            string  `json:"reason"`
	ApprovalRequestID *string `json:"approvalRequestId"`
	TCNumber          *string `json:"tcNumber"`
	Dues              struct {
		Status string `json:"status"`
	} `json:"dues"`
}

type transferResultOut struct {
	Transfer         transferOut `json:"transfer"`
	AlreadyCompleted bool        `json:"alreadyCompleted"`
}

type transferPageOut struct {
	Items []transferOut `json:"items"`
	Page  struct{ Total int }
}

// trUser adds an institute user of alpha who holds exactly the given grants (code -> scope); a class-section grant is
// backed by a real assignment, so it is a narrower scope and not an empty one.
func (s usersStack) trUser(t *testing.T, name string, grants map[string]string) string {
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
	narrow := false
	for code, scope := range grants {
		narrow = narrow || scope == "class_section"
		if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, alpha, rid, code, scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, alpha, uid, rid); err != nil {
		t.Fatal(err)
	}
	if narrow {
		if _, err := s.adm.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', 'cs-1')`, alpha, uid); err != nil {
			t.Fatal(err)
		}
	}
	return s.signIn(t, alphaHost, name, password)
}

const (
	trView   = "stu.student_transfers.view"
	trCreate = "stu.student_transfers.create"
	trEdit   = "stu.student_transfers.edit"
	trDelete = "stu.student_transfers.delete"
)

func (s usersStack) newTransfer(t *testing.T, tok, student string) transferOut {
	t.Helper()
	rec := s.do(http.MethodPost, alphaHost, "/student-transfers", tok, `{"studentId":"`+student+`","reason":"relocation","destinationSchool":"Delhi Public School"}`)
	want(t, rec, http.StatusCreated, "")
	return decode[transferOut](t, rec)
}

func TestATransferGoesFromRequestToCertificateThroughTheApprovalsInbox(t *testing.T) {
	s := newUsersStack(t)
	reg := s.trUser(t, "registrar", map[string]string{trView: "tenant", trCreate: "tenant", trEdit: "tenant", trDelete: "tenant"})
	appr := s.trUser(t, "approver", map[string]string{trEdit: "tenant"})
	st := s.student(t, "alpha", "A-1")

	x := s.newTransfer(t, reg, st)
	if x.Status != "requested" || x.ApprovalRequestID == nil || x.Dues.Status != "not_tracked" || x.Student.RegistrationCode != "A-1" || x.TCNumber != nil {
		t.Fatalf("requested = %+v", x)
	}
	// a second request for the same student is a conflict, not a second approval
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers", reg, `{"studentId":"`+st+`","reason":"health"}`), http.StatusConflict, "open_transfer")
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers", reg, `{"studentId":"`+st+`"}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers", reg, ""), http.StatusBadRequest, "validation_failed")

	// not yet approved: no completion, no certificate
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/complete", reg, ""), http.StatusConflict, "not_approved")
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+x.ID+"/certificate", reg, ""), http.StatusConflict, "not_completed")

	// the requester cannot approve; someone else who may edit transfers can, from the approvals inbox
	ap := *x.ApprovalRequestID
	if got := s.getApproval(t, alphaHost, reg, ap); got.TypeKey != "student_transfer" || got.SubjectID != x.ID || got.CanDecide {
		t.Fatalf("requester's view = %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+ap+"/approve", reg, ""), http.StatusForbidden, "permission_denied")
	if got := s.getApproval(t, alphaHost, appr, ap); !got.CanDecide {
		t.Fatalf("approver's view = %+v", got)
	}
	if n := len(s.inbox(t, alphaHost, appr).Items); n != 1 {
		t.Fatalf("approver's inbox = %d", n)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+ap+"/approve", appr, ""), http.StatusOK, "")
	if got := decode[transferOut](t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+x.ID, reg, "")); got.Status != "approved" {
		t.Fatalf("after approval = %+v", got)
	}

	rec := s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/complete", reg, `{}`)
	want(t, rec, http.StatusOK, "")
	done := decode[transferResultOut](t, rec)
	if done.AlreadyCompleted || done.Transfer.Status != "completed" || done.Transfer.TCNumber == nil || *done.Transfer.TCNumber != "TC-000001" {
		t.Fatalf("completed = %+v", done)
	}
	again := decode[transferResultOut](t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/complete", reg, `{"issuedOn":"2020-01-01"}`))
	if !again.AlreadyCompleted || *again.Transfer.TCNumber != "TC-000001" {
		t.Fatalf("second completion = %+v", again)
	}
	var status string
	if err := s.adm.QueryRow(bg, `SELECT status FROM students WHERE id = $1::uuid`, st).Scan(&status); err != nil || status != "transferred" {
		t.Fatalf("student = %s %v", status, err)
	}

	rec = s.do(http.MethodGet, alphaHost, "/student-transfers/"+x.ID+"/certificate", reg, "")
	want(t, rec, http.StatusOK, "")
	if body := rec.Body.String(); !strings.Contains(body, `"tcNumber":"TC-000001"`) || !strings.Contains(body, `"schoolName":"alpha"`) || !strings.Contains(body, `"guardians":[]`) {
		t.Fatalf("certificate = %s", body)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/cancel", reg, ""), http.StatusConflict, "not_cancellable")
	want(t, s.do(http.MethodDelete, alphaHost, "/student-transfers/"+x.ID, reg, ""), http.StatusConflict, "not_deletable")
	want(t, s.do(http.MethodPut, alphaHost, "/student-transfers/"+x.ID, reg, `{"reason":"health"}`), http.StatusConflict, "not_editable")
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers", reg, `{"studentId":"`+st+`","reason":"health"}`), http.StatusBadRequest, "validation_failed")

	// the list: filters, search by TC number, sorting, and a bad sort or status is a 400
	list := decode[transferPageOut](t, s.do(http.MethodGet, alphaHost, "/student-transfers?status=completed&q=tc-000001&sort=-requested", reg, ""))
	if list.Page.Total != 1 || list.Items[0].ID != x.ID {
		t.Fatalf("list = %+v", list)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers?sort=tc", reg, ""), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers?status=lost", reg, ""), http.StatusBadRequest, "")
	for action, n := range map[string]int{"student_transfers.list": 1, "student_transfers.certificate": 1} {
		var got int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&got); err != nil || got < n {
			t.Errorf("audit %s = %d (%v)", action, got, err)
		}
	}
}

func TestACancelledOrRejectedTransferThroughTheAPI(t *testing.T) {
	s := newUsersStack(t)
	reg := s.trUser(t, "registrar", map[string]string{trView: "tenant", trCreate: "tenant", trEdit: "tenant", trDelete: "tenant"})
	appr := s.trUser(t, "approver", map[string]string{trEdit: "tenant"})
	st := s.student(t, "alpha", "A-1")

	x := s.newTransfer(t, reg, st)
	// only the requester cancels a requested transfer; the approver's refusal is the approval engine's own
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/cancel", appr, `{"comment":"x"}`), http.StatusForbidden, "permission_denied")
	rec := s.do(http.MethodPost, alphaHost, "/student-transfers/"+x.ID+"/cancel", reg, `{"comment":"wrong child"}`)
	want(t, rec, http.StatusOK, "")
	if got := decode[transferOut](t, rec); got.Status != "cancelled" {
		t.Fatalf("cancelled = %+v", got)
	}
	want(t, s.do(http.MethodDelete, alphaHost, "/student-transfers/"+x.ID, reg, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+x.ID, reg, ""), http.StatusNotFound, "not_found")

	y := s.newTransfer(t, reg, st)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+*y.ApprovalRequestID+"/reject", appr, `{"comment":"fees pending"}`), http.StatusOK, "")
	if got := decode[transferOut](t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+y.ID, reg, "")); got.Status != "rejected" {
		t.Fatalf("rejected = %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+y.ID+"/complete", reg, ""), http.StatusConflict, "not_approved")
	want(t, s.do(http.MethodPost, alphaHost, "/student-transfers/"+y.ID+"/cancel", reg, ""), http.StatusConflict, "not_cancellable")
	want(t, s.do(http.MethodDelete, alphaHost, "/student-transfers/"+y.ID, reg, ""), http.StatusNoContent, "")
}

func TestEveryTransferOperationChecksItsPermissionAndTheWholeSchoolScope(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	seed := s.newTransfer(t, admin, st)
	id := seed.ID
	// a student nobody has asked about, for the create calls of the users who may
	spare := []string{s.student(t, "alpha", "A-2"), s.student(t, "alpha", "A-3")}

	type call struct {
		name, method, path, body string
		perm                     string
	}
	calls := []call{
		{"list", http.MethodGet, "/student-transfers", "", trView},
		{"get", http.MethodGet, "/student-transfers/" + id, "", trView},
		{"certificate", http.MethodGet, "/student-transfers/" + id + "/certificate", "", trView},
		{"create", http.MethodPost, "/student-transfers", `{"studentId":"` + spare[0] + `","reason":"health"}`, trCreate},
		{"update", http.MethodPut, "/student-transfers/" + id, `{"reason":"financial"}`, trEdit},
		{"complete", http.MethodPost, "/student-transfers/" + id + "/complete", "", trEdit},
		{"cancel", http.MethodPost, "/student-transfers/" + id + "/cancel", "", trEdit},
		{"delete", http.MethodDelete, "/student-transfers/" + id, "", trDelete},
	}
	// no token
	for _, c := range calls {
		if rec := s.do(c.method, alphaHost, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d", c.name, rec.Code)
		}
	}
	// each user holds one permission, or a narrower scope of everything, or nothing
	users := map[string]string{
		"nobody":   s.signIn(t, alphaHost, "plain", password),
		"viewer":   s.trUser(t, "viewer", map[string]string{trView: "tenant"}),
		"creator":  s.trUser(t, "creator", map[string]string{trCreate: "tenant"}),
		"editor":   s.trUser(t, "editor", map[string]string{trEdit: "tenant"}),
		"deleter":  s.trUser(t, "deleter", map[string]string{trDelete: "tenant"}),
		"narrowed": s.trUser(t, "narrowed", map[string]string{trView: "class_section", trCreate: "class_section", trEdit: "class_section", trDelete: "class_section"}),
	}
	holds := map[string]string{"viewer": trView, "creator": trCreate, "editor": trEdit, "deleter": trDelete}
	for user, tok := range users {
		for _, c := range calls {
			rec := s.do(c.method, alphaHost, c.path, tok, c.body)
			allowed := holds[user] == c.perm
			switch {
			case !allowed && rec.Code != http.StatusForbidden:
				t.Errorf("%s calling %s = %d, want 403: %s", user, c.name, rec.Code, rec.Body)
			case allowed && rec.Code == http.StatusForbidden && !(c.name == "cancel" && strings.Contains(rec.Body.String(), "Only the person who made this request")):
				// cancelling a requested transfer is the requester's alone: that refusal comes from the approval engine
				t.Errorf("%s calling %s = 403: %s", user, c.name, rec.Body)
			}
		}
	}
	// a transfer is not visible to the narrowed or the view-less user even by id
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+id, users["narrowed"], ""), http.StatusForbidden, "permission_denied")
	// the whole-school viewer reads what the admin made
	if got := decode[transferPageOut](t, s.do(http.MethodGet, alphaHost, "/student-transfers", users["viewer"], "")); got.Page.Total < 1 {
		t.Errorf("viewer sees %d transfers", got.Page.Total)
	}
	// the user who may only create has no view: the student lookup for the form is a separate permission
	if rec := s.do(http.MethodGet, alphaHost, "/student-transfers", users["creator"], ""); rec.Code != http.StatusForbidden {
		t.Errorf("creator listed = %d", rec.Code)
	}
}

func TestATransferIsNotVisibleToAnotherInstituteOrASupportVisit(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	betaAdmin := s.signIn(t, betaHost, "betaadmin", password)
	st := s.student(t, "alpha", "A-1")
	x := s.newTransfer(t, admin, st)

	for name, c := range map[string]struct{ method, path, body string }{
		"get":         {http.MethodGet, "/student-transfers/" + x.ID, ""},
		"certificate": {http.MethodGet, "/student-transfers/" + x.ID + "/certificate", ""},
		"update":      {http.MethodPut, "/student-transfers/" + x.ID, `{"reason":"health"}`},
		"complete":    {http.MethodPost, "/student-transfers/" + x.ID + "/complete", ""},
		"cancel":      {http.MethodPost, "/student-transfers/" + x.ID + "/cancel", ""},
		"delete":      {http.MethodDelete, "/student-transfers/" + x.ID, ""},
	} {
		if rec := s.do(c.method, betaHost, c.path, betaAdmin, c.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s of another institute's transfer = %d %s", name, rec.Code, rec.Body)
		}
	}
	if got := decode[transferPageOut](t, s.do(http.MethodGet, betaHost, "/student-transfers", betaAdmin, "")); got.Page.Total != 0 {
		t.Errorf("beta sees %d transfers", got.Page.Total)
	}
	want(t, s.do(http.MethodPost, betaHost, "/student-transfers", betaAdmin, `{"studentId":"`+st+`","reason":"health"}`), http.StatusBadRequest, "validation_failed")
	if got := decode[transferPageOut](t, s.do(http.MethodGet, alphaHost, "/student-transfers", admin, "")); got.Page.Total != 1 {
		t.Errorf("alpha lost its transfer: %d", got.Page.Total)
	}

	// a read-only support visit by platform staff may not read why a child is leaving
	v := s.startVisit(t, s.platformToken(t), s.tenant["alpha"])
	tok := s.redeem(t, alphaHost, v.Code)
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers", tok, ""), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodGet, alphaHost, "/student-transfers/"+x.ID, tok, ""), http.StatusForbidden, "read_only_visit")
}

func TestBuiltInRolesAndTheTransfersScreen(t *testing.T) {
	s := newUsersStack(t)
	grants := func(role string) []string {
		rows, err := s.adm.Query(bg, `SELECT rp.permission_code FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.tenant_id = $1 AND r.code = $2`, s.tenant["alpha"], role)
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
	for _, role := range []string{"accountant", "librarian", "transport_manager", "security_guard", "nurse", "counselor"} {
		if holds(role, trView) {
			t.Errorf("%s must not read student transfers", role)
		}
		if !holds(role, "stu.students.view") || !holds(role, "stu.student_addresses.view") {
			t.Errorf("%s lost its other student reads", role)
		}
	}
	for _, role := range []string{"admin", "registrar", "principal"} {
		if !holds(role, trView) {
			t.Errorf("%s must read student transfers", role)
		}
	}
	for _, p := range []string{trCreate, trEdit, trDelete} {
		if !holds("registrar", p) || holds("principal", p) {
			t.Errorf("registrar must hold, and principal must not hold, %s", p)
		}
	}
	// the screen is offered to a whole-school reader and to nobody else
	admin := s.signIn(t, alphaHost, "admin", password)
	narrow := s.trUser(t, "narrowed", map[string]string{trView: "class_section"})
	onlyView := s.trUser(t, "viewer", map[string]string{trView: "tenant"})
	has := func(tok string) bool {
		rec := s.do(http.MethodGet, alphaHost, "/me/access", tok, "")
		want(t, rec, http.StatusOK, "")
		return strings.Contains(rec.Body.String(), `"screenId":"transfers"`)
	}
	if !has(admin) || !has(onlyView) || has(narrow) {
		t.Errorf("transfers screen: admin=%v viewer=%v narrowed=%v", has(admin), has(onlyView), has(narrow))
	}
}
