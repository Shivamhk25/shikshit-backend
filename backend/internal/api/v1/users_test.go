package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"

	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/roles"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

// usersStack is the real HTTP stack (host tenancy, sign-in, access engine, users service) over an embedded
// Postgres with two institutes. In alpha: "admin" (Admin role), "manager" (may manage users but is not an Admin)
// and "plain" (no roles). In beta: "betaadmin".
type usersStack struct {
	e      *echo.Echo
	adm    *pgx.Conn
	ids    map[string]string // username -> user id
	tenant map[string]string // code -> tenant id
}

const (
	alphaHost = "alpha.shikshit.test"
	betaHost  = "beta.shikshit.test"
)

func newUsersStack(t *testing.T) usersStack {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	t.Cleanup(func() { _ = adm.Close(bg) })
	s := usersStack{adm: adm, ids: map[string]string{}, tenant: map[string]string{}}

	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := adm.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	addUser := func(tenantID, username string) string {
		t.Helper()
		var id string
		if err := adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash)
			VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, tenantID, username, hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		s.ids[username] = id
		return id
	}
	resolver := tenantsByCode{}
	for _, code := range []string{"alpha", "beta"} {
		var id string
		if err := adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text`, code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tx, err := adm.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `SELECT set_config('app.actor_tier', 'platform', true)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `SELECT provision_tenant_roles($1::uuid)`, id); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(bg); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'iam'), ($1, 'stu')`, id)
		s.tenant[code] = id
		resolver[code] = tenancy.Tenant{ID: id, Code: code, Name: code, Status: "live"}
	}
	alpha, beta := s.tenant["alpha"], s.tenant["beta"]
	s.grantAdmin(t, addUser(alpha, "admin"))
	s.grantAdmin(t, addUser(beta, "betaadmin"))
	addUser(alpha, "plain")
	manager := addUser(alpha, "manager")
	var roleID string
	if err := adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'user_manager', 'User manager') RETURNING id::text`, alpha).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"iam.users.view", "iam.users.create", "iam.users.edit"} {
		exec(`INSERT INTO role_permissions (tenant_id, role_id, permission_code) VALUES ($1, $2::uuid, $3)`, alpha, roleID, p)
	}
	exec(`INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, alpha, manager, roleID)

	// "rolemgr" may manage roles and users, and sees students only in one assigned class section.
	rolemgr := addUser(alpha, "rolemgr")
	var mgrRole string
	if err := adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'role_manager', 'Role manager') RETURNING id::text`, alpha).Scan(&mgrRole); err != nil {
		t.Fatal(err)
	}
	for _, g := range []struct{ code, scope string }{{"iam.roles.view", "tenant"}, {"iam.roles.create", "tenant"}, {"iam.roles.edit", "tenant"}, {"iam.roles.delete", "tenant"}, {"iam.users.view", "tenant"}, {"iam.users.edit", "tenant"}, {"stu.students.view", "class_section"}} {
		exec(`INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, alpha, mgrRole, g.code, g.scope)
	}
	exec(`INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, alpha, rolemgr, mgrRole)
	exec(`INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', 'cs-1')`, alpha, rolemgr)

	cfg, err := pgxpool.ParseConfig(d.AppDSN)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	signer, _ := identity.NewSigner([]byte(strings.Repeat("k", 32)), "shikshit", 15*time.Minute)
	auth, err := identity.NewService(pool, signer, identity.DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	s.e = echo.New()
	s.e.HTTPErrorHandler = apiv1.ErrorHandler
	s.e.Use(tenancy.Middleware(tenancy.MiddlewareConfig{Config: tenancy.Config{BaseDomains: []string{"shikshit.test"}}, Resolver: resolver}))
	apiv1.Register(s.e, apiv1.New("test", apiv1.WithAuth(auth), apiv1.WithAccess(&authz.Cache{Next: authz.Service{DB: pool}}),
		apiv1.WithUsers(users.Service{DB: pool}), apiv1.WithRoles(roles.Service{DB: pool})))
	return s
}

func (s usersStack) grantAdmin(t *testing.T, userID string) {
	t.Helper()
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id)
		SELECT u.tenant_id, u.id, r.id FROM users u JOIN roles r ON r.tenant_id = u.tenant_id AND r.code = 'admin'
		WHERE u.id = $1::uuid`, userID); err != nil {
		t.Fatal(err)
	}
}

func (s usersStack) do(method, host, path, token, body string) *httptest.ResponseRecorder {
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, apiv1.BasePath+path, r)
	req.Host = host
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

func (s usersStack) signIn(t *testing.T, host, user, pass string) string {
	t.Helper()
	rec := s.do(http.MethodPost, host, "/auth/login", "", login(user, pass))
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s = %d %s", user, rec.Code, rec.Body)
	}
	return decode[tokenBody](t, rec).AccessToken
}

type userOut struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	InviteLink string `json:"inviteLink"`
	ResetLink  string `json:"resetLink"`
}

type problemOut struct {
	Code string `json:"code"`
}

func want(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if code != "" {
		if got := decode[problemOut](t, rec).Code; got != code {
			t.Fatalf("code = %q, want %q: %s", got, code, rec.Body)
		}
	}
}

func tokenOf(link string) string { return link[strings.Index(link, "token=")+len("token="):] }

func accept(token, pass string) string {
	return `{"token":"` + token + `","password":"` + pass + `"}`
}

func TestUsersAccess(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	plain := s.signIn(t, alphaHost, "plain", password)
	beta := s.signIn(t, betaHost, "betaadmin", password)

	want(t, s.do(http.MethodGet, alphaHost, "/users", "", ""), http.StatusUnauthorized, "")
	want(t, s.do(http.MethodGet, alphaHost, "/users", plain, ""), http.StatusForbidden, "")
	want(t, s.do(http.MethodPost, alphaHost, "/users", plain, `{"username":"x1x","displayName":"X"}`), http.StatusForbidden, "")

	rec := s.do(http.MethodGet, alphaHost, "/users?q=PLA", admin, "")
	want(t, rec, http.StatusOK, "")
	if body := rec.Body.String(); !strings.Contains(body, `"plain"`) || strings.Contains(body, "betaadmin") || strings.Contains(body, "password") {
		t.Fatalf("search body = %s", body)
	}
	// Another institute's user does not exist from here, even for that institute's Admin.
	want(t, s.do(http.MethodGet, betaHost, "/users/"+s.ids["plain"], beta, ""), http.StatusNotFound, "")
	// Reads of user records are audited.
	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'users.list'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("users.list audit rows = %d, %v", n, err)
	}
}

func TestUsersInviteAndAccept(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)

	rec := s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"teacher1","displayName":"Asha Rao","email":"asha@school.test"}`)
	want(t, rec, http.StatusCreated, "")
	u := decode[userOut](t, rec)
	if u.Status != "invited" || !strings.HasPrefix(u.InviteLink, users.AcceptPath+"?token=") {
		t.Fatalf("created = %+v", u)
	}
	token := tokenOf(u.InviteLink)

	// Unique per institute, case-insensitively, for username and email.
	want(t, s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"TEACHER1","displayName":"Other"}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"teacher2","displayName":"Other","email":"ASHA@school.test"}`), http.StatusBadRequest, "validation_failed")
	// The same username is free in another institute.
	beta := s.signIn(t, betaHost, "betaadmin", password)
	want(t, s.do(http.MethodPost, betaHost, "/users", beta, `{"username":"teacher1","displayName":"Beta teacher"}`), http.StatusCreated, "")

	// An invited user cannot sign in yet.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/login", "", login("teacher1", "a long enough password")), http.StatusUnauthorized, "")

	// The token never reaches the audit trail.
	var leaked int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE after::text LIKE '%' || $1 || '%'`, token).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("token in audit rows = %d, %v", leaked, err)
	}

	// The token works only on its own institute's host.
	want(t, s.do(http.MethodPost, betaHost, "/auth/invite/accept", "", accept(token, "a long enough password")), http.StatusBadRequest, "invalid_token")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(token, "short")), http.StatusBadRequest, "")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(token, "a long enough password")), http.StatusOK, "")
	s.signIn(t, alphaHost, "teacher1", "a long enough password")

	// A token works once; garbage gets the same answer.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(token, "another long password")), http.StatusBadRequest, "invalid_token")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept("zz", "another long password")), http.StatusBadRequest, "invalid_token")
}

