package controltower

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound means the domain does not exist or its module is off for this institute.
var ErrNotFound = resource.ErrNotFound

// Blocked is returned when a change is refused, with the reasons.
type Blocked struct{ Reasons []string }

func (b Blocked) Error() string { return "controltower: " + strings.Join(b.Reasons, "; ") }

// Item is one domain as the control tower shows it.
type Item struct {
	ID, Title, Module string
	Status            string
	Percent           int
	HasSource         bool
	Note              string // NoSource when there is no data source yet
	Checks            []Check
	Blockers          []Blocker
	OwnerID           *string
	OwnerName         *string
	DueDate           *string
	Overdue           bool
	State             string
	// OpenImports counts import batches for this domain that are uploaded but not yet committed or abandoned.
	OpenImports       int
	ReviewRequestedAt *time.Time
	ReadyAt           *time.Time
}

// View is the whole tower.
type View struct {
	Domains               []Item
	Total, Ready, Percent int
}

// Service reads and changes the control tower.
type Service struct {
	DB  tenancy.Beginner
	Now func() time.Time
	// ImportDomains maps an import entity key to its domain id, so open import batches are counted per domain.
	ImportDomains map[string]string
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type stored struct {
	ownerID, ownerName, due *string
	state                   string
	reviewAt, readyAt       *time.Time
	reviewBy                *string
}

// load computes every visible domain inside tx.
func (s Service) load(ctx context.Context, tx pgx.Tx) ([]Item, map[string]Measure, error) {
	enabled := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT module_code FROM tenant_modules WHERE enabled`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return nil, nil, err
		}
		enabled[m] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	measures, err := measureAll(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	st := map[string]stored{}
	rows, err = tx.Query(ctx, `SELECT d.domain, d.owner_id::text, u.display_name, d.due_date::text, d.state, d.review_requested_at, d.review_requested_by::text, d.ready_at
		FROM onboarding_domains d LEFT JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.owner_id WHERE d.deleted_at IS NULL`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var r stored
		if err := rows.Scan(&id, &r.ownerID, &r.ownerName, &r.due, &r.state, &r.reviewAt, &r.reviewBy, &r.readyAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		st[id] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	open := map[string]int{}
	rows, err = tx.Query(ctx, `SELECT entity, count(*) FROM import_batches WHERE status IN ('uploaded', 'validating', 'validated', 'committing') GROUP BY entity`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var e string
		var n int
		if err := rows.Scan(&e, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if d, ok := s.ImportDomains[e]; ok {
			open[d] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	states := map[string]string{}
	for id, r := range st {
		states[id] = r.state
	}
	today := s.now().UTC().Format("2006-01-02")
	var items []Item
	for _, c := range Statuses(enabled, measures, states) {
		m := measures[c.Domain.ID]
		r := st[c.Domain.ID]
		it := Item{ID: c.Domain.ID, Title: c.Domain.Title, Module: c.Domain.Module, Status: c.Status, Percent: m.Percent(), HasSource: m.HasSource,
			Checks: m.Checks, Blockers: c.Blockers, OwnerID: r.ownerID, OwnerName: r.ownerName, DueDate: r.due, State: StateOpen,
			OpenImports: open[c.Domain.ID], ReviewRequestedAt: r.reviewAt, ReadyAt: r.readyAt}
		if r.state != "" {
			it.State = r.state
		}
		if !m.HasSource {
			it.Note = NoSource
		}
		if it.Checks == nil {
			it.Checks = []Check{}
		}
		if it.Blockers == nil {
			it.Blockers = []Blocker{}
		}
		it.Overdue = r.due != nil && *r.due < today && c.Status != Ready
		items = append(items, it)
	}
	return items, measures, nil
}

// Overview returns every domain of the enabled modules with its computed status. It reads counts only, no personal data.
func (s Service) Overview(ctx context.Context) (View, error) {
	var v View
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		items, _, err := s.load(ctx, tx)
		if err != nil {
			return err
		}
		v = summarise(items)
		return nil
	})
	return v, err
}

func summarise(items []Item) View {
	v := View{Domains: items, Total: len(items)}
	sum := 0
	for _, it := range items {
		sum += it.Percent
		if it.Status == Ready {
			v.Ready++
		}
	}
	if v.Total > 0 {
		v.Percent = sum / v.Total
	}
	if v.Domains == nil {
		v.Domains = []Item{}
	}
	return v
}

func find(items []Item, id string) (Item, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// ensureRow creates the plan and the domain row if they are missing, under a lock so two callers cannot both create them.
func ensureRow(ctx context.Context, tx pgx.Tx, actorID, domain string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('onb_tower:' || current_setting('app.tenant_id')))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO onboarding_plans (tenant_id, created_by) SELECT app_current_tenant(), NULLIF($1, '')::uuid
		WHERE NOT EXISTS (SELECT 1 FROM onboarding_plans WHERE deleted_at IS NULL)`, actorID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO onboarding_domains (tenant_id, plan_id, domain, created_by)
		SELECT app_current_tenant(), p.id, $2, NULLIF($1, '')::uuid FROM onboarding_plans p WHERE p.deleted_at IS NULL
		ON CONFLICT (tenant_id, plan_id, domain) WHERE deleted_at IS NULL DO NOTHING`, actorID, domain)
	return err
}

// Assignment is who owns a domain and by when. Nil clears the field.
type Assignment struct {
	OwnerID *string
	DueDate *string
}

// Assign sets the owner and due date of a visible domain.
func (s Service) Assign(ctx context.Context, actor audit.Actor, domain string, in Assignment) (Item, error) {
	var out Item
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		items, _, err := s.load(ctx, tx)
		if err != nil {
			return err
		}
		before, ok := find(items, domain)
		if !ok {
			return ErrNotFound
		}
		var fe crud.FieldErrors
		var due *time.Time
		if in.DueDate != nil {
			t, err := time.Parse("2006-01-02", *in.DueDate)
			if err != nil {
				fe.Add("dueDate", "invalid", "Enter the due date as YYYY-MM-DD.")
			}
			due = &t
		}
		if in.OwnerID != nil {
			var ok bool
			if !refs.IsUUID(*in.OwnerID) {
				fe.Add("ownerId", "invalid", "Choose an active user of this institute.")
			} else if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1::uuid AND deleted_at IS NULL AND status = 'active')`, *in.OwnerID).Scan(&ok); err != nil {
				return err
			} else if !ok {
				fe.Add("ownerId", "not_found", "Choose an active user of this institute.")
			}
		}
		if len(fe) > 0 {
			return fe
		}
		if err := ensureRow(ctx, tx, actor.UserID, domain); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE onboarding_domains SET owner_id = $2::uuid, due_date = $3, updated_by = NULLIF($4, '')::uuid
			WHERE domain = $1 AND deleted_at IS NULL`, domain, in.OwnerID, due, actor.UserID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "onb.control_tower.assign", ResourceType: "onboarding_domain", ResourceID: domain,
			Before: map[string]any{"ownerId": before.OwnerID, "dueDate": before.DueDate}, After: map[string]any{"ownerId": in.OwnerID, "dueDate": in.DueDate}}); err != nil {
			return err
		}
		items, _, err = s.load(ctx, tx)
		out, _ = find(items, domain)
		return err
	})
	return out, err
}

// Transition moves a domain to state open, review or ready.
//
//	review: needs every readiness rule met.
//	ready:  needs a review mark, every rule met and every visible prerequisite Ready. Notifies the people waiting on it.
//	open:   withdraws a review or a ready mark.
func (s Service) Transition(ctx context.Context, actor audit.Actor, domain, to string) (Item, error) {
	var out Item
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if to != StateOpen && to != StateReview && to != StateReady {
			var fe crud.FieldErrors
			fe.Add("to", "invalid", "Choose open, review or ready.")
			return fe
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('onb_tower:' || current_setting('app.tenant_id')))`); err != nil {
			return err
		}
		items, measures, err := s.load(ctx, tx)
		if err != nil {
			return err
		}
		cur, ok := find(items, domain)
		if !ok {
			return ErrNotFound
		}
		m := measures[domain]
		var why []string
		if to != StateOpen && !m.HasSource {
			why = append(why, "This domain has no data source yet.")
		} else if to != StateOpen && !m.RulesMet() {
			why = append(why, "Not every readiness rule is met yet.")
		}
		if to == StateReady {
			if cur.State == StateOpen {
				why = append(why, "Mark the domain ready for review first.")
			}
			for _, b := range cur.Blockers {
				why = append(why, b.Title+" is not Ready.")
			}
		}
		if len(why) > 0 {
			return Blocked{Reasons: why}
		}
		if err := ensureRow(ctx, tx, actor.UserID, domain); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE onboarding_domains SET state = $2, updated_by = NULLIF($3, '')::uuid,
			review_requested_by = CASE WHEN $2 = 'review' THEN NULLIF($3, '')::uuid WHEN $2 = 'open' THEN NULL ELSE review_requested_by END,
			review_requested_at = CASE WHEN $2 = 'review' THEN now() WHEN $2 = 'open' THEN NULL ELSE review_requested_at END,
			ready_by = CASE WHEN $2 = 'ready' THEN NULLIF($3, '')::uuid ELSE NULL END,
			ready_at = CASE WHEN $2 = 'ready' THEN now() ELSE NULL END
			WHERE domain = $1 AND deleted_at IS NULL`, domain, to, actor.UserID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "onb.control_tower." + to, ResourceType: "onboarding_domain", ResourceID: domain,
			Before: map[string]any{"state": cur.State, "status": cur.Status}, After: map[string]any{"state": to}}); err != nil {
			return err
		}
		items, _, err = s.load(ctx, tx)
		if err != nil {
			return err
		}
		out, _ = find(items, domain)
		if cur.Status != Ready && out.Status == Ready {
			return s.domainReady(ctx, tx, items, out)
		}
		return nil
	})
	return out, err
}

// domainReady is the event "a domain turned Ready": it notifies the domain's owner, whoever asked for the review, and
// the owners of the domains that were waiting on it.
func (s Service) domainReady(ctx context.Context, tx pgx.Tx, items []Item, done Item) error {
	var who []string
	if done.OwnerID != nil {
		who = append(who, *done.OwnerID)
	}
	var by *string
	if err := tx.QueryRow(ctx, `SELECT review_requested_by::text FROM onboarding_domains WHERE domain = $1 AND deleted_at IS NULL`, done.ID).Scan(&by); err != nil {
		return err
	}
	if by != nil {
		who = append(who, *by)
	}
	for _, it := range items {
		if d, ok := Lookup(it.ID); ok && it.OwnerID != nil {
			for _, n := range d.Needs {
				if n == done.ID {
					who = append(who, *it.OwnerID)
				}
			}
		}
	}
	return notify.Notify(ctx, tx, who, notify.Notification{
		Kind: "onboarding.domain_ready", Title: done.Title + " is ready",
		Body: done.Title + " has been confirmed ready. Domains that were waiting on it can move forward.", Link: "/onboarding"})
}

// ImportDomains maps each import entity to its domain id, from the import registry the import engine already serves.
func ImportDomains(reg *importer.Registry) map[string]string {
	out := map[string]string{}
	for _, e := range reg.All() {
		out[e.Key()] = e.Domain()
	}
	return out
}
