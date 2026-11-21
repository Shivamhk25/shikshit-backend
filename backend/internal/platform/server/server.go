package server

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	apiv1 "github.com/pie360/backend/internal/api/v1"
	"github.com/pie360/backend/internal/modules/aca/rooms"
	"github.com/pie360/backend/internal/platform/authz"
	"github.com/pie360/backend/internal/platform/identity"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// DB is what the server needs from the database pool: transactions and single-row queries.
type DB interface {
	tenancy.Beginner
	tenancy.Querier
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

	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = apiv1.ErrorHandler
	if cfg.TrustForwardedHost {
		// Behind a proxy: take the client address from the proxy's header, nowhere else.
		e.IPExtractor = echo.ExtractIPFromXFFHeader()
	} else {
		e.IPExtractor = echo.ExtractIPDirect()
	}

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())
	e.Use(middleware.BodyLimit("1M"))
	e.Use(secureHeaders(cfg.Production))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins:  cfg.AllowedOrigins,
		AllowMethods:  []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders:  []string{echo.HeaderAuthorization, echo.HeaderContentType, "X-Client", "X-Client-Version", "Idempotency-Key"},
		ExposeHeaders: []string{"X-Access-Version", echo.HeaderXRequestID},
		MaxAge:        600,
	}))

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
		Skip:     func(c echo.Context) bool { return c.Path() == "/livez" || c.Request().Method == http.MethodOptions },
	}))

	cookie := apiv1.CookieConfig{Name: "shikshit_rt", Secure: false}
	if cfg.Production {
		cookie = apiv1.CookieConfig{Name: "__Secure-shikshit_rt", Secure: true}
	}
	apiv1.Register(e, apiv1.New(cfg.Version, apiv1.WithAuth(idSvc), apiv1.WithAccess(&authz.Cache{Next: authz.Service{DB: db}}), apiv1.WithRooms(rooms.Service{DB: db}), apiv1.WithWebRefreshCookie(cookie)))
	return e, nil
}

// secureHeaders sets conservative response headers for an API that serves JSON only.
func secureHeaders(production bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			if production {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			return next(c)
		}
	}
}
