package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Pack is an ordered set of importable entities that together set up one data domain (ONB-10), with the rules that
// say when the domain is ready. Definitions are code: table names and rule SQL are never taken from a request.
type Pack struct {
	Key    string
	Title  string
	Domain string
	Steps  []PackStep
	Rules  []PackRule
}

// PackStep is one entity in a pack's order.
type PackStep struct {
	Entity string
	Title  string
	// Table is counted (live rows only) to show whether the step has data.
	Table string
	// Required steps must have data for the pack to be ready.
	Required bool
	// Requires lists steps whose data this one depends on (a term needs its year). Until they have data the step waits.
	Requires []string
}

// PackRule is one condition for the pack to be ready.
type PackRule struct {
	Key   string
	Label string
	// SQL returns one boolean. It runs inside the institute's transaction.
	SQL string
	// Pending, when set, means the rule cannot be checked yet (the data it needs is not built); it explains why. A
	// pending rule keeps the pack from being reported ready.
	Pending string
}

// StepStatus is a step with what the institute has so far.
type StepStatus struct {
	Entity   string   `json:"entity"`
	Title    string   `json:"title"`
	Order    int      `json:"order"`
	Count    int      `json:"count"`
	State    string   `json:"state"` // done | todo | waiting
	Required bool     `json:"required"`
	Requires []string `json:"requires"`
	Next     bool     `json:"next"`
}

// RuleStatus is a rule and whether it holds.
type RuleStatus struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Met     bool   `json:"met"`
	Checked bool   `json:"checked"`
	Detail  string `json:"detail,omitempty"`
}

// PackStatus is where an institute stands on a pack.
type PackStatus struct {
	Key    string       `json:"key"`
	Title  string       `json:"title"`
	Domain string       `json:"domain"`
	Ready  bool         `json:"ready"`
	Steps  []StepStatus `json:"steps"`
	Rules  []RuleStatus `json:"rules"`
}

// PackStatus computes the status of a pack for the institute in ctx. The next step is the first required step that
// has no data and is not waiting on another.
func (s Service) PackStatus(ctx context.Context, key string) (PackStatus, error) {
	p, ok := s.Packs[key]
	if !ok {
		return PackStatus{}, ErrNotFound
	}
	out := PackStatus{Key: p.Key, Title: p.Title, Domain: p.Domain, Steps: make([]StepStatus, len(p.Steps)), Rules: make([]RuleStatus, len(p.Rules))}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		counts := map[string]int{}
		for i, st := range p.Steps {
			var n int
			if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE deleted_at IS NULL`, st.Table)).Scan(&n); err != nil {
				return err
			}
			counts[st.Entity] = n
			out.Steps[i] = StepStatus{Entity: st.Entity, Title: st.Title, Order: i + 1, Count: n, Required: st.Required, Requires: append([]string{}, st.Requires...)}
		}
		for i := range out.Steps {
			st := &out.Steps[i]
			switch {
			case st.Count > 0:
				st.State = "done"
			default:
				st.State = "todo"
				for _, r := range st.Requires {
					if counts[r] == 0 {
						st.State = "waiting"
					}
				}
			}
		}
		for i, st := range out.Steps {
			if st.Required && st.State == "todo" {
				out.Steps[i].Next = true
				break
			}
		}
		out.Ready = true
		for i, r := range p.Rules {
			rs := RuleStatus{Key: r.Key, Label: r.Label}
			if r.Pending != "" {
				rs.Detail = r.Pending
				out.Ready = false
			} else {
				rs.Checked = true
				if err := tx.QueryRow(ctx, r.SQL).Scan(&rs.Met); err != nil {
					return err
				}
				if !rs.Met {
					out.Ready = false
				}
			}
			out.Rules[i] = rs
		}
		return nil
	})
	return out, err
}
