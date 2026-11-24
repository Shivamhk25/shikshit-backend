package apiv1_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electivechanges"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// approvalsFixture is the approval engine with test-only workflow types. Who may decide is chosen from the users
// newUsersStack creates: admin (everything, whole institute), manager and rolemgr (iam.users.edit), rolemgr
// (iam.roles.edit; stu.students.view only for class section cs-1), plain (nothing).
type approvalsFixture struct {
	svc                           approvals.Service
	approved, rejected, cancelled atomic.Int32
	// skew moves the service clock forward (nanoseconds), so tests can let a delegation run out without waiting
	skew atomic.Int64
}

// advance moves the approval service's clock forward.
func (f *approvalsFixture) advance(d time.Duration) { f.skew.Add(int64(d)) }

func (f *approvalsFixture) now() time.Time { return time.Now().Add(time.Duration(f.skew.Load())) }

func newApprovalsFixture(pool *pgxpool.Pool) *approvalsFixture {
	f := &approvalsFixture{}
	reg := approvals.NewRegistry()
	if err := transfers.Register(reg); err != nil {
		panic(err)
	}
	if err := electivechanges.Register(reg); err != nil {
		panic(err)
	}
	count := func(c *atomic.Int32) approvals.Hook {
		return func(context.Context, pgx.Tx, audit.Actor, approvals.Request) error { c.Add(1); return nil }
	}
	must := func(t approvals.Type) {
		t.OnApproved, t.OnRejected, t.OnCancelled = count(&f.approved), count(&f.rejected), count(&f.cancelled)
		if err := reg.Register(t); err != nil {
			panic(err)
		}
	}
	must(approvals.Type{Key: "test.single", Title: "Test change", Steps: []approvals.Step{{Name: "Review", Permission: "iam.users.edit"}}})
	must(approvals.Type{Key: "test.two", Title: "Test two steps", Steps: []approvals.Step{
		{Name: "Review", Permission: "iam.users.edit"}, {Name: "Final", Permission: "iam.roles.edit"}}})
	must(approvals.Type{Key: "test.scoped", Title: "Test class change", Steps: []approvals.Step{{Name: "Check", Permission: "stu.students.view"}},
		Scope: func(_ context.Context, _ pgx.Tx, subject string) (string, string, error) {
			p := strings.SplitN(subject, "-", 3) // "cs-1-anything" belongs to class section "cs-1"
			return "class_section", p[0] + "-" + p[1], nil
		}})
	// A hook that writes in the deciding transaction and then refuses: nothing it wrote may survive.
	reg2 := approvals.Type{Key: "test.veto", Title: "Test veto", Steps: []approvals.Step{{Name: "Review", Permission: "iam.users.edit"}},
		OnApproved: func(ctx context.Context, tx pgx.Tx, _ audit.Actor, r approvals.Request) error {
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (tenant_id, user_id, kind, title) VALUES (app_current_tenant(), $1::uuid, 'hook.marker', 'marker')`, r.RequesterID); err != nil {
				return err
			}
			return approvals.ConflictError{Message: "The module refused this."}
		}}
	if err := reg.Register(reg2); err != nil {
		panic(err)
	}
	f.svc = approvals.Service{DB: pool, Reg: reg, Access: authz.Service{DB: pool}, Now: f.now}
	return f
}

type approvalOut struct {
	ID          string `json:"id"`
	TypeKey     string `json:"typeKey"`
	SubjectID   string `json:"subjectId"`
	Status      string `json:"status"`
	CurrentStep int    `json:"currentStep"`
	StepCount   int    `json:"stepCount"`
	StepName    string `json:"stepName"`
	CanDecide   bool   `json:"canDecide"`
	CanCancel   bool   `json:"canCancel"`
	Steps       []struct {
		Name      string  `json:"name"`
		Status    string  `json:"status"`
		DecidedBy *string `json:"decidedBy"`
	} `json:"steps"`
	Events []struct {
		Action  string `json:"action"`
		Comment string `json:"comment"`
	} `json:"events"`
}

type approvalPageOut struct {
	Items []approvalOut `json:"items"`
	Page  struct{ Total int }
}

func (s usersStack) tenantCtx(code string) context.Context {
	return tenancy.WithTenant(bg, tenancy.Tenant{ID: s.tenant[code], Code: code, Name: code, Status: "live"})
}

// submit puts a subject up for approval as requester, the way a module would, and commits.
func (s usersStack) submit(code, requester, typeKey, subject string) (approvals.Request, error) {
	var r approvals.Request
	err := tenancy.WithTenantTx(s.tenantCtx(code), s.app, func(ctx context.Context, tx pgx.Tx) (err error) {
		r, err = s.appr.svc.Submit(ctx, tx, audit.Actor{UserID: s.ids[requester], Source: "api"}, approvals.SubmitInput{TypeKey: typeKey, SubjectID: subject, Summary: "Summary of " + subject})
		return err
	})
	return r, err
}

func (s usersStack) mustSubmit(t *testing.T, requester, typeKey, subject string) string {
	t.Helper()
	r, err := s.submit("alpha", requester, typeKey, subject)
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func (s usersStack) inbox(t *testing.T, host, token string) approvalPageOut {
	t.Helper()
	rec := s.do(http.MethodGet, host, "/approvals", token, "")
	want(t, rec, http.StatusOK, "")
	return decode[approvalPageOut](t, rec)
}

func (s usersStack) getApproval(t *testing.T, host, token, id string) approvalOut {
	t.Helper()
	rec := s.do(http.MethodGet, host, "/approvals/"+id, token, "")
	want(t, rec, http.StatusOK, "")
	return decode[approvalOut](t, rec)
}

func (s usersStack) countWhere(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := s.adm.QueryRow(bg, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hasItem(p approvalPageOut, id string) bool {
	for _, it := range p.Items {
		if it.ID == id {
			return true
		}
	}
	return false
}

func TestApprovalHappyPathNotifiesAndAudits(t *testing.T) {
	s := newUsersStack(t)
	plain, manager, admin := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password), s.signIn(t, alphaHost, "admin", password)
	id := s.mustSubmit(t, "plain", "test.single", "u-1")

	// the requester sees it under "mine", never in the inbox; approvers see it in the inbox
	if s.inbox(t, alphaHost, plain).Page.Total != 0 {
		t.Fatal("the requester must not see their own request in the inbox")
	}
	mine := decode[approvalPageOut](t, s.do(http.MethodGet, alphaHost, "/approvals/mine?status=pending", plain, ""))
	if !hasItem(mine, id) {
		t.Fatalf("mine = %+v", mine)
	}
	if !hasItem(s.inbox(t, alphaHost, manager), id) || !hasItem(s.inbox(t, alphaHost, admin), id) {
		t.Fatal("approvers must see it")
	}
	// approvers were told, the requester was not
	for user, n := range map[string]int{"manager": 1, "admin": 1, "plain": 0} {
		if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, s.ids[user]); got != n {
			t.Fatalf("%s got %d approval.requested notifications, want %d", user, got, n)
		}
	}
	d := s.getApproval(t, alphaHost, manager, id)
	if !d.CanDecide || d.CanCancel || d.Status != "pending" || len(d.Steps) != 1 || len(d.Events) != 1 {
		t.Fatalf("detail for an approver = %+v", d)
	}

	rec := s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, `{"comment":"fine"}`)
	want(t, rec, http.StatusOK, "")
	got := decode[approvalOut](t, rec)
	if got.Status != "approved" || got.Steps[0].Status != "approved" || got.Steps[0].DecidedBy == nil || *got.Steps[0].DecidedBy != "manager" ||
		len(got.Events) != 2 || got.Events[1].Action != "approved" || got.Events[1].Comment != "fine" || got.CanDecide {
		t.Fatalf("after approval = %+v", got)
	}
	if s.appr.approved.Load() != 1 || s.appr.rejected.Load() != 0 {
		t.Fatalf("hooks: approved=%d rejected=%d", s.appr.approved.Load(), s.appr.rejected.Load())
	}
	if s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.approved'`, s.ids["plain"]) != 1 {
		t.Fatal("the requester must be told of the approval")
	}
	if s.countWhere(t, `SELECT count(*) FROM audit_log WHERE resource_type = 'approval_request' AND resource_id = $1 AND action IN ('approval.submit','approval.approve')`, id) != 2 {
		t.Fatal("submit and approve must both be audited")
	}
	// it leaves the inbox, and deciding again is refused
	if hasItem(s.inbox(t, alphaHost, admin), id) {
		t.Fatal("a decided request must leave the inbox")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", admin, ``), http.StatusConflict, "conflict")
}

func TestRequesterCannotDecideOwnRequest(t *testing.T) {
	s := newUsersStack(t)
	admin, manager := s.signIn(t, alphaHost, "admin", password), s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "admin", "test.single", "u-self")
	if hasItem(s.inbox(t, alphaHost, admin), id) {
		t.Fatal("an administrator's own request must not be in their inbox")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", admin, ``), http.StatusForbidden, "permission_denied")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", admin, `{"comment":"no"}`), http.StatusForbidden, "permission_denied")
	if d := s.getApproval(t, alphaHost, admin, id); d.Status != "pending" || d.CanDecide {
		t.Fatalf("still pending and not decidable by the requester: %+v", d)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, ``), http.StatusOK, "")
}

