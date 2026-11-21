package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/api/problem"
	"github.com/pie360/backend/internal/platform/authz"
	"github.com/pie360/backend/internal/platform/identity"
	"github.com/pie360/backend/internal/platform/scope"
)

// AccessResolver builds a user's effective access (authz.Service satisfies it).
type AccessResolver interface {
	Resolve(ctx context.Context, userID string) (authz.Access, error)
}

// versionedResolver is implemented by resolvers that can reuse a result for the same access version (authz.Cache).
type versionedResolver interface {
	ResolveAt(ctx context.Context, userID string, version int64) (authz.Access, error)
}

func (h *Handler) resolveFor(ctx context.Context, p identity.Principal) (authz.Access, error) {
	if v, ok := h.access.(versionedResolver); ok {
		return v.ResolveAt(ctx, p.UserID, p.AccessVersion)
	}
	return h.access.Resolve(ctx, p.UserID)
}

// publicOperations are the operations that need no bearer token. A contract test keeps this set equal to the
// operations marked x-public in the OpenAPI spec, so a new operation is protected unless the contract says otherwise.
var publicOperations = map[string]bool{
	"GetHealthz":   true,
	"Login":        true,
	"RefreshToken": true,
	"Logout":       true,
}

// PublicOperations returns a copy of the set of operations served without a bearer token (Go operation names).
func PublicOperations() map[string]bool {
	out := make(map[string]bool, len(publicOperations))
	for k, v := range publicOperations {
		out[k] = v
	}
	return out
}

