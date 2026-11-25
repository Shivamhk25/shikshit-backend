package server

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

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
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/admissions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquirydiscussions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/insights"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/appearance"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/parentlinks"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/roles"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/scopes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/importreg"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/sad/institutes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/sad/platformusers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/addresses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/alumni"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electivechanges"
	studentelectives "github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/guardians"
	stuidentity "github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/links"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/pauses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/promotions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/students"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/ten/rollover"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/colleagues"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/files"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/messaging"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// DB is what the server needs from the database pool: transactions and single-row queries.
type DB interface {
	tenancy.Beginner
	tenancy.Querier
}

// uploadBodyLimit is the request size allowed on the file upload route: the file limit plus room for the form fields.
const uploadBodyLimit = "11M"

// isUpload reports whether the request is the import file upload (the only route that takes a large body).
func isUpload(c echo.Context) bool {
	return c.Request().Method == http.MethodPost && c.Request().URL.Path == apiv1.BasePath+"/onboarding/imports"
}

// uploadReadDeadline gives the upload route more time to receive its body than the server-wide read timeout (15 s),
// which is too short for 10 MB on a slow connection. Other routes keep the server's timeout. Where the connection does
// not support per-request deadlines (tests) the call is a no-op.
func uploadReadDeadline(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if isUpload(c) {
			_ = http.NewResponseController(c.Response()).SetReadDeadline(time.Now().Add(2 * time.Minute))
		}
		return next(c)
	}
}

// bodyLimits caps request bodies: 1 MB everywhere, except the file upload route.
func bodyLimits() []echo.MiddlewareFunc {
	return []echo.MiddlewareFunc{
		uploadReadDeadline,
		middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{Limit: "1M", Skipper: isUpload}),
		middleware.BodyLimitWithConfig(middleware.BodyLimitConfig{Limit: uploadBodyLimit, Skipper: func(c echo.Context) bool { return !isUpload(c) }}),
	}
}

// importService builds the import service over the institute's entities.
func importService(db tenancy.Beginner) importer.Service {
	return importer.Service{DB: db, Reg: importreg.Registry(db), Packs: importreg.Packs()}
}

// outbox returns the email queue, or nil when no mail server is configured (links are then only shown).
func outbox(cfg Config) (*messaging.Outbox, error) {
	if cfg.Mail == nil {
		return nil, nil
	}
	links := messaging.Links{DevOrigin: cfg.WebOrigin}
	if len(cfg.BaseDomains) > 0 {
		links.BaseDomain = cfg.BaseDomains[0]
	}
	return messaging.NewOutbox(cfg.MFAKey, links)
}

