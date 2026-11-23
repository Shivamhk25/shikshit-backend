// Package users implements IAM-01 Users: create and invite, edit, deactivate and reactivate, lock and unlock, reset
// password, and the public accept-invite step that sets a password from a one-time link.
//
// Rules enforced here (each has a test):
//   - username and email are unique per institute, case-insensitively;
//   - nobody can deactivate or lock themselves, and the last active Admin of an institute can be neither;
//   - status changes follow one table (see transitions); anything else is ErrInvalidState;
//   - one-time tokens are 32 random bytes, only their SHA-256 is stored, they work once, and they never reach the
//     audit trail;
//   - disabling, locking, resetting or setting a password revokes every session of the user.
//
// Roles are not given here: the Roles screen (IAM-02) does that, so the "cannot grant what you do not hold" rule
// lives in one place.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/messaging"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var (
	// ErrNotFound is returned when the user does not exist in this institute.
	ErrNotFound = errors.New("users: not found")
	// ErrLastAdmin is returned when a change would leave the institute without an active Admin.
	ErrLastAdmin = errors.New("This is the last active Admin of the institute. Make someone else an Admin first.")
	// ErrSelfAction is returned when users try to deactivate or lock themselves.
	ErrSelfAction = errors.New("You cannot do this to your own account.")
	// ErrInvalidState is returned when the action does not apply to the user's current status.
	ErrInvalidState = errors.New("This action does not apply to the user's current status.")
	// ErrInvalidToken is returned for a bad, expired or used token, or a user who may not set a password now.
	// Every one of those cases gets this same error, so the answer does not reveal which.
	ErrInvalidToken = errors.New("This link is not valid any more. Ask your administrator for a new one.")
)

// Token lifetimes.
const (
	InviteTTL = 7 * 24 * time.Hour
	ResetTTL  = 24 * time.Hour
)

// AcceptPath is the web page that receives the token; the link returned to the admin is AcceptPath + "?token=...".
const AcceptPath = "/accept-invite"

// Statuses are the allowed user statuses.
var Statuses = []string{"invited", "active", "locked", "disabled"}

// PersonTypes are the person records a user can be linked to today. Staff arrives with staff records (HRP).
var PersonTypes = []string{"student", "guardian"}

