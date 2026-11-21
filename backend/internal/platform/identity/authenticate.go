package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID        string
	TenantID      string
	SessionID     string
	AccessVersion int64
}

// ErrUnauthenticated is returned for any token that cannot be accepted. The reason is never exposed to the caller.
var ErrUnauthenticated = errors.New("identity: unauthenticated")

// Authenticate verifies a bearer access token for the institute bound to ctx.
//
// Beyond the signature and expiry it checks, in the database, that the token belongs to this institute, that its
// session family is still live (logout and refresh-reuse revoke the family, which kills outstanding access tokens
// at once instead of after their 15 minutes) and that the user is still active.
//
// The family is checked, not the single session row: refresh rotation revokes the row an access token was issued
// from, and tokens issued before the rotation must keep working until they expire.
func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Principal{}, tenancy.ErrNoTenant
	}
	claims, err := s.signer.Verify(token)
	if err != nil || claims.TenantID != t.ID || claims.Tier != string(tenancy.TierTenant) {
		return Principal{}, ErrUnauthenticated
	}

	p := Principal{UserID: claims.Subject, TenantID: t.ID, SessionID: claims.SessionID}
	var live bool
	err = tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.access_version,
			       EXISTS (SELECT 1 FROM sessions f
			               WHERE f.family_id = s.family_id AND f.revoked_at IS NULL AND f.expires_at > now())
			FROM sessions s
			JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			WHERE s.id::text = $1 AND s.user_id::text = $2 AND u.status = 'active' AND u.deleted_at IS NULL`,
			claims.SessionID, claims.Subject).Scan(&p.AccessVersion, &live)
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !live) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}
