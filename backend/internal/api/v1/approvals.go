package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// ApprovalService is the subset of approvals.Service used by the handler.
type ApprovalService interface {
	Inbox(ctx context.Context, a authz.Access, page, size int) (approvals.Page, error)
	Mine(ctx context.Context, userID, status string, page, size int) (approvals.Page, error)
	Get(ctx context.Context, a authz.Access, id string) (approvals.Detail, error)
	Decide(ctx context.Context, actor audit.Actor, a authz.Access, id string, in approvals.DecideInput) (approvals.Detail, error)
	Cancel(ctx context.Context, actor audit.Actor, a authz.Access, id, comment string) (approvals.Detail, error)
}

// WithApprovals enables the /approvals operations. Without it they answer 501.
func WithApprovals(s ApprovalService) Option { return func(h *Handler) { h.approvals = s } }

// approvalFail is an error answer shared by the approval operations: they all fail the same way.
type approvalFail gen.Problem

func (f approvalFail) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(f.Status)
	return json.NewEncoder(w).Encode(f)
}
func (f approvalFail) VisitGetApprovalResponse(w http.ResponseWriter) error     { return f.write(w) }
func (f approvalFail) VisitApproveApprovalResponse(w http.ResponseWriter) error { return f.write(w) }
func (f approvalFail) VisitRejectApprovalResponse(w http.ResponseWriter) error  { return f.write(w) }
func (f approvalFail) VisitCancelApprovalResponse(w http.ResponseWriter) error  { return f.write(w) }

// failApproval turns a service error into its problem answer; unknown errors come back as ok=false.
func failApproval(err error) (approvalFail, bool) {
	var fe crud.FieldErrors
	var ce approvals.ConflictError
	var fo approvals.ForbiddenError
	switch {
	case errors.Is(err, approvals.ErrNotFound):
		return approvalFail(problemBody(http.StatusNotFound, "not_found", "")), true
	case errors.As(err, &fe):
		return approvalFail(validationProblem(fe)), true
	case errors.As(err, &ce):
		return approvalFail(problemBody(http.StatusConflict, "conflict", ce.Message)), true
	case errors.As(err, &fo):
		return approvalFail(problemBody(http.StatusForbidden, "permission_denied", fo.Message)), true
	}
	return approvalFail{}, false
}

// approvalCaller returns who is asking and their access. Any signed-in school user may use the approval operations;
// what they see and may decide comes from their access.
func (h *Handler) approvalCaller(ctx context.Context) (authz.Access, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.approvals == nil || h.access == nil {
		return authz.Access{}, errNotConfigured
	}
	return h.resolveFor(ctx, p)
}

func toGenApproval(r approvals.Request) gen.Approval {
	return gen.Approval{Id: mustUUID(r.ID), TypeKey: r.TypeKey, TypeTitle: r.TypeTitle, SubjectId: r.SubjectID, Summary: r.Summary, Amount: r.Amount,
		RequesterId: mustUUID(r.RequesterID), RequesterName: r.RequesterName, Status: gen.ApprovalStatus(r.Status), CurrentStep: r.CurrentStep,
		StepCount: r.StepCount, StepName: r.StepName, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt}
}

func toGenApprovalPage(in approvals.Page) gen.ApprovalPage {
	out := gen.ApprovalPage{Items: make([]gen.Approval, len(in.Items)), Page: gen.PageInfo{Number: in.Page, Size: in.Size, Total: in.Total}}
	for i, r := range in.Items {
		out.Items[i] = toGenApproval(r)
	}
	return out
}

func toGenApprovalDetail(d approvals.Detail) gen.ApprovalDetail {
	g := toGenApproval(d.Request)
	out := gen.ApprovalDetail{Id: g.Id, TypeKey: g.TypeKey, TypeTitle: g.TypeTitle, SubjectId: g.SubjectId, Summary: g.Summary, Amount: g.Amount,
		RequesterId: g.RequesterId, RequesterName: g.RequesterName, Status: g.Status, CurrentStep: g.CurrentStep, StepCount: g.StepCount,
		StepName: g.StepName, CreatedAt: g.CreatedAt, DecidedAt: g.DecidedAt, CanDecide: d.CanDecide, CanCancel: d.CanCancel,
		Steps: make([]gen.ApprovalStep, len(d.Steps)), Events: make([]gen.ApprovalEvent, len(d.Events))}
	for i, s := range d.Steps {
		out.Steps[i] = gen.ApprovalStep{Step: s.No, Name: s.Name, Status: gen.ApprovalStepStatus(s.Status), DecidedBy: s.DeciderName, DecidedAt: s.DecidedAt}
	}
	for i, e := range d.Events {
		out.Events[i] = gen.ApprovalEvent{Step: e.StepNo, ActorId: mustUUID(e.ActorID), ActorName: e.ActorName, Action: gen.ApprovalEventAction(e.Action), Comment: e.Comment, CreatedAt: e.CreatedAt}
	}
	return out
}

