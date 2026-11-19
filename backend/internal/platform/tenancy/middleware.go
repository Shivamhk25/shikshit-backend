package tenancy

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/api/problem"
)

// MiddlewareConfig configures Middleware.
type MiddlewareConfig struct {
	Config
	Resolver Resolver
	// Skip lets routes that need no institute (health checks, platform realm) bypass resolution.
	Skip func(c echo.Context) bool
}

// Middleware resolves the institute from the request host, rejects unknown/suspended/archived ones and stores
// the Tenant on the request context. It must run before authentication so login is tenant-aware.
//
// Unknown and archived institutes both answer 404 tenant_not_found so the response does not reveal which codes exist.
func Middleware(cfg MiddlewareConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}
			host := c.Request().Host
			if cfg.TrustForwardedHost {
				if fh := c.Request().Header.Get("X-Forwarded-Host"); fh != "" {
					host = fh
				}
			}
			key, ok := KeyFromHost(host, cfg.Config)
			if !ok {
				key = cfg.DefaultTenant
			}
			if key == "" {
				return problem.Write(c, http.StatusNotFound, "tenant_not_found", "")
			}

			t, err := cfg.Resolver.Resolve(c.Request().Context(), key)
			switch {
			case err == ErrNotFound:
				return problem.Write(c, http.StatusNotFound, "tenant_not_found", "")
			case err != nil:
				c.Logger().Errorf("tenant lookup failed: %v", err)
				return problem.Write(c, http.StatusServiceUnavailable, "tenant_lookup_failed", "")
			}
			switch t.Status {
			case "archived":
				return problem.Write(c, http.StatusNotFound, "tenant_not_found", "")
			case "suspended":
				return problem.Write(c, http.StatusForbidden, "tenant_suspended", "This institute is currently suspended. Please contact support.")
			}
			c.SetRequest(c.Request().WithContext(WithTenant(c.Request().Context(), t)))
			return next(c)
		}
	}
}
