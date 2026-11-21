package identity

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Errors a caller maps to HTTP. Sign-in failures are deliberately indistinguishable (unknown user, wrong password,
// disabled account) so the response does not reveal which usernames exist.
var (
	ErrInvalidCredentials = errors.New("identity: invalid credentials")
	// ErrLocked means too many failed attempts; it is only returned after the password has been checked correctly,
	// so it cannot be used to probe which usernames exist.
	ErrLocked = errors.New("identity: account temporarily locked")
	// ErrInvalidRefresh covers unknown, expired and revoked refresh tokens.
	ErrInvalidRefresh = errors.New("identity: invalid refresh token")
	// ErrRefreshReuse means a refresh token that was already rotated was presented again: the family is revoked.
	ErrRefreshReuse = errors.New("identity: refresh token reuse detected")
)

// Policy holds the lockout and lifetime settings.
type Policy struct {
	MaxFailedLogins int           // consecutive failures before the account locks
	LockDuration    time.Duration // how long it stays locked
	RefreshTTL      time.Duration // lifetime of each refresh token; every rotation starts a fresh window (sliding)
}

// DefaultPolicy is 5 failures -> 15 minute lock, 30 day refresh tokens.
var DefaultPolicy = Policy{MaxFailedLogins: 5, LockDuration: 15 * time.Minute, RefreshTTL: 30 * 24 * time.Hour}

// Client describes the calling app; it is stored on the session so users can see and revoke their devices.
type Client struct {
	Name      string // web | ios | android
	Version   string
	IP        string
	UserAgent string
}

// Tokens is the result of a sign-in or a refresh.
type Tokens struct {
	AccessToken        string
	AccessExpiresAt    time.Time
	RefreshToken       string
	RefreshExpiresAt   time.Time
	UserID             string
	MustChangePassword bool
}

// Service implements sign-in, refresh rotation and sign-out for institute users.
type Service struct {
	db     tenancy.Beginner
	signer *Signer
	policy Policy
	now    func() time.Time
	// dummyHash is verified when the username is unknown so both paths cost the same time.
	dummyHash string
}

// NewService returns a Service. db must be the runtime (row-level-security) pool.
func NewService(db tenancy.Beginner, signer *Signer, policy Policy) (*Service, error) {
	dummy, err := HashPassword("not-a-real-password")
	if err != nil {
		return nil, err
	}
	return &Service{db: db, signer: signer, policy: policy, now: time.Now, dummyHash: dummy}, nil
}

// Login checks a username and password inside the institute bound to ctx and opens a session.
func (s *Service) Login(ctx context.Context, username, password string, c Client) (Tokens, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Tokens{}, tenancy.ErrNoTenant
	}

	var (
		out     Tokens
		failure error
	)
	// The failure counter must be saved even when sign-in fails, so errors are carried out of the transaction
	// as values and the transaction itself commits.
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		var (
			id, status, hash string
			failed           int
			lockedUntil      *time.Time
			mustChange       bool
			accessVersion    int64
		)
		err := tx.QueryRow(ctx, `
			SELECT id::text, status, COALESCE(password_hash, ''), failed_login_count, locked_until, must_change_password, access_version
			FROM users WHERE username = $1::citext AND deleted_at IS NULL FOR UPDATE`, username).
			Scan(&id, &status, &hash, &failed, &lockedUntil, &mustChange, &accessVersion)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !found || hash == "" {
			_, _ = VerifyPassword(password, s.dummyHash)
			failure = ErrInvalidCredentials
			return nil
		}

		match, verr := VerifyPassword(password, hash)
		if verr != nil {
			return verr
		}
		now := s.now()
		if !match {
			failed++
			if failed >= s.policy.MaxFailedLogins {
				until := now.Add(s.policy.LockDuration)
				if _, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = 0, locked_until = $2 WHERE id = $1`, id, until); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = $2 WHERE id = $1`, id, failed); err != nil {
				return err
			}
			failure = ErrInvalidCredentials
			return nil
		}
		// Correct password: only now is it safe to tell the caller why they cannot sign in.
		if lockedUntil != nil && lockedUntil.After(now) {
			failure = ErrLocked
			return nil
		}
		if status != "active" {
			failure = ErrInvalidCredentials
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1`, id, now); err != nil {
			return err
		}
		out, err = s.openSession(ctx, tx, t.ID, id, uuid.NewString(), c, now)
		out.MustChangePassword = mustChange
		return err
	})
	if err != nil {
		return Tokens{}, err
	}
	if failure != nil {
		return Tokens{}, failure
	}
	return out, nil
}

// Refresh exchanges a refresh token for a new access token and a new refresh token (rotation). Presenting a token
// that was already rotated revokes the whole family, which ends the session on every device that shared it.
func (s *Service) Refresh(ctx context.Context, refreshToken string, c Client) (Tokens, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Tokens{}, tenancy.ErrNoTenant
	}
	var (
		out     Tokens
		failure error
	)
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		var (
			sid, userID, family string
			expires             time.Time
			revoked             *time.Time
			status              string
		)
		err := tx.QueryRow(ctx, `
			SELECT s.id::text, s.user_id::text, s.family_id::text, s.expires_at, s.revoked_at, u.status
			FROM sessions s JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			WHERE s.token_hash = $1 FOR UPDATE OF s`, hashToken(refreshToken)).
			Scan(&sid, &userID, &family, &expires, &revoked, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrInvalidRefresh
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		if revoked != nil {
			// An already-rotated (or logged-out) token is being replayed: assume theft and end the family.
			if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, family, now); err != nil {
				return err
			}
			failure = ErrRefreshReuse
			return nil
		}
		if !expires.After(now) || status != "active" {
			failure = ErrInvalidRefresh
			return nil
		}
		next, err := s.openSession(ctx, tx, t.ID, userID, family, c, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2, replaced_by = (SELECT id FROM sessions WHERE token_hash = $3) WHERE id = $1`,
			sid, now, hashToken(next.RefreshToken)); err != nil {
			return err
		}
		out = next
		return nil
	})
	if err != nil {
		return Tokens{}, err
	}
	if failure != nil {
		return Tokens{}, failure
	}
	return out, nil
}

// Logout revokes the session family of the given refresh token. Unknown tokens are ignored so logout is idempotent.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE sessions SET revoked_at = $2
			WHERE revoked_at IS NULL AND family_id = (SELECT family_id FROM sessions WHERE token_hash = $1)`,
			hashToken(refreshToken), s.now())
		return err
	})
}

// openSession stores a new refresh session in family and returns the tokens for it.
func (s *Service) openSession(ctx context.Context, tx pgx.Tx, tenantID, userID, family string, c Client, now time.Time) (Tokens, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	sessionID := uuid.NewString()
	expires := now.Add(s.policy.RefreshTTL)
	_, err = tx.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, user_id, family_id, token_hash, client, client_version, ip, user_agent, created_at, last_used_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, NULLIF($9, ''), $10, $10, $11)`,
		sessionID, tenantID, userID, family, hash, clientName(c.Name), c.Version, parseIP(c.IP), c.UserAgent, now, expires)
	if err != nil {
		return Tokens{}, err
	}
	access, accessExp, err := s.signer.Issue(userID, tenantID, string(tenancy.TierTenant), sessionID)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: refresh, RefreshExpiresAt: expires, UserID: userID}, nil
}

func clientName(n string) string {
	switch n {
	case "ios", "android":
		return n
	}
	return "web"
}

// parseIP returns nil (stored as NULL) for anything that is not a valid address.
func parseIP(s string) *netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &a
}
