package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// PlatformAuthenticator is what the platform (Super Admin) operations need from identity.PlatformService.
type PlatformAuthenticator interface {
	Login(ctx context.Context, email, password string, c identity.Client) (identity.PlatformChallenge, error)
	Verify(ctx context.Context, token, code string, c identity.Client) (identity.Tokens, error)
	StartSetup(ctx context.Context, token string) (identity.MFASetup, error)
	ConfirmSetup(ctx context.Context, token, code string, c identity.Client) (identity.Tokens, error)
	Refresh(ctx context.Context, refreshToken string, c identity.Client) (identity.Tokens, error)
	Logout(ctx context.Context, refreshToken string) error
	Authenticate(ctx context.Context, token string) (identity.PlatformPrincipal, error)
}

// PlatformPrefix is the path every platform operation lives under. The tenancy middleware must skip it: platform
// users belong to no institute (PlatformPath reports whether a request path is one of them).
const PlatformPrefix = BasePath + "/platform/"

// PlatformPath reports whether a request path is a platform operation.
func PlatformPath(path string) bool { return strings.HasPrefix(path, PlatformPrefix) }

// platformOperations need a signed-in platform user. A contract test keeps this set equal to the operations marked
// x-platform, so a platform operation can never be served to an institute user or to nobody.
var platformOperations = map[string]bool{
	"GetPlatformMe": true,
}

// PlatformOperations returns a copy of the set of operations that need a platform user.
func PlatformOperations() map[string]bool {
	out := make(map[string]bool, len(platformOperations))
	for k, v := range platformOperations {
		out[k] = v
	}
	return out
}

// WithPlatform enables the platform operations. Web clients get the platform refresh token in its own httpOnly cookie
// (name and path differ from the institute cookie, so the two sessions never mix).
func WithPlatform(p PlatformAuthenticator, cookie CookieConfig) Option {
	if cookie.Path == "" {
		cookie.Path = BasePath + "/platform/auth"
	}
	return func(h *Handler) {
		h.platform = p
		h.platformCookie = &cookie
	}
}

type platformPrincipalKey struct{}

// PlatformPrincipalFrom returns the signed-in platform user of a platform operation.
func PlatformPrincipalFrom(ctx context.Context) (identity.PlatformPrincipal, bool) {
	p, ok := ctx.Value(platformPrincipalKey{}).(identity.PlatformPrincipal)
	return p, ok
}

// authenticatePlatform is the access check for x-platform operations: a live platform session, nothing else.
func (h *Handler) authenticatePlatform(ctx context.Context, header string) (context.Context, bool, error) {
	token, ok := bearerToken(header)
	if !ok || h.platform == nil {
		return ctx, false, nil
	}
	p, err := h.platform.Authenticate(ctx, token)
	if errors.Is(err, identity.ErrUnauthenticated) {
		return ctx, false, nil
	}
	if err != nil {
		return ctx, false, err
	}
	return context.WithValue(ctx, platformPrincipalKey{}, p), true, nil
}

func (h *Handler) platformConfigured() error {
	if h.platform == nil {
		return errNotConfigured
	}
	return nil
}

// platformTokens returns the token response and, for web, sets the platform refresh cookie instead of the body field.
func (h *Handler) platformTokens(ctx context.Context, t identity.Tokens) gen.TokenResponse {
	m := metaFrom(ctx)
	if h.platformCookie == nil || m == nil {
		tok := t.RefreshToken
		return toTokenResponse(t, &tok)
	}
	m.out = &http.Cookie{
		Name: h.platformCookie.Name, Value: t.RefreshToken, Path: h.platformCookie.Path, Expires: t.RefreshExpiresAt,
		HttpOnly: true, Secure: h.platformCookie.Secure, SameSite: http.SameSiteStrictMode,
	}
	return toTokenResponse(t, nil)
}

func (h *Handler) clearPlatformCookie(ctx context.Context) {
	if m := metaFrom(ctx); m != nil && h.platformCookie != nil {
		m.out = &http.Cookie{
			Name: h.platformCookie.Name, Value: "", Path: h.platformCookie.Path, MaxAge: -1, Expires: time.Unix(0, 0),
			HttpOnly: true, Secure: h.platformCookie.Secure, SameSite: http.SameSiteStrictMode,
		}
	}
}

func platformClient(ctx context.Context) identity.Client {
	web := "web"
	return clientFrom(ctx, &web, nil)
}

