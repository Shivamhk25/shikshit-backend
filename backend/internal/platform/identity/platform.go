package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Sign-in for the platform (Super Admin) tier, decision 0017.
//
// Platform users live outside every institute and can create and change institutes, so the rules are stricter than
// for institute users:
//   - two-step sign-in is always required: a correct password never returns tokens, only a challenge, either to
//     verify a code or (first time) to set up an authenticator app;
//   - there are no recovery codes: a lost phone is reset by an operator on the server (ResetPlatformMFA);
//   - wrong passwords and wrong codes count towards the same lock;
//   - access tokens carry tier "platform" and no institute, so they are refused by every institute endpoint, and
//     institute tokens are refused here.
//
// Every query runs in a platform-tier transaction: the platform tables are visible to nothing else.

// platformAAD binds platform TOTP secrets to the platform realm when sealing, so a secret copied into an institute
// user's row (or the other way round) does not decrypt.
const platformAAD = "platform"

// PlatformChallenge is what a correct platform password returns: kind "verify" or "setup".
type PlatformChallenge struct {
	Token     string
	Kind      string
	ExpiresAt time.Time
}

// PlatformPrincipal is an authenticated platform user.
type PlatformPrincipal struct {
	UserID, SessionID, Email, Name string
}

// PlatformService signs platform users in.
type PlatformService struct {
	db        tenancy.Beginner
	signer    *Signer
	policy    Policy
	sealer    *sealer
	now       func() time.Time
	dummyHash string
}

// NewPlatformService returns a PlatformService. mfaKey is the same 32-byte key institute two-step sign-in uses.
func NewPlatformService(db tenancy.Beginner, signer *Signer, policy Policy, mfaKey []byte) (*PlatformService, error) {
	sl, err := newSealer(mfaKey)
	if err != nil {
		return nil, err
	}
	dummy, err := HashPassword("not-a-real-password")
	if err != nil {
		return nil, err
	}
	return &PlatformService{db: db, signer: signer, policy: policy, sealer: sl, now: time.Now, dummyHash: dummy}, nil
}

func (s *PlatformService) tx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return tenancy.WithTx(ctx, s.db, "", tenancy.TierPlatform, fn)
}

// Login checks an email and password. On success it returns a challenge, never tokens.
func (s *PlatformService) Login(ctx context.Context, email, password string, c Client) (PlatformChallenge, error) {
	var (
		out     PlatformChallenge
		failure error
	)
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var (
			id, status, hash string
			failed           int
			lockedUntil      *time.Time
			enabled          bool
		)
		err := tx.QueryRow(ctx, `SELECT id::text, status, COALESCE(password_hash, ''), failed_login_count, locked_until, mfa_enabled
			FROM platform_users WHERE email = $1::citext FOR UPDATE`, strings.TrimSpace(email)).
			Scan(&id, &status, &hash, &failed, &lockedUntil, &enabled)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err != nil || hash == "" {
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
			failure = ErrInvalidCredentials
			return s.countFailure(ctx, tx, id, failed, now)
		}
		if lockedUntil != nil && lockedUntil.After(now) {
			failure = ErrLocked
			return nil
		}
		if status != "active" {
			failure = ErrInvalidCredentials
			return nil
		}
		token, err := newChallengeToken()
		if err != nil {
			return err
		}
		kind := "verify"
		if !enabled {
			kind = "setup"
		}
		out = PlatformChallenge{Token: token, Kind: kind, ExpiresAt: now.Add(challengeTTL)}
		_, err = tx.Exec(ctx, `INSERT INTO platform_mfa_challenges (platform_user_id, token_hash, kind, client, client_version, expires_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)`, id, hashToken(token), kind, clientName(c.Name), nullIfEmpty(c.Version), out.ExpiresAt)
		return err
	})
	if err != nil {
		return PlatformChallenge{}, err
	}
	if failure != nil {
		return PlatformChallenge{}, failure
	}
	return out, nil
}