// ListApprovalInbox implements GET /approvals.
func (h *Handler) ListApprovalInbox(ctx context.Context, req gen.ListApprovalInboxRequestObject) (gen.ListApprovalInboxResponseObject, error) {
	a, err := h.approvalCaller(ctx)
	if err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, nil, nil, crud.Sorts{"createdAt": "created_at"}, "-createdAt")
	if qerr != nil {
		return gen.ListApprovalInbox400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	page, err := h.approvals.Inbox(ctx, a, q.Page, q.Size)
	if err != nil {
		return nil, err
	}
	return gen.ListApprovalInbox200JSONResponse(toGenApprovalPage(page)), nil
}

// ListMyApprovals implements GET /approvals/mine.
func (h *Handler) ListMyApprovals(ctx context.Context, req gen.ListMyApprovalsRequestObject) (gen.ListMyApprovalsResponseObject, error) {
	a, err := h.approvalCaller(ctx)
	if err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, nil, nil, crud.Sorts{"createdAt": "created_at"}, "-createdAt")
	if qerr != nil {
		return gen.ListMyApprovals400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	page, err := h.approvals.Mine(ctx, a.UserID, status, q.Page, q.Size)
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.ListMyApprovals400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ListMyApprovals200JSONResponse(toGenApprovalPage(page)), nil
}

// GetApproval implements GET /approvals/{id}.
func (h *Handler) GetApproval(ctx context.Context, req gen.GetApprovalRequestObject) (gen.GetApprovalResponseObject, error) {
	a, err := h.approvalCaller(ctx)
	if err != nil {
		return nil, err
	}
	d, err := h.approvals.Get(ctx, a, req.Id.String())
	if f, ok := failApproval(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetApproval200JSONResponse(toGenApprovalDetail(d)), nil
}

func (h *Handler) decideApproval(ctx context.Context, id string, approve bool, body *gen.ApprovalDecision) (approvals.Detail, *approvalFail, error) {
	a, err := h.approvalCaller(ctx)
	if err != nil {
		return approvals.Detail{}, nil, err
	}
	in := approvals.DecideInput{Approve: approve}
	if body != nil {
		if body.Comment != nil {
			in.Comment = *body.Comment
		}
		if body.Step != nil {
			in.Step = *body.Step
		}
	}
	d, err := h.approvals.Decide(ctx, actorFrom(ctx), a, id, in)
	if f, ok := failApproval(err); ok {
		return d, &f, nil
	}
	return d, nil, err
}

// ApproveApproval implements POST /approvals/{id}/approve.
func (h *Handler) ApproveApproval(ctx context.Context, req gen.ApproveApprovalRequestObject) (gen.ApproveApprovalResponseObject, error) {
	d, f, err := h.decideApproval(ctx, req.Id.String(), true, req.Body)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return *f, nil
	}
	return gen.ApproveApproval200JSONResponse(toGenApprovalDetail(d)), nil
}

// RejectApproval implements POST /approvals/{id}/reject.
func (h *Handler) RejectApproval(ctx context.Context, req gen.RejectApprovalRequestObject) (gen.RejectApprovalResponseObject, error) {
	d, f, err := h.decideApproval(ctx, req.Id.String(), false, req.Body)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return *f, nil
	}
	return gen.RejectApproval200JSONResponse(toGenApprovalDetail(d)), nil
}

// CancelApproval implements POST /approvals/{id}/cancel.
func (h *Handler) CancelApproval(ctx context.Context, req gen.CancelApprovalRequestObject) (gen.CancelApprovalResponseObject, error) {
	a, err := h.approvalCaller(ctx)
	if err != nil {
		return nil, err
	}
	comment := ""
	if req.Body != nil && req.Body.Comment != nil {
		comment = *req.Body.Comment
	}
	d, err := h.approvals.Cancel(ctx, actorFrom(ctx), a, req.Id.String(), comment)
	if f, ok := failApproval(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CancelApproval200JSONResponse(toGenApprovalDetail(d)), nil
}
