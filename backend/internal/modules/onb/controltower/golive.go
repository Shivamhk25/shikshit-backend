package controltower

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNoSignoff means there is no active sign-off to revoke.
var ErrNoSignoff = errors.New("controltower: no active go-live sign-off")

// GateDomain is one enabled domain as the go-live gate judges it. Assessable is false when the domain has no data
// source yet: such a domain cannot be judged and so blocks go-live.
type GateDomain struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Percent    int    `json:"percent"`
	Assessable bool   `json:"assessable"`
	Ready      bool   `json:"ready"`
}

// Evaluate applies the go-live rule to the visible domains. Go-live is allowed only when there is at least one
// domain, every domain has a data source (is assessable) and every domain is Ready. It returns the reasons in
// dependency order; no reasons means the gate is open. Nothing is ever passed by default.
func Evaluate(items []Item) ([]GateDomain, []string) {
	out := make([]GateDomain, 0, len(items))
	var why []string
	if len(items) == 0 {
		why = append(why, "No onboarding domain is enabled for this institute, so there is nothing to confirm.")
	}
	for _, it := range items {
		g := GateDomain{ID: it.ID, Title: it.Title, Status: it.Status, Percent: it.Percent, Assessable: it.HasSource, Ready: it.Status == Ready}
		out = append(out, g)
		switch {
		case !g.Assessable:
			why = append(why, it.Title+" cannot be assessed yet: it has no data source.")
		case !g.Ready:
			why = append(why, it.Title+" is not Ready.")
		}
	}
	return out, why
}

// Signoff is a recorded go-live decision. Snapshot is the readiness of every domain at the moment of signing.
type Signoff struct {
	ID           string
	SignedBy     string
	SignedByName *string
	SignedAt     time.Time
	Notes        string
	Snapshot     []GateDomain
	RevokedAt    *time.Time
	RevokedBy    *string
	RevokeReason *string
}

// Gate is the go-live checklist: the institute's status, the verdict per domain, the reasons it is closed and the
// sign-offs so far (newest first).
type Gate struct {
	TenantStatus string
	Open         bool
	Domains      []GateDomain
	Reasons      []string
	Active       *Signoff
	History      []Signoff
}

const signoffCols = `s.id::text, s.signed_by::text, u.display_name, s.signed_at, s.notes, s.snapshot, s.revoked_at, s.revoked_by::text, s.revoke_reason`

func scanSignoffs(rows pgx.Rows) ([]Signoff, error) {
	defer rows.Close()
	var out []Signoff
	for rows.Next() {
		var s Signoff
		var snap []byte
		if err := rows.Scan(&s.ID, &s.SignedBy, &s.SignedByName, &s.SignedAt, &s.Notes, &snap, &s.RevokedAt, &s.RevokedBy, &s.RevokeReason); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(snap, &s.Snapshot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s Service) gate(ctx context.Context, tx pgx.Tx) (Gate, error) {
	g := Gate{}
	if err := tx.QueryRow(ctx, `SELECT status FROM tenants WHERE id = app_current_tenant()`).Scan(&g.TenantStatus); err != nil {
		return g, err
	}
	items, _, err := s.load(ctx, tx)
	if err != nil {
		return g, err
	}
	g.Domains, g.Reasons = Evaluate(items)
	// A gate for an institute that is not onboarding is closed: there is nothing to sign off.
	if g.TenantStatus != "onboarding" {
		g.Reasons = append(g.Reasons, "The institute is "+g.TenantStatus+", not onboarding.")
	}
	g.Open = len(g.Reasons) == 0
	rows, err := tx.Query(ctx, `SELECT `+signoffCols+` FROM golive_signoffs s LEFT JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.signed_by
		WHERE s.deleted_at IS NULL ORDER BY s.signed_at DESC LIMIT 20`)
	if err != nil {
		return g, err
	}
	if g.History, err = scanSignoffs(rows); err != nil {
		return g, err
	}
	for i := range g.History {
		if g.History[i].RevokedAt == nil {
			g.Active = &g.History[i]
		}
	}
	return g, nil
}

// GoLive returns the gate as it stands now. It reads counts only, no personal data.
func (s Service) GoLive(ctx context.Context) (Gate, error) {
	var g Gate
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		g, err = s.gate(ctx, tx)
		return err
	})
	return g, err
}

