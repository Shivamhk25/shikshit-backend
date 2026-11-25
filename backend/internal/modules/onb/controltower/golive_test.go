package controltower_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/onb/controltower"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

func TestEvaluateRule(t *testing.T) {
	it := func(id, status string, source bool) controltower.Item {
		return controltower.Item{ID: id, Title: id, Status: status, HasSource: source}
	}
	cases := []struct {
		name  string
		items []controltower.Item
		open  bool
		want  string
	}{
		{"nothing enabled never passes", nil, false, "No onboarding domain"},
		{"all ready opens", []controltower.Item{it("a", "ready", true), it("b", "ready", true)}, true, ""},
		{"one not ready blocks", []controltower.Item{it("a", "ready", true), it("b", "needs_review", true)}, false, "b is not Ready"},
		{"no source blocks even when it says ready", []controltower.Item{it("a", "ready", true), it("b", "ready", false)}, false, "cannot be assessed"},
	}
	for _, c := range cases {
		_, why := controltower.Evaluate(c.items)
		if (len(why) == 0) != c.open {
			t.Errorf("%s: reasons = %v", c.name, why)
		}
		if c.want != "" && !strings.Contains(strings.Join(why, "|"), c.want) {
			t.Errorf("%s: reasons %v lack %q", c.name, why, c.want)
		}
	}
}

// ready confirms academic and students Ready (they have data) so only the other enabled domains can still block.
func (w world) readyCore(t *testing.T) {
	t.Helper()
	w.student(t, "S1", true, true)
	for _, d := range []string{"academic", "students"} {
		for _, to := range []string{"review", "ready"} {
			if _, err := w.svc.Transition(modtest.In(w.f.Alpha), w.f.Actor, d, to); err != nil {
				t.Fatalf("%s -> %s: %v", d, to, err)
			}
		}
	}
}

func (w world) tenantStatus(t *testing.T, id string) string {
	t.Helper()
	return w.id(t, `SELECT status FROM tenants WHERE id = $1`, id)
}

func TestGoLiveBlockedUntilEveryDomainIsReadyAndAssessable(t *testing.T) {
	w := setup(t) // aca, stu and fee are enabled; fees has no data source yet
	ctx := modtest.In(w.f.Alpha)

	// Nothing ready yet.
	_, err := w.svc.SignOff(ctx, w.f.Actor, "go")
	var b controltower.Blocked
	if !errors.As(err, &b) || !strings.Contains(strings.Join(b.Reasons, "|"), "Academic structure is not Ready") {
		t.Fatalf("sign-off with nothing ready = %v", err)
	}

	// Academic ready but students only in review: students still blocks.
	w.student(t, "S1", true, true)
	for _, to := range []string{"review", "ready"} {
		_, err := w.svc.Transition(ctx, w.f.Actor, "academic", to)
		must(t, err)
	}
	_, err = w.svc.Transition(ctx, w.f.Actor, "students", "review")
	must(t, err)
	_, err = w.svc.SignOff(ctx, w.f.Actor, "")
	if !errors.As(err, &b) || !strings.Contains(strings.Join(b.Reasons, "|"), "Students and guardians is not Ready") {
		t.Fatalf("sign-off with students in review = %v", err)
	}
	_, err = w.svc.Transition(ctx, w.f.Actor, "students", "ready")
	must(t, err)

	// Everything with data is Ready; fees has no source and must still block.
	_, err = w.svc.SignOff(ctx, w.f.Actor, "")
	if !errors.As(err, &b) || !strings.Contains(strings.Join(b.Reasons, "|"), "Fees cannot be assessed") || len(b.Reasons) != 1 {
		t.Fatalf("sign-off with an unassessable domain = %v", err)
	}
	if got := w.tenantStatus(t, w.f.Alpha.ID); got != "onboarding" {
		t.Errorf("status after refused sign-off = %s", got)
	}
	if n := w.id(t, `SELECT count(*)::text FROM golive_signoffs`); n != "0" {
		t.Errorf("refused sign-off left %s rows", n)
	}
	g, err := w.svc.GoLive(ctx)
	must(t, err)
	if g.Open {
		t.Error("gate open with an unassessable domain")
	}
}