// Storage returns the object store the configuration describes, or nil when none is configured (file features are
// then off and the hourly cleanup of unconfirmed uploads is skipped).
func Storage(cfg Config) (storage.Store, error) {
	if cfg.Storage.Endpoint == "" {
		return nil, nil
	}
	store, err := storage.New(cfg.Storage)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// Worker returns the background worker: imports (validate, commit, roll back) and, when a mail server is configured,
// queued emails. Run it next to the server (its Run method blocks until the context ends).
func Worker(cfg Config, db tenancy.Beginner) (*jobs.Worker, error) {
	handlers := importService(db).Handlers()
	mail, err := outbox(cfg)
	if err != nil {
		return nil, err
	}
	if mail != nil {
		for kind, h := range mail.Handlers(messaging.SMTPSender{Config: *cfg.Mail}) {
			handlers[kind] = h
		}
	}
	return &jobs.Worker{DB: db, Handlers: handlers}, nil
}

// approvalRegistry lists the workflow types modules have registered (FND-20). Build it once: the approval service and
// every module that files requests must share it.
func approvalRegistry() (*approvals.Registry, error) {
	reg := approvals.NewRegistry()
	if err := transfers.Register(reg); err != nil {
		return nil, err
	}
	if err := electivechanges.Register(reg); err != nil {
		return nil, err
	}
	return reg, nil
}

// New builds the HTTP handler. db must be the runtime role (row-level security applies).
func New(cfg Config, db DB) (*echo.Echo, error) {
	signer, err := identity.NewSigner(cfg.JWTSecret, cfg.JWTIssuer, cfg.AccessTTL)
	if err != nil {
		return nil, err
	}
	idSvc, err := identity.NewService(db, signer, identity.DefaultPolicy)
	if err != nil {
		return nil, err
	}
	if err := idSvc.ConfigureMFA(cfg.MFAKey, cfg.MFARequiredForAdmins); err != nil {
		return nil, err
	}
	platformSvc, err := identity.NewPlatformService(db, signer, identity.DefaultPolicy, cfg.MFAKey)
	if err != nil {
		return nil, err
	}
	mail, err := outbox(cfg)
	if err != nil {
		return nil, err
	}
	approvalReg, err := approvalRegistry()
	if err != nil {
		return nil, err
	}
	approvalSvc := approvals.Service{DB: db, Reg: approvalReg, Access: authz.Service{DB: db}}
	var fileOpts []apiv1.Option
	if cfg.Storage.Endpoint != "" {
		store, err := storage.New(cfg.Storage)
		if err != nil {
			return nil, err
		}
		fileOpts = append(fileOpts, apiv1.WithFiles(files.Service{DB: db, Store: store}))
	}

	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = apiv1.ErrorHandler
	e.IPExtractor = ipExtractor(cfg.TrustForwardedHost)

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(bodyLimits()...)
	e.Use(secureHeaders(cfg.Production))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:  cfg.AllowedOrigins,
		AllowMethods:  []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders:  []string{echo.HeaderAuthorization, echo.HeaderContentType, "X-Client", "X-Client-Version", "Idempotency-Key"},
		ExposeHeaders: []string{"X-Access-Version", echo.HeaderXRequestID},
		MaxAge:        600,
	}))
	if cfg.RateLimit.Enabled {
		// After CORS so a browser can read the 429; before the institute lookup so a flood costs no database work.
		e.Use(rateLimits(cfg.RateLimit, time.Now))
	}
	e.Use(originGuard(cfg.AllowedOrigins))

	// Liveness is outside the institute-aware API so probes need no host.
	e.GET("/livez", func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]string{"status": "ok"}) })

	resolver := &tenancy.CachedResolver{Next: tenancy.PgResolver{DB: db}}
	e.Use(tenancy.Middleware(tenancy.MiddlewareConfig{
		Config: tenancy.Config{
			BaseDomains:        cfg.BaseDomains,
			ReservedLabels:     []string{"www", "api", "admin", "app", "static", "mail"},
			DefaultTenant:      cfg.DefaultTenant,
			TrustForwardedHost: cfg.TrustForwardedHost,
		},
		Resolver: resolver,
		// Platform (Super Admin) operations belong to no institute (decision 0017).
		Skip: func(c echo.Context) bool {
			return c.Path() == "/livez" || c.Request().Method == http.MethodOptions || apiv1.PlatformPath(c.Request().URL.Path)
		},
	}))

	cookie := apiv1.CookieConfig{Name: "shikshit_rt", Secure: false}
	platformCookie := apiv1.CookieConfig{Name: "shikshit_prt", Secure: false}
	if cfg.Production {
		cookie = apiv1.CookieConfig{Name: "__Secure-shikshit_rt", Secure: true}
		platformCookie = apiv1.CookieConfig{Name: "__Secure-shikshit_prt", Secure: true}
	}
	h := apiv1.New(cfg.Version, apiv1.WithAuth(idSvc), apiv1.WithAccess(&authz.Cache{Next: authz.Service{DB: db}}), apiv1.WithRooms(rooms.Service{DB: db}),
		apiv1.WithYearRollover(rollover.Service{DB: db}),
		apiv1.WithAcademicYears(years.Service{DB: db}), apiv1.WithAcademicTerms(terms.Service{DB: db}), apiv1.WithSchoolClasses(classes.Service{DB: db}),
		apiv1.WithSections(sections.Service{DB: db}), apiv1.WithSubjects(subjects.Service{DB: db}),
		apiv1.WithSchoolDivisions(divisions.Service{DB: db}), apiv1.WithCampuses(campuses.Service{DB: db}), apiv1.WithHolidays(holidays.Service{DB: db}),
		apiv1.WithCalendarEvents(events.Service{DB: db}), apiv1.WithElectiveGroups(electives.Service{DB: db}),
		apiv1.WithClassSectionMappings(classsections.Service{DB: db}), apiv1.WithAdmissions(admissions.Service{DB: db}), apiv1.WithPromotions(promotions.Service{DB: db}), apiv1.WithSubjectClassMappings(subjectclasses.Service{DB: db}), apiv1.WithTextbooks(textbooks.Service{DB: db}), apiv1.WithTextbookChapters(chapters.Service{DB: db}), apiv1.WithStudents(students.Service{DB: db}), apiv1.WithStudentIdentity(stuidentity.Service{DB: db}), apiv1.WithStudentEnrollments(enrollments.Service{DB: db}), apiv1.WithGuardians(guardians.Service{DB: db}), apiv1.WithStudentGuardians(links.Service{DB: db}), apiv1.WithStudentAddresses(addresses.Service{DB: db}), apiv1.WithEnquiries(enquiries.Service{DB: db}), apiv1.WithScopeAssignments(scopes.Service{DB: db}), apiv1.WithUsers(users.Service{DB: db, Mail: mail}), apiv1.WithRoles(roles.Service{DB: db}), apiv1.WithParentLinks(parentlinks.Service{DB: db}), apiv1.WithAppearance(appearance.Service{DB: db}), apiv1.WithNotifications(notify.Service{DB: db}), apiv1.WithColleagues(colleagues.Service{DB: db}), apiv1.WithEnquiryDiscussions(enquirydiscussions.Service{DB: db}), apiv1.WithApprovals(approvalSvc), apiv1.WithImports(importService(db), importreg.Registry(db)), apiv1.WithWebRefreshCookie(cookie), apiv1.WithPlatform(platformSvc, platformCookie), apiv1.WithInstitutes(institutes.Service{DB: db, Mail: mail}), apiv1.WithPlatformUsers(platformusers.Service{DB: db, Mail: mail}), apiv1.WithStudentRegistrations(registrations.Service{DB: db}), apiv1.WithAdmissionInsights(insights.Service{DB: db}),
		apiv1.WithStudentTransfers(transfers.Service{DB: db, Approvals: approvalSvc}),
		apiv1.WithStudentPauses(pauses.Service{DB: db}),
		apiv1.WithAlumni(alumni.Service{DB: db}),
		apiv1.WithStudentElectives(studentelectives.Service{DB: db}), apiv1.WithStudentElectiveChanges(electivechanges.Service{DB: db, Approvals: approvalSvc}))
	for _, o := range fileOpts {
		o(h)
	}
	apiv1.Register(e, h)
	return e, nil
}
