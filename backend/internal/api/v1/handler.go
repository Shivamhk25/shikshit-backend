// Package apiv1 implements the HTTP contract defined in backend/api/openapi.
//
// The interface it satisfies is generated (internal/api/gen). Adding or changing an
// operation in the OpenAPI spec and regenerating makes this package stop compiling until
// the handler matches, so the implementation cannot silently drift from the contract.
package apiv1

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
)

// BasePath is the URL prefix every contract operation is served under.
const BasePath = "/api/v1"

// Handler implements gen.StrictServerInterface. Operations that are declared in the
// contract but not built yet answer 501 until the work item that owns them lands.
type Handler struct {
	version string
	now     func() time.Time
	auth    Authenticator
	access  AccessResolver
	cookie  *CookieConfig
	rooms   RoomsService

	imports   ImportService
	importReg *importer.Registry

	academicYears AcademicYearService
	academicTerms AcademicTermService
	classes       SchoolClassService
	sections      SectionService
	subjects      SubjectService

	schoolDivisions SchoolDivisionService
	campuses        CampusService
	holidays        HolidayService
	calendarEvents  CalendarEventService
	electiveGroups  ElectiveGroupService

	classSectionMappings ClassSectionMappingService
	subjectClassMappings SubjectClassMappingService
	textbooks            TextbookService
	textbookChapters     TextbookChapterService

	students               StudentService
	studentEnrollments     StudentEnrollmentService
	guardians              GuardianService
	studentGuardians       StudentGuardianService
	studentAddresses       StudentAddressService
	enquiries              EnquiryService
	enquiryDiscussions     EnquiryDiscussionService
	scopeAssignments       ScopeAssignmentService
	users                  UserService
	roles                  RoleService
	parentLinks            ParentLinkService
	appearance             AppearanceService
	notifications          NotificationService
	approvals              ApprovalService
	colleagues             ColleagueService
	yearRollover           YearRolloverService
	controlTower           ControlTowerService
	studentRegistrations   StudentRegistrationService
	admissionInsights      AdmissionInsightsService
	studentIdentity        StudentIdentityService
	admissions             AdmissionService
	promotions             PromotionService
	studentTransfers       StudentTransferService
	studentPauses          StudentPauseService
	alumni                 AlumniService
	studentElectives       StudentElectiveService
	studentElectiveChanges StudentElectiveChangeService

	platform       PlatformAuthenticator
	institutes     InstituteService
	platformUsers  PlatformUserService
	files          PhotoService
	platformCookie *CookieConfig
}

// UserService is the subset of users.Service used by the handler.
type UserService interface {
	ListAudited(ctx context.Context, actor audit.Actor, q crud.ListQuery, f users.Filter) (crud.Page[users.User], error)
	View(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	Create(ctx context.Context, actor audit.Actor, in users.CreateInput) (users.UserWithInviteLink, error)
	Update(ctx context.Context, actor audit.Actor, id string, in users.UpdateInput) (users.User, error)
	Deactivate(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	Reactivate(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	Lock(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	Unlock(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	ResetPassword(ctx context.Context, actor audit.Actor, id string) (users.UserWithResetLink, error)
	ResetMFA(ctx context.Context, actor audit.Actor, id string) (users.User, error)
	AcceptInvite(ctx context.Context, actor audit.Actor, token, password string) error
}

// Option configures optional collaborators of a Handler.
type Option func(*Handler)

// WithAccess enables GET /me/access. Without it the operation answers 501.
func WithAccess(r AccessResolver) Option { return func(h *Handler) { h.access = r } }

// WithAuth enables the /auth operations. Without it they answer 501.
func WithAuth(a Authenticator) Option { return func(h *Handler) { h.auth = a } }

// WithUsers enables the users operations. Without them they answer 501.
func WithUsers(u UserService) Option { return func(h *Handler) { h.users = u } }

// Compile-time guard: the handler must implement every operation in the contract.
var _ gen.StrictServerInterface = (*Handler)(nil)

// New returns a Handler that reports version in health responses.
func New(version string, opts ...Option) *Handler {
	h := &Handler{version: version, now: time.Now}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Register mounts every contract operation on e under BasePath.
// It is not called from the application yet; wiring happens with FND-06.
func Register(e *echo.Echo, h *Handler) {
	gen.RegisterHandlersWithBaseURL(e, gen.NewStrictHandler(h, []gen.StrictMiddlewareFunc{h.withClientMeta, h.authenticate}), BasePath)
}

// GetHealthz implements GET /healthz. It takes no input and always reports ok.
func (h *Handler) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{
		Status:  gen.Ok,
		Version: h.version,
		Time:    h.now().UTC(),
	}, nil
}

// AcceptInvite implements POST /auth/invite/accept. Every bad-token case gets the same answer.
func (h *Handler) AcceptInvite(ctx context.Context, request gen.AcceptInviteRequestObject) (gen.AcceptInviteResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	if request.Body == nil {
		return gen.AcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	err := h.users.AcceptInvite(ctx, actorFrom(ctx), request.Body.Token, request.Body.Password)
	switch {
	case errors.Is(err, users.ErrInvalidToken):
		return gen.AcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "invalid_token", err.Error()))}, nil
	case errors.Is(err, identity.ErrWeakPassword):
		return gen.AcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "weak_password", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.AcceptInvite200JSONResponse{Message: "Your password is set. You can sign in now."}, nil
}
