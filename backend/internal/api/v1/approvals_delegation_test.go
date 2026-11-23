package apiv1_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Users from newUsersStack: admin (everything), manager (iam.users.edit), rolemgr (iam.users.edit, iam.roles.edit,
// stu.students.view for class section cs-1 only), plain (nothing).

type delegationOut struct {
	ID            string    `json:"id"`
	DelegatorID   string    `json:"delegatorId"`
	DelegatorName string    `json:"delegatorName"`
	DelegateID    string    `json:"delegateId"`
	DelegateName  string    `json:"delegateName"`
	StartsAt      time.Time `json:"startsAt"`
	EndsAt        time.Time `json:"endsAt"`
	TypeKeys      *[]string `json:"typeKeys"`
	EndedAt       *string   `json:"endedAt"`
	Status        string    `json:"status"`
}

type delegationListOut struct {
	Given    []delegationOut `json:"given"`
	Received []delegationOut `json:"received"`
}

// delegationBody builds a request body; extra are raw JSON members such as `"startsAt":"..."`.
func (s usersStack) delegationBody(delegate string, hours int, extra ...string) string {
	b := fmt.Sprintf(`{"delegateId":%q,"endsAt":%q`, s.ids[delegate], time.Now().Add(time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339))
	for _, e := range extra {
		b += "," + e
	}
	return b + "}"
}

// delegate creates a delegation from the token's owner to the named user and expects 201.
func (s usersStack) delegate(t *testing.T, token, delegate string, hours int, extra ...string) delegationOut {
	t.Helper()
	rec := s.do(http.MethodPost, alphaHost, "/approvals/delegations", token, s.delegationBody(delegate, hours, extra...))
	want(t, rec, http.StatusCreated, "")
	return decode[delegationOut](t, rec)
}

func (s usersStack) approve(token, id string) int {
	return s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", token, ``).Code
}

type delegatedDetail struct {
	approvalOut
	DelegatedFromName *string `json:"delegatedFromName"`
	Events            []struct {
		Action         string  `json:"action"`
		ActorName      string  `json:"actorName"`
		OnBehalfOfName *string `json:"onBehalfOfName"`
	} `json:"events"`
}

func (s usersStack) detail(t *testing.T, token, id string) delegatedDetail {
	t.Helper()
	rec := s.do(http.MethodGet, alphaHost, "/approvals/"+id, token, "")
	want(t, rec, http.StatusOK, "")
	return decode[delegatedDetail](t, rec)
}

func TestDelegateDecidesOnBehalfOnlyWhileActive(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	first := s.mustSubmit(t, "admin", "test.single", "d-1")

	// before the delegation the delegate has no part in the request
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/"+first, plain, ""), http.StatusNotFound, "not_found")
	if s.approve(plain, first) != http.StatusNotFound {
		t.Fatal("a user with no rights and no delegation must not decide")
	}

	d := s.delegate(t, manager, "plain", 1, `"reason":"on leave"`)
	if d.Status != "active" || d.DelegatorName != "manager" || d.DelegateName != "plain" || d.TypeKeys != nil {
		t.Fatalf("delegation = %+v", d)
	}

	// the inbox lists it, marked as delegated; the detail says who handed it over
	in := s.inbox(t, alphaHost, plain)
	var marked struct {
		Items []struct {
			ID                string  `json:"id"`
			DelegatedFromName *string `json:"delegatedFromName"`
		} `json:"items"`
	}
	rec := s.do(http.MethodGet, alphaHost, "/approvals", plain, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &marked); err != nil || !hasItem(in, first) || len(marked.Items) != 1 || marked.Items[0].DelegatedFromName == nil || *marked.Items[0].DelegatedFromName != "manager" {
		t.Fatalf("inbox = %s", rec.Body)
	}
	if got := s.detail(t, plain, first); !got.CanDecide || got.DelegatedFromName == nil || *got.DelegatedFromName != "manager" {
		t.Fatalf("detail for the delegate = %+v", got)
	}
	// the manager's own inbox entry is not marked
	rec = s.do(http.MethodGet, alphaHost, "/approvals", manager, "")
	if json.Unmarshal(rec.Body.Bytes(), &marked) != nil || len(marked.Items) != 1 || marked.Items[0].DelegatedFromName != nil {
		t.Fatalf("manager inbox = %s", rec.Body)
	}

	// the delegate decides; the history and the audit trail name both people
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+first+"/approve", plain, `{"comment":"fine"}`), http.StatusOK, "")
	got := s.detail(t, plain, first)
	last := got.Events[len(got.Events)-1]
	if got.Status != "approved" || last.Action != "approved" || last.ActorName != "plain" || last.OnBehalfOfName == nil || *last.OnBehalfOfName != "manager" {
		t.Fatalf("history = %+v", got.Events)
	}
	if got.Steps[0].DecidedBy == nil || *got.Steps[0].DecidedBy != "plain" {
		t.Fatalf("the step must show who actually decided: %+v", got.Steps)
	}
	if s.countWhere(t, `SELECT count(*) FROM audit_log WHERE action = 'approval.approve' AND resource_id = $1 AND actor_id = $2::uuid AND after->>'onBehalfOf' = $3`,
		first, s.ids["plain"], s.ids["manager"]) != 1 {
		t.Fatal("the audit row must record the delegate as actor and the delegator as onBehalfOf")
	}
	// a decision on one's own authority carries no on-behalf-of
	second := s.mustSubmit(t, "admin", "test.single", "d-2")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+second+"/approve", manager, ``), http.StatusOK, "")
	if ev := s.detail(t, manager, second).Events; ev[len(ev)-1].OnBehalfOfName != nil {
		t.Fatalf("own decision marked as delegated: %+v", ev)
	}

	// the delegation runs out by time alone: nothing was switched off
	third := s.mustSubmit(t, "admin", "test.single", "d-3")
	s.appr.advance(2 * time.Hour)
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/"+third, plain, ""), http.StatusNotFound, "not_found")
	if s.approve(plain, third) != http.StatusNotFound || s.inbox(t, alphaHost, plain).Page.Total != 0 {
		t.Fatal("an expired delegation must give nothing")
	}
	var list delegationListOut
	list = decode[delegationListOut](t, s.do(http.MethodGet, alphaHost, "/approvals/delegations", plain, ""))
	if len(list.Received) != 1 || list.Received[0].Status != "expired" {
		t.Fatalf("received = %+v", list.Received)
	}
	// the delegate can still read what they decided themselves
	s.detail(t, plain, first)
}