func (s *PlatformService) countFailure(ctx context.Context, tx pgx.Tx, userID string, failed int, now time.Time) error {
	failed++
	if failed >= s.policy.MaxFailedLogins {
		_, err := tx.Exec(ctx, `UPDATE platform_users SET failed_login_count = 0, locked_until = $2 WHERE id = $1::uuid`, userID, now.Add(s.policy.LockDuration))
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE platform_users SET failed_login_count = $2 WHERE id = $1::uuid`, userID, failed)
	return err
}

type platformChallengeRow struct {
	id, userID, kind, client string
	version                  *string
	attempts                 int
	expires                  time.Time
	used                     *time.Time
	pending                  []byte
}

type platformUserRow struct {
	email, status string
	enabled       bool
	secret        []byte
	lastStep      *int64
	failed        int
	lockedUntil   *time.Time
}

// step loads and checks the challenge and user, runs check, and on success opens a session. A wrong code counts
// against the challenge and the account.
func (s *PlatformService) step(ctx context.Context, token, kind string, c Client, check func(ctx context.Context, tx pgx.Tx, ch platformChallengeRow, u platformUserRow, now time.Time) (bool, error)) (Tokens, error) {
	var (
		out     Tokens
		failure error
	)
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		var ch platformChallengeRow
		err := tx.QueryRow(ctx, `SELECT id::text, platform_user_id::text, kind, client, client_version, attempts, expires_at, used_at, pending_secret_enc
			FROM platform_mfa_challenges WHERE token_hash = $1 FOR UPDATE`, hashToken(token)).
			Scan(&ch.id, &ch.userID, &ch.kind, &ch.client, &ch.version, &ch.attempts, &ch.expires, &ch.used, &ch.pending)
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrMFAChallenge
			return nil
		}
		if err != nil {
			return err
		}
		if ch.kind != kind || ch.used != nil || !ch.expires.After(now) || ch.attempts >= maxCodeAttempts {
			failure = ErrMFAChallenge
			return nil
		}
		var u platformUserRow
		err = tx.QueryRow(ctx, `SELECT email::text, status, mfa_enabled, mfa_secret_enc, mfa_last_step, failed_login_count, locked_until
			FROM platform_users WHERE id = $1::uuid FOR UPDATE`, ch.userID).
			Scan(&u.email, &u.status, &u.enabled, &u.secret, &u.lastStep, &u.failed, &u.lockedUntil)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && u.status != "active") {
			failure = ErrMFAChallenge
			return nil
		}
		if err != nil {
			return err
		}
		if u.lockedUntil != nil && u.lockedUntil.After(now) {
			failure = ErrLocked
			return nil
		}
		passed, err := check(ctx, tx, ch, u, now)
		if errors.Is(err, ErrMFAChallenge) {
			failure = err
			return nil
		}
		if err != nil {
			return err
		}
		if !passed {
			failure = ErrMFACode
			attempts := ch.attempts + 1
			if _, err := tx.Exec(ctx, `UPDATE platform_mfa_challenges SET attempts = $2::int, used_at = CASE WHEN $2::int >= $3::int THEN now() END WHERE id = $1::uuid`,
				ch.id, attempts, maxCodeAttempts); err != nil {
				return err
			}
			return s.countFailure(ctx, tx, ch.userID, u.failed, now)
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_mfa_challenges SET used_at = now() WHERE id = $1::uuid`, ch.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_users SET failed_login_count = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1::uuid`, ch.userID, now); err != nil {
			return err
		}
		client := Client{Name: ch.client, IP: c.IP, UserAgent: c.UserAgent}
		if ch.version != nil {
			client.Version = *ch.version
		}
		out, err = s.openSession(ctx, tx, ch.userID, uuid.NewString(), client, now)
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

// Verify completes a sign-in with a code from the authenticator app. A code is accepted once: a code for the same or
// an earlier time step is refused, so a code seen over someone's shoulder cannot be replayed.
func (s *PlatformService) Verify(ctx context.Context, token, code string, c Client) (Tokens, error) {
	return s.step(ctx, token, "verify", c, func(ctx context.Context, tx pgx.Tx, ch platformChallengeRow, u platformUserRow, now time.Time) (bool, error) {
		if !u.enabled || len(u.secret) == 0 {
			return false, ErrMFAChallenge
		}
		secret, err := s.sealer.open(u.secret, platformAAD, ch.userID)
		if err != nil {
			return false, err
		}
		var last int64
		if u.lastStep != nil {
			last = *u.lastStep
		}
		step, ok := verifyTOTP(secret, code, now, last)
		if !ok {
			return false, nil
		}
		_, err = tx.Exec(ctx, `UPDATE platform_users SET mfa_last_step = $2 WHERE id = $1::uuid`, ch.userID, step)
		return err == nil, err
	})
}

// StartSetup returns a new authenticator secret for a "setup" challenge. Calling it again replaces the secret.
func (s *PlatformService) StartSetup(ctx context.Context, token string) (MFASetup, error) {
	var out MFASetup
	var failure error
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var id, userID, email string
		err := tx.QueryRow(ctx, `SELECT c.id::text, u.id::text, u.email::text FROM platform_mfa_challenges c JOIN platform_users u ON u.id = c.platform_user_id
			WHERE c.token_hash = $1 AND c.kind = 'setup' AND c.used_at IS NULL AND c.expires_at > $2 AND c.attempts < $3 AND u.status = 'active'
			FOR UPDATE OF c`, hashToken(token), s.now(), maxCodeAttempts).Scan(&id, &userID, &email)
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrMFAChallenge
			return nil
		}
		if err != nil {
			return err
		}
		secret, err := newTOTPSecret()
		if err != nil {
			return err
		}
		sealed, err := s.sealer.seal(secret, platformAAD, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_mfa_challenges SET pending_secret_enc = $2 WHERE id = $1::uuid`, id, sealed); err != nil {
			return err
		}
		out = MFASetup{Secret: grouped(secret), URI: otpauthURI("Shikshit platform", email, secret)}
		return nil
	})
	if err != nil {
		return MFASetup{}, err
	}
	return out, failure
}

