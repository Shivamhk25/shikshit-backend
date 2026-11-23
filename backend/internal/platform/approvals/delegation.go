package approvals

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/colleagues"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Delegation rules (FND-20 slice 2, approval A-17).
//
// A delegation lets a person who may decide approvals hand them to another user for a period. The delegate acts for
// the delegator, at the delegator's own access and scope, and the history records both. Nothing is copied: the
// delegator's access is resolved again at every decision, so a delegator who loses a permission takes it from the
// delegate too, and a delegator's own access never contains what was delegated to them, so a delegation cannot be
// passed on (no chains).
const (
	// MaxDelegationDays is the longest a delegation may run, counted from its start.
	MaxDelegationDays = 90
	// maxDelegationReason and maxDelegationTypes bound the free parts of a delegation.
	maxDelegationReason = 300
	maxDelegationTypes  = 50
	// delegationListLimit bounds each direction of the "mine" list.
	delegationListLimit = 100
	// maxGrants bounds how many delegations to one person are looked at in one decision or inbox read.
	maxGrants = 50
	// startTolerance lets a client whose clock runs a little behind ask for "now".
	startTolerance = 2 * time.Minute
)

// Delegation states, worked out from the clock when asked.
const (
	DelegationScheduled = "scheduled"
	DelegationActive    = "active"
	DelegationEnded     = "ended"
	DelegationExpired   = "expired"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Delegation is one hand-over of approvals.
type Delegation struct {
	ID            string
	DelegatorID   string
	DelegatorName string
	DelegateID    string
	DelegateName  string
	StartsAt      time.Time
	EndsAt        time.Time
	Reason        string
	// TypeKeys limits the delegation to these workflow types; nil means every type.
	TypeKeys  []string
	EndedAt   *time.Time
	CreatedAt time.Time
	Status    string
}

// DelegationInput describes a delegation to create.
type DelegationInput struct {
	DelegateID string
	// StartsAt defaults to now.
	StartsAt *time.Time
	EndsAt   time.Time
	Reason   string
	TypeKeys []string
}

// DelegationList is a user's delegations in both directions, newest first.
type DelegationList struct{ Given, Received []Delegation }

// grant is one delegation to the viewer that is in force, with the delegator's own access.
type grant struct {
	delegator Person
	access    authz.Access
	typeKeys  []string
}

func (g grant) covers(typeKey string) bool {
	if g.typeKeys == nil {
		return true
	}
	for _, k := range g.typeKeys {
		if k == typeKey {
			return true
		}
	}
	return false
}

func delegationState(d Delegation, now time.Time) string {
	switch {
	case d.EndedAt != nil:
		return DelegationEnded
	case !d.EndsAt.After(now):
		return DelegationExpired
	case d.StartsAt.After(now):
		return DelegationScheduled
	}
	return DelegationActive
}

// activeGrants returns the delegations to userID in force right now (starts_at <= now < ends_at, not ended early),
// each with the delegator's own access resolved fresh. A delegator who is no longer an active user grants nothing.
// The access is the delegator's own roles and scopes only, never anything delegated to them.
func (s Service) activeGrants(ctx context.Context, tx pgx.Tx, userID string) ([]grant, error) {
	if s.Access == nil {
		return nil, nil
	}
	now := s.now()
	rows, err := tx.Query(ctx, `SELECT d.delegator_id::text, u.display_name, d.type_keys
		FROM approval_delegations d JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.delegator_id
		WHERE d.delegate_id = $1::uuid AND d.ended_at IS NULL AND d.starts_at <= $2 AND d.ends_at > $2
		ORDER BY d.created_at, d.id LIMIT $3`, userID, now, maxGrants)
	if err != nil {
		return nil, err
	}
	var grants []grant
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.delegator.ID, &g.delegator.Name, &g.typeKeys); err != nil {
			rows.Close()
			return nil, err
		}
		grants = append(grants, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := grants[:0]
	for _, g := range grants {
		a, err := s.Access.Resolve(ctx, g.delegator.ID)
		if err != nil {
			continue // deactivated or removed meanwhile: nothing to act for
		}
		g.access = a
		out = append(out, g)
	}
	return out, nil
}

// rights is what one viewer may do with one request.
type rights struct {
	// sees: the viewer has a part in the request (own, or through a delegation).
	sees bool
	// own: the viewer may decide the current step with their own access.
	own bool
	// via: when own is false, the delegator the viewer may decide for.
	via *Person
}

func (r rights) canDecide() bool { return r.own || r.via != nil }

// decisionRights is the single rule for reading and deciding a request, own access first, then delegations. A
// delegation never lets the viewer touch their own request, nor the delegator's own request.
func decisionRights(a authz.Access, grants []grant, r Request, steps []StepInfo) rights {
	out := rights{sees: involved(a, r, steps), own: canDecide(a, r, steps)}
	if r.RequesterID == a.UserID {
		return out
	}
	for _, g := range grants {
		if !g.covers(r.TypeKey) || g.access.UserID == r.RequesterID {
			continue
		}
		for _, st := range steps {
			if MayDecide(g.access, st.Permission, r.ScopeType, r.ScopeRef) {
				out.sees = true
				break
			}
		}
		if !out.own && out.via == nil && canDecide(g.access, r, steps) {
			p := g.delegator
			out.via = &p
		}
	}
	return out
}

// stepPermissions lists the permission of every step of the given types (every registered type when none are given).
func (s Service) stepPermissions(typeKeys []string) []string {
	var out []string
	for _, t := range s.Reg.Types() {
		if typeKeys != nil && !contains(typeKeys, t.Key) {
			continue
		}
		for _, st := range t.Steps {
			out = append(out, st.Permission)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// mayDecideAnyStep reports whether access holds the permission of at least one step of the type.
func mayDecideAnyStep(a authz.Access, t Type) bool {
	for _, st := range t.Steps {
		if a.Effective.Can(st.Permission) {
			return true
		}
	}
	return false
}

const delegationColumns = `d.id::text, d.delegator_id::text, dr.display_name, d.delegate_id::text, de.display_name,
	d.starts_at, d.ends_at, d.reason, d.type_keys, d.ended_at, d.created_at`

const delegationFrom = `FROM approval_delegations d
	JOIN users dr ON dr.tenant_id = d.tenant_id AND dr.id = d.delegator_id
	JOIN users de ON de.tenant_id = d.tenant_id AND de.id = d.delegate_id`

func (s Service) scanDelegation(r pgx.Row) (Delegation, error) {
	var d Delegation
	err := r.Scan(&d.ID, &d.DelegatorID, &d.DelegatorName, &d.DelegateID, &d.DelegateName, &d.StartsAt, &d.EndsAt, &d.Reason, &d.TypeKeys, &d.EndedAt, &d.CreatedAt)
	d.Status = delegationState(d, s.now())
	return d, err
}

// CreateDelegation hands the approvals of the owner of access to another active user for a period. Anyone who may
// decide at least one approval step (of the chosen types, when given) may delegate; no separate permission exists,
// because a delegation can never give more than the delegator holds. It must end within MaxDelegationDays of its
// start. The delegate is told.
func (s Service) CreateDelegation(ctx context.Context, actor audit.Actor, a authz.Access, in DelegationInput) (Delegation, error) {
	now := s.now()
	in.DelegateID, in.Reason = strings.TrimSpace(in.DelegateID), strings.TrimSpace(in.Reason)
	var fe crud.FieldErrors
	switch {
	case !uuidPattern.MatchString(in.DelegateID):
		fe.Add("delegateId", "invalid", "Choose the person to hand your approvals to.")
	case strings.EqualFold(in.DelegateID, a.UserID):
		fe.Add("delegateId", "invalid", "You cannot hand your approvals to yourself.")
	}
	if utf8.RuneCountInString(in.Reason) > maxDelegationReason {
		fe.Add("reason", "too_long", "The reason is at most 300 characters.")
	}
	start := now
	if in.StartsAt != nil {
		start = *in.StartsAt
		if start.Before(now.Add(-startTolerance)) {
			fe.Add("startsAt", "invalid", "The start cannot be in the past.")
		}
	}
	switch {
	case in.EndsAt.IsZero():
		fe.Add("endsAt", "required", "Say when the delegation ends.")
	case !in.EndsAt.After(start):
		fe.Add("endsAt", "invalid", "The end must be after the start.")
	case in.EndsAt.After(start.AddDate(0, 0, MaxDelegationDays)):
		fe.Add("endsAt", "too_long", "A delegation lasts at most 90 days.")
	}
	var types []string
	if in.TypeKeys != nil {
		if len(in.TypeKeys) == 0 || len(in.TypeKeys) > maxDelegationTypes {
			fe.Add("typeKeys", "invalid", "Choose between 1 and 50 workflow types, or leave this out for all.")
		}
		seen := map[string]bool{}
		for _, k := range in.TypeKeys {
			k = strings.TrimSpace(k)
			t, ok := s.Reg.Get(k)
			switch {
			case !ok:
				fe.Add("typeKeys", "invalid", "Unknown workflow type.")
			case !mayDecideAnyStep(a, t):
				fe.Add("typeKeys", "invalid", "You cannot decide "+t.Title+", so you cannot hand it over.")
			case !seen[k]:
				seen[k] = true
				types = append(types, k)
			}
		}
		sort.Strings(types)
	}
	if err := fe.OrNil(); err != nil {
		return Delegation{}, err
	}
	// anyone who may decide at least one step of some type (of the chosen types) may delegate
	held := false
	for _, p := range s.stepPermissions(types) {
		if a.Effective.Can(p) {
			held = true
			break
		}
	}
	if !held {
		return Delegation{}, ForbiddenError{"You have no approvals to hand over."}
	}

	var out Delegation
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		// the delegate must be a colleague: exactly the people GET /colleagues offers (active staff accounts of this
		// institute, never a parent or student account)
		ok, err := colleagues.Is(ctx, tx, in.DelegateID)
		if err != nil {
			return err
		}
		if !ok {
			var e crud.FieldErrors
			e.Add("delegateId", "not_found", "That person is not an active staff member of this institute.")
			return e.OrNil()
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO approval_delegations (tenant_id, delegator_id, delegate_id, starts_at, ends_at, reason, type_keys)
			VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3, $4, $5, $6) RETURNING id::text`,
			a.UserID, in.DelegateID, start, in.EndsAt, in.Reason, types).Scan(&id); err != nil {
			return err
		}
		out, err = s.scanDelegation(tx.QueryRow(ctx, `SELECT `+delegationColumns+` `+delegationFrom+` WHERE d.id = $1::uuid`, id))
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "approval.delegation.create", ResourceType: "approval_delegation", ResourceID: id,
			After: map[string]any{"delegator": a.UserID, "delegate": in.DelegateID, "startsAt": start, "endsAt": in.EndsAt, "types": types, "reason": in.Reason}}); err != nil {
			return err
		}
		body := "Until " + in.EndsAt.UTC().Format("2 Jan 2006 15:04") + " UTC."
		if in.Reason != "" {
			body = trunc(body+" "+in.Reason, notify.MaxBody)
		}
		return notify.Notify(ctx, tx, []string{in.DelegateID}, notify.Notification{
			Kind: "approval.delegation_started", Title: trunc(a.UserName+" handed their approvals to you", notify.MaxTitle), Body: body, Link: "/approvals"})
	})
	return out, err
}

// ListDelegations returns the delegations the user gave and received, active and past, newest first.
func (s Service) ListDelegations(ctx context.Context, userID string) (DelegationList, error) {
	out := DelegationList{Given: []Delegation{}, Received: []Delegation{}}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		for _, dir := range []struct {
			column string
			into   *[]Delegation
		}{{"d.delegator_id", &out.Given}, {"d.delegate_id", &out.Received}} {
			rows, err := tx.Query(ctx, `SELECT `+delegationColumns+` `+delegationFrom+` WHERE `+dir.column+` = $1::uuid
				ORDER BY d.created_at DESC, d.id DESC LIMIT $2`, userID, delegationListLimit)
			if err != nil {
				return err
			}
			for rows.Next() {
				d, err := s.scanDelegation(rows)
				if err != nil {
					rows.Close()
					return err
				}
				*dir.into = append(*dir.into, d)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// EndDelegation ends a delegation the owner of access made, now. Anyone else gets ErrNotFound; one that is already
// over answers a ConflictError. The delegate is told.
func (s Service) EndDelegation(ctx context.Context, actor audit.Actor, a authz.Access, id string) (Delegation, error) {
	var out Delegation
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		d, err := s.scanDelegation(tx.QueryRow(ctx, `SELECT `+delegationColumns+` `+delegationFrom+` WHERE d.id = $1::uuid AND d.delegator_id = $2::uuid FOR UPDATE OF d`, id, a.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if d.Status == DelegationEnded || d.Status == DelegationExpired {
			return ConflictError{"This delegation is already over."}
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_delegations SET ended_at = $2 WHERE id = $1::uuid`, id, now); err != nil {
			return err
		}
		d.EndedAt, d.Status = &now, DelegationEnded
		out = d
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "approval.delegation.end", ResourceType: "approval_delegation", ResourceID: id,
			After: map[string]any{"delegator": d.DelegatorID, "delegate": d.DelegateID, "endedAt": now}}); err != nil {
			return err
		}
		return notify.Notify(ctx, tx, []string{d.DelegateID}, notify.Notification{
			Kind: "approval.delegation_ended", Title: trunc(d.DelegatorName+" ended the approvals they handed to you", notify.MaxTitle), Link: "/approvals"})
	})
	return out, err
}

// delegatesOf returns the active users who hold an in-force delegation from any of the delegators that covers the
// type, except the excluded ids (people already told, and the requester).
func (s Service) delegatesOf(ctx context.Context, tx pgx.Tx, delegators []string, typeKey string, exclude map[string]bool) ([]string, error) {
	if len(delegators) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT d.delegate_id::text FROM approval_delegations d
		JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.delegate_id AND u.status = 'active' AND u.deleted_at IS NULL
		WHERE d.delegator_id = ANY($1::uuid[]) AND d.ended_at IS NULL AND d.starts_at <= $2 AND d.ends_at > $2
		AND (d.type_keys IS NULL OR $3 = ANY(d.type_keys)) LIMIT $4`, delegators, s.now(), typeKey, maxApprovers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !exclude[id] {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}
