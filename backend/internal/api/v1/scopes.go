package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/modules/iam/scopes"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
)

// ScopeAssignmentService is what the scope-assignment operations need (scopes.Service satisfies it).
type ScopeAssignmentService interface {
	List(ctx context.Context, f scopes.Filter) ([]scopes.Assignment, error)
	Create(ctx context.Context, a audit.Actor, in scopes.Input) (scopes.Assignment, error)
	Delete(ctx context.Context, a audit.Actor, id string) error
}

// WithScopeAssignments enables the /scope-assignments operations.
func WithScopeAssignments(s ScopeAssignmentService) Option {
	return func(h *Handler) { h.scopeAssignments = s }
}

func toGenScopeAssignment(x scopes.Assignment) gen.ScopeAssignment {
	return gen.ScopeAssignment{
		Id: openapi_types.UUID(uuid.MustParse(x.ID)), UserId: openapi_types.UUID(uuid.MustParse(x.UserID)), UserName: x.UserName,
		ScopeType: gen.ScopeAssignmentScopeType(x.ScopeType), ScopeRef: x.ScopeRef, Label: x.Label, StartsAt: x.StartsAt, EndsAt: x.EndsAt, CreatedAt: x.CreatedAt,
	}
}

func uuidOr(v *openapi_types.UUID) string {
	if v == nil {
		return ""
	}
	return v.String()
}

// ListScopeAssignments implements GET /scope-assignments.
func (h *Handler) ListScopeAssignments(ctx context.Context, req gen.ListScopeAssignmentsRequestObject) (gen.ListScopeAssignmentsResponseObject, error) {
	if h.scopeAssignments == nil {
		return nil, errNotConfigured
	}
	f := scopes.Filter{UserID: uuidOr(req.Params.UserId), ScopeRef: uuidOr(req.Params.ScopeRef)}
	if req.Params.ScopeType != nil {
		f.ScopeType = string(*req.Params.ScopeType)
	}
	list, err := h.scopeAssignments.List(ctx, f)
	if err != nil {
		return nil, err
	}
	out := gen.ListScopeAssignments200JSONResponse{Items: make([]gen.ScopeAssignment, len(list))}
	for i, x := range list {
		out.Items[i] = toGenScopeAssignment(x)
	}
	return out, nil
}

// CreateScopeAssignment implements POST /scope-assignments.
func (h *Handler) CreateScopeAssignment(ctx context.Context, req gen.CreateScopeAssignmentRequestObject) (gen.CreateScopeAssignmentResponseObject, error) {
	if h.scopeAssignments == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.CreateScopeAssignment400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	b := req.Body
	x, err := h.scopeAssignments.Create(ctx, actorFrom(ctx), scopes.Input{UserID: b.UserId.String(), ScopeType: string(b.ScopeType), ScopeRef: b.ScopeRef.String(), StartsAt: b.StartsAt, EndsAt: b.EndsAt})
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.CreateScopeAssignment400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateScopeAssignment201JSONResponse(toGenScopeAssignment(x)), nil
}

// DeleteScopeAssignment implements DELETE /scope-assignments/{assignmentId}.
func (h *Handler) DeleteScopeAssignment(ctx context.Context, req gen.DeleteScopeAssignmentRequestObject) (gen.DeleteScopeAssignmentResponseObject, error) {
	if h.scopeAssignments == nil {
		return nil, errNotConfigured
	}
	err := h.scopeAssignments.Delete(ctx, actorFrom(ctx), req.AssignmentId.String())
	if errors.Is(err, scopes.ErrNotFound) {
		return gen.DeleteScopeAssignment404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteScopeAssignment204Response{}, nil
}
