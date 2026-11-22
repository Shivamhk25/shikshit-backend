package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Read-only support visits (SAD-05, decision 0018). A platform user gives a reason and gets a one-time code; the code
// is redeemed on the school's own address and opens a session that:
//   - lasts at most 30 minutes and cannot be refreshed;
//   - carries tier "support", so it is never mistaken for a school user or a platform session;
//   - may only read (the API refuses anything else), and every read goes into the school's audit trail with the
//     reason, under the platform user's name.

// SupportTier is the token tier of a support visit.
const SupportTier = "support"

// SupportVisitTTL is how long a visit lasts from the moment it is started.
const SupportVisitTTL = 30 * time.Minute

var (
	// ErrVisitReason is returned when the reason is shorter than 10 or longer than 500 characters.
	ErrVisitReason = errors.New("Give a reason of 10 to 500 characters. The school sees it.")
	// ErrVisitUnavailable is returned for an unknown institute, or one that is not onboarding or live.
	ErrVisitUnavailable = errors.New("identity: this institute cannot be visited")
	// ErrVisitCode is returned for an unknown, used or expired visit code.
	ErrVisitCode = errors.New("This visit link is not valid any more. Start a new visit from the console.")
)

// SupportVisit describes an open support visit.
type SupportVisit struct {
	ID, PlatformUserName, Reason string
	ExpiresAt                    time.Time
}

// VisitStart is what the console gets when a visit is started: the one-time code (shown once) and where to use it.
type VisitStart struct {
	ID, Code, InstituteCode string
	ExpiresAt               time.Time
}

// StartSupportVisit opens a visit to an institute for the signed-in platform user.
func (s *PlatformService) StartSupportVisit(ctx context.Context, a audit.Actor, p PlatformPrincipal, tenantID, reason string) (VisitStart, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 10 || n > 500 {
		return VisitStart{}, ErrVisitReason
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return VisitStart{}, err
	}
	code := hex.EncodeToString(raw)
	var out VisitStart
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT code::text, status FROM tenants WHERE id = $1::uuid AND deleted_at IS NULL`, tenantID).Scan(&out.InstituteCode, &status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "onboarding" && status != "live") {
			return ErrVisitUnavailable
		}
		if err != nil {
			return err
		}
		out.ExpiresAt = s.now().Add(SupportVisitTTL)
		out.Code = code
		if err := tx.QueryRow(ctx, `INSERT INTO support_visits (tenant_id, platform_user_id, platform_user_name, reason, code_hash, expires_at)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6) RETURNING id::text`, tenantID, p.UserID, p.Name, reason, hashToken(code), out.ExpiresAt).Scan(&out.ID); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, a, tenantID, audit.Entry{Action: "support.visit_start", ResourceType: "support_visit", ResourceID: out.ID,
			After: map[string]any{"reason": reason, "expiresAt": out.ExpiresAt}})
	})
	if err != nil {
		return VisitStart{}, err
	}
	return out, nil
}

// RedeemSupportVisit trades a visit code for the visit's access token, on the school's address (ctx carries the
// institute). The token expires when the visit does; there is no refresh token.
func (s *Service) RedeemSupportVisit(ctx context.Context, code string) (Tokens, SupportVisit, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Tokens{}, SupportVisit{}, tenancy.ErrNoTenant
	}
	var (
		v          SupportVisit
		platformID string
		failure    error
	)
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text, platform_user_id::text, platform_user_name, reason, expires_at FROM redeem_support_visit($1)`, hashToken(strings.TrimSpace(code))).
			Scan(&v.ID, &platformID, &v.PlatformUserName, &v.Reason, &v.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			failure = ErrVisitCode
			return nil
		}
		if err != nil {
			return err
		}
		return audit.RecordSupport(ctx, tx, platformID, v.ID, v.Reason, audit.Entry{Action: "support.visit_enter", ResourceType: "support_visit", ResourceID: v.ID})
	})
	if err != nil {
		return Tokens{}, SupportVisit{}, err
	}
	if failure != nil {
		return Tokens{}, SupportVisit{}, failure
	}
	access, err := s.signer.IssueUntil(platformID, t.ID, SupportTier, v.ID, v.ExpiresAt)
	if err != nil {
		return Tokens{}, SupportVisit{}, err
	}
	return Tokens{AccessToken: access, AccessExpiresAt: v.ExpiresAt, RefreshExpiresAt: v.ExpiresAt, UserID: platformID}, v, nil
}

// authenticateSupport accepts a support token while its visit is open: redeemed, not ended, not expired.
func (s *Service) authenticateSupport(ctx context.Context, claims *Claims) (Principal, error) {
	t, _ := tenancy.FromContext(ctx)
	p := Principal{UserID: claims.Subject, TenantID: t.ID, SessionID: claims.SessionID, Support: &SupportVisit{ID: claims.SessionID}}
	err := tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT platform_user_name, reason, expires_at FROM support_visits
			WHERE id::text = $1 AND platform_user_id::text = $2 AND code_used_at IS NOT NULL AND ended_at IS NULL AND expires_at > now()`,
			claims.SessionID, claims.Subject).Scan(&p.Support.PlatformUserName, &p.Support.Reason, &p.Support.ExpiresAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, err
	}
	return p, nil
}

// EndSupportVisit closes the visit at once; its token stops working on the next request.
func (s *Service) EndSupportVisit(ctx context.Context, p Principal) error {
	if p.Support == nil {
		return ErrUnauthenticated
	}
	return tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		var ended bool
		if err := tx.QueryRow(ctx, `SELECT end_support_visit($1::uuid)`, p.Support.ID).Scan(&ended); err != nil {
			return err
		}
		return audit.RecordSupport(ctx, tx, p.UserID, p.Support.ID, p.Support.Reason, audit.Entry{Action: "support.visit_end", ResourceType: "support_visit", ResourceID: p.Support.ID})
	})
}

// RecordSupportRead writes one audit row in the school for a read during a visit (the operation that was called).
func (s *Service) RecordSupportRead(ctx context.Context, p Principal, operationID, path string) error {
	if p.Support == nil {
		return nil
	}
	return tenancy.WithTenantTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		return audit.RecordSupport(ctx, tx, p.UserID, p.Support.ID, p.Support.Reason,
			audit.Entry{Action: "support.read", ResourceType: "operation", ResourceID: operationID, After: map[string]string{"path": path}})
	})
}
