package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Two-step sign-in (FND-05).
//
// After a correct password, a user with two-step sign-in on gets a challenge (kind "verify") instead of tokens, and
// trades it with a code from their authenticator app, or a recovery code, for tokens. When the institute requires it
// for Admins and an Admin has not set it up, the challenge has kind "setup": the Admin scans a new secret and
// confirms a code before getting tokens, so an Admin can never skip it. A challenge lasts 10 minutes, works once and
// dies after 5 wrong codes; every wrong code also counts towards the account lock, like a wrong password.

const (
	challengeTTL      = 10 * time.Minute
	maxCodeAttempts   = 5
	recoveryCodeCount = 10
)

var (
	// ErrMFAChallenge is returned for a challenge that is unknown, expired, used up or of the wrong kind.
	ErrMFAChallenge = errors.New("identity: sign-in step expired")
	// ErrMFACode is returned for a wrong or reused code.
	ErrMFACode = errors.New("identity: wrong code")
	// ErrMFARequired is returned when a user tries to turn off two-step sign-in the institute requires of them.
	ErrMFARequired = errors.New("identity: two-step sign-in is required for this account")
	// ErrMFAAlreadyOn is returned when a user who has two-step sign-in starts setting it up again.
	ErrMFAAlreadyOn = errors.New("identity: two-step sign-in is already on")
	// ErrMFANotOn is returned when a user without two-step sign-in tries to turn it off.
	ErrMFANotOn = errors.New("identity: two-step sign-in is not on")
)

// MFAChallengeError is returned by Login when the password was right and a second step is needed.
type MFAChallengeError struct {
	Token     string
	Kind      string // "verify" or "setup"
	ExpiresAt time.Time
}

func (e *MFAChallengeError) Error() string {
	return "identity: second sign-in step needed (" + e.Kind + ")"
}

// MFASetup is what a user needs to add the account to an authenticator app.
type MFASetup struct {
	Secret string // base32, in groups of four for typing
	URI    string // otpauth:// link, for a QR code or a tap on the phone
}

// MFAStatus is a user's two-step sign-in state.
type MFAStatus struct {
	Enabled           bool
	Required          bool
	RecoveryCodesLeft int
}

// ConfigureMFA turns two-step sign-in on for this service. key is 32 bytes and lives outside the database; with
// requireForAdmins, holders of the Admin role must set it up on their next sign-in.
func (s *Service) ConfigureMFA(key []byte, requireForAdmins bool) error {
	sl, err := newSealer(key)
	if err != nil {
		return err
	}
	s.sealer, s.requireMFAForAdmins = sl, requireForAdmins
	return nil
}

func newChallengeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func isAdmin(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.tenant_id = ur.tenant_id AND r.id = ur.role_id
		WHERE ur.user_id = $1::uuid AND r.code = 'admin' AND r.deleted_at IS NULL AND ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now()))`, userID).Scan(&ok)
	return ok, err
}

// mfaStep decides, after a correct password, whether a second step is needed, and opens the challenge if so.
func (s *Service) mfaStep(ctx context.Context, tx pgx.Tx, tenantID, userID string, c Client, now time.Time) (*MFAChallengeError, error) {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT mfa_enabled FROM users WHERE id = $1::uuid`, userID).Scan(&enabled); err != nil {
		return nil, err
	}
	kind := ""
	switch {
	case enabled:
		kind = "verify"
	case s.requireMFAForAdmins:
		admin, err := isAdmin(ctx, tx, userID)
		if err != nil {
			return nil, err
		}
		if admin {
			kind = "setup"
		}
	}
	if kind == "" {
		return nil, nil
	}
	if s.sealer == nil {
		return nil, errNoMFAKey
	}
	token, err := newChallengeToken()
	if err != nil {
		return nil, err
	}
	expires := now.Add(challengeTTL)
	if _, err := tx.Exec(ctx, `INSERT INTO mfa_challenges (tenant_id, user_id, token_hash, kind, client, client_version, expires_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, $7)`, tenantID, userID, hashToken(token), kind, clientName(c.Name), nullIfEmpty(c.Version), expires); err != nil {
		return nil, err
	}
	return &MFAChallengeError{Token: token, Kind: kind, ExpiresAt: expires}, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// challenge is one row of mfa_challenges, locked for the rest of the transaction.