func TestOnlyPeopleWithThePermissionSeeAndDecide(t *testing.T) {
	s := newUsersStack(t)
	plain, other := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "admin", "test.two", "u-perm")
	// plain has no part in it: it looks missing, for reading and for deciding
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/"+id, plain, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", plain, ``), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/cancel", plain, ``), http.StatusNotFound, "not_found")
	if s.inbox(t, alphaHost, plain).Page.Total != 0 {
		t.Fatal("plain has nothing to decide")
	}
	// manager may decide step 1 (iam.users.edit) but holds nothing for step 2 (iam.roles.edit)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", other, `{"step":1}`), http.StatusOK, "")
	rec := s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", other, ``)
	want(t, rec, http.StatusForbidden, "permission_denied")
	if d := s.getApproval(t, alphaHost, other, id); d.CanDecide || d.CurrentStep != 2 {
		t.Fatalf("manager may not decide step 2: %+v", d)
	}
}

func TestTwoStepsRunInOrderAndHookRunsOnce(t *testing.T) {
	s := newUsersStack(t)
	manager, rolemgr := s.signIn(t, alphaHost, "manager", password), s.signIn(t, alphaHost, "rolemgr", password)
	id := s.mustSubmit(t, "plain", "test.two", "u-2")
	if d := s.getApproval(t, alphaHost, manager, id); d.StepName != "Review" || d.StepCount != 2 || d.CurrentStep != 1 {
		t.Fatalf("detail = %+v", d)
	}
	// rolemgr holds step 2's permission but not step 1's: not decidable yet, and not asked yet
	if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, s.ids["rolemgr"]); got != 1 {
		t.Fatalf("rolemgr is a step-1 approver too (iam.users.edit): %d", got)
	}
	// a decision aimed at a step the request has not reached is refused
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", rolemgr, `{"step":2}`), http.StatusConflict, "conflict")

	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, `{"step":1}`), http.StatusOK, "")
	d := s.getApproval(t, alphaHost, manager, id)
	if d.Status != "pending" || d.CurrentStep != 2 || d.StepName != "Final" || d.Steps[0].Status != "approved" || s.appr.approved.Load() != 0 {
		t.Fatalf("after step 1 = %+v approved-hook=%d", d, s.appr.approved.Load())
	}
	if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, s.ids["manager"]); got != 1 {
		t.Fatalf("the step-2 approvers are told, not the step-1 approver again: %d", got)
	}
	if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, s.ids["rolemgr"]); got != 2 {
		t.Fatalf("rolemgr must be told when step 2 opens: %d", got)
	}
	// a stale click on step 1 does not decide step 2
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", rolemgr, `{"step":1}`), http.StatusConflict, "conflict")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", rolemgr, `{"step":2}`), http.StatusOK, "")
	if d := s.getApproval(t, alphaHost, manager, id); d.Status != "approved" || len(d.Events) != 3 {
		t.Fatalf("final = %+v", d)
	}
	if s.appr.approved.Load() != 1 {
		t.Fatalf("OnApproved ran %d times", s.appr.approved.Load())
	}
}

