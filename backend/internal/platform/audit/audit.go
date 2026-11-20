// Package audit writes the append-only audit trail. Callers record inside the same transaction as the change, so a
// change and its audit row commit or roll back together.
package audit

import (
	"context"
	"encoding/json"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/tenancy"
)

// Actor says who did something and from where.
type Actor struct {
	UserID    string // institute user
	Source    string // web | ios | android | api | job
	RequestID string
	IP        string
	UserAgent string
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
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (tenant_id, actor_type, actor_id, source, action, resource_type, resource_id, before, after, request_id, ip, user_agent)
		VALUES ($1, 'user', NULLIF($2, '')::uuid, $3, $4, $5, NULLIF($6, ''), $7, $8, NULLIF($9, ''), $10, NULLIF($11, ''))`,
		t.ID, actor.UserID, source, e.Action, e.ResourceType, e.ResourceID, before, after, actor.RequestID, ip, actor.UserAgent)
	return err
}

func marshal(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
