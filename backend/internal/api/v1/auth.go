package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// errAuthNotConfigured is returned when the server was built without an identity service (a wiring bug, not a client error).
var errAuthNotConfigured = errors.New("apiv1: authentication is not configured")

// errNotConfigured is returned when an operation is routed but its service was not provided (a wiring bug).
var errNotConfigured = errors.New("apiv1: operation is not configured")

// Authenticator is what the /auth operations need from the identity service.
type Authenticator interface {
	Login(ctx context.Context, username, password string, c identity.Client) (identity.Tokens, error)
	Refresh(ctx context.Context, refreshToken string, c identity.Client) (identity.Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
	Authenticate(ctx context.Context, token string) (identity.Principal, error)
}

type clientMetaKey struct{}

// requestMeta is per-request state strict handlers cannot reach through their context alone: the caller's address and
// user agent (stored on the session), the refresh cookie sent by the browser, and the cookie the handler wants set.
type requestMeta struct {
	ip, userAgent  string
	client         string // value of X-Client, used as the audit source
	requestID      string
	cookie         string       // refresh token from the request cookie, if any
	platformCookie string       // platform refresh token from the platform cookie, if any
	out            *http.Cookie // cookie to set (or clear) on the response
}

// CookieConfig describes the httpOnly cookie that carries the refresh token for web clients.
type CookieConfig struct {
	Name   string // use a "__Secure-" prefixed name in production; "__Host-" is not possible because Path is restricted
	Secure bool   // must be true outside local development
	Path   string // restricts the cookie to the auth endpoints, default /api/v1/auth
}

// WithWebRefreshCookie keeps web clients' refresh tokens in an httpOnly cookie instead of the response body, so a
// script injected into the page cannot read them. Without it web clients get the token in the body like mobile.
func WithWebRefreshCookie(c CookieConfig) Option {
	if c.Path == "" {
		c.Path = BasePath + "/auth"
	}
	return func(h *Handler) { h.cookie = &c }
}

// withClientMeta stores requestMeta on the request context and applies any cookie the handler asked for.
func (h *Handler) withClientMeta(next gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
	return func(c echo.Context, request any) (any, error) {
		meta := &requestMeta{ip: c.RealIP(), userAgent: c.Request().UserAgent(), client: c.Request().Header.Get("X-Client"), requestID: c.Response().Header().Get(echo.HeaderXRequestID)}
		if h.cookie != nil {
			if ck, err := c.Cookie(h.cookie.Name); err == nil {
				meta.cookie = ck.Value
			}
		}
		if h.platformCookie != nil {
			if ck, err := c.Cookie(h.platformCookie.Name); err == nil {
				meta.platformCookie = ck.Value
			}
		}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), clientMetaKey{}, meta)))
		resp, err := next(c, request)
		if meta.out != nil {
			c.SetCookie(meta.out)
		}
		return resp, err
	}
}

func metaFrom(ctx context.Context) *requestMeta {
	m, _ := ctx.Value(clientMetaKey{}).(*requestMeta)
	return m
}

func isWeb(client string) bool { return client == "" || client == "web" }

func clientFrom(ctx context.Context, name *string, version *string) identity.Client {
	c := identity.Client{Name: "web"}
	if name != nil {
		c.Name = *name
	}
	if version != nil {
		c.Version = *version
	}
	if m := metaFrom(ctx); m != nil {
		c.IP, c.UserAgent = m.ip, m.userAgent
	}
	return c
}

// deliverRefresh puts the refresh token where the client can safely keep it: an httpOnly cookie for web (and the body
// field is left empty), the response body for mobile.
func (h *Handler) deliverRefresh(ctx context.Context, client string, t identity.Tokens) *string {
	m := metaFrom(ctx)
	if h.cookie == nil || m == nil || !isWeb(client) {
		tok := t.RefreshToken
		return &tok
	}
	m.out = &http.Cookie{
		Name: h.cookie.Name, Value: t.RefreshToken, Path: h.cookie.Path, Expires: t.RefreshExpiresAt,
		HttpOnly: true, Secure: h.cookie.Secure, SameSite: http.SameSiteStrictMode,
	}
	return nil
}

// clearRefreshCookie expires the cookie on the response.
func (h *Handler) clearRefreshCookie(ctx context.Context) {
	if m := metaFrom(ctx); m != nil && h.cookie != nil {
		m.out = &http.Cookie{
			Name: h.cookie.Name, Value: "", Path: h.cookie.Path, MaxAge: -1, Expires: time.Unix(0, 0),
			HttpOnly: true, Secure: h.cookie.Secure, SameSite: http.SameSiteStrictMode,
		}
	}
}

