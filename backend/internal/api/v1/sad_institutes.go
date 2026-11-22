package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/sad/institutes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// InstituteService is the subset of institutes.Service used by the handler (SAD-01, SAD-02, SAD-03).
type InstituteService interface {
	Plans(ctx context.Context) ([]institutes.Plan, error)
	List(ctx context.Context, q crud.ListQuery, f institutes.Filter) (crud.Page[institutes.Institute], error)
	Get(ctx context.Context, id string) (institutes.Institute, error)
	Create(ctx context.Context, a audit.Actor, in institutes.CreateInput) (institutes.Created, error)
	Update(ctx context.Context, a audit.Actor, id string, name, planCode *string) (institutes.Institute, error)
	SetStatus(ctx context.Context, a audit.Actor, id, status string) (institutes.Institute, error)
	SetModules(ctx context.Context, a audit.Actor, id string, modules []string) (institutes.Institute, error)
}

// WithInstitutes enables the Super Admin institute operations.
func WithInstitutes(s InstituteService) Option { return func(h *Handler) { h.institutes = s } }

func init() {
	for _, op := range []string{"ListPlatformPlans", "ListInstitutes", "CreateInstitute", "GetInstitute", "UpdateInstitute", "SetInstituteStatus", "SetInstituteModules"} {
		platformOperations[op] = true
	}
}

// platformActor is the audit actor of a platform operation.
func platformActor(ctx context.Context) audit.Actor {
	a := audit.Actor{Source: "web"}
	if p, ok := PlatformPrincipalFrom(ctx); ok {
		a.UserID = p.UserID
	}
	if m := metaFrom(ctx); m != nil {
		a.IP, a.UserAgent, a.RequestID = m.ip, m.userAgent, m.requestID
	}
	return a
}

func toGenInstitute(i institutes.Institute) gen.Institute {
	return gen.Institute{Id: mustUUID(i.ID), Code: i.Code, Name: i.Name, Status: gen.InstituteStatus(i.Status), PlanCode: i.PlanCode, PlanName: i.PlanName,
		Modules: nonNil(i.Modules), CreatedAt: i.CreatedAt, WentLiveAt: i.WentLiveAt}
}

func instituteNotFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

func badRequestFields(fe crud.FieldErrors) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))
}

// ListPlatformPlans implements GET /platform/plans.
func (h *Handler) ListPlatformPlans(ctx context.Context, _ gen.ListPlatformPlansRequestObject) (gen.ListPlatformPlansResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	plans, err := h.institutes.Plans(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListPlatformPlans200JSONResponse{Items: make([]gen.Plan, len(plans))}
	for i, p := range plans {
		out.Items[i] = gen.Plan{Code: p.Code, Name: p.Name, Modules: nonNil(p.Modules)}
	}
	return out, nil
}

// ListInstitutes implements GET /platform/institutes.
func (h *Handler) ListInstitutes(ctx context.Context, req gen.ListInstitutesRequestObject) (gen.ListInstitutesResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, institutes.SortKeys, institutes.DefaultSort)
	if qerr != nil {
		return gen.ListInstitutes400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(qerr)}, nil
	}
	f := institutes.Filter{}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		var fe crud.FieldErrors
		oneOf(&fe, "status", f.Status, institutes.Statuses...)
		if fe != nil {
			return gen.ListInstitutes400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(fe)}, nil
		}
	}
	p, err := h.institutes.List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := gen.InstitutePage{Items: make([]gen.Institute, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenInstitute(it)
	}
	return gen.ListInstitutes200JSONResponse(out), nil
}

// CreateInstitute implements POST /platform/institutes.
func (h *Handler) CreateInstitute(ctx context.Context, req gen.CreateInstituteRequestObject) (gen.CreateInstituteResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.CreateInstitute400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	b := req.Body
	c, err := h.institutes.Create(ctx, platformActor(ctx), institutes.CreateInput{Code: b.Code, Name: b.Name, PlanCode: b.PlanCode,
		Admin: users.CreateInput{Username: b.Admin.Username, DisplayName: b.Admin.DisplayName, Email: b.Admin.Email, Phone: b.Admin.Phone}})
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.CreateInstitute400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(fe)}, nil
	case err != nil:
		return nil, err
	}
	i := toGenInstitute(c.Institute)
	return gen.CreateInstitute201JSONResponse{Id: i.Id, Code: i.Code, Name: i.Name, Status: gen.InstituteCreatedStatus(i.Status), PlanCode: i.PlanCode, PlanName: i.PlanName,
		Modules: i.Modules, CreatedAt: i.CreatedAt, WentLiveAt: i.WentLiveAt, AdminUsername: c.AdminUsername, AdminInviteLink: c.AdminInviteLink}, nil
}

// GetInstitute implements GET /platform/institutes/{instituteId}.
func (h *Handler) GetInstitute(ctx context.Context, req gen.GetInstituteRequestObject) (gen.GetInstituteResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	i, err := h.institutes.Get(ctx, req.InstituteId.String())
	if errors.Is(err, institutes.ErrNotFound) {
		return gen.GetInstitute404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetInstitute200JSONResponse(toGenInstitute(i)), nil
}

// UpdateInstitute implements PATCH /platform/institutes/{instituteId}.
func (h *Handler) UpdateInstitute(ctx context.Context, req gen.UpdateInstituteRequestObject) (gen.UpdateInstituteResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.UpdateInstitute400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	i, err := h.institutes.Update(ctx, platformActor(ctx), req.InstituteId.String(), req.Body.Name, req.Body.PlanCode)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.UpdateInstitute400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(fe)}, nil
	case errors.Is(err, institutes.ErrNotFound):
		return gen.UpdateInstitute404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.UpdateInstitute200JSONResponse(toGenInstitute(i)), nil
}

// SetInstituteStatus implements POST /platform/institutes/{instituteId}/status.
func (h *Handler) SetInstituteStatus(ctx context.Context, req gen.SetInstituteStatusRequestObject) (gen.SetInstituteStatusResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	status := ""
	if req.Body != nil {
		status = string(req.Body.Status)
	}
	i, err := h.institutes.SetStatus(ctx, platformActor(ctx), req.InstituteId.String(), status)
	switch {
	case errors.Is(err, institutes.ErrNotFound):
		return gen.SetInstituteStatus404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	case errors.Is(err, institutes.ErrInvalidState):
		return gen.SetInstituteStatus409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetInstituteStatus200JSONResponse(toGenInstitute(i)), nil
}

// SetInstituteModules implements PUT /platform/institutes/{instituteId}/modules.
func (h *Handler) SetInstituteModules(ctx context.Context, req gen.SetInstituteModulesRequestObject) (gen.SetInstituteModulesResponseObject, error) {
	if h.institutes == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.SetInstituteModules400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	i, err := h.institutes.SetModules(ctx, platformActor(ctx), req.InstituteId.String(), req.Body.Modules)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.SetInstituteModules400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(fe)}, nil
	case errors.Is(err, institutes.ErrNotFound):
		return gen.SetInstituteModules404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetInstituteModules200JSONResponse(toGenInstitute(i)), nil
}
