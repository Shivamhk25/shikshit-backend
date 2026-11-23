package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"gopkg.in/yaml.v3"

	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/campuses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/chapters"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classsections"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/divisions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/events"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/holidays"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/rooms"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/sections"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/subjectclasses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/subjects"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/terms"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/textbooks"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquirydiscussions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/scopes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/importreg"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/addresses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/guardians"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/links"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/seed"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

// accessStack is the demo institute (seeded roles and modules) with one user per interesting role.
type accessStack struct {
	stack
	adm      *pgx.Conn
	tenantID string
	worker   *jobs.Worker
}

type accessBody struct {
	User        struct{ ID, Name, Tier string } `json:"user"`
	Tenant      struct{ Modules []string }      `json:"tenant"`
	Permissions []string                        `json:"permissions"`
	Scopes      map[string][]string             `json:"scopes"`
	Workspace   struct {
		Home       string `json:"home"`
		Navigation []struct {
			ScreenID string `json:"screenId"`
		} `json:"navigation"`
	} `json:"workspace"`
	Mobile    struct{ Tabs, Features []string } `json:"mobile"`
	Approvals struct{ CanApprove []string }     `json:"approvals"`
	FieldMask map[string][]string               `json:"fieldMasks"`
	Version   int                               `json:"version"`
}

func newAccessStack(t *testing.T) accessStack {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	t.Cleanup(func() { _ = adm.Close(bg) })
	res, err := seed.Demo(bg, adm)
	if err != nil {
		t.Fatal(err)
	}
	// approval is off unless a test turns it on, so existing tests can commit their own files
	if _, err := adm.Exec(bg, `UPDATE tenants SET settings = jsonb_set(settings, '{importApprovalRequired}', 'false') WHERE id = $1`, res.TenantID); err != nil {
		t.Fatal(err)
	}
	hash, _ := identity.HashPassword(password)
	// Two custom roles for the import tests: "dean" may import academic data, "intake" has the import permissions but
	// none for any data domain.
	for role, perms := range map[string][]string{
		"dean":   {"onb.imports.view", "onb.imports.import", "onb.imports.commit", "onb.imports.rollback", "onb.academic.view", "onb.academic.import", "onb.academic.commit", "onb.academic.rollback"},
		"intake": {"onb.imports.view", "onb.imports.import", "onb.imports.commit", "onb.imports.rollback"},
		"viewer": {"onb.imports.view", "onb.imports.commit", "onb.academic.view"},
		// can approve imports in general and see academic files, but has no review right for the academic domain
		// can read the approval setting but not change it
		"auditor": {"onb.maker_checker.view"},
		"checker": {"onb.imports.view", "onb.imports.review", "onb.academic.view"},
		// can approve academic files and nothing else about imports
		"reviewer": {"onb.imports.view", "onb.imports.review", "onb.academic.view", "onb.academic.review"},
	} {
		var rid string
		if err := adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, res.TenantID, role).Scan(&rid); err != nil {
			t.Fatal(err)
		}
		for _, p := range perms {
			if _, err := adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code) VALUES ($1, $2, $3)`, res.TenantID, rid, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, u := range []struct{ name, role string }{{"boss", "admin"}, {"clerk", "registrar"}, {"nobody", ""}, {"tina", "teacher"}, {"dean", "dean"}, {"intake", "intake"}, {"viewer", "viewer"}, {"pria", "principal"}, {"chet", "checker"}, {"rina", "reviewer"}, {"ada", "auditor"}, {"acct", "accountant"}} {
		var id string
		if err := adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash)
			VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, res.TenantID, u.name, hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if u.role != "" {
			if _, err := adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id)
				SELECT $1, $2, id FROM roles WHERE tenant_id = $1 AND code = $3`, res.TenantID, id, u.role); err != nil {
				t.Fatal(err)
			}
		}
	}

	cfg, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	signer, _ := identity.NewSigner([]byte(strings.Repeat("k", 32)), "shikshit", 15*time.Minute)
	svc, err := identity.NewService(pool, signer, identity.DefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	demo := tenancy.Tenant{ID: res.TenantID, Code: "demo", Name: "Demo Public School", Status: "live", Brand: map[string]any{"primary": "#2F5BFF"}}

	e := echo.New()
	e.HTTPErrorHandler = apiv1.ErrorHandler
	e.Use(tenancy.Middleware(tenancy.MiddlewareConfig{
		Config:   tenancy.Config{BaseDomains: []string{"shikshit.test"}},
		Resolver: tenantsByCode{"demo": demo},
	}))
	apiv1.Register(e, apiv1.New("test", apiv1.WithAuth(svc), apiv1.WithAccess(&authz.Cache{Next: authz.Service{DB: pool}}), apiv1.WithRooms(rooms.Service{DB: pool}),
		apiv1.WithAcademicYears(years.Service{DB: pool}), apiv1.WithAcademicTerms(terms.Service{DB: pool}), apiv1.WithSchoolClasses(classes.Service{DB: pool}),
		apiv1.WithSections(sections.Service{DB: pool}), apiv1.WithSubjects(subjects.Service{DB: pool}),
		apiv1.WithSchoolDivisions(divisions.Service{DB: pool}), apiv1.WithCampuses(campuses.Service{DB: pool}), apiv1.WithHolidays(holidays.Service{DB: pool}),
		apiv1.WithCalendarEvents(events.Service{DB: pool}), apiv1.WithElectiveGroups(electives.Service{DB: pool}),
		apiv1.WithClassSectionMappings(classsections.Service{DB: pool}), apiv1.WithSubjectClassMappings(subjectclasses.Service{DB: pool}), apiv1.WithTextbooks(textbooks.Service{DB: pool}), apiv1.WithTextbookChapters(chapters.Service{DB: pool}), apiv1.WithStudents(students.Service{DB: pool}), apiv1.WithStudentEnrollments(enrollments.Service{DB: pool}), apiv1.WithGuardians(guardians.Service{DB: pool}), apiv1.WithStudentGuardians(links.Service{DB: pool}), apiv1.WithStudentAddresses(addresses.Service{DB: pool}), apiv1.WithEnquiries(enquiries.Service{DB: pool}), apiv1.WithEnquiryDiscussions(enquirydiscussions.Service{DB: pool}), apiv1.WithScopeAssignments(scopes.Service{DB: pool}), apiv1.WithStudentRegistrations(registrations.Service{DB: pool}),
		apiv1.WithImports(importer.Service{DB: pool, Reg: importreg.Registry(pool), Packs: importreg.Packs()}, importreg.Registry(pool))))
	worker := &jobs.Worker{DB: pool, Handlers: importer.Service{DB: pool, Reg: importreg.Registry(pool), Packs: importreg.Packs()}.Handlers()}
	return accessStack{stack: stack{e: e, signer: signer}, adm: adm, tenantID: res.TenantID, worker: worker}
}