// ConfirmSetup checks the first code from the new secret, turns two-step sign-in on and completes the sign-in.
func (s *PlatformService) ConfirmSetup(ctx context.Context, token, code string, c Client) (Tokens, error) {
	return s.step(ctx, token, "setup", c, func(ctx context.Context, tx pgx.Tx, ch platformChallengeRow, _ platformUserRow, now time.Time) (bool, error) {
		if len(ch.pending) == 0 {
			return false, ErrMFAChallenge
		}
		secret, err := s.sealer.open(ch.pending, platformAAD, ch.userID)
		if err != nil {
			return false, err
		}
		step, ok := verifyTOTP(secret, code, now, 0)
		if !ok {
			return false, nil
		}
		_, err = tx.Exec(ctx, `UPDATE platform_users SET mfa_enabled = true, mfa_secret_enc = $2, mfa_last_step = $3 WHERE id = $1::uuid`, ch.userID, ch.pending, step)
		return err == nil, err
	})
}

func (s *PlatformService) openSession(ctx context.Context, tx pgx.Tx, userID, family string, c Client, now time.Time) (Tokens, error) {
	refresh, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	sessionID := uuid.NewString()
	expires := now.Add(s.policy.RefreshTTL)
	if _, err := tx.Exec(ctx, `
		INSERT INTO sessions (id, platform_user_id, family_id, token_hash, client, client_version, ip, user_agent, created_at, last_used_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, NULLIF($8, ''), $9, $9, $10)`,
		sessionID, userID, family, hash, clientName(c.Name), c.Version, parseIP(c.IP), c.UserAgent, now, expires); err != nil {
		return Tokens{}, err
	}
	access, accessExp, err := s.signer.Issue(userID, "", string(tenancy.TierPlatform), sessionID)
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: refresh, RefreshExpiresAt: expires, UserID: userID}, nil
}

