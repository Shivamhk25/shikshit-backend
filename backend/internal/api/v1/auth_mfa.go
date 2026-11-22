package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// MFAService is two-step sign-in (FND-05). The identity service implements it next to Authenticator; the handler
// finds it on the same value, so wiring stays one option.
type MFAService interface {
	VerifyMFA(ctx context.Context, token, code string, c identity.Client) (identity.Tokens, error)
	StartMFASetup(ctx context.Context, token string) (identity.MFASetup, error)
	ConfirmMFASetup(ctx context.Context, a audit.Actor, token, code string, c identity.Client) (identity.Tokens, []string, error)
	MFAStatusFor(ctx context.Context, userID string) (identity.MFAStatus, error)
	BeginMFAEnrollment(ctx context.Context, userID string) (identity.MFASetup, error)
	ConfirmMFAEnrollment(ctx context.Context, a audit.Actor, userID, code string) ([]string, error)
	DisableMFA(ctx context.Context, a audit.Actor, userID, code string) error
}

func (h *Handler) mfa() (MFAService, error) {
	m, ok := h.auth.(MFAService)
	if !ok {
		return nil, errAuthNotConfigured
	}
	return m, nil
}

func unauthorized(code, detail string) gen.UnauthorizedApplicationProblemPlusJSONResponse {
	return gen.UnauthorizedApplicationProblemPlusJSONResponse(problemBody(http.StatusUnauthorized, code, detail))
}

func mfaBadRequest(code, detail string) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, code, detail))
}

const (
	msgWrongCode = "That code is not right. Check the time on your phone and try again."
	msgExpired   = "This sign-in took too long or had too many wrong codes. Please sign in again."
	msgLocked    = "Too many failed attempts. Try again later."
)

func mfaChallenge(ce *identity.MFAChallengeError) gen.Login202JSONResponse {
	return gen.Login202JSONResponse{MfaRequired: gen.MfaChallengeMfaRequired(ce.Kind), ChallengeToken: ce.Token, ExpiresAt: ce.ExpiresAt}
}

// VerifyMfa implements POST /auth/mfa/verify.
func (h *Handler) VerifyMfa(ctx context.Context, req gen.VerifyMfaRequestObject) (gen.VerifyMfaResponseObject, error) {
	m, err := h.mfa()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.VerifyMfa400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken and code are required")}, nil
	}
	client := clientFrom(ctx, asString(req.Params.XClient), nil)
	tokens, err := m.VerifyMFA(ctx, req.Body.ChallengeToken, req.Body.Code, client)
	switch {
	case err == nil:
		return gen.VerifyMfa200JSONResponse(toTokenResponse(tokens, h.deliverRefresh(ctx, client.Name, tokens))), nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.VerifyMfa401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFAChallenge):
		return gen.VerifyMfa401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.VerifyMfa423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked", msgLocked)), nil
	}
	return nil, err
}

// StartMfaSetup implements POST /auth/mfa/setup.
func (h *Handler) StartMfaSetup(ctx context.Context, req gen.StartMfaSetupRequestObject) (gen.StartMfaSetupResponseObject, error) {
	m, err := h.mfa()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.StartMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken is required")}, nil
	}
	s, err := m.StartMFASetup(ctx, req.Body.ChallengeToken)
	if errors.Is(err, identity.ErrMFAChallenge) {
		return gen.StartMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.StartMfaSetup200JSONResponse{Secret: s.Secret, OtpauthUri: s.URI}, nil
}

// ConfirmMfaSetup implements POST /auth/mfa/setup/confirm.
func (h *Handler) ConfirmMfaSetup(ctx context.Context, req gen.ConfirmMfaSetupRequestObject) (gen.ConfirmMfaSetupResponseObject, error) {
	m, err := h.mfa()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.ConfirmMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "challengeToken and code are required")}, nil
	}
	client := clientFrom(ctx, asString(req.Params.XClient), nil)
	tokens, codes, err := m.ConfirmMFASetup(ctx, actorFrom(ctx), req.Body.ChallengeToken, req.Body.Code, client)
	switch {
	case err == nil:
		t := toTokenResponse(tokens, h.deliverRefresh(ctx, client.Name, tokens))
		return gen.ConfirmMfaSetup200JSONResponse{AccessToken: t.AccessToken, AccessTokenExpiresAt: t.AccessTokenExpiresAt, RefreshToken: t.RefreshToken,
			RefreshTokenExpiresAt: t.RefreshTokenExpiresAt, MustChangePassword: t.MustChangePassword, RecoveryCodes: codes}, nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.ConfirmMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFAChallenge):
		return gen.ConfirmMfaSetup401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("challenge_expired", msgExpired)}, nil
	case errors.Is(err, identity.ErrLocked):
		return gen.ConfirmMfaSetup423ApplicationProblemPlusJSONResponse(problemBody(http.StatusLocked, "account_locked", msgLocked)), nil
	}
	return nil, err
}