// operationAccess is the permission and module each protected operation needs. A contract test keeps it equal to the
// x-permission / x-module declared in the OpenAPI spec, so the declaration and the enforcement cannot drift apart.
// Operations marked only x-authenticated (any signed-in user) are absent.
var operationAccess = map[string]struct{ Permission, Module string }{
	"ListRooms":                 {"aca.rooms.view", "aca"},
	"ListImportEntities":        {"onb.imports.view", "onb"},
	"GetImportPack":             {"onb.imports.view", "onb"},
	"GetImportSettings":         {"onb.maker_checker.view", "onb"},
	"UpdateImportSettings":      {"onb.maker_checker.manage", "onb"},
	"DownloadImportTemplate":    {"onb.imports.import", "onb"},
	"ListImportBatches":         {"onb.imports.view", "onb"},
	"UploadImport":              {"onb.imports.import", "onb"},
	"GetImportBatch":            {"onb.imports.view", "onb"},
	"ListImportRows":            {"onb.imports.view", "onb"},
	"DownloadImportErrors":      {"onb.imports.view", "onb"},
	"CommitImportBatch":         {"onb.imports.commit", "onb"},
	"RollbackImportBatch":       {"onb.imports.rollback", "onb"},
	"ApproveImportBatch":        {"onb.imports.review", "onb"},
	"RejectImportBatch":         {"onb.imports.review", "onb"},
	"GetRoom":                   {"aca.rooms.view", "aca"},
	"CreateRoom":                {"aca.rooms.create", "aca"},
	"UpdateRoom":                {"aca.rooms.edit", "aca"},
	"DeleteRoom":                {"aca.rooms.delete", "aca"},
	"ListAcademicYears":         {"aca.academic_years.view", "aca"},
	"GetAcademicYear":           {"aca.academic_years.view", "aca"},
	"CreateAcademicYear":        {"aca.academic_years.create", "aca"},
	"UpdateAcademicYear":        {"aca.academic_years.edit", "aca"},
	"DeleteAcademicYear":        {"aca.academic_years.delete", "aca"},
	"ListAcademicTerms":         {"aca.academic_terms.view", "aca"},
	"GetAcademicTerm":           {"aca.academic_terms.view", "aca"},
	"CreateAcademicTerm":        {"aca.academic_terms.create", "aca"},
	"UpdateAcademicTerm":        {"aca.academic_terms.edit", "aca"},
	"DeleteAcademicTerm":        {"aca.academic_terms.delete", "aca"},
	"ListSchoolClasses":         {"aca.classes.view", "aca"},
	"GetSchoolClass":            {"aca.classes.view", "aca"},
	"CreateSchoolClass":         {"aca.classes.create", "aca"},
	"UpdateSchoolClass":         {"aca.classes.edit", "aca"},
	"DeleteSchoolClass":         {"aca.classes.delete", "aca"},
	"ListSections":              {"aca.sections.view", "aca"},
	"GetSection":                {"aca.sections.view", "aca"},
	"CreateSection":             {"aca.sections.create", "aca"},
	"UpdateSection":             {"aca.sections.edit", "aca"},
	"DeleteSection":             {"aca.sections.delete", "aca"},
	"ListSubjects":              {"aca.subjects.view", "aca"},
	"GetSubject":                {"aca.subjects.view", "aca"},
	"CreateSubject":             {"aca.subjects.create", "aca"},
	"UpdateSubject":             {"aca.subjects.edit", "aca"},
	"DeleteSubject":             {"aca.subjects.delete", "aca"},
	"ListSchoolDivisions":       {"aca.school_divisions.view", "aca"},
	"GetSchoolDivision":         {"aca.school_divisions.view", "aca"},
	"CreateSchoolDivision":      {"aca.school_divisions.create", "aca"},
	"UpdateSchoolDivision":      {"aca.school_divisions.edit", "aca"},
	"DeleteSchoolDivision":      {"aca.school_divisions.delete", "aca"},
	"ListCampuses":              {"aca.campuses.view", "aca"},
	"GetCampus":                 {"aca.campuses.view", "aca"},
	"CreateCampus":              {"aca.campuses.create", "aca"},
	"UpdateCampus":              {"aca.campuses.edit", "aca"},
	"DeleteCampus":              {"aca.campuses.delete", "aca"},
	"ListHolidays":              {"aca.holiday_calendars.view", "aca"},
	"GetHoliday":                {"aca.holiday_calendars.view", "aca"},
	"CreateHoliday":             {"aca.holiday_calendars.create", "aca"},
	"UpdateHoliday":             {"aca.holiday_calendars.edit", "aca"},
	"DeleteHoliday":             {"aca.holiday_calendars.delete", "aca"},
	"ListCalendarEvents":        {"aca.academic_calendars.view", "aca"},
	"GetCalendarEvent":          {"aca.academic_calendars.view", "aca"},
	"CreateCalendarEvent":       {"aca.academic_calendars.create", "aca"},
	"UpdateCalendarEvent":       {"aca.academic_calendars.edit", "aca"},
	"DeleteCalendarEvent":       {"aca.academic_calendars.delete", "aca"},
	"ListElectiveGroups":        {"aca.elective_groups.view", "aca"},
	"GetElectiveGroup":          {"aca.elective_groups.view", "aca"},
	"CreateElectiveGroup":       {"aca.elective_groups.create", "aca"},
	"UpdateElectiveGroup":       {"aca.elective_groups.edit", "aca"},
	"DeleteElectiveGroup":       {"aca.elective_groups.delete", "aca"},
	"ListClassSectionMappings":  {"aca.class_section_mappings.view", "aca"},
	"GetClassSectionMapping":    {"aca.class_section_mappings.view", "aca"},
	"CreateClassSectionMapping": {"aca.class_section_mappings.create", "aca"},
	"UpdateClassSectionMapping": {"aca.class_section_mappings.edit", "aca"},
	"DeleteClassSectionMapping": {"aca.class_section_mappings.delete", "aca"},
	"ListSubjectClassMappings":  {"aca.subject_class_mappings.view", "aca"},
	"GetSubjectClassMapping":    {"aca.subject_class_mappings.view", "aca"},
	"CreateSubjectClassMapping": {"aca.subject_class_mappings.create", "aca"},
	"UpdateSubjectClassMapping": {"aca.subject_class_mappings.edit", "aca"},
	"DeleteSubjectClassMapping": {"aca.subject_class_mappings.delete", "aca"},
	"ListTextbooks":             {"aca.textbooks.view", "aca"},
	"GetTextbook":               {"aca.textbooks.view", "aca"},
	"CreateTextbook":            {"aca.textbooks.create", "aca"},
	"UpdateTextbook":            {"aca.textbooks.edit", "aca"},
	"DeleteTextbook":            {"aca.textbooks.delete", "aca"},
	"ListTextbookChapters":      {"aca.textbook_chapters.view", "aca"},
	"GetTextbookChapter":        {"aca.textbook_chapters.view", "aca"},
	"CreateTextbookChapter":     {"aca.textbook_chapters.create", "aca"},
	"UpdateTextbookChapter":     {"aca.textbook_chapters.edit", "aca"},
	"DeleteTextbookChapter":     {"aca.textbook_chapters.delete", "aca"},
	"ListStudents":              {"stu.students.view", "stu"},
	"GetStudent":                {"stu.students.view", "stu"},
	"CreateStudent":             {"stu.students.create", "stu"},
	"UpdateStudent":             {"stu.students.edit", "stu"},
	"DeleteStudent":             {"stu.students.delete", "stu"},
	"ListStudentEnrollments":    {"stu.students.view", "stu"},
	"GetStudentEnrollment":      {"stu.students.view", "stu"},
	"CreateStudentEnrollment":   {"stu.students.create", "stu"},
	"UpdateStudentEnrollment":   {"stu.students.edit", "stu"},
	"DeleteStudentEnrollment":   {"stu.students.delete", "stu"},
	"ListGuardians":             {"stu.students.view", "stu"},
	"GetGuardian":               {"stu.students.view", "stu"},
	"CreateGuardian":            {"stu.students.create", "stu"},
	"UpdateGuardian":            {"stu.students.edit", "stu"},
	"DeleteGuardian":            {"stu.students.delete", "stu"},
	"ListStudentGuardians":      {"stu.students.view", "stu"},
	"GetStudentGuardian":        {"stu.students.view", "stu"},
	"CreateStudentGuardian":     {"stu.students.create", "stu"},
	"UpdateStudentGuardian":     {"stu.students.edit", "stu"},
	"DeleteStudentGuardian":     {"stu.students.delete", "stu"},
	"ListStudentAddresses":      {"stu.student_addresses.view", "stu"},
	"GetStudentAddress":         {"stu.student_addresses.view", "stu"},
	"CreateStudentAddress":      {"stu.student_addresses.create", "stu"},
	"UpdateStudentAddress":      {"stu.student_addresses.edit", "stu"},
	"DeleteStudentAddress":      {"stu.student_addresses.delete", "stu"},
	"ListScopeAssignments":      {"iam.scope_assignments.view", "iam"},
	"CreateScopeAssignment":     {"iam.scope_assignments.edit", "iam"},
	"DeleteScopeAssignment":     {"iam.scope_assignments.edit", "iam"},
}