func TestRejectNeedsACommentAndEndsTheRequest(t *testing.T) {
	s := newUsersStack(t)
	manager := s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "plain", "test.two", "u-rej")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", manager, ``), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", manager, `{"comment":"   "}`), http.StatusBadRequest, "validation_failed")
	if d := s.getApproval(t, alphaHost, manager, id); d.Status != "pending" || s.appr.rejected.Load() != 0 {
		t.Fatal("a refused rejection must change nothing")
	}
	rec := s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", manager, `{"comment":"Fee does not match the circular"}`)
	want(t, rec, http.StatusOK, "")
	if d := decode[approvalOut](t, rec); d.Status != "rejected" || d.Steps[0].Status != "rejected" || d.Steps[1].Status != "pending" {
		t.Fatalf("rejected = %+v", d)
	}
	if s.appr.rejected.Load() != 1 || s.appr.approved.Load() != 0 {
		t.Fatal("OnRejected must run once and OnApproved not at all")
	}
	var body string
	if err := s.adm.QueryRow(bg, `SELECT body FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.rejected'`, s.ids["plain"]).Scan(&body); err != nil || !strings.Contains(body, "circular") {
		t.Fatalf("the requester must be told why: %q %v", body, err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, ``), http.StatusConflict, "conflict")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", manager, `{"comment":"again"}`), http.StatusConflict, "conflict")
}

