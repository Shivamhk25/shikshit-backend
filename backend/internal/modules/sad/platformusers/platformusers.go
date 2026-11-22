// Package platformusers implements SAD-04: the Shikshit team's own accounts, managed from the console (decision 0019).
//
// Rules enforced here (each has a test):
//   - a new user is "invited" and signs in only after setting a password from a one-time link (7 days); a reset link
//     lasts 24 hours; only SHA-256 of a link's token is stored and a link works once;
//   - emails are unique (case-insensitive);
//   - nobody can disable themselves or reset their own authenticator, and the last active platform user cannot be
//     disabled (an advisory lock makes the check safe against two operators at once);
//   - disabling, resetting the password or resetting the authenticator ends every session of that user;
//   - two-step sign-in is always required (decision 0017), so there is no switch to turn it off.
package platformusers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var (
	// ErrNotFound is returned for an unknown platform user.
	ErrNotFound = errors.New("platformusers: not found")
	// ErrSelf is returned when someone tries to disable or reset themselves.
	ErrSelf = errors.New("You cannot do this to your own account.")
	// ErrLastActive is returned when a change would leave no active platform user.
	ErrLastActive = errors.New("This is the last active platform user. Add and activate someone else first.")
	// ErrInvalidState is returned when the action does not apply to the user's status.
	ErrInvalidState = errors.New("This action does not apply to the user's current status.")
	// ErrInvalidToken is returned for a bad, used or expired link (all the same, so the answer reveals nothing).
	ErrInvalidToken = errors.New("This link is not valid any more. Ask a colleague for a new one.")
)

// Link lifetimes.
const (
	InviteTTL = 7 * 24 * time.Hour
	ResetTTL  = 24 * time.Hour
)

// AcceptPath is the console page that receives a set-password link.
const AcceptPath = "/accept-invite"

// User is a platform user as the console shows it.
type User struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	MFAEnabled  bool       `json:"mfaEnabled"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// WithLink is a user plus a one-time set-password link (shown once).
type WithLink struct {
	User
	Link string `json:"link"`
}

// Service manages platform users.
type Service struct {
	DB  tenancy.Beginner
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) tx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return tenancy.WithTx(ctx, s.DB, "", tenancy.TierPlatform, fn)
}

const columns = `id::text, email::text, display_name, status, mfa_enabled, last_login_at, created_at`

func scan(r interface{ Scan(...any) error }, u *User) error {
	return r.Scan(&u.ID, &u.Email, &u.Name, &u.Status, &u.MFAEnabled, &u.LastLoginAt, &u.CreatedAt)
}

func get(ctx context.Context, tx pgx.Tx, id string, lock bool) (User, error) {
	var u User
	if _, err := uuid.Parse(id); err != nil {
		return u, ErrNotFound
	}
	sql := `SELECT ` + columns + ` FROM platform_users WHERE id = $1::uuid`
	if lock {
		sql += ` FOR UPDATE`
	}
	err := scan(tx.QueryRow(ctx, sql, id), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// List returns every platform user, active ones first.
func (s Service) List(ctx context.Context) ([]User, error) {
	out := []User{}
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+columns+` FROM platform_users
			ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'invited' THEN 1 ELSE 2 END, lower(display_name), id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u User
			if err := scan(rows, &u); err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

func (s Service) issue(ctx context.Context, tx pgx.Tx, userID, purpose, by string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	ttl := InviteTTL
	if purpose == "reset" {
		ttl = ResetTTL
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_user_invites SET used_at = now() WHERE platform_user_id = $1::uuid AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform_user_invites (platform_user_id, token_hash, purpose, expires_at, created_by) VALUES ($1::uuid, $2, $3, $4, NULLIF($5, '')::uuid)`,
		userID, hashToken(token), purpose, s.now().Add(ttl), by); err != nil {
		return "", err
	}
	return AcceptPath + "?token=" + token, nil
}

// Create invites a new platform user and returns their one-time link (7 days).
func (s Service) Create(ctx context.Context, a audit.Actor, email, name string) (WithLink, error) {
	var errs crud.FieldErrors
	email, name = strings.TrimSpace(email), strings.TrimSpace(name)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || len(email) > 254 {
		errs.Add("email", "invalid_email", "Enter a valid email address.")
	}
	errs.Required("name", "a name", name, 200)
	if err := errs.OrNil(); err != nil {
		return WithLink{}, err
	}
	var out WithLink
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform_users (email, display_name, status) VALUES ($1::citext, $2, 'invited') RETURNING id::text`, email, name).Scan(&id); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return crud.FieldErrors{{Field: "email", Code: "duplicate", Message: "Another platform user already has this email."}}
			}
			return err
		}
		link, err := s.issue(ctx, tx, id, "invite", a.UserID)
		if err != nil {
			return err
		}
		u, err := get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out = WithLink{User: u, Link: link}
		return audit.RecordPlatform(ctx, tx, a, "", audit.Entry{Action: "platform_users.create", ResourceType: "platform_user", ResourceID: id, After: u})
	})
	return out, err
}

