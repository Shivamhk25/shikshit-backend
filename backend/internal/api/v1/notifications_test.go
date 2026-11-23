package apiv1_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

type notificationOut struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Title  string  `json:"title"`
	Link   *string `json:"link"`
	ReadAt *string `json:"readAt"`
}

type notificationsOut struct {
	Items []notificationOut `json:"items"`
	Page  struct {
		Number, Size, Total int
	} `json:"page"`
	UnreadCount int `json:"unreadCount"`
}

// seedNotes inserts n notifications for user, oldest first ("title 1" is the oldest), and returns their ids.
func (s usersStack) seedNotes(t *testing.T, tenant, user string, n int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		var id string
		if err := s.adm.QueryRow(bg, `INSERT INTO notifications (tenant_id, user_id, kind, title, created_at)
			VALUES ($1, $2::uuid, 'test', $3::text, now() - make_interval(mins => $4)) RETURNING id::text`,
			tenant, user, fmt.Sprintf("title %d", i), n-i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func (s usersStack) notes(t *testing.T, host, token, query string) notificationsOut {
	t.Helper()
	rec := s.do(http.MethodGet, host, "/me/notifications"+query, token, "")
	want(t, rec, http.StatusOK, "")
	return decode[notificationsOut](t, rec)
}

func TestNotificationsAreOwnRowsOnly(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	plain := s.signIn(t, alphaHost, "plain", password)
	betaadmin := s.signIn(t, betaHost, "betaadmin", password)
	mine := s.seedNotes(t, s.tenant["alpha"], s.ids["plain"], 2)
	theirs := s.seedNotes(t, s.tenant["alpha"], s.ids["admin"], 3)
	s.seedNotes(t, s.tenant["beta"], s.ids["betaadmin"], 4)

	if got := s.notes(t, alphaHost, plain, ""); got.Page.Total != 2 || got.UnreadCount != 2 || len(got.Items) != 2 {
		t.Fatalf("plain sees %+v", got)
	}
	if got := s.notes(t, alphaHost, admin, ""); got.Page.Total != 3 {
		t.Fatalf("admin sees %+v", got)
	}
	if got := s.notes(t, betaHost, betaadmin, ""); got.Page.Total != 4 {
		t.Fatalf("betaadmin sees %+v", got)
	}

	// Someone else's notification (same institute, or another institute) cannot be read or marked: it looks missing.
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+theirs[0]+"/read", plain, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, betaHost, "/me/notifications/"+mine[0]+"/read", betaadmin, ""), http.StatusNotFound, "not_found")
	// read-all touches only the caller's rows.
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/read-all", plain, ""), http.StatusNoContent, "")
	var unread int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&unread); err != nil {
		t.Fatal(err)
	}
	if unread != 3+4 {
		t.Fatalf("read-all changed other people's rows: %d unread left, want 7", unread)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/me/notifications", "", ""), http.StatusUnauthorized, "unauthenticated")
}

func TestNotificationsPagingAndUnreadCount(t *testing.T) {
	s := newUsersStack(t)
	plain := s.signIn(t, alphaHost, "plain", password)
	ids := s.seedNotes(t, s.tenant["alpha"], s.ids["plain"], 5)

	// Newest first, and pages do not overlap.
	p1 := s.notes(t, alphaHost, plain, "?pageSize=2")
	p3 := s.notes(t, alphaHost, plain, "?pageSize=2&page=3")
	if p1.Page.Total != 5 || len(p1.Items) != 2 || p1.Items[0].Title != "title 5" || p1.Items[1].Title != "title 4" {
		t.Fatalf("page 1 = %+v", p1)
	}
	if len(p3.Items) != 1 || p3.Items[0].Title != "title 1" {
		t.Fatalf("page 3 = %+v", p3)
	}

	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+ids[4]+"/read", plain, ""), http.StatusNoContent, "")
	// The unread count covers everything unread, not just the page or the filter.
	all := s.notes(t, alphaHost, plain, "?pageSize=1")
	if all.UnreadCount != 4 || all.Page.Total != 5 || all.Items[0].ReadAt == nil {
		t.Fatalf("after one read: %+v", all)
	}
	un := s.notes(t, alphaHost, plain, "?unread=true")
	if un.Page.Total != 4 || len(un.Items) != 4 || un.UnreadCount != 4 || un.Items[0].Title != "title 4" {
		t.Fatalf("unread filter: %+v", un)
	}
	for _, q := range []string{"?page=0", "?pageSize=0", "?pageSize=1000", "?page=-1"} {
		want(t, s.do(http.MethodGet, alphaHost, "/me/notifications"+q, plain, ""), http.StatusBadRequest, "validation_failed")
	}
}

