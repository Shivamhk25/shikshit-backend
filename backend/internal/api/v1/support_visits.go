package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// supportAuthenticator is what read-only support visits (SAD-05) need from the institute identity service.
type supportAuthenticator interface {
	RedeemSupportVisit(ctx context.Context, code string) (identity.Tokens, identity.SupportVisit, error)
	EndSupportVisit(ctx context.Context, p identity.Principal) error
	RecordSupportRead(ctx context.Context, p identity.Principal, operationID, path string) error
}

// visitStarter is what starting a visit needs from the platform identity service.
type visitStarter interface {
	StartSupportVisit(ctx context.Context, a audit.Actor, p identity.PlatformPrincipal, tenantID, reason string) (identity.VisitStart, error)
}

func init() { platformOperations["StartSupportVisit"] = true }

// StartSupportVisit implements POST /platform/institutes/{instituteId}/visits.
func (h *Handler) StartSupportVisit(ctx context.Context, req gen.StartSupportVisitRequestObject) (gen.StartSupportVisitResponseObject, error) {
	starter, ok := h.platform.(visitStarter)
	p, signed := PlatformPrincipalFrom(ctx)
	if !ok || !signed {
		return nil, errNotConfigured
	}
	reason := ""
	if req.Body != nil {
		reason = req.Body.Reason
	}
	v, err := starter.StartSupportVisit(ctx, platformActor(ctx), p, req.InstituteId.String(), reason)
	switch {
	case errors.Is(err, identity.ErrVisitReason):
		return gen.StartSupportVisit400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", err.Error()))}, nil
	case errors.Is(err, identity.ErrVisitUnavailable):
		return gen.StartSupportVisit409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "not_visitable", "Only onboarding and live institutes can be visited."))}, nil
	case err != nil:
		return nil, err
	}
	return gen.StartSupportVisit201JSONResponse{VisitId: mustUUID(v.ID), Code: v.Code, InstituteCode: v.InstituteCode, ExpiresAt: v.ExpiresAt}, nil
}

// RedeemSupportVisit implements POST /auth/support-visit (on the school's address).
func (h *Handler) RedeemSupportVisit(ctx context.Context, req gen.RedeemSupportVisitRequestObject) (gen.RedeemSupportVisitResponseObject, error) {
	s, ok := h.auth.(supportAuthenticator)
	if !ok {
		return nil, errAuthNotConfigured
	}
	if req.Body == nil || req.Body.Code == "" {
		return gen.RedeemSupportVisit400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "code is required")}, nil
	}
	t, _, err := s.RedeemSupportVisit(ctx, req.Body.Code)
	if errors.Is(err, identity.ErrVisitCode) {
		return gen.RedeemSupportVisit401ApplicationProblemPlusJSONResponse{UnauthorizedApplicationProblemPlusJSONResponse: unauthorized("invalid_visit_code", err.Error())}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RedeemSupportVisit200JSONResponse(toTokenResponse(t, nil)), nil
}

// EndSupportVisit implements POST /support-visit/end.
func (h *Handler) EndSupportVisit(ctx context.Context, _ gen.EndSupportVisitRequestObject) (gen.EndSupportVisitResponseObject, error) {
	s, ok := h.auth.(supportAuthenticator)
	p, signed := PrincipalFrom(ctx)
	if !ok || !signed {
		return nil, errAuthNotConfigured
	}
	if p.Support == nil {
		return gen.EndSupportVisit403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(problemBody(http.StatusForbidden, "not_a_visit", "Only a support visit can be ended here."))}, nil
	}
	if err := s.EndSupportVisit(ctx, p); err != nil {
		return nil, err
	}
	return gen.EndSupportVisit204Response{}, nil
}