// SignOff records the go-live decision and moves the institute from onboarding to live, in one transaction. It is
// refused with Blocked while any domain is not Ready or not assessable, or when the institute is not onboarding.
func (s Service) SignOff(ctx context.Context, actor audit.Actor, notes string) (Signoff, error) {
	var out Signoff
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		notes = strings.TrimSpace(notes)
		if len(notes) > 2000 {
			var fe crud.FieldErrors
			fe.Add("notes", "too_long", "Keep the notes under 2000 characters.")
			return fe
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('onb_tower:' || current_setting('app.tenant_id')))`); err != nil {
			return err
		}
		g, err := s.gate(ctx, tx)
		if err != nil {
			return err
		}
		if !g.Open {
			return Blocked{Reasons: g.Reasons}
		}
		snap, err := json.Marshal(g.Domains)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO golive_signoffs (tenant_id, signed_by, notes, snapshot) VALUES (app_current_tenant(), $1::uuid, $2, $3) RETURNING id::text`,
			actor.UserID, notes, snap).Scan(&id); err != nil {
			return err
		}
		var moved bool
		if err := tx.QueryRow(ctx, `SELECT onb_set_tenant_status('onboarding', 'live')`).Scan(&moved); err != nil {
			return err
		}
		if !moved {
			return Blocked{Reasons: []string{"The institute is not onboarding."}}
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "onb.golive.signoff", ResourceType: "golive_signoff", ResourceID: id,
			Before: map[string]any{"tenantStatus": "onboarding"}, After: map[string]any{"tenantStatus": "live", "notes": notes, "snapshot": g.Domains}}); err != nil {
			return err
		}
		return s.oneSignoff(ctx, tx, id, &out)
	})
	return out, err
}

// Revoke withdraws the active sign-off, with a reason, and returns the institute to onboarding so the gate can be run
// again. The revoked sign-off stays in the history.
func (s Service) Revoke(ctx context.Context, actor audit.Actor, reason string) (Signoff, error) {
	var out Signoff
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		reason = strings.TrimSpace(reason)
		if reason == "" || len(reason) > 2000 {
			var fe crud.FieldErrors
			fe.Add("reason", "invalid", "Give a reason of up to 2000 characters.")
			return fe
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('onb_tower:' || current_setting('app.tenant_id')))`); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `UPDATE golive_signoffs SET revoked_at = now(), revoked_by = $1::uuid, revoke_reason = $2
			WHERE revoked_at IS NULL AND deleted_at IS NULL RETURNING id::text`, actor.UserID, reason).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoSignoff
		}
		if err != nil {
			return err
		}
		var moved bool
		if err := tx.QueryRow(ctx, `SELECT onb_set_tenant_status('live', 'onboarding')`).Scan(&moved); err != nil {
			return err
		}
		if !moved {
			return Blocked{Reasons: []string{"The institute is not live, so the sign-off cannot be revoked here."}}
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "onb.golive.revoke", ResourceType: "golive_signoff", ResourceID: id,
			Before: map[string]any{"tenantStatus": "live"}, After: map[string]any{"tenantStatus": "onboarding", "reason": reason}}); err != nil {
			return err
		}
		return s.oneSignoff(ctx, tx, id, &out)
	})
	return out, err
}

func (s Service) oneSignoff(ctx context.Context, tx pgx.Tx, id string, out *Signoff) error {
	rows, err := tx.Query(ctx, `SELECT `+signoffCols+` FROM golive_signoffs s LEFT JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.signed_by WHERE s.id = $1::uuid`, id)
	if err != nil {
		return err
	}
	list, err := scanSignoffs(rows)
	if err != nil || len(list) != 1 {
		return errors.Join(err, pgx.ErrNoRows)
	}
	*out = list[0]
	return nil
}
