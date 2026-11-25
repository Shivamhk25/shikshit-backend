package controltower_test

import (
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/onb/controltower"
)

func met(done, total int) controltower.Measure {
	return controltower.Measure{HasSource: true, Checks: []controltower.Check{{Key: "k", Label: "k", Done: done, Total: total}}}
}

func statusOf(t *testing.T, got []controltower.Computed, id string) controltower.Computed {
	t.Helper()
	for _, c := range got {
		if c.Domain.ID == id {
			return c
		}
	}
	t.Fatalf("domain %s not listed", id)
	return controltower.Computed{}
}

// Domains must be in dependency order and only name domains that exist, because Statuses relies on it.
func TestTheGraphIsInDependencyOrder(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range controltower.Domains {
		for _, n := range d.Needs {
			if !seen[n] {
				t.Errorf("%s needs %s, which does not come before it", d.ID, n)
			}
		}
		seen[d.ID] = true
	}
	if len(controltower.Domains) != 13 {
		t.Errorf("domains = %d, the spec lists 13", len(controltower.Domains))
	}
}

func TestADomainCannotBeReadyWhileAPrerequisiteIsNot(t *testing.T) {
	enabled := map[string]bool{"aca": true, "stu": true, "fee": true}
	full := met(3, 3)
	measures := map[string]controltower.Measure{"academic": full, "students": full}
	// Students is marked ready, academic is only marked for review: students must not show Ready.
	got := controltower.Statuses(enabled, measures, map[string]string{"academic": controltower.StateReview, "students": controltower.StateReady})
	if s := statusOf(t, got, "academic").Status; s != controltower.NeedsReview {
		t.Fatalf("academic = %s", s)
	}
	st := statusOf(t, got, "students")
	if st.Status == controltower.Ready {
		t.Fatal("students shows Ready while academic is not Ready")
	}
	if len(st.Blockers) != 1 || st.Blockers[0].ID != "academic" {
		t.Errorf("blockers = %+v", st.Blockers)
	}
	// Once academic is Ready, students is too, and fees (no data source) has no blockers but is still not started.
	got = controltower.Statuses(enabled, measures, map[string]string{"academic": controltower.StateReady, "students": controltower.StateReady})
	if statusOf(t, got, "students").Status != controltower.Ready {
		t.Error("students should be Ready")
	}
	if f := statusOf(t, got, "fees"); f.Status != controltower.NotStarted || len(f.Blockers) != 0 {
		t.Errorf("fees = %+v", f)
	}
}

func TestADisabledModuleHidesItsDomainAndBlocksNothing(t *testing.T) {
	enabled := map[string]bool{"aca": true, "fee": true} // stu is off
	got := controltower.Statuses(enabled, map[string]controltower.Measure{}, nil)
	for _, c := range got {
		if c.Domain.ID == "students" {
			t.Fatal("students listed with its module off")
		}
	}
	// fees needs academic (shown, not ready) and students (hidden): only academic blocks.
	f := statusOf(t, got, "fees")
	if len(f.Blockers) != 1 || f.Blockers[0].ID != "academic" {
		t.Errorf("fees blockers = %+v", f.Blockers)
	}
}

func TestStatusFollowsTheData(t *testing.T) {
	enabled := map[string]bool{"aca": true}
	cases := []struct {
		name  string
		m     controltower.Measure
		state string
		want  string
	}{
		{"no source", controltower.Measure{}, controltower.StateReady, controltower.NotStarted},
		{"no data", met(0, 3), controltower.StateReady, controltower.NotStarted},
		{"partial", met(2, 3), controltower.StateReview, controltower.InProgress},
		{"nothing to measure is not met", met(0, 0), controltower.StateReview, controltower.NotStarted},
		{"met, not asked", met(3, 3), controltower.StateOpen, controltower.InProgress},
		{"met, review", met(3, 3), controltower.StateReview, controltower.NeedsReview},
		{"met, ready", met(3, 3), controltower.StateReady, controltower.Ready},
		{"ready mark but data regressed", met(2, 3), controltower.StateReady, controltower.InProgress},
	}
	for _, c := range cases {
		got := controltower.Statuses(enabled, map[string]controltower.Measure{"academic": c.m}, map[string]string{"academic": c.state})
		if s := statusOf(t, got, "academic").Status; s != c.want {
			t.Errorf("%s: %s, want %s", c.name, s, c.want)
		}
	}
}

func TestPercentIsFlooredSoFullMeansEveryCheckMet(t *testing.T) {
	m := controltower.Measure{HasSource: true, Checks: []controltower.Check{{Done: 199, Total: 200}, {Done: 1, Total: 1}}}
	if p := m.Percent(); p != 99 {
		t.Errorf("percent = %d, want 99", p)
	}
	if (controltower.Measure{}).Percent() != 0 {
		t.Error("an empty measure is 0")
	}
}
