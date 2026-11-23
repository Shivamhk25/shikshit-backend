package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/colleagues"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// ColleagueService is the subset of colleagues.Service used by the handler.
type ColleagueService interface {
	List(ctx context.Context, callerID string, q colleagues.Query) (crud.Page[colleagues.Colleague], error)
}

// WithColleagues enables GET /colleagues. Without it the operation answers 501.
func WithColleagues(s ColleagueService) Option { return func(h *Handler) { h.colleagues = s } }

// ListColleagues implements GET /colleagues.
//
// Who may call: any signed-in, active staff account of the institute, with no permission, so a Principal or
// Registrar without iam.users.view can still pick a person. A parent or student account (users.person_type) is
// refused, and so is a read-only support visit: its principal is a platform user, not a person of the school.
// The rule is the same one the delegation and enquiry-assignee checks use (platform/colleagues).
func (h *Handler) ListColleagues(ctx context.Context, req gen.ListColleaguesRequestObject) (gen.ListColleaguesResponseObject, error) {
	if h.colleagues == nil {
		return nil, errNotConfigured
	}
	p, ok := PrincipalFrom(ctx)
	if !ok {
		return nil, errAuthNotConfigured
	}
	forbidden := gen.ListColleagues403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(
		problemBody(http.StatusForbidden, "permission_denied", "Only school staff can look up colleagues."))}
	if p.Support != nil {
		return forbidden, nil
	}
	var page, size int
	if req.Params.Page != nil {
		if page = *req.Params.Page; page == 0 {
			page = -1 // an explicit 0 is invalid, not "not given"
		}
	}
	if req.Params.PageSize != nil {
		if size = *req.Params.PageSize; size == 0 {
			size = -1
		}
	}
	search := ""
	if req.Params.Q != nil {
		search = *req.Params.Q
	}
	q, err := colleagues.ParseQuery(page, size, search)
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.ListColleagues400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := h.colleagues.List(ctx, p.UserID, q)
	if errors.Is(err, colleagues.ErrNotStaff) {
		return forbidden, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.ColleaguePage{Items: make([]gen.Colleague, len(res.Items)), Page: gen.PageInfo{Number: res.Page, Size: res.Size, Total: res.Total}}
	for i, c := range res.Items {
		out.Items[i] = gen.Colleague{Id: mustUUID(c.ID), DisplayName: c.DisplayName, Label: c.Label}
	}
	return gen.ListColleagues200JSONResponse(out), nil
}
