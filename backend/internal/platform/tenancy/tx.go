package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Beginner starts transactions; *pgxpool.Pool and *pgx.Conn satisfy it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ErrNoTenant is returned when an institute-scoped operation runs without a resolved tenant.
var ErrNoTenant = errors.New("tenancy: no tenant bound to this request")

// WithTx runs fn in a transaction bound to the given tenant and tier.
//
// The context is set with set_config(..., is_local = true), so it lives only for this transaction and can never
// leak to another request that reuses the pooled connection (also safe with pgbouncer transaction pooling).
// Every query for institute data must go through here: row-level security returns nothing otherwise.
func WithTx(ctx context.Context, db Beginner, tenantID string, tier Tier, fn func(ctx context.Context, tx pgx.Tx) error) (err error) {
	if tier != TierTenant && tier != TierPlatform {
		return fmt.Errorf("tenancy: unknown tier %q", tier)
	}
	if tenantID != "" {
		if _, perr := uuid.Parse(tenantID); perr != nil {
			return fmt.Errorf("tenancy: invalid tenant id")
		}
	} else if tier == TierTenant {
		return ErrNoTenant
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', $2, true)", tenantID, string(tier)); err != nil {
		return err
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WithTenantTx runs fn for the institute resolved by Middleware, as an institute user.
func WithTenantTx(ctx context.Context, db Beginner, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t, ok := FromContext(ctx)
	if !ok {
		return ErrNoTenant
	}
	return WithTx(ctx, db, t.ID, TierTenant, fn)
}