type challenge struct {
	id, userID, kind, client string
	version                  *string
	attempts                 int
	expires                  time.Time
	used                     *time.Time
	pending                  []byte
}

func (ch challenge) usable(kind string, now time.Time) bool {
	return ch.kind == kind && ch.used == nil && ch.expires.After(now) && ch.attempts < maxCodeAttempts
}

func loadChallenge(ctx context.Context, tx pgx.Tx, where string, arg any) (challenge, error) {
	var ch challenge
	err := tx.QueryRow(ctx, `SELECT id::text, user_id::text, kind, client, client_version, attempts, expires_at, used_at, pending_secret_enc
		FROM mfa_challenges WHERE `+where+` FOR UPDATE`, arg).
		Scan(&ch.id, &ch.userID, &ch.kind, &ch.client, &ch.version, &ch.attempts, &ch.expires, &ch.used, &ch.pending)
	return ch, err
}

// mfaUser is the part of a user row the second step needs, locked for the rest of the transaction.
type mfaUser struct {
	status, username string
	enabled          bool
	secret           []byte
	lastStep         *int64
	failed           int
	lockedUntil      *time.Time
	mustChange       bool
}

func loadMFAUser(ctx context.Context, tx pgx.Tx, id string) (mfaUser, error) {
	var u mfaUser
	err := tx.QueryRow(ctx, `SELECT status, username::text, mfa_enabled, mfa_secret_enc, mfa_last_step, failed_login_count, locked_until, must_change_password
		FROM users WHERE id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&u.status, &u.username, &u.enabled, &u.secret, &u.lastStep, &u.failed, &u.lockedUntil, &u.mustChange)
	return u, err
}

// wrongCode counts a wrong code against the challenge (if any) and the account, locking it like wrong passwords do.
func (s *Service) wrongCode(ctx context.Context, tx pgx.Tx, challengeID, userID string, attempts, failed int, now time.Time) error {
	if challengeID != "" {
		attempts++
		if _, err := tx.Exec(ctx, `UPDATE mfa_challenges SET attempts = $2::int, used_at = CASE WHEN $2::int >= $3::int THEN now() END WHERE id = $1::uuid`, challengeID, attempts, maxCodeAttempts); err != nil {
			return err
		}
	}
	failed++
	if failed >= s.policy.MaxFailedLogins {
		_, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = 0, locked_until = $2 WHERE id = $1::uuid`, userID, now.Add(s.policy.LockDuration))
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = $2 WHERE id = $1::uuid`, userID, failed)
	return err
}

// checkCode accepts a current authenticator code (returning its step) or an unused recovery code (using it up).
func checkCode(ctx context.Context, tx pgx.Tx, secret []byte, lastStep *int64, userID, code string, now time.Time) (step int64, ok bool, err error) {
	var last int64
	if lastStep != nil {
		last = *lastStep
	}
	if step, ok := verifyTOTP(secret, code, now, last); ok {
		return step, true, nil
	}
	norm := normaliseRecovery(code)
	if len(norm) != 10 {
		return 0, false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE mfa_recovery_codes SET used_at = now() WHERE user_id = $1::uuid AND code_hash = $2 AND used_at IS NULL`, userID, hashToken(norm))
	if err != nil {
		return 0, false, err
	}
	return last, tag.RowsAffected() == 1, nil
}

func normaliseRecovery(code string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(code)))
}