// scopedResources lists resources whose records are personal data of children. A caller whose scope for the resource
// covers the whole institute uses them freely. Anyone else may only read, and only through a narrower scope the
// resource's service knows how to apply; every other case is refused (403) rather than served unfiltered.
var scopedResources = map[string]*scopePolicy{
	// a teacher reads the students, enrollments and guardians of the class sections they are assigned to
	"stu.students": {classSections: true},
	// addresses are not offered to class-section scopes: a teacher has no need for a child's home address
	"stu.student_addresses": {},
}

type scopePolicy struct{ classSections bool }

// narrowToScope returns the context to serve the request with, or false to refuse it. scopes are the caller's scopes
// for the resource ("tenant", "class_section:<mapping id>", ...).
func narrowToScope(ctx context.Context, p *scopePolicy, scopes []string, permission string) (context.Context, bool) {
	if contains(scopes, authz.ScopeTenant) {
		return ctx, true
	}
	if !p.classSections || !strings.HasSuffix(permission, ".view") {
		return ctx, false
	}
	ids := []string{}
	for _, sc := range scopes {
		ref, ok := strings.CutPrefix(sc, "class_section:")
		if !ok {
			return ctx, false // a scope this resource cannot apply (self, owned, a hostel): refuse rather than guess
		}
		ids = append(ids, ref)
	}
	// no assignment at all still narrows: the caller sees nothing
	return scope.WithClassSections(ctx, ids), true
}

// permissionResource is the "module.resource" part of a permission code: "stu.students.view" gives "stu.students".
func permissionResource(code string) string {
	if i := strings.LastIndexByte(code, '.'); i > 0 {
		return code[:i]
	}
	return code
}

// OperationAccess returns a copy of the permission table (for the contract test).
func OperationAccess() map[string][2]string {
	out := make(map[string][2]string, len(operationAccess))
	for k, v := range operationAccess {
		out[k] = [2]string{v.Permission, v.Module}
	}
	return out
}

type principalKey struct{}
type accessKey struct{}

// AccessFrom returns the caller's resolved access, available to handlers of operations that declare a permission.
func AccessFrom(ctx context.Context) (authz.Access, bool) {
	a, ok := ctx.Value(accessKey{}).(authz.Access)
	return a, ok
}