// User is a user as the API shows it. It never contains the password hash or the MFA secret.
type User struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"displayName"`
	Email              *string    `json:"email"`
	Phone              *string    `json:"phone"`
	Status             string     `json:"status"`
	PersonType         *string    `json:"personType"`
	PersonID           *string    `json:"personId"`
	MustChangePassword bool       `json:"mustChangePassword"`
	MFAEnabled         bool       `json:"mfaEnabled"`
	FailedLoginCount   int        `json:"failedLoginCount"`
	LockedUntil        *time.Time `json:"lockedUntil"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	AccessVersion      int64      `json:"accessVersion"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// UserWithInviteLink is returned by Create: the user plus the one-time invite link (shown once).
type UserWithInviteLink struct {
	User
	InviteLink  string `json:"inviteLink"`
	EmailQueued bool   `json:"emailQueued"`
}

// UserWithResetLink is returned by ResetPassword: the user plus the one-time set-password link (shown once).
type UserWithResetLink struct {
	User
	ResetLink   string `json:"resetLink"`
	EmailQueued bool   `json:"emailQueued"`
}

// CreateInput is what Create accepts.
type CreateInput struct {
	Username    string
	DisplayName string
	Email       *string
	Phone       *string
	PersonType  *string
	PersonID    *string
}

// UpdateInput is what Update accepts. Nil means "leave as is"; the username cannot change.
type UpdateInput struct {
	DisplayName *string
	Email       *string
	Phone       *string
	PersonType  *string
	PersonID    *string
}

// Filter narrows a list.
type Filter struct {
	Status string
}

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{
	"username":    "lower(username)",
	"displayName": "lower(display_name)",
	"email":       "lower(email)",
	"status":      "status",
	"createdAt":   "created_at",
}

// DefaultSort lists users by username.
const DefaultSort = "username"

const columns = `id::text, username::text, display_name, email::text, phone, status, person_type, person_id::text,
	must_change_password, mfa_enabled, failed_login_count, locked_until, last_login_at, access_version,
	created_at, updated_at`

func scanInto(r interface{ Scan(...any) error }, u *User, extra ...any) error {
	dest := []any{&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Phone, &u.Status, &u.PersonType, &u.PersonID,
		&u.MustChangePassword, &u.MFAEnabled, &u.FailedLoginCount, &u.LockedUntil, &u.LastLoginAt, &u.AccessVersion,
		&u.CreatedAt, &u.UpdatedAt}
	return r.Scan(append(dest, extra...)...)
}

func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && len(s) <= 254
}

func validPhone(s string) bool { return len(s) >= 7 && len(s) <= 30 }

func checkPerson(errs *crud.FieldErrors, personType, personID *string) {
	if (personType == nil) != (personID == nil) {
		errs.Add("personId", "incomplete", "Give both the person type and the person, or neither.")
		return
	}
	if personType != nil {
		errs.Choice("personType", *personType, PersonTypes...)
	}
	if personID != nil {
		if _, err := uuid.Parse(*personID); err != nil {
			errs.Add("personId", "invalid", "Choose a person from the list.")
		}
	}
}

// NormaliseCreate trims and validates create input.
func NormaliseCreate(in CreateInput) (CreateInput, error) {
	var errs crud.FieldErrors
	in.Username = strings.TrimSpace(in.Username)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.Email = crud.TrimPtr(in.Email)
	in.Phone = crud.TrimPtr(in.Phone)
	in.PersonType = crud.TrimPtr(in.PersonType)
	in.PersonID = crud.TrimPtr(in.PersonID)

	errs.Required("displayName", "a display name", in.DisplayName, 200)
	if n := len(in.Username); n < 3 || n > 64 {
		errs.Add("username", "invalid_length", "A username has 3 to 64 characters.")
	} else if strings.ContainsAny(in.Username, " \t\n") {
		errs.Add("username", "invalid", "A username has no spaces.")
	}
	if in.Email != nil && !validEmail(*in.Email) {
		errs.Add("email", "invalid_email", "Enter a valid email address.")
	}
	if in.Phone != nil && !validPhone(*in.Phone) {
		errs.Add("phone", "invalid", "A phone number has 7 to 30 characters.")
	}
	checkPerson(&errs, in.PersonType, in.PersonID)
	return in, errs.OrNil()
}

// NormaliseUpdate trims and validates update input.
func NormaliseUpdate(in UpdateInput) (UpdateInput, error) {
	var errs crud.FieldErrors
	in.DisplayName = crud.TrimPtr(in.DisplayName)
	in.Email = crud.TrimPtr(in.Email)
	in.Phone = crud.TrimPtr(in.Phone)
	in.PersonType = crud.TrimPtr(in.PersonType)
	in.PersonID = crud.TrimPtr(in.PersonID)

	if in.DisplayName != nil && len(*in.DisplayName) > 200 {
		errs.Add("displayName", "too_long", "A display name has at most 200 characters.")
	}
	if in.Email != nil && !validEmail(*in.Email) {
		errs.Add("email", "invalid_email", "Enter a valid email address.")
	}
	if in.Phone != nil && !validPhone(*in.Phone) {
		errs.Add("phone", "invalid", "A phone number has 7 to 30 characters.")
	}
	checkPerson(&errs, in.PersonType, in.PersonID)
	return in, errs.OrNil()
}

// Service reads and writes users.
type Service struct {
	DB tenancy.Beginner
	// Mail emails invite and reset links (FND-19). nil: links are only returned, for the admin to share.
	Mail *messaging.Outbox
	// Now is the clock; nil means time.Now. Tests set it to expire tokens.
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ListAudited returns one page of users and records the read in the audit trail.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[User], error) {
	var page crud.Page[User]
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if page, err = listIn(ctx, tx, q, f); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{
			Action: "users.list", ResourceType: "user",
			After: map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "status": f.Status},
		})
	})
	return page, err
}

func listIn(ctx context.Context, tx pgx.Tx, q crud.ListQuery, f Filter) (crud.Page[User], error) {
	page := crud.Page[User]{Items: []User{}, Page: q.Page, Size: q.Size}
	where := []string{"deleted_at IS NULL"}
	args := []any{}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if q.Search != "" {
		p := arg(crud.LikePattern(q.Search))
		where = append(where, fmt.Sprintf(`(username::text ILIKE %[1]s ESCAPE '\' OR display_name ILIKE %[1]s ESCAPE '\' OR email::text ILIKE %[1]s ESCAPE '\')`, p))
	}
	if f.Status != "" {
		where = append(where, "status = "+arg(f.Status))
	}
	limit, offset := arg(q.Size), arg(q.Offset())
	sql := fmt.Sprintf(`SELECT %s, count(*) OVER() FROM users WHERE %s ORDER BY %s, id LIMIT %s OFFSET %s`,
		columns, strings.Join(where, " AND "), q.OrderBy, limit, offset)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if err := scanInto(rows, &u, &page.Total); err != nil {
			return page, err
		}
		page.Items = append(page.Items, u)
	}
	return page, rows.Err()
}