func TestOnlyTheRequesterCancelsAndOnlyWhileOpen(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "plain", "test.single", "u-can")
	// an approver can see it but may not withdraw it
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/cancel", manager, ``), http.StatusForbidden, "permission_denied")
	if s.appr.cancelled.Load() != 0 {
		t.Fatal("hook ran for a refused cancel")
	}
	if d := s.getApproval(t, alphaHost, plain, id); !d.CanCancel || d.CanDecide {
		t.Fatalf("requester flags = %+v", d)
	}
	rec := s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/cancel", plain, `{"comment":"typo"}`)
	want(t, rec, http.StatusOK, "")
	if d := decode[approvalOut](t, rec); d.Status != "cancelled" || d.Events[len(d.Events)-1].Action != "cancelled" {
		t.Fatalf("cancelled = %+v", d)
	}
	if s.appr.cancelled.Load() != 1 {
		t.Fatal("OnCancelled must run once")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, ``), http.StatusConflict, "conflict")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/cancel", plain, ``), http.StatusConflict, "conflict")
	// after a decision a request cannot be withdrawn either
	id2 := s.mustSubmit(t, "plain", "test.single", "u-can2")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id2+"/approve", manager, ``), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id2+"/cancel", plain, ``), http.StatusConflict, "conflict")
}

func TestScopedRequestsGoOnlyToApproversWhoCoverTheRecord(t *testing.T) {
	s := newUsersStack(t)
	rolemgr, admin, plain := s.signIn(t, alphaHost, "rolemgr", password), s.signIn(t, alphaHost, "admin", password), s.signIn(t, alphaHost, "plain", password)
	in1 := s.mustSubmit(t, "plain", "test.scoped", "cs-1-a")
	in2 := s.mustSubmit(t, "plain", "test.scoped", "cs-2-a")
	box := s.inbox(t, alphaHost, rolemgr)
	if !hasItem(box, in1) || hasItem(box, in2) {
		t.Fatalf("rolemgr is assigned to cs-1 only: %+v", box)
	}
	if b := s.inbox(t, alphaHost, admin); !hasItem(b, in1) || !hasItem(b, in2) {
		t.Fatal("admin covers the whole institute")
	}
	if s.inbox(t, alphaHost, plain).Page.Total != 0 {
		t.Fatal("plain covers nothing")
	}
	// notifications follow the same rule
	if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested'`, s.ids["rolemgr"]); got != 1 {
		t.Fatalf("rolemgr was told of %d requests, want 1 (cs-1)", got)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/"+in2, rolemgr, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+in2+"/approve", rolemgr, ``), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+in1+"/approve", rolemgr, ``), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+in2+"/approve", admin, ``), http.StatusOK, "")
}

func TestApprovalsStayInsideTheirInstitute(t *testing.T) {
	s := newUsersStack(t)
	betaadmin := s.signIn(t, betaHost, "betaadmin", password)
	id := s.mustSubmit(t, "plain", "test.single", "u-tenant")
	if s.inbox(t, betaHost, betaadmin).Page.Total != 0 {
		t.Fatal("another institute's administrator must see nothing")
	}
	want(t, s.do(http.MethodGet, betaHost, "/approvals/"+id, betaadmin, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, betaHost, "/approvals/"+id+"/approve", betaadmin, ``), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, betaHost, "/approvals/"+id+"/reject", betaadmin, `{"comment":"x"}`), http.StatusNotFound, "not_found")
	if s.appr.approved.Load() != 0 || s.appr.rejected.Load() != 0 {
		t.Fatal("a hook ran for another institute's request")
	}
	// the same subject in another institute is a different request, and the database hides rows outside a tenant
	if _, err := s.submit("beta", "betaadmin", "test.single", "u-tenant"); err != nil {
		t.Fatalf("same subject id in another institute: %v", err)
	}
	var n int
	if err := s.app.QueryRow(bg, `SELECT count(*) FROM approval_requests`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a connection with no institute must see no requests: %d %v", n, err)
	}
	for _, table := range []string{"approval_steps", "approval_events"} {
		if err := s.app.QueryRow(bg, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s visible without an institute: %d %v", table, n, err)
		}
	}
}