// PrincipalFrom returns the authenticated caller stored by the authentication middleware.
func PrincipalFrom(ctx context.Context) (identity.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identity.Principal)
	return p, ok
}

// authenticate requires a valid bearer token on every operation that is not public, then stores the Principal on the
// request context and advertises the caller's access version so clients know when to refetch /me/access.
func (h *Handler) authenticate(next gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if publicOperations[operationID] {
		return next
	}
	return func(c echo.Context, request any) (any, error) {
		token, ok := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
		if !ok || h.auth == nil {
			return nil, problem.Write(c, http.StatusUnauthorized, "unauthenticated", "")
		}
		p, err := h.auth.Authenticate(c.Request().Context(), token)
		if err != nil {
			if errors.Is(err, identity.ErrUnauthenticated) {
				c.Response().Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				return nil, problem.Write(c, http.StatusUnauthorized, "unauthenticated", "")
			}
			return nil, err
		}
		c.Response().Header().Set("X-Access-Version", itoa(p.AccessVersion))
		ctx := context.WithValue(c.Request().Context(), principalKey{}, p)

		if need, ok := operationAccess[operationID]; ok {
			if h.access == nil {
				return nil, errAuthNotConfigured
			}
			a, err := h.resolveFor(ctx, p)
			if err != nil {
				if errors.Is(err, authz.ErrUnknownUser) {
					return nil, problem.Write(c, http.StatusUnauthorized, "unauthenticated", "")
				}
				return nil, err
			}
			// A module the institute has not bought does not exist for it (404); a module it has, but a permission the
			// user lacks, is a clear 403.
			if !contains(a.Modules, need.Module) {
				return nil, problem.Write(c, http.StatusNotFound, "not_found", "")
			}
			if !a.Effective.Can(need.Permission) {
				return nil, problem.Write(c, http.StatusForbidden, "permission_denied", "You do not have permission to do this.")
			}
			if res := permissionResource(need.Permission); scopedResources[res] != nil {
				narrowed, ok := narrowToScope(ctx, scopedResources[res], a.Effective.Scopes[res], need.Permission)
				if !ok {
					return nil, problem.Write(c, http.StatusForbidden, "permission_denied", "You do not have permission to do this.")
				}
				ctx = narrowed
			}
			ctx = context.WithValue(ctx, accessKey{}, a)
		}
		c.SetRequest(c.Request().WithContext(ctx))
		return next(c, request)
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>" (the scheme is case-insensitive).
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// GetMyAccess implements GET /me/access: everything web and mobile need to decide what to show.
func (h *Handler) GetMyAccess(ctx context.Context, _ gen.GetMyAccessRequestObject) (gen.GetMyAccessResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.access == nil {
		return nil, errAuthNotConfigured
	}
	a, err := h.access.Resolve(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return gen.GetMyAccess200JSONResponse{Body: toAccessPayload(a), Headers: gen.GetMyAccess200ResponseHeaders{XAccessVersion: int(a.Version)}}, nil
}

func toAccessPayload(a authz.Access) gen.AccessPayload {
	var out gen.AccessPayload
	out.User = gen.AccessUser{Id: openapi_types.UUID(uuid.MustParse(a.UserID)), Name: a.UserName, Tier: gen.AccessUserTier("tenant")}
	t := gen.AccessTenant{Id: openapi_types.UUID(uuid.MustParse(a.Tenant.ID)), Name: a.Tenant.Name, Modules: nonNil(a.Modules)}
	if primary, ok := a.Tenant.Brand["primary"].(string); ok {
		t.Brand.Primary = primary
	}
	out.Tenant = &t
	out.Permissions = nonNil(a.Effective.Permissions)
	out.Scopes = a.Effective.Scopes
	out.FieldMasks = map[string][]string{} // field masks arrive with IAM-07
	out.Workspace.Home = a.Home
	out.Workspace.Navigation = make([]gen.NavItem, 0, len(a.Screens))
	for _, s := range a.Screens {
		icon := s.Icon
		item := gen.NavItem{ScreenId: s.ID, Label: s.Label, Order: s.Order}
		if icon != "" {
			item.Icon = &icon
		}
		out.Workspace.Navigation = append(out.Workspace.Navigation, item)
	}
	out.Mobile.Tabs = authz.MobileTabs(a.Screens)
	out.Mobile.Features = []string{}
	out.Approvals.CanApprove = []string{} // approval types arrive with the workflow engine
	out.Version = int(a.Version)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