// View returns one user and records the read in the audit trail.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (User, error) {
	var u User
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if u, err = get(ctx, tx, id, false); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.view", ResourceType: "user", ResourceID: id})
	})
	return u, err
}

// get loads a user; forUpdate locks the row for the rest of the transaction.
func get(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (User, error) {
	var u User
	if _, err := uuid.Parse(id); err != nil {
		return u, ErrNotFound
	}
	sql := `SELECT ` + columns + ` FROM users WHERE id = $1::uuid AND deleted_at IS NULL`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	err := scanInto(tx.QueryRow(ctx, sql, id), &u)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// Create adds a user with status invited and returns a one-time invite link.
func (s Service) Create(ctx context.Context, a audit.Actor, in CreateInput) (UserWithInviteLink, error) {
	in, err := NormaliseCreate(in)
	if err != nil {
		return UserWithInviteLink{}, err
	}
	var out UserWithInviteLink
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		if err := checkPersonExists(ctx, tx, in.PersonType, in.PersonID); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, username, display_name, email, phone, status, person_type, person_id, must_change_password)
			VALUES ($1, $2, $3, $4, $5, 'invited', $6, $7::uuid, true)
			RETURNING id::text`,
			t.ID, in.Username, in.DisplayName, in.Email, in.Phone, in.PersonType, in.PersonID).Scan(&id)
		if err != nil {
			return uniqueError(err)
		}
		token, err := s.issueToken(ctx, tx, t.ID, id, "invite", a.UserID)
		if err != nil {
			return err
		}
		u, err := get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out = UserWithInviteLink{User: u, InviteLink: AcceptPath + "?token=" + token}
		if out.EmailQueued, err = s.emailLink(ctx, tx, t, u, out.InviteLink, true); err != nil {
			return err
		}
		// The audit row holds the user, never the link: the token would let anyone reading the log take the account.
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.create", ResourceType: "user", ResourceID: id, After: u})
	})
	return out, err
}

// emailLink queues the invite or reset link for the user's email address, when there is one and email is set up.
func (s Service) emailLink(ctx context.Context, tx pgx.Tx, t tenancy.Tenant, u User, path string, invite bool) (bool, error) {
	if s.Mail == nil || u.Email == nil {
		return false, nil
	}
	hours := int(ResetTTL.Hours())
	if invite {
		hours = int(InviteTTL.Hours())
	}
	return s.Mail.QueueAccountLink(ctx, tx, messaging.AccountLink{To: *u.Email, Name: u.DisplayName, Place: t.Name,
		Username: u.Username, Link: s.Mail.Links.School(t.Code, path), Hours: hours, Invite: invite})
}

// Update changes display name, email, phone or the person link.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in UpdateInput) (User, error) {
	in, err := NormaliseUpdate(in)
	if err != nil {
		return User{}, err
	}
	var out User
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if err := checkPersonExists(ctx, tx, in.PersonType, in.PersonID); err != nil {
			return err
		}
		sets, args := []string{}, []any{id}
		set := func(col string, v any, cast string) {
			args = append(args, v)
			sets = append(sets, fmt.Sprintf("%s = $%d%s", col, len(args), cast))
		}
		if in.DisplayName != nil {
			set("display_name", *in.DisplayName, "")
		}
		if in.Email != nil {
			set("email", *in.Email, "")
		}
		if in.Phone != nil {
			set("phone", *in.Phone, "")
		}
		if in.PersonType != nil {
			set("person_type", *in.PersonType, "")
			set("person_id", *in.PersonID, "::uuid")
			// The person link decides what a parent or student may see, so access must be recomputed.
			sets = append(sets, "access_version = access_version + 1")
		}
		if len(sets) == 0 {
			out = before
			return nil
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(`UPDATE users SET %s WHERE id = $1::uuid AND deleted_at IS NULL RETURNING %s`,
			strings.Join(sets, ", "), columns), args...)
		if err := scanInto(row, &out); err != nil {
			return uniqueError(err)
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.update", ResourceType: "user", ResourceID: id, Before: before, After: out})
	})
	return out, err
}

// transition is one allowed status change.
type transition struct {
	from          []string
	to            func(u User, hasPassword bool) string
	selfForbidden bool
	lastAdmin     bool // the change takes an Admin out of active service
	revoke        bool // revoke every session
	clearLock     bool // reset failed logins and the temporary lock
}

var transitions = map[string]transition{
	"users.deactivate": {from: []string{"invited", "active", "locked"}, to: fixed("disabled"), selfForbidden: true, lastAdmin: true, revoke: true},
	"users.reactivate": {from: []string{"disabled"}, to: func(_ User, hasPassword bool) string {
		if hasPassword {
			return "active"
		}
		return "invited" // never signed in: they still need their invite or reset link
	}, clearLock: true},
	"users.lock":   {from: []string{"active"}, to: fixed("locked"), selfForbidden: true, lastAdmin: true, revoke: true},
	"users.unlock": {from: []string{"locked", "active"}, to: fixed("active"), clearLock: true},
}

func fixed(s string) func(User, bool) string { return func(User, bool) string { return s } }

// Deactivate disables a user and signs them out everywhere.
func (s Service) Deactivate(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "users.deactivate")
}

// Reactivate brings a disabled user back: active if they have a password, otherwise invited.
func (s Service) Reactivate(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "users.reactivate")
}

// Lock stops an active user from signing in until an admin unlocks them, and signs them out everywhere.
func (s Service) Lock(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "users.lock")
}

// Unlock ends an admin lock, or clears the temporary lock after too many wrong passwords.
func (s Service) Unlock(ctx context.Context, a audit.Actor, id string) (User, error) {
	return s.change(ctx, a, id, "users.unlock")
}

func (s Service) change(ctx context.Context, a audit.Actor, id, action string) (User, error) {
	tr := transitions[action]
	var out User
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		if tr.lastAdmin {
			if err := LockAdmins(ctx, tx, t.ID); err != nil {
				return err
			}
		}
		before, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if tr.selfForbidden && before.ID == a.UserID {
			return ErrSelfAction
		}
		if !contains(tr.from, before.Status) {
			return ErrInvalidState
		}
		if tr.lastAdmin {
			if err := CheckNotLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		var hasPassword bool
		if err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM users WHERE id = $1::uuid`, id).Scan(&hasPassword); err != nil {
			return err
		}
		sets := "status = $2, access_version = access_version + 1"
		if tr.clearLock {
			sets += ", failed_login_count = 0, locked_until = NULL"
		}
		row := tx.QueryRow(ctx, `UPDATE users SET `+sets+` WHERE id = $1::uuid RETURNING `+columns, id, tr.to(before, hasPassword))
		if err := scanInto(row, &out); err != nil {
			return err
		}
		if tr.revoke {
			if err := revokeSessions(ctx, tx, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: action, ResourceType: "user", ResourceID: id,
			Before: map[string]any{"status": before.Status}, After: map[string]any{"status": out.Status}})
	})
	return out, err
}