func TestDelegationStartsOnlyWhenItsPeriodBegins(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	id := s.mustSubmit(t, "admin", "test.single", "d-future")
	start := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	d := s.delegate(t, manager, "plain", 3, `"startsAt":"`+start+`"`)
	if d.Status != "scheduled" {
		t.Fatalf("status = %s", d.Status)
	}
	if s.approve(plain, id) != http.StatusNotFound {
		t.Fatal("a delegation that has not started must give nothing")
	}
	s.appr.advance(90 * time.Minute)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+id+"/approve", plain, ``), http.StatusOK, "")
}

func TestDelegateActsAtTheDelegatorsAccessOnly(t *testing.T) {
	s := newUsersStack(t)
	plain, manager, rolemgr := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password), s.signIn(t, alphaHost, "rolemgr", password)

	// rights: manager can decide step 1 (iam.users.edit) of test.two but not step 2 (iam.roles.edit)
	s.delegate(t, manager, "plain", 1)
	two := s.mustSubmit(t, "admin", "test.two", "d-two")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+two+"/approve", plain, ``), http.StatusOK, "")
	if code := s.approve(plain, two); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("the delegate decided a step the delegator may not decide: %d", code)
	}
	if d := s.detail(t, manager, two); d.Status != "pending" || d.CurrentStep != 2 {
		t.Fatalf("after = %+v", d)
	}

	// scope: rolemgr may check students of class section cs-1 only
	s.delegate(t, rolemgr, "plain", 1)
	inScope := s.mustSubmit(t, "admin", "test.scoped", "cs-1-a")
	outScope := s.mustSubmit(t, "admin", "test.scoped", "cs-2-b")
	in := s.inbox(t, alphaHost, plain)
	if !hasItem(in, inScope) || hasItem(in, outScope) {
		t.Fatalf("delegated inbox must follow the delegator's scope: %+v", in)
	}
	if s.approve(plain, outScope) != http.StatusNotFound {
		t.Fatal("the delegate decided a record outside the delegator's scope")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+inScope+"/approve", plain, ``), http.StatusOK, "")

	// the delegator's access is read again at each decision: take it away and the delegate loses it
	more := s.mustSubmit(t, "admin", "test.single", "d-revoked")
	if _, err := s.adm.Exec(bg, `DELETE FROM user_roles WHERE user_id = $1::uuid`, s.ids["manager"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `DELETE FROM user_roles WHERE user_id = $1::uuid AND user_id <> $2::uuid`, s.ids["rolemgr"], s.ids["plain"]); err != nil {
		t.Fatal(err)
	}
	if s.approve(plain, more) != http.StatusNotFound {
		t.Fatal("a delegate must lose what the delegator lost")
	}
}