func TestNotificationsReadIsIdempotent(t *testing.T) {
	s := newUsersStack(t)
	plain := s.signIn(t, alphaHost, "plain", password)
	ids := s.seedNotes(t, s.tenant["alpha"], s.ids["plain"], 3)
	read := func(id string) string {
		var r *time.Time
		if err := s.adm.QueryRow(bg, `SELECT read_at FROM notifications WHERE id = $1::uuid`, id).Scan(&r); err != nil || r == nil {
			t.Fatalf("read_at of %s = %v, %v", id, r, err)
		}
		return r.String()
	}

	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+ids[0]+"/read", plain, ""), http.StatusNoContent, "")
	first := read(ids[0])
	time.Sleep(20 * time.Millisecond)
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+ids[0]+"/read", plain, ""), http.StatusNoContent, "")
	if read(ids[0]) != first {
		t.Fatal("a second read moved the read time")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+ids[1]+"/read", plain, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/read-all", plain, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/read-all", plain, ""), http.StatusNoContent, "")
	if got := s.notes(t, alphaHost, plain, ""); got.UnreadCount != 0 {
		t.Fatalf("unread = %d", got.UnreadCount)
	}
	if read(ids[0]) != first {
		t.Fatal("read-all moved an earlier read time")
	}
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/00000000-0000-0000-0000-000000000000/read", plain, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/not-a-uuid/read", plain, ""), http.StatusBadRequest, "")
}

