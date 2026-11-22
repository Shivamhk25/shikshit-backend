package apiv1_test

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type appearanceOut struct {
	Accent string `json:"accent"`
	Preset string `json:"preset"`
}

type brandOut struct {
	Tenant struct {
		Brand struct {
			Primary string `json:"primary"`
			Preset  string `json:"preset"`
		} `json:"brand"`
	} `json:"tenant"`
}

func TestAnAdminChangesTheInstituteLookAndEveryoneGetsIt(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)

	rec := s.do(http.MethodGet, alphaHost, "/institute/appearance", admin, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[appearanceOut](t, rec); got != (appearanceOut{"#1F3A5F", "classic"}) {
		t.Fatalf("defaults = %+v", got)
	}

	var before int64
	if err := s.adm.QueryRow(bg, `SELECT access_version FROM users WHERE id = $1::uuid`, s.ids["plain"]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec = s.do(http.MethodPut, alphaHost, "/institute/appearance", admin, `{"accent":"#1a7a6d","preset":"modern"}`)
	want(t, rec, http.StatusOK, "")
	if got := decode[appearanceOut](t, rec); got != (appearanceOut{"#1A7A6D", "modern"}) {
		t.Fatalf("saved = %+v", got)
	}
	var after int64
	if err := s.adm.QueryRow(bg, `SELECT access_version FROM users WHERE id = $1::uuid`, s.ids["plain"]).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("access version %d -> %d: other users' apps would not repaint", before, after)
	}

	// Everyone reads the new look in /me/access, straight away (the host lookup in this stack never changes, so
	// this also proves the brand is read fresh).
	plain := s.signIn(t, alphaHost, "plain", password)
	rec = s.do(http.MethodGet, alphaHost, "/me/access", plain, "")
	want(t, rec, http.StatusOK, "")
	if b := decode[brandOut](t, rec).Tenant.Brand; b.Primary != "#1A7A6D" || b.Preset != "modern" {
		t.Fatalf("brand in /me/access = %+v", b)
	}

	// The other institute is untouched.
	beta := s.signIn(t, betaHost, "betaadmin", password)
	rec = s.do(http.MethodGet, betaHost, "/institute/appearance", beta, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[appearanceOut](t, rec); got != (appearanceOut{"#1F3A5F", "classic"}) {
		t.Fatalf("beta changed too: %+v", got)
	}
}

func TestAppearanceRules(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	for _, body := range []string{`{"accent":"navy","preset":"modern"}`, `{"accent":"#12345","preset":"modern"}`, `{"accent":"#1A7A6D","preset":"neon"}`} {
		want(t, s.do(http.MethodPut, alphaHost, "/institute/appearance", admin, body), http.StatusBadRequest, "validation_failed")
	}
	// Without the permission: read and change are both refused.
	manager := s.signIn(t, alphaHost, "manager", password)
	want(t, s.do(http.MethodGet, alphaHost, "/institute/appearance", manager, ""), http.StatusForbidden, "permission_denied")
	want(t, s.do(http.MethodPut, alphaHost, "/institute/appearance", manager, `{"accent":"#1A7A6D","preset":"modern"}`), http.StatusForbidden, "permission_denied")
}

// The database function is the last guard: an institute session changes only its own institute, nothing else can
// call it, and bad values never reach the row.
func TestSetTenantAppearanceChangesOnlyTheCallersInstitute(t *testing.T) {
	s := newUsersStack(t)
	call := func(tenant, tier, accent, preset string) error {
		tx, err := s.adm.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err := tx.Exec(bg, `SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', $2, true)`, tenant, tier); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `SELECT set_tenant_appearance($1::text, $2::text)`, accent, preset); err != nil {
			return err
		}
		return tx.Commit(bg)
	}
	code := func(err error) string {
		if pg, ok := err.(*pgconn.PgError); ok {
			return pg.Code
		}
		return ""
	}
	if err := call(s.tenant["beta"], "tenant", "#7a2e3e", "contrast"); err != nil {
		t.Fatal(err)
	}
	var alpha, beta string
	if err := s.adm.QueryRow(bg, `SELECT brand::text FROM tenants WHERE id = $1`, s.tenant["alpha"]).Scan(&alpha); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `SELECT brand->>'primary' || ' ' || (brand->>'preset') FROM tenants WHERE id = $1`, s.tenant["beta"]).Scan(&beta); err != nil {
		t.Fatal(err)
	}
	if alpha != "{}" || beta != "#7A2E3E contrast" {
		t.Fatalf("alpha brand %s, beta %s", alpha, beta)
	}
	for _, c := range []struct{ tenant, tier, accent, preset, want string }{
		{"", "anonymous", "#7A2E3E", "classic", "42501"},
		{"", "platform", "#7A2E3E", "classic", "42501"},
		{s.tenant["alpha"], "platform", "#7A2E3E", "classic", "42501"}, // platform changes go through Super Admin, not here
		{s.tenant["alpha"], "tenant", "red", "classic", "22023"},
		{s.tenant["alpha"], "tenant", "#7A2E3E", "neon", "22023"},
	} {
		if got := code(call(c.tenant, c.tier, c.accent, c.preset)); got != c.want {
			t.Fatalf("%+v: code %q, want %q", c, got, c.want)
		}
	}
}

func TestEachUserChoosesTheirOwnThemeMode(t *testing.T) {
	s := newUsersStack(t)
	plain := s.signIn(t, alphaHost, "plain", password)
	admin := s.signIn(t, alphaHost, "admin", password)
	mode := func(token string) string {
		t.Helper()
		rec := s.do(http.MethodGet, alphaHost, "/me/preferences", token, "")
		want(t, rec, http.StatusOK, "")
		return decode[struct {
			ThemeMode string `json:"themeMode"`
		}](t, rec).ThemeMode
	}
	if got := mode(plain); got != "system" {
		t.Fatalf("default mode = %q", got)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/me/preferences", plain, `{"themeMode":"dark"}`), http.StatusOK, "")
	if got := mode(plain); got != "dark" {
		t.Fatalf("after saving, mode = %q", got)
	}
	if got := mode(admin); got != "system" {
		t.Fatalf("another user's mode changed to %q", got)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/me/preferences", plain, `{"themeMode":"blue"}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodGet, alphaHost, "/me/preferences", "", ""), http.StatusUnauthorized, "unauthenticated")
}