func TestDelegationCanBeLimitedToTypes(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	d := s.delegate(t, manager, "plain", 1, `"typeKeys":["test.two"]`)
	if d.TypeKeys == nil || len(*d.TypeKeys) != 1 {
		t.Fatalf("types = %+v", d.TypeKeys)
	}
	single, two := s.mustSubmit(t, "admin", "test.single", "d-t1"), s.mustSubmit(t, "admin", "test.two", "d-t2")
	if s.approve(plain, single) != http.StatusNotFound {
		t.Fatal("a type-limited delegation covered another type")
	}
	if got := s.inbox(t, alphaHost, plain); hasItem(got, single) || !hasItem(got, two) {
		t.Fatalf("inbox = %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+two+"/approve", plain, ``), http.StatusOK, "")
}

func TestDelegatorEndsItEarly(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	d := s.delegate(t, manager, "plain", 24)
	id := s.mustSubmit(t, "admin", "test.single", "d-end")

	// only the delegator may end it; to anyone else it does not exist
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/delegations/"+d.ID+"/end", plain, ""), http.StatusNotFound, "not_found")
	rec := s.do(http.MethodPost, alphaHost, "/approvals/delegations/"+d.ID+"/end", manager, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[delegationOut](t, rec); got.Status != "ended" || got.EndedAt == nil {
		t.Fatalf("ended = %+v", got)
	}
	if s.approve(plain, id) != http.StatusNotFound {
		t.Fatal("an ended delegation must give nothing")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/delegations/"+d.ID+"/end", manager, ""), http.StatusConflict, "conflict")
	if s.countWhere(t, `SELECT count(*) FROM audit_log WHERE resource_type = 'approval_delegation' AND resource_id = $1 AND action IN ('approval.delegation.create','approval.delegation.end')`, d.ID) != 2 {
		t.Fatal("create and end must both be audited")
	}
	// the delegate was told when it started and when it ended early
	for kind, n := range map[string]int{"approval.delegation_started": 1, "approval.delegation_ended": 1} {
		if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = $2`, s.ids["plain"], kind); got != n {
			t.Fatalf("%s notifications = %d, want %d", kind, got, n)
		}
	}
	// the delegator sees it as given, ended; the delegate as received
	list := decode[delegationListOut](t, s.do(http.MethodGet, alphaHost, "/approvals/delegations", manager, ""))
	if len(list.Given) != 1 || list.Given[0].Status != "ended" || len(list.Received) != 0 {
		t.Fatalf("list = %+v", list)
	}
}

func TestDelegationCannotBePassedOn(t *testing.T) {
	s := newUsersStack(t)
	plain, manager, rolemgr := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password), s.signIn(t, alphaHost, "rolemgr", password)

	// someone who holds nothing of their own has nothing to hand over, delegated rights do not count
	s.delegate(t, manager, "plain", 1)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/delegations", plain, s.delegationBody("rolemgr", 1)), http.StatusForbidden, "permission_denied")

	// rolemgr gives manager the roles step; manager passes "everything" on to plain. plain gets only manager's own.
	s.delegate(t, rolemgr, "manager", 1)
	first := s.mustSubmit(t, "admin", "test.two", "d-chain-1")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+first+"/approve", manager, ``), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+first+"/approve", manager, ``), http.StatusOK, "") // step 2 for rolemgr
	if ev := s.detail(t, manager, first).Events; ev[len(ev)-1].OnBehalfOfName == nil || *ev[len(ev)-1].OnBehalfOfName != "rolemgr" {
		t.Fatalf("manager's second approval should be on behalf of rolemgr: %+v", ev)
	}
	second := s.mustSubmit(t, "admin", "test.two", "d-chain-2")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+second+"/approve", manager, ``), http.StatusOK, "")
	if code := s.approve(plain, second); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("plain used what rolemgr gave manager: %d", code)
	}
	if d := s.detail(t, manager, second); d.Status != "pending" {
		t.Fatalf("second = %+v", d)
	}
	_ = rolemgr
}

func TestNobodyDecidesTheirOwnRequestThroughADelegation(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	s.delegate(t, manager, "plain", 1)

	// the delegate's own request: delegator's rights must not decide it for them
	own := s.mustSubmit(t, "plain", "test.single", "d-own")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+own+"/approve", plain, ``), http.StatusForbidden, "permission_denied")
	if d := s.detail(t, plain, own); d.CanDecide {
		t.Fatal("canDecide on one's own request")
	}
	// the delegator's own request: the delegate may not decide it for them either
	theirs := s.mustSubmit(t, "manager", "test.single", "d-theirs")
	if s.approve(plain, theirs) != http.StatusNotFound {
		t.Fatal("the delegate approved the delegator's own request")
	}
	if got := s.inbox(t, alphaHost, plain); hasItem(got, theirs) || hasItem(got, own) {
		t.Fatalf("inbox = %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/"+theirs+"/approve", s.signIn(t, alphaHost, "admin", password), ``), http.StatusOK, "")
	if s.appr.approved.Load() != 1 {
		t.Fatalf("approved hooks = %d, want only the admin's", s.appr.approved.Load())
	}
	_ = manager
}

func TestDelegatesAreToldWhenAStepOpens(t *testing.T) {
	s := newUsersStack(t)
	manager := s.signIn(t, alphaHost, "manager", password)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	s.delegate(t, manager, "plain", 1)
	s.delegate(t, manager, "rolemgr", 3, `"startsAt":"`+future+`"`) // not in force yet: not told

	s.mustSubmit(t, "admin", "test.single", "d-notify")
	count := func(user string) int {
		return s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested' AND title LIKE '%delegated%'`, s.ids[user])
	}
	if count("plain") != 1 {
		t.Fatalf("the active delegate must be told, got %d", count("plain"))
	}
	if count("manager") != 0 {
		t.Fatal("the approver gets the normal notice, not the delegated one")
	}
	// the delegator who is the requester has no delegate told
	before := count("plain")
	s.mustSubmit(t, "manager", "test.single", "d-notify-own")
	if count("plain") != before {
		t.Fatal("the delegate was told about the delegator's own request")
	}
	// a delegate who is an approver already gets one notice, not two
	s.delegate(t, s.signIn(t, alphaHost, "admin", password), "rolemgr", 1)
	other := s.mustSubmit(t, "plain", "test.single", "d-notify-dup")
	_ = other
	if got := s.countWhere(t, `SELECT count(*) FROM notifications WHERE user_id = $1::uuid AND kind = 'approval.requested' AND link LIKE '%' || $2`, s.ids["rolemgr"], other); got != 1 {
		t.Fatalf("an approver who is also a delegate got %d notices, want 1", got)
	}
}

func TestDelegationRulesAreValidated(t *testing.T) {
	s := newUsersStack(t)
	plain, manager := s.signIn(t, alphaHost, "plain", password), s.signIn(t, alphaHost, "manager", password)
	post := func(token, body string) int {
		return s.do(http.MethodPost, alphaHost, "/approvals/delegations", token, body).Code
	}
	end := func(hours int) string {
		return time.Now().Add(time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
	}
	id := func(u string) string { return s.ids[u] }

	// anyone with no approval right cannot delegate
	if got := post(plain, s.delegationBody("manager", 1)); got != http.StatusForbidden {
		t.Fatalf("no rights: %d", got)
	}
	cases := map[string]struct{ body, field string }{
		"to oneself":         {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q}`, id("manager"), end(1)), "delegateId"},
		"no end":             {fmt.Sprintf(`{"delegateId":%q}`, id("plain")), "endsAt"},
		"end before start":   {fmt.Sprintf(`{"delegateId":%q,"startsAt":%q,"endsAt":%q}`, id("plain"), end(5), end(2)), "endsAt"},
		"start in the past":  {fmt.Sprintf(`{"delegateId":%q,"startsAt":%q,"endsAt":%q}`, id("plain"), end(-5), end(2)), "startsAt"},
		"over 90 days":       {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q}`, id("plain"), end(91*24)), "endsAt"},
		"unknown user":       {fmt.Sprintf(`{"delegateId":"11111111-1111-4111-8111-111111111111","endsAt":%q}`, end(1)), "delegateId"},
		"unknown type":       {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q,"typeKeys":["no.such"]}`, id("plain"), end(1)), "typeKeys"},
		"type not decidable": {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q,"typeKeys":["test.scoped"]}`, id("plain"), end(1)), "typeKeys"},
		"empty types":        {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q,"typeKeys":[]}`, id("plain"), end(1)), "typeKeys"},
		"long reason":        {fmt.Sprintf(`{"delegateId":%q,"endsAt":%q,"reason":%q}`, id("plain"), end(1), strings.Repeat("a", 301)), "reason"},
	}
	for name, c := range cases {
		rec := s.do(http.MethodPost, alphaHost, "/approvals/delegations", manager, c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 on %s", name, rec.Code, rec.Body, c.field)
		}
	}
	if got := post(manager, fmt.Sprintf(`{"delegateId":%q,"endsAt":%q}`, id("plain"), end(89*24))); got != http.StatusCreated {
		t.Fatalf("89 days: %d", got)
	}
	// someone who cannot sign in (disabled) cannot receive one
	if _, err := s.adm.Exec(bg, `UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, id("rolemgr")); err != nil {
		t.Fatal(err)
	}
	if got := post(manager, s.delegationBody("rolemgr", 1)); got != http.StatusBadRequest {
		t.Fatalf("disabled delegate: %d", got)
	}
	// a request without a token is refused
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/delegations", "", ""), http.StatusUnauthorized, "")
}