// lockTeam serialises changes that could leave nobody active.
func lockTeam(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('platform_users.active'))`)
	return err
}

func endSessions(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE platform_user_id = $1::uuid AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM platform_mfa_challenges WHERE platform_user_id = $1::uuid`, id)
	return err
}

// Disable stops a platform user signing in and ends their sessions.
func (s Service) Disable(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "platform_users.disable", func(ctx context.Context, tx pgx.Tx, u User) error {
		if u.Status == "disabled" {
			return ErrInvalidState
		}
		if u.Status == "active" {
			var others int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform_users WHERE status = 'active' AND id <> $1::uuid`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastActive
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_users SET status = 'disabled' WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_user_invites SET used_at = now() WHERE platform_user_id = $1::uuid AND used_at IS NULL`, id); err != nil {
			return err
		}
		return endSessions(ctx, tx, id)
	})
}

// Enable lets a disabled user back: active if they have a password, otherwise invited (they need a new link).
func (s Service) Enable(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "platform_users.enable", func(ctx context.Context, tx pgx.Tx, u User) error {
		if u.Status != "disabled" {
			return ErrInvalidState
		}
		_, err := tx.Exec(ctx, `UPDATE platform_users SET status = CASE WHEN password_hash IS NULL THEN 'invited' ELSE 'active' END,
			failed_login_count = 0, locked_until = NULL WHERE id = $1::uuid`, id)
		return err
	})
}

// ResetMFA clears a user's authenticator (they set it up again at their next sign-in) and ends their sessions.
func (s Service) ResetMFA(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "platform_users.reset_mfa", func(ctx context.Context, tx pgx.Tx, u User) error {
		if u.Status == "disabled" {
			return ErrInvalidState
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_users SET mfa_enabled = false, mfa_secret_enc = NULL, mfa_last_step = NULL WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		return endSessions(ctx, tx, id)
	})
}

// ResetPassword issues a new set-password link (24 hours; a new invite link, 7 days, for someone still invited) and
// ends the user's sessions.
func (s Service) ResetPassword(ctx context.Context, a audit.Actor, id string) (WithLink, error) {
	var link string
	u, err := s.change(ctx, a, id, "platform_users.reset_password", func(ctx context.Context, tx pgx.Tx, u User) error {
		if u.Status == "disabled" {
			return ErrInvalidState
		}
		purpose := "reset"
		if u.Status == "invited" {
			purpose = "invite"
		}
		var err error
		if link, err = s.issue(ctx, tx, id, purpose, a.UserID); err != nil {
			return err
		}
		return endSessions(ctx, tx, id)
	})
	if err != nil {
		return WithLink{}, err
	}
	return WithLink{User: u, Link: link}, nil
}

// change runs fn for a locked user (never the caller) and records the change. The link of ResetPassword never
// reaches the audit trail: it would let anyone reading the log take the account.
func (s Service) change(ctx context.Context, a audit.Actor, id, action string, fn func(ctx context.Context, tx pgx.Tx, u User) error) (User, error) {
	var out User
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if id == a.UserID {
			return ErrSelf
		}
		if err := lockTeam(ctx, tx); err != nil {
			return err
		}
		before, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := fn(ctx, tx, before); err != nil {
			return err
		}
		if out, err = get(ctx, tx, id, false); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, a, "", audit.Entry{Action: action, ResourceType: "platform_user", ResourceID: id,
			Before: map[string]any{"status": before.Status, "mfaEnabled": before.MFAEnabled}, After: map[string]any{"status": out.Status, "mfaEnabled": out.MFAEnabled}})
	})
	return out, err
}

// AcceptInvite sets a password from a one-time link and makes an invited user active. Every bad-link case returns
// ErrInvalidToken; a weak password returns identity.ErrWeakPassword.
func (s Service) AcceptInvite(ctx context.Context, token, password string) error {
	var failure error
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var inviteID, userID, email, status string
		err := tx.QueryRow(ctx, `SELECT i.id::text, u.id::text, u.email::text, u.status FROM platform_user_invites i JOIN platform_users u ON u.id = i.platform_user_id
			WHERE i.token_hash = $1 AND i.used_at IS NULL AND i.expires_at > $2 FOR UPDATE OF i, u`, hashToken(strings.TrimSpace(token)), s.now()).
			Scan(&inviteID, &userID, &email, &status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status == "disabled") {
			failure = ErrInvalidToken
			return nil
		}
		if err != nil {
			return err
		}
		if err := identity.CheckPassword(password, email); err != nil {
			failure = err
			return nil
		}
		hash, err := identity.HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_user_invites SET used_at = now() WHERE id = $1::uuid`, inviteID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform_users SET password_hash = $2, status = 'active', failed_login_count = 0, locked_until = NULL WHERE id = $1::uuid`, userID, hash); err != nil {
			return err
		}
		if err := endSessions(ctx, tx, userID); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, audit.Actor{UserID: userID, Source: "web"}, "", audit.Entry{Action: "platform_users.set_password", ResourceType: "platform_user", ResourceID: userID})
	})
	if err != nil {
		return err
	}
	return failure
}