func TestASupportVisitCannotMarkNotificationsRead(t *testing.T) {
	s := newUsersStack(t)
	ids := s.seedNotes(t, s.tenant["alpha"], s.ids["plain"], 1)
	tok := s.redeem(t, alphaHost, s.startVisit(t, s.platformToken(t), s.tenant["alpha"]).Code)
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/"+ids[0]+"/read", tok, ""), http.StatusForbidden, "read_only_visit")
	want(t, s.do(http.MethodPost, alphaHost, "/me/notifications/read-all", tok, ""), http.StatusForbidden, "read_only_visit")
	var unread int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM notifications WHERE read_at IS NULL`).Scan(&unread); err != nil || unread != 1 {
		t.Fatalf("unread = %d, %v", unread, err)
	}
}

func TestAdminResetsNotifyTheUser(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	plain := s.signIn(t, alphaHost, "plain", password)
	id := s.ids["plain"]

	want(t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-mfa", admin, ""), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, ""), http.StatusOK, "")
	// Both resets sign the user out; the notices wait for the next sign-in.
	plain = s.signIn(t, alphaHost, "plain", password)
	got := s.notes(t, alphaHost, plain, "")
	kinds := map[string]bool{}
	for _, it := range got.Items {
		kinds[it.Kind] = true
	}
	if got.Page.Total != 2 || !kinds["user.password_reset"] || !kinds["user.mfa_reset"] {
		t.Fatalf("notifications after resets = %+v", got)
	}
	// Nobody else is told, and a refused reset (self) tells nobody.
	if got := s.notes(t, alphaHost, admin, ""); got.Page.Total != 0 {
		t.Fatalf("admin was notified: %+v", got)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+s.ids["admin"]+"/reset-mfa", admin, ""), http.StatusConflict, "self_action")
	if got := s.notes(t, alphaHost, admin, ""); got.Page.Total != 0 {
		t.Fatalf("a refused reset left a notification: %+v", got)
	}
}

func TestNotifyInsertsInTheCallersTransaction(t *testing.T) {
	s := newUsersStack(t)
	ctx := tenancy.WithTenant(context.Background(), tenancy.Tenant{ID: s.tenant["alpha"], Code: "alpha", Status: "live"})
	n := notify.Notification{Kind: "test.kind", Title: "Hello", Link: "/students"}
	count := func() int {
		var c int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM notifications`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}

	// A rolled-back transaction leaves nothing behind.
	err := tenancy.WithTenantTx(ctx, s.app, func(ctx context.Context, tx pgx.Tx) error {
		if err := notify.Notify(ctx, tx, []string{s.ids["plain"]}, n); err != nil {
			return err
		}
		return fmt.Errorf("roll back")
	})
	if err == nil || count() != 0 {
		t.Fatalf("rollback: err=%v rows=%d", err, count())
	}
	// Duplicated recipients get one each; an invalid notification inserts nothing.
	if err := tenancy.WithTenantTx(ctx, s.app, func(ctx context.Context, tx pgx.Tx) error {
		return notify.Notify(ctx, tx, []string{s.ids["plain"], s.ids["plain"], s.ids["admin"]}, n)
	}); err != nil || count() != 2 {
		t.Fatalf("commit: err=%v rows=%d", err, count())
	}
	if err := tenancy.WithTenantTx(ctx, s.app, func(ctx context.Context, tx pgx.Tx) error {
		return notify.Notify(ctx, tx, []string{s.ids["plain"]}, notify.Notification{Kind: "k", Title: "t", Link: "https://x.test"})
	}); err == nil || count() != 2 {
		t.Fatalf("invalid link: err=%v rows=%d", err, count())
	}
	// A recipient from another institute is refused by the database.
	if err := tenancy.WithTenantTx(ctx, s.app, func(ctx context.Context, tx pgx.Tx) error {
		return notify.Notify(ctx, tx, []string{s.ids["betaadmin"]}, n)
	}); err == nil || count() != 2 {
		t.Fatalf("cross-institute recipient: err=%v rows=%d", err, count())
	}
}

func TestPurgeReadKeepsRecentAndUnread(t *testing.T) {
	s := newUsersStack(t)
	ids := s.seedNotes(t, s.tenant["alpha"], s.ids["plain"], 4)
	betaIDs := s.seedNotes(t, s.tenant["beta"], s.ids["betaadmin"], 1)
	now := time.Now()
	set := func(id string, readAgo time.Duration) {
		if _, err := s.adm.Exec(bg, `UPDATE notifications SET read_at = $2 WHERE id = $1::uuid`, id, now.Add(-readAgo)); err != nil {
			t.Fatal(err)
		}
	}
	set(ids[0], 91*24*time.Hour) // old and read: goes
	set(ids[1], 89*24*time.Hour) // read recently: stays
	// ids[2] unread and old: stays
	if _, err := s.adm.Exec(bg, `UPDATE notifications SET created_at = now() - interval '200 days' WHERE id = $1::uuid`, ids[2]); err != nil {
		t.Fatal(err)
	}
	set(ids[3], 120*24*time.Hour)     // goes
	set(betaIDs[0], 100*24*time.Hour) // another institute's, also goes
	n, err := notify.PurgeRead(bg, s.app, now)
	if err != nil || n != 3 {
		t.Fatalf("purged %d, %v; want 3", n, err)
	}
	var left []string
	rows, _ := s.adm.Query(bg, `SELECT id::text FROM notifications ORDER BY title`)
	defer rows.Close()
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	if len(left) != 2 || left[0] != ids[1] || left[1] != ids[2] {
		t.Fatalf("left = %v, want %v", left, ids[1:3])
	}
}