// newRecoveryCodes replaces the user's recovery codes and returns them, formatted "abcde-fghij", to show once.
func newRecoveryCodes(ctx context.Context, tx pgx.Tx, tenantID, userID string) ([]string, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1::uuid`, userID); err != nil {
		return nil, err
	}
	enc := base32Lower()
	out := make([]string, recoveryCodeCount)
	for i := range out {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		raw := enc.EncodeToString(b)[:10]
		out[i] = raw[:5] + "-" + raw[5:]
		if _, err := tx.Exec(ctx, `INSERT INTO mfa_recovery_codes (tenant_id, user_id, code_hash) VALUES ($1, $2::uuid, $3)`, tenantID, userID, hashToken(raw)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func base32Lower() interface{ EncodeToString([]byte) string } {
	return lowerEncoding{}
}

type lowerEncoding struct{}

func (lowerEncoding) EncodeToString(b []byte) string { return strings.ToLower(b32.EncodeToString(b)) }

func grouped(secret []byte) string {
	s := b32.EncodeToString(secret)
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:min(i+4, len(s))])
	}
	return strings.Join(parts, " ")
}

// VerifyMFA completes a sign-in that needed a second step.
func (s *Service) VerifyMFA(ctx context.Context, token, code string, c Client) (Tokens, error) {
	return s.secondStep(ctx, token, c, func(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, ch challenge, u mfaUser, now time.Time) (bool, error) {
		if ch.kind != "verify" || !u.enabled || s.sealer == nil {
			return false, ErrMFAChallenge
		}
		secret, err := s.sealer.open(u.secret, t.ID, ch.userID)
		if err != nil {
			return false, err
		}
		step, ok, err := checkCode(ctx, tx, secret, u.lastStep, ch.userID, code, now)
		if err != nil || !ok {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE users SET mfa_last_step = $2 WHERE id = $1::uuid`, ch.userID, step)
		return true, err
	})
}

// StartMFASetup gives an Admin who must set up two-step sign-in a new secret for their app. Calling it again
// replaces the secret, which is how "show me a new code" works.
func (s *Service) StartMFASetup(ctx context.Context, token string) (MFASetup, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return MFASetup{}, tenancy.ErrNoTenant
	}
	if s.sealer == nil {
		return MFASetup{}, errNoMFAKey
	}
	var out MFASetup
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		ch, err := loadChallenge(ctx, tx, `token_hash = $1`, hashToken(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMFAChallenge
		}
		if err != nil {
			return err
		}
		if !ch.usable("setup", s.now()) {
			return ErrMFAChallenge
		}
		u, err := loadMFAUser(ctx, tx, ch.userID)
		if err != nil || u.status != "active" {
			return ErrMFAChallenge
		}
		out, err = s.pendingSecret(ctx, tx, t, ch.id, ch.userID, u.username)
		return err
	})
	return out, err
}