func TestDelegationsStayInsideTheirInstitute(t *testing.T) {
	s := newUsersStack(t)
	manager, beta := s.signIn(t, alphaHost, "manager", password), s.signIn(t, betaHost, "betaadmin", password)
	d := s.delegate(t, manager, "plain", 1)

	// beta's admin cannot hand approvals to a user of alpha, nor see or end alpha's delegation
	rec := s.do(http.MethodPost, betaHost, "/approvals/delegations", beta, s.delegationBody("plain", 1))
	want(t, rec, http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, betaHost, "/approvals/delegations/"+d.ID+"/end", beta, ""), http.StatusNotFound, "not_found")
	if l := decode[delegationListOut](t, s.do(http.MethodGet, betaHost, "/approvals/delegations", beta, "")); len(l.Given)+len(l.Received) != 0 {
		t.Fatalf("beta sees %+v", l)
	}
	// the database refuses a delegation across institutes even when written directly
	_, err := s.adm.Exec(bg, `INSERT INTO approval_delegations (tenant_id, delegator_id, delegate_id, starts_at, ends_at)
		VALUES ($1, $2::uuid, $3::uuid, now(), now() + interval '1 hour')`, s.tenant["beta"], s.ids["betaadmin"], s.ids["plain"])
	if err == nil {
		t.Fatal("a cross-institute delegation was accepted by the database")
	}
}

func TestSupportVisitsCannotUseDelegations(t *testing.T) {
	s := newUsersStack(t)
	manager := s.signIn(t, alphaHost, "manager", password)
	d := s.delegate(t, manager, "plain", 1)
	tok := s.redeem(t, alphaHost, s.startVisit(t, s.platformToken(t), s.tenant["alpha"]).Code)
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/delegations", tok, s.delegationBody("plain", 1)), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPost, alphaHost, "/approvals/delegations/"+d.ID+"/end", tok, ""), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodGet, alphaHost, "/approvals/delegations", tok, ""), http.StatusForbidden, "read_only_visit")
	if s.countWhere(t, `SELECT count(*) FROM approval_delegations`) != 1 {
		t.Fatal("a visit changed delegations")
	}
}