func TestSimultaneousDecisionsHaveOneWinner(t *testing.T) {
	s := newUsersStack(t)
	tokens := []string{s.signIn(t, alphaHost, "admin", password), s.signIn(t, alphaHost, "manager", password), s.signIn(t, alphaHost, "rolemgr", password)}
	id := s.mustSubmit(t, "plain", "test.single", "u-race")
	const n = 9
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			path, body := "/approvals/"+id+"/approve", ``
			if i%3 == 2 {
				path, body = "/approvals/"+id+"/reject", `{"comment":"no"}`
			}
			codes[i] = s.do(http.MethodPost, alphaHost, path, tokens[i%3], body).Code
		}(i)
	}
	close(start)
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("codes = %v", codes)
	}
	if hooks := s.appr.approved.Load() + s.appr.rejected.Load(); hooks != 1 {
		t.Fatalf("hooks ran %d times for one decision", hooks)
	}
	if got := s.countWhere(t, `SELECT count(*) FROM approval_events WHERE request_id = $1::uuid AND action IN ('approved','rejected')`, id); got != 1 {
		t.Fatalf("%d decision events", got)
	}
	if got := s.countWhere(t, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action IN ('approval.approve','approval.reject')`, id); got != 1 {
		t.Fatalf("%d decision audit rows", got)
	}
}

func TestAHookRefusalUndoesTheDecision(t *testing.T) {
	s := newUsersStack(t)
	manager := s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "plain", "test.veto", "u-veto")
	rec := s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", manager, ``)
	want(t, rec, http.StatusConflict, "conflict")
	if !strings.Contains(rec.Body.String(), "The module refused this.") {
		t.Fatalf("the module's message must reach the approver: %s", rec.Body)
	}
	d := s.getApproval(t, alphaHost, manager, id)
	if d.Status != "pending" || d.Steps[0].Status != "pending" || len(d.Events) != 1 || !d.CanDecide {
		t.Fatalf("the decision must roll back completely: %+v", d)
	}
	for _, sql := range []string{
		`SELECT count(*) FROM notifications WHERE kind = 'hook.marker'`,
		`SELECT count(*) FROM notifications WHERE kind = 'approval.approved'`,
		`SELECT count(*) FROM audit_log WHERE action = 'approval.approve'`,
	} {
		if s.countWhere(t, sql) != 0 {
			t.Fatalf("%s: something written in the failed decision survived", sql)
		}
	}
}

func TestOneOpenRequestPerSubject(t *testing.T) {
	s := newUsersStack(t)
	manager := s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "plain", "test.single", "u-dup")
	if _, err := s.submit("alpha", "plain", "test.single", "u-dup"); err == nil || !strings.Contains(err.Error(), "already has an open") {
		t.Fatalf("second open request: %v", err)
	}
	// another type for the same subject is separate; after a decision the subject can be put up again
	if _, err := s.submit("alpha", "plain", "test.two", "u-dup"); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", manager, `{"comment":"redo"}`), http.StatusOK, "")
	if _, err := s.submit("alpha", "plain", "test.single", "u-dup"); err != nil {
		t.Fatalf("after a rejection: %v", err)
	}
	if _, err := s.submit("alpha", "plain", "no.such.type", "u-x"); err == nil {
		t.Fatal("an unregistered type must be refused")
	}
}

func TestSupportVisitsCannotUseApprovals(t *testing.T) {
	s := newUsersStack(t)
	id := s.mustSubmit(t, "plain", "test.single", "u-visit")
	tok := s.redeem(t, alphaHost, s.startVisit(t, s.platformToken(t), s.tenant["alpha"]).Code)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", tok, ``), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/reject", tok, `{"comment":"x"}`), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/cancel", tok, ``), http.StatusForbidden, "read_only_visit")
	if s.appr.approved.Load() != 0 {
		t.Fatal("a visit decided a request")
	}
}

func TestMineIsPagedAndFiltered(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	for i := 0; i < 3; i++ {
		s.mustSubmit(t, "plain", "test.single", fmt.Sprintf("u-page-%d", i))
	}
	s.mustSubmit(t, "manager", "test.single", "u-not-plains")
	rec := s.do(http.MethodGet, alphaHost, "/approvals/mine?pageSize=2", plain, "")
	want(t, rec, http.StatusOK, "")
	if p := decode[approvalPageOut](t, rec); p.Page.Total != 3 || len(p.Items) != 2 {
		t.Fatalf("page = %+v", p)
	}
	first := s.inbox(t, alphaHost, manager).Items[0]
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+first.ID+"/approve", manager, ``), http.StatusOK, "")
	if p := decode[approvalPageOut](t, s.do(http.MethodGet, alphaHost, "/approvals/mine?status=approved", plain, "")); p.Page.Total != 1 {
		t.Fatalf("approved = %+v", p)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/mine?status=bogus", plain, ""), http.StatusBadRequest, "")
	want(t, s.do(http.MethodGet, alphaHost, "/approvals", "", ""), http.StatusUnauthorized, "")
}