func (s *Service) pendingSecret(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, challengeID, userID, username string) (MFASetup, error) {
	secret, err := newTOTPSecret()
	if err != nil {
		return MFASetup{}, err
	}
	sealed, err := s.sealer.seal(secret, t.ID, userID)
	if err != nil {
		return MFASetup{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE mfa_challenges SET pending_secret_enc = $2 WHERE id = $1::uuid`, challengeID, sealed); err != nil {
		return MFASetup{}, err
	}
	issuer := "Shikshit"
	if t.Name != "" {
		issuer = "Shikshit · " + t.Name
	}
	return MFASetup{Secret: grouped(secret), URI: otpauthURI(issuer, username, secret)}, nil
}

// ConfirmMFASetup checks the first code from the new secret, turns two-step sign-in on, completes the sign-in and
// returns the recovery codes to show once.
func (s *Service) ConfirmMFASetup(ctx context.Context, a audit.Actor, token, code string, c Client) (Tokens, []string, error) {
	var codes []string
	tokens, err := s.secondStep(ctx, token, c, func(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, ch challenge, u mfaUser, now time.Time) (bool, error) {
		if ch.kind != "setup" || ch.pending == nil || s.sealer == nil {
			return false, ErrMFAChallenge
		}
		ok, err := s.enable(ctx, tx, t, ch, code, now)
		if err != nil || !ok {
			return false, err
		}
		if codes, err = newRecoveryCodes(ctx, tx, t.ID, ch.userID); err != nil {
			return false, err
		}
		a.UserID = ch.userID
		return true, audit.Record(ctx, tx, a, audit.Entry{Action: "auth.mfa_enabled", ResourceType: "user", ResourceID: ch.userID})
	})
	return tokens, codes, err
}

// enable checks code against the challenge's pending secret and, if it matches, makes it the user's secret.
func (s *Service) enable(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, ch challenge, code string, now time.Time) (bool, error) {
	secret, err := s.sealer.open(ch.pending, t.ID, ch.userID)
	if err != nil {
		return false, err
	}
	step, ok := verifyTOTP(secret, code, now, 0)
	if !ok {
		return false, nil
	}
	_, err = tx.Exec(ctx, `UPDATE users SET mfa_enabled = true, mfa_secret_enc = $2, mfa_last_step = $3 WHERE id = $1::uuid`, ch.userID, ch.pending, step)
	return err == nil, err
}

type stepFunc func(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, ch challenge, u mfaUser, now time.Time) (bool, error)

// secondStep is the shared part of VerifyMFA and ConfirmMFASetup: load and lock the challenge and the user, run the
// check, count a wrong code, or finish the sign-in. Wrong codes must be saved even though the call fails, so the
// failure is carried out of the transaction as a value and the transaction commits.
func (s *Service) secondStep(ctx context.Context, token string, c Client, check stepFunc) (Tokens, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Tokens{}, tenancy.ErrNoTenant
	}
	var (
		out     Tokens
		failure error
	)
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		ch, err := loadChallenge(ctx, tx, `token_hash = $1`, hashToken(token))
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrMFAChallenge
			return nil
		}
		if err != nil {
			return err
		}
		if ch.used != nil || !ch.expires.After(now) || ch.attempts >= maxCodeAttempts || ch.kind == "enroll" {
			failure = ErrMFAChallenge
			return nil
		}
		u, err := loadMFAUser(ctx, tx, ch.userID)
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
		passed, err := check(ctx, tx, t, ch, u, now)
		if errors.Is(err, ErrMFAChallenge) {
			failure = err
			return nil
		}
		if err != nil {
			return err
		}
		if !passed {
			failure = ErrMFACode
			return s.wrongCode(ctx, tx, ch.id, ch.userID, ch.attempts, u.failed, now)
		}
		if _, err := tx.Exec(ctx, `UPDATE mfa_challenges SET used_at = now() WHERE id = $1::uuid`, ch.id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1::uuid`, ch.userID, now); err != nil {
			return err
		}
		client := Client{Name: ch.client, IP: c.IP, UserAgent: c.UserAgent}
		if ch.version != nil {
			client.Version = *ch.version
		}
		out, err = s.openSession(ctx, tx, t.ID, ch.userID, uuid.NewString(), client, now)
		out.MustChangePassword = u.mustChange
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

// ---- signed-in users managing their own two-step sign-in

func (s *Service) required(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	if !s.requireMFAForAdmins {
		return false, nil
	}
	return isAdmin(ctx, tx, userID)
}

// MFAStatusFor reports a signed-in user's two-step sign-in state.
func (s *Service) MFAStatusFor(ctx context.Context, userID string) (MFAStatus, error) {
	var st MFAStatus
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT mfa_enabled, (SELECT count(*) FROM mfa_recovery_codes WHERE user_id = $1::uuid AND used_at IS NULL)
			FROM users WHERE id = $1::uuid`, userID).Scan(&st.Enabled, &st.RecoveryCodesLeft); err != nil {
			return err
		}
		var err error
		st.Required, err = s.required(ctx, tx, userID)
		return err
	})
	return st, err
}

// BeginMFAEnrollment gives a signed-in user without two-step sign-in a secret to add to their app.
func (s *Service) BeginMFAEnrollment(ctx context.Context, userID string) (MFASetup, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return MFASetup{}, tenancy.ErrNoTenant
	}
	if s.sealer == nil {
		return MFASetup{}, errNoMFAKey
	}
	var out MFASetup
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		u, err := loadMFAUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.enabled {
			return ErrMFAAlreadyOn
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1::uuid AND kind = 'enroll'`, userID); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO mfa_challenges (tenant_id, user_id, kind, expires_at) VALUES ($1, $2::uuid, 'enroll', $3) RETURNING id::text`,
			t.ID, userID, s.now().Add(challengeTTL)).Scan(&id); err != nil {
			return err
		}
		out, err = s.pendingSecret(ctx, tx, t, id, userID, u.username)
		return err
	})
	return out, err
}

// ConfirmMFAEnrollment checks the first code, turns two-step sign-in on and returns the recovery codes. A wrong code
// counts towards the account lock.
func (s *Service) ConfirmMFAEnrollment(ctx context.Context, a audit.Actor, userID, code string) ([]string, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return nil, tenancy.ErrNoTenant
	}
	if s.sealer == nil {
		return nil, errNoMFAKey
	}
	var (
		codes   []string
		failure error
	)
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		ch, err := loadChallenge(ctx, tx, `user_id = $1::uuid AND kind = 'enroll' AND used_at IS NULL ORDER BY created_at DESC LIMIT 1`, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrMFAChallenge
			return nil
		}
		if err != nil {
			return err
		}
		u, err := loadMFAUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if !ch.usable("enroll", now) || ch.pending == nil || u.enabled {
			failure = ErrMFAChallenge
			return nil
		}
		passed, err := s.enable(ctx, tx, t, ch, code, now)
		if err != nil {
			return err
		}
		if !passed {
			failure = ErrMFACode
			return s.wrongCode(ctx, tx, ch.id, userID, ch.attempts, u.failed, now)
		}
		if _, err := tx.Exec(ctx, `UPDATE mfa_challenges SET used_at = now() WHERE id = $1::uuid`, ch.id); err != nil {
			return err
		}
		if codes, err = newRecoveryCodes(ctx, tx, t.ID, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "auth.mfa_enabled", ResourceType: "user", ResourceID: userID})
	})
	if err != nil {
		return nil, err
	}
	return codes, failure
}

// DisableMFA turns two-step sign-in off after a valid code. Refused when the institute requires it of the user.
func (s *Service) DisableMFA(ctx context.Context, a audit.Actor, userID, code string) error {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return tenancy.ErrNoTenant
	}
	if s.sealer == nil {
		return errNoMFAKey
	}
	var failure error
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		now := s.now()
		u, err := loadMFAUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if !u.enabled {
			failure = ErrMFANotOn
			return nil
		}
		if req, err := s.required(ctx, tx, userID); err != nil {
			return err
		} else if req {
			failure = ErrMFARequired
			return nil
		}
		secret, err := s.sealer.open(u.secret, t.ID, userID)
		if err != nil {
			return err
		}
		_, ok, err := checkCode(ctx, tx, secret, u.lastStep, userID, code, now)
		if err != nil {
			return err
		}
		if !ok {
			failure = ErrMFACode
			return s.wrongCode(ctx, tx, "", userID, 0, u.failed, now)
		}
		if err := ClearMFA(ctx, tx, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "auth.mfa_disabled", ResourceType: "user", ResourceID: userID})
	})
	if err != nil {
		return err
	}
	return failure
}

// ClearMFA removes a user's two-step sign-in (secret, recovery codes, open challenges). Used when they turn it off
// and when an admin resets it for someone who lost their phone (IAM-01).
func ClearMFA(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx, `UPDATE users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_last_step = NULL WHERE id = $1::uuid`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM mfa_recovery_codes WHERE user_id = $1::uuid`, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM mfa_challenges WHERE user_id = $1::uuid`, userID)
	return err
}