func (s accessStack) signIn(t *testing.T, user string) tokenBody {
	t.Helper()
	rec := s.post("demo.shikshit.test", "/auth/login", login(user, password))
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s = %d %s", user, rec.Code, rec.Body)
	}
	return decode[tokenBody](t, rec)
}

func (s accessStack) get(path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, apiv1.BasePath+path, nil)
	req.Host = "demo.shikshit.test"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

func (s accessStack) access(t *testing.T, user string) (accessBody, *httptest.ResponseRecorder) {
	t.Helper()
	rec := s.get("/me/access", s.signIn(t, user).AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me/access for %s = %d %s", user, rec.Code, rec.Body)
	}
	return decode[accessBody](t, rec), rec
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestAdminSeesEverythingInTheEnabledModules(t *testing.T) {
	s := newAccessStack(t)
	got, rec := s.access(t, "boss")
	if got.User.Tier != "tenant" || got.Workspace.Home != "admin" {
		t.Errorf("user/home = %+v / %q", got.User, got.Workspace.Home)
	}
	for _, want := range []string{"iam.users.create", "iam.roles.view", "onb.control_tower.view", "wfl.requests.approve"} {
		if !contains(got.Permissions, want) {
			t.Errorf("admin lacks %s", want)
		}
	}
	if !contains(got.Tenant.Modules, "onb") {
		t.Errorf("modules = %v", got.Tenant.Modules)
	}
	var ids []string
	for _, n := range got.Workspace.Navigation {
		ids = append(ids, n.ScreenID)
	}
	if strings.Join(ids, ",") != "home,onboarding,rooms,academic_years,academic_terms,classes,sections,subjects,school_divisions,campuses,holidays,calendar_events,elective_groups,class_section_mappings,subject_class_mappings,textbooks,textbook_chapters,enquiries,enquiry_discussions,registrations,students,student_enrollments,guardians,student_guardians,student_addresses,scope_assignments,users,roles,parent_links,appearance,approvals,profile" || strings.Join(got.Mobile.Tabs, ",") != "home,profile" {
		t.Errorf("navigation %v tabs %v", ids, got.Mobile.Tabs)
	}
	if got.Version < 1 || rec.Header().Get("X-Access-Version") == "" {
		t.Errorf("version %d header %q", got.Version, rec.Header().Get("X-Access-Version"))
	}
	if got.FieldMask == nil || got.Approvals.CanApprove == nil || got.Mobile.Features == nil {
		t.Error("empty collections must encode as [] / {} not null")
	}
}

func TestLimitedRoleSeesOnlyItsOwnPermissions(t *testing.T) {
	s := newAccessStack(t)
	got, _ := s.access(t, "clerk")
	if got.Workspace.Home != "registrar" {
		t.Errorf("home = %q", got.Workspace.Home)
	}
	if !contains(got.Permissions, "onb.workspace.view") || !contains(got.Permissions, "onb.students.import") {
		t.Errorf("registrar lacks its onboarding permissions: %v", got.Permissions)
	}
	for _, forbidden := range []string{"iam.users.create", "onb.control_tower.view", "iam.roles.view"} {
		if contains(got.Permissions, forbidden) {
			t.Errorf("registrar must not hold %s", forbidden)
		}
	}
	if got.Scopes["onb.workspace"][0] != "tenant" {
		t.Errorf("scopes = %v", got.Scopes)
	}
}

func TestUserWithoutRolesGetsNoPermissionsAndOnlyBasicScreens(t *testing.T) {
	s := newAccessStack(t)
	got, _ := s.access(t, "nobody")
	if len(got.Permissions) != 0 || got.Workspace.Home != "member" {
		t.Errorf("permissions %v home %q", got.Permissions, got.Workspace.Home)
	}
	if len(got.Workspace.Navigation) != 3 { // home, approvals (only own requests and what they may decide) and profile need no permission
		t.Errorf("navigation = %+v", got.Workspace.Navigation)
	}
}

func TestDenyOverrideAndVersionBump(t *testing.T) {
	s := newAccessStack(t)
	before, _ := s.access(t, "boss")
	if _, err := s.adm.Exec(bg, `INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, reason)
		SELECT tenant_id, id, 'iam.users.delete', 'deny', 'test' FROM users WHERE username = 'boss'`); err != nil {
		t.Fatal(err)
	}
	after, rec := s.access(t, "boss")
	if contains(after.Permissions, "iam.users.delete") || !contains(after.Permissions, "iam.users.create") {
		t.Fatalf("deny not applied precisely: %v", after.Permissions)
	}
	if after.Version <= before.Version {
		t.Errorf("version %d -> %d: a grant change must raise it so clients refetch", before.Version, after.Version)
	}
	if rec.Header().Get("X-Access-Version") == "" {
		t.Error("missing X-Access-Version")
	}
}

func TestExpiredOverrideAndExpiredRoleAreIgnored(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `INSERT INTO user_permission_overrides (tenant_id, user_id, permission_code, effect, expires_at, reason)
		SELECT tenant_id, id, 'iam.users.create', 'deny', now() - interval '1 hour', 'expired' FROM users WHERE username = 'boss'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.access(t, "boss"); !contains(got.Permissions, "iam.users.create") {
		t.Error("an expired deny still applied")
	}
	if _, err := s.adm.Exec(bg, `UPDATE user_roles SET starts_at = now() - interval '2 days', ends_at = now() - interval '1 day'
		WHERE user_id = (SELECT id FROM users WHERE username = 'clerk')`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.access(t, "clerk"); len(got.Permissions) != 0 {
		t.Errorf("an expired role still grants %v", got.Permissions)
	}
}

func TestDisablingAModuleRemovesItEverywhere(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `UPDATE tenant_modules SET enabled = false WHERE tenant_id = $1 AND module_code = 'onb'`, s.tenantID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.access(t, "boss")
	for _, p := range got.Permissions {
		if strings.HasPrefix(p, "onb.") {
			t.Fatalf("%s survived the module being disabled", p)
		}
	}
	for _, n := range got.Workspace.Navigation {
		if n.ScreenID == "onboarding" {
			t.Error("onboarding screen still offered")
		}
	}
	if contains(got.Tenant.Modules, "onb") {
		t.Error("module still listed")
	}
}

func TestScopeAssignmentsAppearAsRecords(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `UPDATE role_permissions SET scope = 'class_section'
		WHERE permission_code = 'onb.students.*' AND role_id IN (SELECT id FROM roles WHERE code = 'registrar')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref)
		SELECT tenant_id, id, 'class_section', '12' FROM users WHERE username = 'clerk'`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.access(t, "clerk")
	if v := got.Scopes["onb.students"]; len(v) != 1 || v[0] != "class_section:12" {
		t.Fatalf("scopes = %v", got.Scopes)
	}
}

func TestAccessRequiresAValidLiveToken(t *testing.T) {
	s := newAccessStack(t)
	tok := s.signIn(t, "boss")

	for name, hdr := range map[string]string{
		"no header":     "",
		"wrong scheme":  "Basic abc",
		"empty bearer":  "Bearer ",
		"garbage":       "Bearer not.a.jwt",
		"refresh token": "Bearer " + tok.RefreshToken,
	} {
		req := httptest.NewRequest(http.MethodGet, apiv1.BasePath+"/me/access", nil)
		req.Host = "demo.shikshit.test"
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || decode[problemBody](t, rec).Code != "unauthenticated" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	if rec := s.get("/me/access", tok.AccessToken); rec.Code != 200 {
		t.Fatalf("valid token = %d", rec.Code)
	}
	// Logging out kills the access token immediately, not after its 15 minutes.
	if rec := s.post("demo.shikshit.test", "/auth/logout", `{"refreshToken":"`+tok.RefreshToken+`"}`); rec.Code != 204 {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := s.get("/me/access", tok.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("access token after logout = %d, want 401", rec.Code)
	}
}

func TestAccessTokenSurvivesRefreshRotationOfItsSession(t *testing.T) {
	s := newAccessStack(t)
	tok := s.signIn(t, "boss")
	if rec := s.post("demo.shikshit.test", "/auth/refresh", `{"refreshToken":"`+tok.RefreshToken+`"}`); rec.Code != 200 {
		t.Fatalf("refresh = %d", rec.Code)
	}
	if rec := s.get("/me/access", tok.AccessToken); rec.Code != 200 {
		t.Errorf("token issued before a rotation = %d, want 200 until it expires", rec.Code)
	}
}

func TestRefreshReuseKillsOutstandingAccessTokens(t *testing.T) {
	s := newAccessStack(t)
	tok := s.signIn(t, "boss")
	s.post("demo.shikshit.test", "/auth/refresh", `{"refreshToken":"`+tok.RefreshToken+`"}`)
	s.post("demo.shikshit.test", "/auth/refresh", `{"refreshToken":"`+tok.RefreshToken+`"}`) // replay
	if rec := s.get("/me/access", tok.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("after reuse = %d, want 401", rec.Code)
	}
}

func TestDisabledUserLosesAccessAtOnce(t *testing.T) {
	s := newAccessStack(t)
	tok := s.signIn(t, "clerk")
	if _, err := s.adm.Exec(bg, `UPDATE users SET status = 'disabled' WHERE username = 'clerk'`); err != nil {
		t.Fatal(err)
	}
	if rec := s.get("/me/access", tok.AccessToken); rec.Code != http.StatusUnauthorized {
		t.Errorf("disabled user = %d, want 401", rec.Code)
	}
}

func TestTokenOfAnotherInstituteIsRejected(t *testing.T) {
	s := newAccessStack(t)
	// A correctly signed token that names a different institute must not authenticate against this host.
	other, _, err := s.signer.Issue("00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-0000000000aa", "tenant", "00000000-0000-0000-0000-000000000002")
	if err != nil {
		t.Fatal(err)
	}
	if rec := s.get("/me/access", other); rec.Code != http.StatusUnauthorized {
		t.Errorf("foreign-tenant token = %d", rec.Code)
	}
}

// Every operation is protected unless the contract marks it x-public. This ties the middleware's list to the spec so
// a new operation can never ship unauthenticated by accident.
func TestPublicOperationsMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi/openapi.bundled.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	got := apiv1.PublicOperations()
	seen := 0
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				continue
			}
			seen++
			public, _ := op["x-public"].(bool)
			auth, _ := op["x-authenticated"].(bool)
			perm, _ := op["x-permission"].(string)
			platform, _ := op["x-platform"].(bool)
			name := strings.ToUpper(id[:1]) + id[1:]
			if public != got[name] {
				t.Errorf("%s %s (%s): contract x-public=%v but middleware public=%v", strings.ToUpper(method), path, name, public, got[name])
			}
			if !public && !auth && perm == "" && !platform {
				t.Errorf("%s %s declares no access rule", method, path)
			}
		}
	}
	if seen < 4 {
		t.Fatalf("only %d operations found in the contract", seen)
	}
}

// With the full catalog, a wildcard grant in a role template (teacher: att.*.* on class sections) turns into concrete
// permissions whose scope is the classes the teacher is assigned to.
func TestTeacherGetsAttendancePermissionsScopedToAssignedClasses(t *testing.T) {
	s := newAccessStack(t)
	if _, err := s.adm.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref)
		SELECT tenant_id, id, 'class_section', v.ref FROM users, (VALUES ('12'), ('14')) AS v(ref) WHERE username = 'tina'`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.access(t, "tina")
	if !hasPrefix(got.Permissions, "att.") {
		t.Fatalf("teacher has no attendance permissions: %v", got.Permissions)
	}
	var scoped []string
	for res, list := range got.Scopes {
		if strings.HasPrefix(res, "att.") {
			scoped = list
			break
		}
	}
	if strings.Join(scoped, ",") != "class_section:12,class_section:14" {
		t.Errorf("attendance scopes = %v", got.Scopes)
	}
	if hasPrefix(got.Permissions, "fee.") || hasPrefix(got.Permissions, "iam.") {
		t.Errorf("teacher must not hold fee or iam permissions: %v", got.Permissions)
	}
}

func hasPrefix(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