// Refresh rotates a platform refresh token; a replayed token ends the whole family.
func (s *PlatformService) Refresh(ctx context.Context, refreshToken string, c Client) (Tokens, error) {
	var (
		out     Tokens
		failure error
	)
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var (
			sid, userID, family, status string
			expires                     time.Time
			revoked                     *time.Time
		)
		err := tx.QueryRow(ctx, `SELECT s.id::text, s.platform_user_id::text, s.family_id::text, s.expires_at, s.revoked_at, u.status
			FROM sessions s JOIN platform_users u ON u.id = s.platform_user_id
			WHERE s.token_hash = $1 AND s.tenant_id IS NULL FOR UPDATE OF s`, hashToken(refreshToken)).
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
		next, err := s.openSession(ctx, tx, userID, family, c, now)
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

// Logout ends the session family of a platform refresh token. Unknown tokens are ignored.
func (s *PlatformService) Logout(ctx context.Context, refreshToken string) error {
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2
			WHERE revoked_at IS NULL AND tenant_id IS NULL AND family_id = (SELECT family_id FROM sessions WHERE token_hash = $1 AND tenant_id IS NULL)`,
			hashToken(refreshToken), s.now())
		return err
	})
}

// Authenticate accepts only platform access tokens whose session family is live and whose user is active.
func (s *PlatformService) Authenticate(ctx context.Context, token string) (PlatformPrincipal, error) {
	claims, err := s.signer.Verify(token)
	if err != nil || claims.Tier != string(tenancy.TierPlatform) || claims.TenantID != "" {
		return PlatformPrincipal{}, ErrUnauthenticated
	}
	p := PlatformPrincipal{UserID: claims.Subject, SessionID: claims.SessionID}
	var live bool
	err = s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.email::text, u.display_name,
			       EXISTS (SELECT 1 FROM sessions f WHERE f.family_id = s.family_id AND f.revoked_at IS NULL AND f.expires_at > now())
			FROM sessions s JOIN platform_users u ON u.id = s.platform_user_id
			WHERE s.id::text = $1 AND s.platform_user_id::text = $2 AND s.tenant_id IS NULL AND u.status = 'active'`,
			claims.SessionID, claims.Subject).Scan(&p.Email, &p.Name, &live)
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !live) {
		return PlatformPrincipal{}, ErrUnauthenticated
	}
	if err != nil {
		return PlatformPrincipal{}, err
	}
	return p, nil
}

// CreatePlatformUser adds a platform user (run by an operator on the server, cmd/platform-user). They set up their
// authenticator app at their first sign-in.
func CreatePlatformUser(ctx context.Context, db tenancy.Beginner, email, name, password string) (string, error) {
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	if email == "" || name == "" || !strings.Contains(email, "@") {
		return "", errors.New("identity: a platform user needs an email and a name")
	}
	if err := CheckPassword(password, email); err != nil {
		return "", err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	var id string
	err = tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform_users (email, display_name, password_hash) VALUES ($1::citext, $2, $3) RETURNING id::text`,
			email, name, hash).Scan(&id)
	})
	return id, err
}

// ResetPlatformMFA clears a platform user's authenticator and signs them out everywhere; they set it up again at
// their next sign-in. Run by an operator on the server when someone loses their phone.
func ResetPlatformMFA(ctx context.Context, db tenancy.Beginner, email string) error {
	return tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `UPDATE platform_users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_last_step = NULL
			WHERE email = $1::citext RETURNING id::text`, strings.TrimSpace(email)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("identity: no platform user with that email")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform_mfa_challenges WHERE platform_user_id = $1::uuid`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE platform_user_id = $1::uuid AND revoked_at IS NULL`, id)
		return err
	})
}
