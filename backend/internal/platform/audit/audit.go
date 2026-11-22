// Package audit writes the append-only audit trail. Callers record inside the same transaction as the change, so a
// change and its audit row commit or roll back together.
package audit

import (
	"context"
	"encoding/json"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Actor says who did something and from where.
type Actor struct {
	UserID    string // institute user
	Source    string // web | ios | android | api | job
	RequestID string
	IP        string
	UserAgent string
	// ImportBatch, when set, tags every audit row with the import batch that caused the change, so a batch can be
	// traced and rolled back from the trail.
	ImportBatch string
}

// Entry is one audited change.
type Entry struct {
	Action        string // e.g. "rooms.create"
	ResourceType  string // e.g. "room"
	ResourceID    string
	Before, After any // JSON-encodable snapshots; nil for none
}

// Record inserts the audit row inside tx, which must be bound to the institute (tenancy.WithTenantTx).
func Record(ctx context.Context, tx pgx.Tx, actor Actor, e Entry) error {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return tenancy.ErrNoTenant
	}
	before, err := marshal(e.Before)
	if err != nil {
		return err
	}
	after, err := marshal(e.After)
	if err != nil {
		return err
	}
	var ip *netip.Addr
	if a, err := netip.ParseAddr(actor.IP); err == nil {
		ip = &a
	}
	source := actor.Source
	switch source {
	case "web", "ios", "android", "api", "job":
	default:
		source = "api"
	}
	meta := []byte("{}")
	if actor.ImportBatch != "" {
		meta, _ = json.Marshal(map[string]string{"importBatch": actor.ImportBatch})
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_type, actor_id, source, action, resource_type, resource_id, before, after, request_id, ip, user_agent, meta)
		VALUES ($1, 'user', NULLIF($2, '')::uuid, $3, $4, $5, NULLIF($6, ''), $7, $8, NULLIF($9, ''), $10, NULLIF($11, ''), $12)`,
		t.ID, actor.UserID, source, e.Action, e.ResourceType, e.ResourceID, before, after, actor.RequestID, ip, actor.UserAgent, meta)
	return err
}

func marshal(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// RecordPlatform inserts an audit row for an action by a platform (Super Admin) user. tenantID is the institute the
// action concerns, or "" for platform-wide actions. tx must be in the platform tier.
func RecordPlatform(ctx context.Context, tx pgx.Tx, actor Actor, tenantID string, e Entry) error {
	before, err := marshal(e.Before)
	if err != nil {
		return err
	}
	after, err := marshal(e.After)
	if err != nil {
		return err
	}
	var ip *netip.Addr
	if a, err := netip.ParseAddr(actor.IP); err == nil {
		ip = &a
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_type, actor_id, source, action, resource_type, resource_id, before, after, request_id, ip, user_agent)
		VALUES (NULLIF($1, '')::uuid, 'platform_user', NULLIF($2, '')::uuid, 'web', $3, $4, NULLIF($5, ''), $6, $7, NULLIF($8, ''), $9, NULLIF($10, ''))`,
		tenantID, actor.UserID, e.Action, e.ResourceType, e.ResourceID, before, after, actor.RequestID, ip, actor.UserAgent)
	return err
}

// RecordSupport inserts an audit row in the institute bound to ctx for something a platform user did during a
// read-only support visit (SAD-05). The school's own audit trail shows who it was, the visit and the reason.
func RecordSupport(ctx context.Context, tx pgx.Tx, platformUserID, visitID, reason string, e Entry) error {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return tenancy.ErrNoTenant
	}
	after, err := marshal(e.After)
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]string{"supportVisit": visitID, "reason": reason})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_type, actor_id, impersonator_id, source, action, resource_type, resource_id, after, meta)
		VALUES ($1, 'platform_user', $2::uuid, $2::uuid, 'impersonation', $3, $4, NULLIF($5, ''), $6, $7)`,
		t.ID, platformUserID, e.Action, e.ResourceType, e.ResourceID, after, meta)
	return err
}