func (h *Handler) me(ctx context.Context) (MFAService, string, error) {
	m, err := h.mfa()
	if err != nil {
		return nil, "", err
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, "", errAuthNotConfigured
	}
	return m, p.UserID, nil
}

// GetMyMfa implements GET /me/mfa.
func (h *Handler) GetMyMfa(ctx context.Context, _ gen.GetMyMfaRequestObject) (gen.GetMyMfaResponseObject, error) {
	m, id, err := h.me(ctx)
	if err != nil {
		return nil, err
	}
	st, err := m.MFAStatusFor(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.GetMyMfa200JSONResponse{Enabled: st.Enabled, Required: st.Required, RecoveryCodesLeft: st.RecoveryCodesLeft}, nil
}

// StartMyMfaSetup implements POST /me/mfa/setup.
func (h *Handler) StartMyMfaSetup(ctx context.Context, _ gen.StartMyMfaSetupRequestObject) (gen.StartMyMfaSetupResponseObject, error) {
	m, id, err := h.me(ctx)
	if err != nil {
		return nil, err
	}
	s, err := m.BeginMFAEnrollment(ctx, id)
	if errors.Is(err, identity.ErrMFAAlreadyOn) {
		return gen.StartMyMfaSetup409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "mfa_already_on", "Two-step sign-in is already on."))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.StartMyMfaSetup200JSONResponse{Secret: s.Secret, OtpauthUri: s.URI}, nil
}

// ConfirmMyMfaSetup implements POST /me/mfa/confirm.
func (h *Handler) ConfirmMyMfaSetup(ctx context.Context, req gen.ConfirmMyMfaSetupRequestObject) (gen.ConfirmMyMfaSetupResponseObject, error) {
	m, id, err := h.me(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.ConfirmMyMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "code is required")}, nil
	}
	codes, err := m.ConfirmMFAEnrollment(ctx, actorFrom(ctx), id, req.Body.Code)
	switch {
	case err == nil:
		return gen.ConfirmMyMfaSetup200JSONResponse{RecoveryCodes: codes}, nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.ConfirmMyMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFAChallenge):
		return gen.ConfirmMyMfaSetup400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("setup_expired", "Setup took too long. Start again.")}, nil
	}
	return nil, err
}

// DisableMyMfa implements POST /me/mfa/disable.
func (h *Handler) DisableMyMfa(ctx context.Context, req gen.DisableMyMfaRequestObject) (gen.DisableMyMfaResponseObject, error) {
	m, id, err := h.me(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.DisableMyMfa400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "code is required")}, nil
	}
	err = m.DisableMFA(ctx, actorFrom(ctx), id, req.Body.Code)
	conflict := func(code, detail string) gen.DisableMyMfa409ApplicationProblemPlusJSONResponse {
		return gen.DisableMyMfa409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, code, detail))}
	}
	switch {
	case err == nil:
		return gen.DisableMyMfa204Response{}, nil
	case errors.Is(err, identity.ErrMFACode):
		return gen.DisableMyMfa400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("invalid_code", msgWrongCode)}, nil
	case errors.Is(err, identity.ErrMFARequired):
		return conflict("mfa_required", "Your school requires two-step sign-in for your account, so it cannot be turned off."), nil
	case errors.Is(err, identity.ErrMFANotOn):
		return conflict("mfa_not_on", "Two-step sign-in is not on."), nil
	}
	return nil, err
}

// ResetUserMfa implements POST /users/{userId}/reset-mfa.
func (h *Handler) ResetUserMfa(ctx context.Context, req gen.ResetUserMfaRequestObject) (gen.ResetUserMfaResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.ResetMFA(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.ResetUserMfa404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrSelfAction):
		return gen.ResetUserMfa409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "self_action", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.ResetUserMfa200JSONResponse(toGenUser(u)), nil
}