// Notification kinds this package produces.
const (
	KindPasswordReset = "user.password_reset"
	KindMFAReset      = "user.mfa_reset"
)

// ResetPassword issues a one-time set-password link (24 hours), cancels earlier links and signs the user out.
// A disabled or locked user must be reactivated or unlocked first, so a reset cannot undo an admin's decision.
func (s Service) ResetPassword(ctx context.Context, a audit.Actor, id string) (UserWithResetLink, error) {
	var out UserWithResetLink
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		u, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if u.Status != "active" && u.Status != "invited" {
			return ErrInvalidState
		}
		token, err := s.issueToken(ctx, tx, t.ID, id, "reset", a.UserID)
		if err != nil {
			return err
		}
		if err := revokeSessions(ctx, tx, id); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE users SET must_change_password = true, access_version = access_version + 1
			WHERE id = $1::uuid RETURNING `+columns, id)
		if err := scanInto(row, &out.User); err != nil {
			return err
		}
		out.ResetLink = AcceptPath + "?token=" + token
		if out.EmailQueued, err = s.emailLink(ctx, tx, t, out.User, out.ResetLink, false); err != nil {
			return err
		}
		if err := notify.Notify(ctx, tx, []string{id}, notify.Notification{Kind: KindPasswordReset, Title: "Your password was reset by an administrator",
			Body: "You were signed out everywhere. Use the link you were sent to choose a new password. If you did not expect this, tell your school."}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.reset_password", ResourceType: "user", ResourceID: id,
			After: map[string]any{"linkIssued": true, "validHours": int(ResetTTL.Hours())}})
	})
	return out, err
}

// ResetMFA turns off two-step sign-in for someone who lost their phone and signs them out everywhere. Where the
// institute requires it, they set it up again at their next sign-in. Nobody can do this to their own account: that
// would let someone with a stolen session drop the second step.
func (s Service) ResetMFA(ctx context.Context, a audit.Actor, id string) (User, error) {
	var out User
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		u, err := get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if u.ID == a.UserID {
			return ErrSelfAction
		}
		if err := identity.ClearMFA(ctx, tx, id); err != nil {
			return err
		}
		if err := revokeSessions(ctx, tx, id); err != nil {
			return err
		}
		if out, err = get(ctx, tx, id, false); err != nil {
			return err
		}
		if err := notify.Notify(ctx, tx, []string{id}, notify.Notification{Kind: KindMFAReset, Title: "Your two-step sign-in was reset by an administrator",
			Body: "You were signed out everywhere. You may be asked to set up two-step sign-in again. If you did not expect this, tell your school."}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.reset_mfa", ResourceType: "user", ResourceID: id,
			Before: map[string]any{"mfaEnabled": u.MFAEnabled}, After: map[string]any{"mfaEnabled": false}})
	})
	return out, err
}

// AcceptInvite sets the password from a one-time invite or reset token. It is public: the institute comes from the
// request host, so a token issued by another institute is simply not found (row-level security).
func (s Service) AcceptInvite(ctx context.Context, a audit.Actor, token, password string) error {
	raw, err := hex.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return ErrInvalidToken
	}
	sum := sha256.Sum256(raw)
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var inviteID, userID, username, status string
		var expires time.Time
		var used *time.Time
		// FOR UPDATE on both rows: two requests with the same token cannot both succeed.
		err := tx.QueryRow(ctx, `
			SELECT i.id::text, i.expires_at, i.used_at, u.id::text, u.username::text, u.status
			FROM user_invites i JOIN users u ON u.tenant_id = i.tenant_id AND u.id = i.user_id AND u.deleted_at IS NULL
			WHERE i.token_hash = $1 FOR UPDATE OF i, u`, hex.EncodeToString(sum[:])).
			Scan(&inviteID, &expires, &used, &userID, &username, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if used != nil || !expires.After(s.now()) || (status != "invited" && status != "active") {
			return ErrInvalidToken
		}
		if err := identity.CheckPassword(password, username); err != nil {
			return err
		}
		hash, err := identity.HashPassword(password)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, status = 'active', must_change_password = false,
			failed_login_count = 0, locked_until = NULL, access_version = access_version + 1 WHERE id = $1::uuid`, userID, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_invites SET used_at = now() WHERE id = $1::uuid`, inviteID); err != nil {
			return err
		}
		if err := revokeSessions(ctx, tx, userID); err != nil {
			return err
		}
		a.UserID = userID
		return audit.Record(ctx, tx, a, audit.Entry{Action: "users.accept_invite", ResourceType: "user", ResourceID: userID,
			After: map[string]any{"status": "active"}})
	})
}

// issueToken cancels the user's unused links and stores a new one; it returns the token to put in the link.
func (s Service) issueToken(ctx context.Context, tx pgx.Tx, tenantID, userID, purpose, createdBy string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	ttl := InviteTTL
	if purpose == "reset" {
		ttl = ResetTTL
	}
	if _, err := tx.Exec(ctx, `UPDATE user_invites SET used_at = now() WHERE user_id = $1::uuid AND used_at IS NULL`, userID); err != nil {
		return "", err
	}
	var by any
	if createdBy != "" {
		by = createdBy
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_invites (tenant_id, user_id, token_hash, purpose, expires_at, created_by)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::uuid)`, tenantID, userID, hex.EncodeToString(sum[:]), purpose, s.now().Add(ttl), by); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func revokeSessions(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID)
	return err
}

