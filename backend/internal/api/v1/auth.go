package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/platform/identity"
)

// errAuthNotConfigured is returned when the server was built without an identity service (a wiring bug, not a client error).
var errAuthNotConfigured = errors.New("apiv1: authentication is not configured")

// Authenticator is what the /auth operations need from the identity service.
type Authenticator interface {
	Login(ctx context.Context, username, password string, c identity.Client) (identity.Tokens, error)
	Refresh(ctx context.Context, refreshToken string, c identity.Client) (identity.Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
}

type clientMetaKey struct{}

// clientMeta is the request metadata stored on a session: caller address and user agent.
type clientMeta struct{ ip, userAgent string }

// withClientMeta copies the caller's address and user agent onto the request context, because strict handlers
// only receive a context. The address comes from echo's RealIP, which honours the configured IP extractor.
func withClientMeta(next gen.StrictHandlerFunc, _ string) gen.StrictHandlerFunc {
	return func(c echo.Context, request any) (any, error) {
		meta := clientMeta{ip: c.RealIP(), userAgent: c.Request().UserAgent()}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), clientMetaKey{}, meta)))
		return next(c, request)
	}
}

func clientFrom(ctx context.Context, name *string, version *string) identity.Client {
	c := identity.Client{Name: "web"}
	if name != nil {
		c.Name = *name
	}
	if version != nil {
		c.Version = *version
	}
	if m, ok := ctx.Value(clientMetaKey{}).(clientMeta); ok {
		c.IP, c.UserAgent = m.ip, m.userAgent
	}
	return c
}

func problemBody(status int, code, detail string) gen.Problem {
	p := gen.Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

func toTokenResponse(t identity.Tokens) gen.TokenResponse {
	return gen.TokenResponse{
		AccessToken: t.AccessToken, AccessTokenExpiresAt: t.AccessExpiresAt,
		RefreshToken: t.RefreshToken, RefreshTokenExpiresAt: t.RefreshExpiresAt,
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
	tokens, err := h.auth.Login(ctx, username, req.Body.Password, clientFrom(ctx, asString(req.Params.XClient), req.Params.XClientVersion))
	switch {
	case err == nil:
		return gen.Login200JSONResponse(toTokenResponse(tokens)), nil
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
	if req.Body == nil || req.Body.RefreshToken == "" {
		return gen.RefreshToken400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
			problemBody(http.StatusBadRequest, "validation_failed", "refreshToken is required"))}, nil
	}
	tokens, err := h.auth.Refresh(ctx, req.Body.RefreshToken, clientFrom(ctx, asString(req.Params.XClient), req.Params.XClientVersion))
	switch {
	case err == nil:
		return gen.RefreshToken200JSONResponse(toTokenResponse(tokens)), nil
	case errors.Is(err, identity.ErrRefreshReuse):
		return gen.RefreshToken401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: gen.UnauthorizedApplicationProblemPlusJSONResponse(
			problemBody(http.StatusUnauthorized, "refresh_reuse", "This session was ended for your security. Please sign in again."))}, nil
	case errors.Is(err, identity.ErrInvalidRefresh):
		return gen.RefreshToken401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: gen.UnauthorizedApplicationProblemPlusJSONResponse(
			problemBody(http.StatusUnauthorized, "invalid_refresh_token", "Your session has expired. Please sign in again."))}, nil
	default:
		return nil, err
	}
}

// Logout implements POST /auth/logout.
func (h *Handler) Logout(ctx context.Context, req gen.LogoutRequestObject) (gen.LogoutResponseObject, error) {
	if h.auth == nil {
		return nil, errAuthNotConfigured
	}
	if req.Body == nil || req.Body.RefreshToken == "" {
		return gen.Logout400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
			problemBody(http.StatusBadRequest, "validation_failed", "refreshToken is required"))}, nil
	}
	if err := h.auth.Logout(ctx, req.Body.RefreshToken); err != nil {
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
