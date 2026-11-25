package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/onb/controltower"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// ControlTowerService is what the onboarding control tower needs (controltower.Service satisfies it).
type ControlTowerService interface {
	Overview(ctx context.Context) (controltower.View, error)
	Assign(ctx context.Context, actor audit.Actor, domain string, in controltower.Assignment) (controltower.Item, error)
	Transition(ctx context.Context, actor audit.Actor, domain, to string) (controltower.Item, error)
}

// WithControlTower enables the /onboarding/control-tower operations.
func WithControlTower(s ControlTowerService) Option { return func(h *Handler) { h.controlTower = s } }

// mayTower says whether the caller holds the permission institute-wide: the tower shows the whole institute's progress,
// so a narrower grant is not enough. The permission itself is checked per operation from the contract.
func mayTower(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && contains(a.Effective.Scopes["onb.control_tower"], authz.ScopeTenant)
}

func toGenTowerDomain(it controltower.Item) gen.ControlTowerDomain {
	out := gen.ControlTowerDomain{Id: it.ID, Title: it.Title, Module: it.Module, Status: gen.ControlTowerDomainStatus(it.Status), Percent: it.Percent,
		HasSource: it.HasSource, Checks: make([]gen.ReadinessCheck, len(it.Checks)), Blockers: make([]gen.ControlTowerBlocker, len(it.Blockers)),
		OwnerName: it.OwnerName, Overdue: it.Overdue, State: gen.ControlTowerDomainState(it.State), OpenImports: it.OpenImports,
		ReviewRequestedAt: it.ReviewRequestedAt, ReadyAt: it.ReadyAt}
	for i, c := range it.Checks {
		out.Checks[i] = gen.ReadinessCheck{Key: c.Key, Label: c.Label, Done: c.Done, Total: c.Total}
	}
	for i, b := range it.Blockers {
		out.Blockers[i] = gen.ControlTowerBlocker{Id: b.ID, Title: b.Title}
	}
	if it.Note != "" {
		out.Note = &it.Note
	}
	if it.OwnerID != nil {
		id := mustUUID(*it.OwnerID)
		out.OwnerId = &id
	}
	if it.DueDate != nil {
		if t, err := time.Parse("2006-01-02", *it.DueDate); err == nil {
			out.DueDate = &openapi_types.Date{Time: t}
		}
	}
	return out
}

func towerNotFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

// GetControlTower implements GET /onboarding/control-tower.
func (h *Handler) GetControlTower(ctx context.Context, _ gen.GetControlTowerRequestObject) (gen.GetControlTowerResponseObject, error) {
	if h.controlTower == nil {
		return nil, errNotConfigured
	}
	if !mayTower(ctx) {
		return gen.GetControlTower403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	v, err := h.controlTower.Overview(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ControlTower{Domains: make([]gen.ControlTowerDomain, len(v.Domains)), Total: v.Total, Ready: v.Ready, Percent: v.Percent}
	for i, d := range v.Domains {
		out.Domains[i] = toGenTowerDomain(d)
	}
	return gen.GetControlTower200JSONResponse(out), nil
}

// AssignControlTowerDomain implements PUT /onboarding/control-tower/domains/{domain}.
func (h *Handler) AssignControlTowerDomain(ctx context.Context, req gen.AssignControlTowerDomainRequestObject) (gen.AssignControlTowerDomainResponseObject, error) {
	if h.controlTower == nil {
		return nil, errNotConfigured
	}
	if !mayTower(ctx) {
		return gen.AssignControlTowerDomain403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.AssignControlTowerDomain400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	in := controltower.Assignment{}
	if req.Body.OwnerId != nil {
		s := req.Body.OwnerId.String()
		in.OwnerID = &s
	}
	if req.Body.DueDate != nil {
		s := req.Body.DueDate.Format("2006-01-02")
		in.DueDate = &s
	}
	it, err := h.controlTower.Assign(ctx, actorFrom(ctx), string(req.Domain), in)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.AssignControlTowerDomain400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, controltower.ErrNotFound):
		return gen.AssignControlTowerDomain404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: towerNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.AssignControlTowerDomain200JSONResponse(toGenTowerDomain(it)), nil
}

// MoveControlTowerDomain implements POST /onboarding/control-tower/domains/{domain}/state.
func (h *Handler) MoveControlTowerDomain(ctx context.Context, req gen.MoveControlTowerDomainRequestObject) (gen.MoveControlTowerDomainResponseObject, error) {
	if h.controlTower == nil {
		return nil, errNotConfigured
	}
	if !mayTower(ctx) {
		return gen.MoveControlTowerDomain403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.MoveControlTowerDomain400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	it, err := h.controlTower.Transition(ctx, actorFrom(ctx), string(req.Domain), string(req.Body.To))
	var fe crud.FieldErrors
	var blocked controltower.Blocked
	switch {
	case errors.As(err, &fe):
		return gen.MoveControlTowerDomain400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, controltower.ErrNotFound):
		return gen.MoveControlTowerDomain404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: towerNotFound()}, nil
	case errors.As(err, &blocked):
		p := problemBody(http.StatusConflict, "blocked", "This change cannot be made yet: "+strings.Join(blocked.Reasons, " "))
		list := make([]gen.ProblemFieldError, len(blocked.Reasons))
		for i, r := range blocked.Reasons {
			list[i] = gen.ProblemFieldError{Field: "to", Code: "blocked", Message: r}
		}
		p.Errors = &list
		return gen.MoveControlTowerDomain409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	case err != nil:
		return nil, err
	}
	return gen.MoveControlTowerDomain200JSONResponse(toGenTowerDomain(it)), nil
}