// LockAdmins serialises every change that can remove an active Admin, per institute, for the rest of the transaction.
// Without it two Admins could deactivate each other at the same moment and both pass the check.
func LockAdmins(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('iam.last_admin:' || $1::text, 0))`, tenantID)
	return err
}

// CheckNotLastAdmin returns ErrLastAdmin when userID holds the Admin role and no other active user does. Call it
// after LockAdmins in the same transaction. IAM-02 uses it before taking the Admin role away.
func CheckNotLastAdmin(ctx context.Context, tx pgx.Tx, userID string) error {
	const holders = `
		SELECT DISTINCT ur.user_id FROM user_roles ur
		JOIN roles r ON r.tenant_id = ur.tenant_id AND r.id = ur.role_id AND r.code = 'admin' AND r.deleted_at IS NULL
		JOIN users u ON u.tenant_id = ur.tenant_id AND u.id = ur.user_id AND u.deleted_at IS NULL AND u.status = 'active'
		WHERE ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now())`
	var isAdmin, othersExist bool
	err := tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM (`+holders+`) h WHERE h.user_id = $1::uuid),
			EXISTS (SELECT 1 FROM (`+holders+`) h WHERE h.user_id <> $1::uuid)`, userID).Scan(&isAdmin, &othersExist)
	if err != nil {
		return err
	}
	if isAdmin && !othersExist {
		return ErrLastAdmin
	}
	return nil
}

