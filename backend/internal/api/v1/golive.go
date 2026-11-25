package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/onb/controltower"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// GoLiveService is what the go-live gate needs (controltower.Service satisfies it).
type GoLiveService interface {
	GoLive(ctx context.Context) (controltower.Gate, error)
	SignOff(ctx context.Context, actor audit.Actor, notes string) (controltower.Signoff, error)
	Revoke(ctx context.Context, actor audit.Actor, reason string) (controltower.Signoff, error)
}

// WithGoLive enables the /onboarding/go-live operations.
func WithGoLive(s GoLiveService) Option { return func(h *Handler) { h.goLive = s } }

// mayGoLive says whether the caller holds the permission institute-wide: the gate decides for the whole institute, so a
// narrower grant is not enough. The permission itself is checked per operation from the contract.
func mayGoLive(ctx context.Context, resourcePerm string) bool {
	a, ok := AccessFrom(ctx)
	return ok && contains(a.Effective.Scopes[resourcePerm], authz.ScopeTenant)
}

func toGenGoLiveDomains(in []controltower.GateDomain) []gen.GoLiveDomain {
	out := make([]gen.GoLiveDomain, len(in))
	for i, d := range in {
		out[i] = gen.GoLiveDomain{Id: d.ID, Title: d.Title, Status: gen.GoLiveDomainStatus(d.Status), Percent: d.Percent, Assessable: d.Assessable, Ready: d.Ready}
	}
	return out
}

func toGenSignoff(s controltower.Signoff) gen.GoLiveSignoff {
	out := gen.GoLiveSignoff{Id: mustUUID(s.ID), SignedBy: mustUUID(s.SignedBy), SignedByName: s.SignedByName, SignedAt: s.SignedAt, Notes: s.Notes,
		Snapshot: toGenGoLiveDomains(s.Snapshot), RevokedAt: s.RevokedAt, RevokeReason: s.RevokeReason}
	if s.RevokedBy != nil {
		id := mustUUID(*s.RevokedBy)
		out.RevokedBy = &id
	}
	return out
}

// goLiveBlocked is the 409 body: the reasons the gate gave, one per field error.
func goLiveBlocked(b controltower.Blocked) gen.ConflictApplicationProblemPlusJSONResponse {
	p := problemBody(http.StatusConflict, "blocked", "This change cannot be made yet: "+strings.Join(b.Reasons, " "))
	list := make([]gen.ProblemFieldError, len(b.Reasons))
	for i, r := range b.Reasons {
		list[i] = gen.ProblemFieldError{Field: "gate", Code: "blocked", Message: r}
	}
	p.Errors = &list
	return gen.ConflictApplicationProblemPlusJSONResponse(p)
}

// GetGoLive implements GET /onboarding/go-live.
func (h *Handler) GetGoLive(ctx context.Context, _ gen.GetGoLiveRequestObject) (gen.GetGoLiveResponseObject, error) {
	if h.goLive == nil {
		return nil, errNotConfigured
	}
	if !mayGoLive(ctx, "onb.golive") {
		return gen.GetGoLive403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	g, err := h.goLive.GoLive(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.GoLiveGate{TenantStatus: gen.GoLiveGateTenantStatus(g.TenantStatus), Open: g.Open, Domains: toGenGoLiveDomains(g.Domains),
		Reasons: append([]string{}, g.Reasons...), History: make([]gen.GoLiveSignoff, len(g.History))}
	for i, s := range g.History {
		out.History[i] = toGenSignoff(s)
	}
	if g.Active != nil {
		a := toGenSignoff(*g.Active)
		out.Active = &a
	}
	return gen.GetGoLive200JSONResponse(out), nil
}

// SignOffGoLive implements POST /onboarding/go-live/sign-off.
func (h *Handler) SignOffGoLive(ctx context.Context, req gen.SignOffGoLiveRequestObject) (gen.SignOffGoLiveResponseObject, error) {
	if h.goLive == nil {
		return nil, errNotConfigured
	}
	if !mayGoLive(ctx, "onb.golive") {
		return gen.SignOffGoLive403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.SignOffGoLive400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	notes := ""
	if req.Body.Notes != nil {
		notes = *req.Body.Notes
	}
	s, err := h.goLive.SignOff(ctx, actorFrom(ctx), notes)
	var fe crud.FieldErrors
	var blocked controltower.Blocked
	switch {
	case errors.As(err, &fe):
		return gen.SignOffGoLive400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.As(err, &blocked):
		return gen.SignOffGoLive409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: goLiveBlocked(blocked)}, nil
	case err != nil:
		return nil, err
	}
	return gen.SignOffGoLive201JSONResponse(toGenSignoff(s)), nil
}

// RevokeGoLive implements POST /onboarding/go-live/revoke.
func (h *Handler) RevokeGoLive(ctx context.Context, req gen.RevokeGoLiveRequestObject) (gen.RevokeGoLiveResponseObject, error) {
	if h.goLive == nil {
		return nil, errNotConfigured
	}
	if !mayGoLive(ctx, "onb.golive") {
		return gen.RevokeGoLive403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.RevokeGoLive400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	s, err := h.goLive.Revoke(ctx, actorFrom(ctx), req.Body.Reason)
	var fe crud.FieldErrors
	var blocked controltower.Blocked
	switch {
	case errors.As(err, &fe):
		return gen.RevokeGoLive400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, controltower.ErrNoSignoff):
		return gen.RevokeGoLive404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: towerNotFound()}, nil
	case errors.As(err, &blocked):
		return gen.RevokeGoLive409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: goLiveBlocked(blocked)}, nil
	case err != nil:
		return nil, err
	}
	return gen.RevokeGoLive200JSONResponse(toGenSignoff(s)), nil
}