func TestSignOffSnapshotRevokeAndRerun(t *testing.T) {
	w := setup(t)
	ctx := modtest.In(w.f.Alpha)
	w.exec(t, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'fee'`, w.f.Alpha.ID)
	w.readyCore(t)

	s, err := w.svc.SignOff(ctx, w.f.Actor, "  all loaded  ")
	must(t, err)
	if s.Notes != "all loaded" || s.SignedBy != w.f.Actor.UserID || s.SignedAt.IsZero() || len(s.Snapshot) != 2 {
		t.Fatalf("sign-off = %+v", s)
	}
	for _, d := range s.Snapshot {
		if !d.Ready || !d.Assessable || d.Status != controltower.Ready || d.Percent != 100 {
			t.Errorf("snapshot %+v is not the ready state at sign-off", d)
		}
	}
	if got := w.tenantStatus(t, w.f.Alpha.ID); got != "live" {
		t.Errorf("status = %s", got)
	}
	if w.id(t, `SELECT (went_live_at IS NOT NULL)::text FROM tenants WHERE id = $1`, w.f.Alpha.ID) != "true" {
		t.Error("went_live_at not set")
	}
	if w.f.AuditCount(t, "onb.golive.signoff") != 1 {
		t.Error("sign-off not audited")
	}

	// The data changes afterwards: the stored snapshot does not.
	w.student(t, "S2", false, false)
	g, err := w.svc.GoLive(ctx)
	must(t, err)
	if g.Active == nil || g.Active.ID != s.ID || len(g.Active.Snapshot) != 2 || g.Active.Snapshot[1].Percent != 100 || g.Active.Snapshot[1].Status != controltower.Ready {
		t.Errorf("snapshot changed after sign-off: %+v", g.Active)
	}
	if g.TenantStatus != "live" || g.Open {
		t.Errorf("gate for a live institute = %+v", g)
	}
	// Signing twice is refused.
	var b controltower.Blocked
	if _, err := w.svc.SignOff(ctx, w.f.Actor, ""); !errors.As(err, &b) {
		t.Errorf("second sign-off = %v", err)
	}

	// Revoke needs a reason, then returns the institute to onboarding and keeps the history.
	var fe crud.FieldErrors
	if _, err := w.svc.Revoke(ctx, w.f.Actor, "  "); !errors.As(err, &fe) {
		t.Errorf("revoke without reason = %v", err)
	}
	r, err := w.svc.Revoke(ctx, w.f.Actor, "found a gap")
	must(t, err)
	if r.RevokedAt == nil || r.RevokeReason == nil || *r.RevokeReason != "found a gap" || r.ID != s.ID {
		t.Errorf("revoked = %+v", r)
	}
	if got := w.tenantStatus(t, w.f.Alpha.ID); got != "onboarding" {
		t.Errorf("status after revoke = %s", got)
	}
	if _, err := w.svc.Revoke(ctx, w.f.Actor, "again"); !errors.Is(err, controltower.ErrNoSignoff) {
		t.Errorf("second revoke = %v", err)
	}
	// Re-run: S2 is not enrolled, so students is no longer Ready until the mark is redone.
	if _, err := w.svc.SignOff(ctx, w.f.Actor, ""); !errors.As(err, &b) {
		t.Errorf("re-run with changed data = %v", err)
	}
	w.exec(t, `UPDATE students SET status = 'transferred' WHERE registration_code = 'S2'`)
	if _, err := w.svc.SignOff(ctx, w.f.Actor, "second time"); err != nil {
		t.Fatalf("re-run = %v", err)
	}
	g, err = w.svc.GoLive(ctx)
	must(t, err)
	if len(g.History) != 2 || g.Active == nil || g.Active.Notes != "second time" || g.History[1].RevokedAt == nil {
		t.Errorf("history = %+v", g.History)
	}
	if w.f.AuditCount(t, "onb.golive.revoke") != 1 {
		t.Error("revoke not audited")
	}
}

func TestGoLiveIsPerInstitute(t *testing.T) {
	w := setup(t)
	w.exec(t, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'fee'`, w.f.Alpha.ID)
	w.readyCore(t)
	_, err := w.svc.SignOff(modtest.In(w.f.Alpha), w.f.Actor, "")
	must(t, err)

	bravo := modtest.In(w.f.Bravo)
	g, err := w.svc.GoLive(bravo)
	must(t, err)
	if g.TenantStatus != "onboarding" || len(g.History) != 0 || g.Active != nil || g.Open {
		t.Errorf("bravo sees %+v", g)
	}
	if got := w.tenantStatus(t, w.f.Bravo.ID); got != "onboarding" {
		t.Errorf("bravo status = %s", got)
	}
	// Bravo cannot revoke alpha's sign-off, and alpha's row is untouched.
	if _, err := w.svc.Revoke(bravo, w.f.Actor, "no"); !errors.Is(err, controltower.ErrNoSignoff) {
		t.Errorf("bravo revoke = %v", err)
	}
	if w.id(t, `SELECT count(*)::text FROM golive_signoffs WHERE revoked_at IS NULL`) != "1" {
		t.Error("alpha's sign-off was touched")
	}
}