// refreshTokenFrom picks the refresh token: the body for mobile; for web the cookie, which is accepted only when the
// request carries "X-Client: web" (a custom header cannot be added by a cross-site form, so this is the CSRF guard on
// top of SameSite=Strict).
func (h *Handler) refreshTokenFrom(ctx context.Context, body *gen.RefreshRequest, xClient *string) string {
	if body != nil && body.RefreshToken != nil && *body.RefreshToken != "" {
		return *body.RefreshToken
	}
	if m := metaFrom(ctx); m != nil && h.cookie != nil && xClient != nil && *xClient == "web" {
		return m.cookie
	}
	return ""
}

func problemBody(status int, code, detail string) gen.Problem {
	p := gen.Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

func toTokenResponse(t identity.Tokens, refresh *string) gen.TokenResponse {
	return gen.TokenResponse{
		AccessToken: t.AccessToken, AccessTokenExpiresAt: t.AccessExpiresAt,
		RefreshToken: refresh, RefreshTokenExpiresAt: t.RefreshExpiresAt,
		MustChangePassword: t.MustChangePassword,
	}
}

// Login implements POST /auth/login.
func (h *Handler) Login(ctx context.Context, req gen.LoginRequestObject) (gen.LoginResponseObject, error) {
	if h.auth == nil {
		return nil, errAuthNotConfigured
	}
	username := ""
	if req.Body != nil {
		username = strings.TrimSpace(req.Body.Username)
	}
	if req.Body == nil || len(username) < 3 || len(username) > 64 || req.Body.Password == "" || len(req.Body.Password) > 256 {
		return gen.Login400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
			problemBody(http.StatusBadRequest, "validation_failed", "username and password are required"))}, nil
	}
	client := clientFrom(ctx, asString(req.Params.XClient), req.Params.XClientVersion)
	tokens, err := h.auth.Login(ctx, username, req.Body.Password, client)
	switch {
	case err == nil:
		return gen.Login200JSONResponse(toTokenResponse(tokens, h.deliverRefresh(ctx, client.Name, tokens))), nil
	case errors.As(err, new(*identity.MFAChallengeError)):
		var ce *identity.MFAChallengeError
		errors.As(err, &ce)
		return mfaChallenge(ce), nil
	case errors.Is(err, identity.ErrInvalidCredentials):
		return gen.Login401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: gen.UnauthorizedApplicationProblemPlusJSONResponse(
			problemBody(http.StatusUnauthorized, "invalid_credentials", "Incorrect username or password."))}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.Login423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked",
			"Too many failed attempts. Try again later.")), nil
	default:
		return nil, err // handled as 500 without leaking internals
	}
}

// RefreshToken implements POST /auth/refresh.
func (h *Handler) RefreshToken(ctx context.Context, req gen.RefreshTokenRequestObject) (gen.RefreshTokenResponseObject, error) {
	if h.auth == nil {
		return nil, errAuthNotConfigured
	}
	xClient := asString(req.Params.XClient)
	token := h.refreshTokenFrom(ctx, req.Body, xClient)
	if token == "" {
		return gen.RefreshToken400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
			problemBody(http.StatusBadRequest, "validation_failed", "refreshToken is required"))}, nil
	}
	client := clientFrom(ctx, xClient, req.Params.XClientVersion)
	tokens, err := h.auth.Refresh(ctx, token, client)
	switch {
	case err == nil:
		return gen.RefreshToken200JSONResponse(toTokenResponse(tokens, h.deliverRefresh(ctx, client.Name, tokens))), nil
	case errors.Is(err, identity.ErrRefreshReuse):
		h.clearRefreshCookie(ctx)
		return gen.RefreshToken401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: gen.UnauthorizedApplicationProblemPlusJSONResponse(
			problemBody(http.StatusUnauthorized, "refresh_reuse", "This session was ended for your security. Please sign in again."))}, nil
	case errors.Is(err, identity.ErrInvalidRefresh):
		h.clearRefreshCookie(ctx)
		return gen.RefreshToken401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: gen.UnauthorizedApplicationProblemPlusJSONResponse(
			problemBody(http.StatusUnauthorized, "invalid_refresh_token", "Your session has expired. Please sign in again."))}, nil
	default:
		return nil, err
	}
}

// Logout implements POST /auth/logout. It never fails for a missing or unknown token: the goal state (signed out,
// cookie cleared) is reached either way.
func (h *Handler) Logout(ctx context.Context, req gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	if h.auth == nil {
		return nil, errAuthNotConfigured
	}
	token := ""
	if req.Body != nil && req.Body.RefreshToken != nil {
		token = *req.Body.RefreshToken
	}
	if token == "" {
		if m := metaFrom(ctx); m != nil {
			token = m.cookie
		}
	}
	h.clearRefreshCookie(ctx)
	if token == "" {
		return gen.Logout204Response{}, nil
	}
	if err := h.auth.Logout(ctx, token); err != nil {
		return nil, err
	}
	return gen.Logout204Response{}, nil
}

func asString[T ~string](p *T) *string {
	if p == nil {
		return nil
	}
	s := string(*p)
	return &s
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