// PlatformLogin implements POST /platform/auth/login.
func (h *Handler) PlatformLogin(ctx context.Context, req gen.PlatformLoginRequestObject) (gen.PlatformLoginResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	if req.Body == nil || strings.TrimSpace(req.Body.Email) == "" || req.Body.Password == "" || len(req.Body.Password) > 256 {
		return gen.PlatformLogin400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "email and password are required")}, nil
	}
	ch, err := h.platform.Login(ctx, req.Body.Email, req.Body.Password, platformClient(ctx))
	switch {
	case err == nil:
		return gen.PlatformLogin202JSONResponse{MfaRequired: gen.MfaChallengeMfaRequired(ch.Kind), ChallengeToken: ch.Token, ExpiresAt: ch.ExpiresAt}, nil
	case errors.Is(err, identity.ErrInvalidCredentials):
		return gen.PlatformLogin401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_credentials", "Incorrect email or password.")}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.PlatformLogin423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked", msgLocked)), nil
	}
	return nil, err
}

// PlatformVerifyMfa implements POST /platform/auth/mfa/verify.
func (h *Handler) PlatformVerifyMfa(ctx context.Context, req gen.PlatformVerifyMfaRequestObject) (gen.PlatformVerifyMfaResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.PlatformVerifyMfa400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken and code are required")}, nil
	}
	t, err := h.platform.Verify(ctx, req.Body.ChallengeToken, req.Body.Code, platformClient(ctx))
	switch {
	case err == nil:
		return gen.PlatformVerifyMfa200JSONResponse(h.platformTokens(ctx, t)), nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.PlatformVerifyMfa401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFAChallenge):
		return gen.PlatformVerifyMfa401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.PlatformVerifyMfa423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked", msgLocked)), nil
	}
	return nil, err
}

// PlatformStartMfaSetup implements POST /platform/auth/mfa/setup.
func (h *Handler) PlatformStartMfaSetup(ctx context.Context, req gen.PlatformStartMfaSetupRequestObject) (gen.PlatformStartMfaSetupResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.PlatformStartMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken is required")}, nil
	}
	s, err := h.platform.StartSetup(ctx, req.Body.ChallengeToken)
	if errors.Is(err, identity.ErrMFAChallenge) {
		return gen.PlatformStartMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.PlatformStartMfaSetup200JSONResponse{Secret: s.Secret, OtpauthUri: s.URI}, nil
}

// PlatformConfirmMfaSetup implements POST /platform/auth/mfa/setup/confirm.
func (h *Handler) PlatformConfirmMfaSetup(ctx context.Context, req gen.PlatformConfirmMfaSetupRequestObject) (gen.PlatformConfirmMfaSetupResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.PlatformConfirmMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken and code are required")}, nil
	}
	t, err := h.platform.ConfirmSetup(ctx, req.Body.ChallengeToken, req.Body.Code, platformClient(ctx))
	switch {
	case err == nil:
		return gen.PlatformConfirmMfaSetup200JSONResponse(h.platformTokens(ctx, t)), nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.PlatformConfirmMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFAChallenge):
		return gen.PlatformConfirmMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.PlatformConfirmMfaSetup423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked", msgLocked)), nil
	}
	return nil, err
}

// PlatformRefresh implements POST /platform/auth/refresh. The cookie is accepted only with "X-Client: web", which a
// cross-site form cannot send.
func (h *Handler) PlatformRefresh(ctx context.Context, req gen.PlatformRefreshRequestObject) (gen.PlatformRefreshResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	token := ""
	if m := metaFrom(ctx); m != nil && req.Params.XClient != nil && *req.Params.XClient == "web" {
		token = m.platformCookie
	}
	if token == "" {
		return gen.PlatformRefresh400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "no platform session")}, nil
	}
	t, err := h.platform.Refresh(ctx, token, platformClient(ctx))
	switch {
	case err == nil:
		return gen.PlatformRefresh200JSONResponse(h.platformTokens(ctx, t)), nil
	case errors.Is(err, identity.ErrRefreshReuse), errors.Is(err, identity.ErrInvalidRefresh):
		h.clearPlatformCookie(ctx)
		return gen.PlatformRefresh401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_refresh_token", "Your session has ended. Please sign in again.")}, nil
	}
	return nil, err
}

// PlatformLogout implements POST /platform/auth/logout.
func (h *Handler) PlatformLogout(ctx context.Context, _ gen.PlatformLogoutRequestObject) (gen.PlatformLogoutResponseObject, error) {
	if err := h.platformConfigured(); err != nil {
		return nil, err
	}
	token := ""
	if m := metaFrom(ctx); m != nil {
		token = m.platformCookie
	}
	h.clearPlatformCookie(ctx)
	if token != "" {
		if err := h.platform.Logout(ctx, token); err != nil {
			return nil, err
		}
	}
	return gen.PlatformLogout204Response{}, nil
}

// GetPlatformMe implements GET /platform/me.
func (h *Handler) GetPlatformMe(ctx context.Context, _ gen.GetPlatformMeRequestObject) (gen.GetPlatformMeResponseObject, error) {
	p, ok := PlatformPrincipalFrom(ctx)
	if !ok {
		return nil, errAuthNotConfigured
	}
	return gen.GetPlatformMe200JSONResponse{Id: mustUUID(p.UserID), Email: p.Email, Name: p.Name}, nil
}
