package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/problem"
	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/ratelimit"
)

// ipExtractor decides which address counts as the client's. Directly exposed, only the TCP peer counts and a
// forwarded header is ignored (a client could write anything in it). Behind a proxy that overwrites the header, the
// address the proxy appended is used.
func ipExtractor(trustForwarded bool) echo.IPExtractor {
	if trustForwarded {
		return echo.ExtractIPFromXFFHeader()
	}
	return echo.ExtractIPDirect()
}

// strictPaths are the unauthenticated operations that check a secret (password, code, one-time link). They share one
// small bucket per client so attempts cannot be spread across them.
var strictPaths = map[string]bool{
	apiv1.BasePath + "/auth/login":                      true,
	apiv1.BasePath + "/auth/mfa/verify":                 true,
	apiv1.BasePath + "/auth/mfa/setup/confirm":          true,
	apiv1.BasePath + "/auth/invite/accept":              true,
	apiv1.BasePath + "/auth/support-visit":              true,
	apiv1.BasePath + "/platform/auth/login":             true,
	apiv1.BasePath + "/platform/auth/mfa/verify":        true,
	apiv1.BasePath + "/platform/auth/mfa/setup/confirm": true,
	apiv1.BasePath + "/platform/auth/invite/accept":     true,
}

// rateLimits limits requests per client address: a small shared bucket for the operations in strictPaths and a larger
// one for everything else. Probes (/livez) and CORS preflights are not counted. A refused request gets 429 with a
// Retry-After header in whole seconds and the usual problem+json body.
func rateLimits(cfg RateLimitConfig, now func() time.Time) echo.MiddlewareFunc {
	general := ratelimit.New(ratelimit.Rule{PerMinute: cfg.GeneralPerMin, Burst: minInt(200, cfg.GeneralPerMin)}, cfg.MaxKeys, now)
	strict := ratelimit.New(ratelimit.Rule{PerMinute: cfg.StrictPerMin, Burst: minInt(10, cfg.StrictPerMin)}, cfg.MaxKeys, now)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			if r.Method == http.MethodOptions || r.URL.Path == "/livez" {
				return next(c)
			}
			l := general
			if strictPaths[r.URL.Path] {
				l = strict
			}
			ok, wait := l.Allow(ratelimit.ClientKey(c.RealIP()))
			if ok {
				return next(c)
			}
			secs := int((wait + time.Second - 1) / time.Second)
			if secs < 1 {
				secs = 1
			}
			c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
			return problem.Write(c, http.StatusTooManyRequests, "rate_limited", "Too many requests. Please wait a moment and try again.")
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// originGuard is a second CSRF defence for requests that carry cookies (the refresh cookie is the only one we set):
// a state-changing request whose Origin is neither the API's own nor on the allow-list is refused. Browsers always
// send Origin on cross-site POSTs; mobile apps send none and carry no cookies. With no allow-list configured (local
// development) only the API's own origin is accepted. SameSite=Strict does not cover requests from a sibling
// subdomain, which is the same site, so this check does.
func originGuard(allowed []string) echo.MiddlewareFunc {
	ok := map[string]bool{}
	for _, o := range allowed {
		ok[strings.ToLower(o)] = true
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			r := c.Request()
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			origin := r.Header.Get(echo.HeaderOrigin)
			if origin == "" || r.Header.Get(echo.HeaderCookie) == "" {
				return next(c)
			}
			if ok[strings.ToLower(origin)] || sameHost(origin, r.Host) {
				return next(c)
			}
			return problem.Write(c, http.StatusForbidden, "origin_not_allowed", "This request came from an origin that is not allowed.")
		}
	}
}

// sameHost reports whether origin names the host the request was sent to.
func sameHost(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}

// secureHeaders sets conservative response headers for an API that serves JSON only. Every response, errors and 429
// included, is marked non-cacheable (sign-in and token responses must never be stored by a browser or proxy).
func secureHeaders(production bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			h.Set("Pragma", "no-cache")
			h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-site")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
			h.Set("X-Permitted-Cross-Domain-Policies", "none")
			if production {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			return next(c)
		}
	}
}