func TestUsersResetPassword(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	plain := s.signIn(t, alphaHost, "plain", password)
	id := s.ids["plain"]

	first := decode[userOut](t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, ""))
	rec := s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, "")
	want(t, rec, http.StatusOK, "")
	second := decode[userOut](t, rec)

	// Reset signs the user out everywhere.
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", plain, ""), http.StatusUnauthorized, "")
	// A newer link cancels the older one.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(tokenOf(first.ResetLink), "brand new password")), http.StatusBadRequest, "invalid_token")
	// An expired link is refused.
	if _, err := s.adm.Exec(bg, `UPDATE user_invites SET expires_at = now() - interval '1 minute' WHERE used_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(tokenOf(second.ResetLink), "brand new password")), http.StatusBadRequest, "invalid_token")

	third := decode[userOut](t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, ""))
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(tokenOf(third.ResetLink), "brand new password")), http.StatusOK, "")
	s.signIn(t, alphaHost, "plain", "brand new password")

	// A disabled user cannot get a reset link (reactivate first), and a link issued before disabling stops working.
	fourth := decode[userOut](t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, ""))
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/deactivate", admin, ""), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+id+"/reset-password", admin, ""), http.StatusConflict, "invalid_state")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(tokenOf(fourth.ResetLink), "yet another password")), http.StatusBadRequest, "invalid_token")
}

func TestUsersStatusRules(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	manager := s.signIn(t, alphaHost, "manager", password)
	plain := s.signIn(t, alphaHost, "plain", password)
	post := func(token, id, action string) *httptest.ResponseRecorder {
		return s.do(http.MethodPost, alphaHost, "/users/"+id+"/"+action, token, "")
	}

	// Nobody can disable or lock themselves.
	want(t, post(manager, s.ids["manager"], "deactivate"), http.StatusConflict, "self_action")
	want(t, post(manager, s.ids["manager"], "lock"), http.StatusConflict, "self_action")

	// The last active Admin can be neither disabled nor locked, even by someone allowed to manage users.
	want(t, post(manager, s.ids["admin"], "deactivate"), http.StatusConflict, "last_admin")
	want(t, post(manager, s.ids["admin"], "lock"), http.StatusConflict, "last_admin")
	// An Admin who is not active does not count as the second Admin.
	s.grantAdmin(t, s.ids["plain"])
	if _, err := s.adm.Exec(bg, `UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, s.ids["plain"]); err != nil {
		t.Fatal(err)
	}
	want(t, post(manager, s.ids["admin"], "deactivate"), http.StatusConflict, "last_admin")
	if _, err := s.adm.Exec(bg, `UPDATE users SET status = 'active' WHERE id = $1::uuid`, s.ids["plain"]); err != nil {
		t.Fatal(err)
	}
	// With a second active Admin it is allowed.
	want(t, post(manager, s.ids["plain"], "lock"), http.StatusOK, "")
	want(t, post(manager, s.ids["plain"], "unlock"), http.StatusOK, "")

	// Lock: the user is signed out and cannot sign in; unlock lets them back in, but not with an old session.
	oldRefresh := decode[tokenBody](t, s.do(http.MethodPost, alphaHost, "/auth/login", "", login("manager", password))).RefreshToken
	want(t, post(admin, s.ids["manager"], "lock"), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", manager, ""), http.StatusUnauthorized, "")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/login", "", login("manager", password)), http.StatusUnauthorized, "")
	want(t, post(admin, s.ids["manager"], "lock"), http.StatusConflict, "invalid_state")
	want(t, post(admin, s.ids["manager"], "reset-password"), http.StatusConflict, "invalid_state")
	want(t, post(admin, s.ids["manager"], "unlock"), http.StatusOK, "")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/refresh", "", `{"refreshToken":"`+oldRefresh+`"}`), http.StatusUnauthorized, "")
	s.signIn(t, alphaHost, "manager", password)

	// Unlock and reactivate never turn a disabled user back on by the side door.
	want(t, post(admin, s.ids["plain"], "deactivate"), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", plain, ""), http.StatusUnauthorized, "")
	want(t, post(admin, s.ids["plain"], "unlock"), http.StatusConflict, "invalid_state")
	want(t, post(admin, s.ids["plain"], "deactivate"), http.StatusConflict, "invalid_state")
	rec := post(admin, s.ids["plain"], "reactivate")
	want(t, rec, http.StatusOK, "")
	if got := decode[userOut](t, rec).Status; got != "active" {
		t.Fatalf("reactivated status = %q", got)
	}
	want(t, post(admin, s.ids["plain"], "reactivate"), http.StatusConflict, "invalid_state")

	// A never-signed-in user comes back as invited, not active.
	invited := decode[userOut](t, s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"newbie","displayName":"New"}`))
	want(t, post(admin, invited.ID, "deactivate"), http.StatusOK, "")
	if got := decode[userOut](t, post(admin, invited.ID, "reactivate")).Status; got != "invited" {
		t.Fatalf("reactivated invited user status = %q", got)
	}

	// Every change is audited.
	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action IN ('users.lock','users.unlock','users.deactivate','users.reactivate')`).Scan(&n); err != nil || n < 6 {
		t.Fatalf("status audit rows = %d, %v", n, err)
	}
}

func TestUsersUpdateAndPersonLink(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	id := s.ids["plain"]
	patch := func(body string) *httptest.ResponseRecorder {
		return s.do(http.MethodPatch, alphaHost, "/users/"+id, admin, body)
	}
	want(t, patch(`{"displayName":"Plain Person","phone":"9876543210"}`), http.StatusOK, "")
	want(t, patch(`{"email":"not-an-email"}`), http.StatusBadRequest, "validation_failed")
	// Type and person come together, and the person must exist in this institute.
	want(t, patch(`{"personType":"student"}`), http.StatusBadRequest, "validation_failed")
	want(t, patch(`{"personType":"student","personId":"`+s.ids["admin"]+`"}`), http.StatusBadRequest, "validation_failed")
	want(t, s.do(http.MethodPatch, alphaHost, "/users/"+s.ids["betaadmin"], admin, `{"displayName":"x"}`), http.StatusNotFound, "")
}
