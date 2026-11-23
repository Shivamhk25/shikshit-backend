// Package colleagues is the one definition of "a colleague": a person the school's pickers may offer, and the only
// kind of person an approval hand-over or an enquiry assignment may name.
//
// A colleague is a user of the institute who is
//   - live (not deleted) and active (not invited, locked or disabled: they can sign in now), and
//   - a staff account: not linked to a guardian (parent) or a student record (users.person_type).
//
// The same predicate drives the lookup (GET /colleagues), the check that the caller is staff, the delegation check
// and the enquiry-assignee check, so what the picker offers and what the server accepts cannot drift apart.
//
// The lookup returns an id, a display name and a role label. It never returns contact details, and it is not audited:
// decision 0009 audits reads of student, guardian and user records, and this returns none of that data.
package colleagues

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Limits of one lookup.
const (
	DefaultPageSize = 25
	MaxPageSize     = 50
	// MaxPage keeps the offset small; nobody pages through ten thousand pages of colleagues.
	MaxPage = 10000
	// MinSearch and MaxSearch bound the name search, in characters.
	MinSearch = 2
	MaxSearch = 100
	// maxLabelRoles is how many role names the label spells out.
	maxLabelRoles = 3
	// StaffLabel is the label of a colleague with no role.
	StaffLabel = "Staff"
)

// ErrNotStaff is returned to a caller whose account is not an active staff account of the institute (a parent or
// student account, or one that is no longer active).
var ErrNotStaff = errors.New("colleagues: the caller is not a staff account")

// Predicate is the SQL condition that makes a users row a colleague. It needs the table aliased as u.
const Predicate = `u.deleted_at IS NULL AND u.tenant_id = app_current_tenant() AND u.status = 'active'
	AND u.person_type IS DISTINCT FROM 'guardian' AND u.person_type IS DISTINCT FROM 'student'`

// Is reports whether userID is a colleague of the institute bound to tx. A malformed id is simply not one.
func Is(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id = $1::uuid AND `+Predicate+`)`, userID).Scan(&ok)
	return ok, err
}

// Colleague is one person offered by the lookup. It deliberately has no contact details.
type Colleague struct {
	ID          string
	DisplayName string
	Label       string
}

// Query is a validated lookup request.
type Query struct {
	Search     string
	Page, Size int
}

// Offset is the number of rows to skip.
func (q Query) Offset() int { return (q.Page - 1) * q.Size }

// ParseQuery validates the lookup parameters. A zero page or size means "not given". The search text is trimmed; it
// is either empty (first page of everyone) or at least MinSearch characters.
func ParseQuery(page, size int, search string) (Query, error) {
	var fe crud.FieldErrors
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = DefaultPageSize
	}
	if page < 1 || page > MaxPage {
		fe.Add("page", "invalid_page", fmt.Sprintf("Page must be between 1 and %d.", MaxPage))
	}
	if size < 1 || size > MaxPageSize {
		fe.Add("pageSize", "invalid_size", fmt.Sprintf("Page size must be between 1 and %d.", MaxPageSize))
	}
	search = strings.TrimSpace(search)
	switch n := utf8.RuneCountInString(search); {
	case n == 0:
	case n < MinSearch:
		fe.Add("q", "too_short", fmt.Sprintf("Type at least %d characters of the name.", MinSearch))
	case n > MaxSearch:
		fe.Add("q", "too_long", fmt.Sprintf("The search is at most %d characters.", MaxSearch))
	}
	if err := fe.OrNil(); err != nil {
		return Query{}, err
	}
	return Query{Search: search, Page: page, Size: size}, nil
}

// Service finds colleagues.
type Service struct{ DB tenancy.Beginner }

// List returns one page of colleagues for the caller, ordered by display name and then id. It returns ErrNotStaff
// when the caller is not themselves a colleague (a parent or student account). Row-level security and the
// predicate both keep the answer inside the caller's institute.
func (s Service) List(ctx context.Context, callerID string, q Query) (crud.Page[Colleague], error) {
	page := crud.Page[Colleague]{Items: []Colleague{}, Page: q.Page, Size: q.Size}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := Is(ctx, tx, callerID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotStaff
		}
		sql := `SELECT u.id::text, u.display_name,
				COALESCE((SELECT array_agg(r.name ORDER BY lower(r.name), r.name) FROM user_roles ur
					JOIN roles r ON r.tenant_id = ur.tenant_id AND r.id = ur.role_id AND r.deleted_at IS NULL
					WHERE ur.tenant_id = u.tenant_id AND ur.user_id = u.id
					AND ur.starts_at <= now() AND (ur.ends_at IS NULL OR ur.ends_at > now())), '{}'::text[]),
				count(*) OVER()
			FROM users u WHERE ` + Predicate
		args := []any{}
		if q.Search != "" {
			args = append(args, crud.LikePattern(q.Search))
			sql += ` AND u.display_name ILIKE $1 ESCAPE '\'`
		}
		sql += fmt.Sprintf(` ORDER BY lower(u.display_name), u.display_name, u.id LIMIT $%d OFFSET $%d`, len(args)+1, len(args)+2)
		args = append(args, q.Size, q.Offset())
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Colleague
			var roles []string
			if err := rows.Scan(&c.ID, &c.DisplayName, &roles, &page.Total); err != nil {
				return err
			}
			c.Label = Label(roles)
			page.Items = append(page.Items, c)
		}
		return rows.Err()
	})
	return page, err
}

// Label spells out up to three role names ("Principal, Teacher"), then "+N more", or StaffLabel when there are none.
func Label(roles []string) string {
	switch {
	case len(roles) == 0:
		return StaffLabel
	case len(roles) <= maxLabelRoles:
		return strings.Join(roles, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(roles[:maxLabelRoles], ", "), len(roles)-maxLabelRoles)
}
