// Package notify stores in-app notifications (FND-16).
//
// A producer calls Notify inside its own transaction, so a notification exists only if the change it announces was
// committed. Reading is always scoped to one recipient: every query here filters on user_id, and the institute
// boundary comes from row-level security.
package notify

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Limits on what a notification may hold (the table checks the same).
const (
	MaxKind  = 60
	MaxTitle = 120
	MaxBody  = 500
	MaxLink  = 300
	// KeepRead is how long a read notification is kept before the hourly clean-up deletes it.
	KeepRead = 90 * 24 * time.Hour
)

// Notification is what a producer sends. Link is optional: a path inside the app that starts with "/".
type Notification struct {
	Kind  string
	Title string
	Body  string
	Link  string
}

// Validate checks lengths and that Link is a relative path (not "//host" or a full address).
func (n Notification) Validate() error {
	var errs crud.FieldErrors
	if k := strings.TrimSpace(n.Kind); k == "" || utf8.RuneCountInString(k) > MaxKind {
		errs.Add("kind", "invalid", "Kind is required and at most 60 characters.")
	}
	if t := strings.TrimSpace(n.Title); t == "" || utf8.RuneCountInString(t) > MaxTitle {
		errs.Add("title", "invalid", "Title is required and at most 120 characters.")
	}
	if utf8.RuneCountInString(n.Body) > MaxBody {
		errs.Add("body", "too_long", "Body is at most 500 characters.")
	}
	if n.Link != "" && (!strings.HasPrefix(n.Link, "/") || strings.HasPrefix(n.Link, "//") || strings.Contains(n.Link, `\`) ||
		utf8.RuneCountInString(n.Link) > MaxLink) {
		errs.Add("link", "invalid", "Link must be a path inside the app, starting with a single /.")
	}
	return errs.OrNil()
}

// Notify adds n for each user in userIDs (duplicates once) inside tx, which must be an institute transaction.
// The recipients must belong to that institute; otherwise the insert fails and tx should roll back.
func Notify(ctx context.Context, tx pgx.Tx, userIDs []string, n Notification) error {
	if err := n.Validate(); err != nil {
		return err
	}
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return tenancy.ErrNoTenant
	}
	seen := map[string]bool{}
	for _, id := range userIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var link *string
		if n.Link != "" {
			link = &n.Link
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (tenant_id, user_id, kind, title, body, link)
			VALUES ($1, $2::uuid, $3, $4, $5, $6)`, t.ID, id, strings.TrimSpace(n.Kind), strings.TrimSpace(n.Title), n.Body, link); err != nil {
			return err
		}
	}
	return nil
}

// Item is one notification as its recipient sees it.
type Item struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      *string    `json:"link"`
	CreatedAt time.Time  `json:"createdAt"`
	ReadAt    *time.Time `json:"readAt"`
}

// Inbox is a page of one user's notifications plus how many are unread in total.
type Inbox struct {
	Items       []Item
	Total       int
	Page, Size  int
	UnreadCount int
}

// ErrNotFound means the notification does not exist for this user (it may belong to somebody else).
var ErrNotFound = errors.New("notify: not found")

// Service reads and marks one user's notifications.
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

// List returns userID's notifications, newest first. unreadOnly hides read ones.
func (s Service) List(ctx context.Context, userID string, page, size int, unreadOnly bool) (Inbox, error) {
	out := Inbox{Items: []Item{}, Page: page, Size: size}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE read_at IS NULL), count(*) FILTER (WHERE $2 OR read_at IS NULL)
			FROM notifications WHERE user_id = $1::uuid`, userID, !unreadOnly).Scan(&out.UnreadCount, &out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, kind, title, body, link, created_at, read_at FROM notifications
			WHERE user_id = $1::uuid AND ($2 OR read_at IS NULL)
			ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4`, userID, !unreadOnly, size, (page-1)*size)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it Item
			if err := rows.Scan(&it.ID, &it.Kind, &it.Title, &it.Body, &it.Link, &it.CreatedAt, &it.ReadAt); err != nil {
				return err
			}
			out.Items = append(out.Items, it)
		}
		return rows.Err()
	})
	return out, err
}

// MarkRead marks one of userID's notifications read. It is idempotent (a second call keeps the first read time) and
// answers ErrNotFound for an id that is not theirs.
func (s Service) MarkRead(ctx context.Context, userID, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var found bool
		if err := tx.QueryRow(ctx, `WITH t AS (SELECT id FROM notifications WHERE id = $1::uuid AND user_id = $2::uuid),
			u AS (UPDATE notifications SET read_at = $3 WHERE id IN (SELECT id FROM t) AND read_at IS NULL)
			SELECT EXISTS (SELECT 1 FROM t)`, id, userID, s.now()).Scan(&found); err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
}

// MarkAllRead marks all of userID's unread notifications read and returns how many changed.
func (s Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	var n int64
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE notifications SET read_at = $2 WHERE user_id = $1::uuid AND read_at IS NULL`, userID, s.now())
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// PurgeRead deletes notifications read before now-KeepRead, in every institute, and returns how many went. It needs
// a platform-tier connection.
func PurgeRead(ctx context.Context, db tenancy.Beginner, now time.Time) (int64, error) {
	var n int64
	err := tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM notifications WHERE read_at IS NOT NULL AND read_at < $1`, now.Add(-KeepRead))
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
