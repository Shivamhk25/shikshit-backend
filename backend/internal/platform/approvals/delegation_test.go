package approvals

import (
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
)

func accessOf(id string, perms []string, scopes map[string][]string) authz.Access {
	return authz.Access{UserID: id, Effective: authz.Effective{Permissions: perms, Scopes: scopes}}
}

func TestDelegationStateFollowsTheClock(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	cases := []struct {
		name string
		d    Delegation
		want string
	}{
		{"before start", Delegation{StartsAt: now.Add(time.Second), EndsAt: now.Add(time.Hour)}, DelegationScheduled},
		{"at start", Delegation{StartsAt: now, EndsAt: now.Add(time.Hour)}, DelegationActive},
		{"inside", Delegation{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}, DelegationActive},
		{"at end", Delegation{StartsAt: now.Add(-time.Hour), EndsAt: now}, DelegationExpired},
		{"after end", Delegation{StartsAt: now.Add(-2 * time.Hour), EndsAt: now.Add(-time.Hour)}, DelegationExpired},
		{"ended early", Delegation{StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), EndedAt: &ended}, DelegationEnded},
	}
	for _, c := range cases {
		if got := delegationState(c.d, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDecisionRights(t *testing.T) {
	const perm = "fee.concessions.approve"
	steps := []StepInfo{{No: 1, Permission: perm}}
	req := func(requester string) Request {
		return Request{ID: "r", TypeKey: "fee.concession", RequesterID: requester, Status: StatusPending, CurrentStep: 1, StepCount: 1}
	}
	holder := accessOf("holder", []string{perm}, map[string][]string{"fee.concessions": {"tenant"}})
	delegate := accessOf("delegate", nil, nil)
	g := grant{delegator: Person{ID: "holder", Name: "Holder"}, access: holder}

	if rt := decisionRights(delegate, nil, req("asker"), steps); rt.sees || rt.canDecide() {
		t.Error("no delegation, no rights")
	}
	if rt := decisionRights(delegate, []grant{g}, req("asker"), steps); !rt.sees || rt.own || rt.via == nil || rt.via.ID != "holder" {
		t.Errorf("delegated: %+v", rt)
	}
	if rt := decisionRights(holder, []grant{g}, req("asker"), steps); !rt.own || rt.via != nil {
		t.Errorf("own rights come first: %+v", rt)
	}
	if rt := decisionRights(delegate, []grant{g}, req("delegate"), steps); rt.canDecide() {
		t.Error("a delegate decided their own request")
	}
	if rt := decisionRights(delegate, []grant{g}, req("holder"), steps); rt.sees || rt.canDecide() {
		t.Error("a delegate acted on the delegator's own request")
	}
	limited := g
	limited.typeKeys = []string{"other.type"}
	if rt := decisionRights(delegate, []grant{limited}, req("asker"), steps); rt.sees || rt.canDecide() {
		t.Error("a type-limited delegation covered another type")
	}
	closed := req("asker")
	closed.Status = StatusApproved
	if rt := decisionRights(delegate, []grant{g}, closed, steps); rt.canDecide() {
		t.Error("decided a closed request")
	}
	scoped := req("asker")
	scoped.ScopeType, scoped.ScopeRef = ptr("class_section"), ptr("cs-2")
	oneClass := grant{delegator: Person{ID: "holder"}, access: accessOf("holder", []string{perm}, map[string][]string{"fee.concessions": {"class_section:cs-1"}})}
	if rt := decisionRights(delegate, []grant{oneClass}, scoped, steps); rt.sees || rt.canDecide() {
		t.Error("a delegate went beyond the delegator's scope")
	}
}