func checkPersonExists(ctx context.Context, tx pgx.Tx, personType, personID *string) error {
	if personType == nil {
		return nil
	}
	table := map[string]string{"student": "students", "guardian": "guardians"}[*personType]
	var ok bool
	// The table name comes from the fixed map above, never from input.
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1::uuid AND deleted_at IS NULL)`, *personID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return crud.FieldErrors{{Field: "personId", Code: "not_found", Message: "This person is not in this institute."}}
	}
	return nil
}

func uniqueError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		switch {
		case strings.Contains(pg.ConstraintName, "username"):
			return crud.FieldErrors{{Field: "username", Code: "duplicate", Message: "Another user already has this username."}}
		case strings.Contains(pg.ConstraintName, "email"):
			return crud.FieldErrors{{Field: "email", Code: "duplicate", Message: "Another user already has this email."}}
		case strings.Contains(pg.ConstraintName, "person"):
			// users_tenant_person_uq (decision 0016): one account per student or guardian
			return crud.FieldErrors{{Field: "personId", Code: "duplicate", Message: "Another account is already linked to this person."}}
		}
	}
	return err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CreateFirstAdmin adds an institute's first user, gives them the Admin role and returns their one-time invite link.
// It is used by the platform's new-institute wizard (SAD-02) inside its own transaction, which must already be bound
// to the new institute as an institute session (tenant tier). The audit row is written by the caller, as the platform
// user who created the institute; createdBy is that platform user's id (kept on the invite).
func CreateFirstAdmin(ctx context.Context, tx pgx.Tx, tenantID string, in CreateInput, createdBy string) (UserWithInviteLink, error) {
	in.PersonType, in.PersonID = nil, nil
	in, err := NormaliseCreate(in)
	if err != nil {
		return UserWithInviteLink{}, err
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, username, display_name, email, phone, status, must_change_password)
		VALUES ($1, $2, $3, $4, $5, 'invited', true) RETURNING id::text`,
		tenantID, in.Username, in.DisplayName, in.Email, in.Phone).Scan(&id); err != nil {
		return UserWithInviteLink{}, uniqueError(err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO user_roles (tenant_id, user_id, role_id)
		SELECT $1, $2::uuid, id FROM roles WHERE tenant_id = $1 AND code = 'admin' AND deleted_at IS NULL`, tenantID, id)
	if err != nil {
		return UserWithInviteLink{}, err
	}
	if tag.RowsAffected() != 1 {
		return UserWithInviteLink{}, errors.New("users: the institute has no Admin role")
	}
	token, err := Service{}.issueToken(ctx, tx, tenantID, id, "invite", createdBy)
	if err != nil {
		return UserWithInviteLink{}, err
	}
	u, err := get(ctx, tx, id, false)
	if err != nil {
		return UserWithInviteLink{}, err
	}
	return UserWithInviteLink{User: u, InviteLink: AcceptPath + "?token=" + token}, nil
}
