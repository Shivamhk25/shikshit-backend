package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/parentlinks"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// ParentLinkService is the subset of parentlinks.Service used by the handler.
type ParentLinkService interface {
	List(ctx context.Context, a audit.Actor, q crud.ListQuery) (crud.Page[parentlinks.Account], error)
	Link(ctx context.Context, a audit.Actor, userID, guardianID string) (parentlinks.Account, error)
	Unlink(ctx context.Context, a audit.Actor, userID string) error
	SetAccess(ctx context.Context, a audit.Actor, linkID string, noAccess bool) (parentlinks.Child, error)
	MyChildren(ctx context.Context, a audit.Actor, userID string) ([]parentlinks.MyChild, error)
}

// WithParentLinks enables the parent-link operations and GET /me/children. Without them they answer 501.
func WithParentLinks(s ParentLinkService) Option { return func(h *Handler) { h.parentLinks = s } }

func toGenParentChild(c parentlinks.Child) gen.ParentChild {
	return gen.ParentChild{LinkId: mustUUID(c.LinkID), StudentId: mustUUID(c.StudentID), StudentName: c.StudentName,
		RegistrationCode: c.RegistrationCode, Relation: gen.ParentChildRelation(c.Relation), NoAccess: c.NoAccess}
}

func toGenParentAccount(a parentlinks.Account) gen.ParentAccount {
	out := gen.ParentAccount{UserId: mustUUID(a.UserID), Username: a.Username, DisplayName: a.DisplayName,
		Status: gen.ParentAccountStatus(a.Status), GuardianId: mustUUID(a.GuardianID), GuardianName: a.GuardianName,
		GuardianPhone: a.GuardianPhone, Children: make([]gen.ParentChild, len(a.Children))}
	for i, c := range a.Children {
		out.Children[i] = toGenParentChild(c)
	}
	return out
}

func parentLinkNotFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

// ListParentLinks implements GET /parent-links.
func (h *Handler) ListParentLinks(ctx context.Context, req gen.ListParentLinksRequestObject) (gen.ListParentLinksResponseObject, error) {
	if h.parentLinks == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, parentlinks.SortKeys, parentlinks.DefaultSort)
	if qerr != nil {
		return gen.ListParentLinks400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	p, err := h.parentLinks.List(ctx, actorFrom(ctx), q)
	if err != nil {
		return nil, err
	}
	out := gen.ParentAccountPage{Items: make([]gen.ParentAccount, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenParentAccount(it)
	}
	return gen.ListParentLinks200JSONResponse(out), nil
}

// LinkParentAccount implements POST /parent-links.
func (h *Handler) LinkParentAccount(ctx context.Context, req gen.LinkParentAccountRequestObject) (gen.LinkParentAccountResponseObject, error) {
	if h.parentLinks == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.LinkParentAccount400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	acc, err := h.parentLinks.Link(ctx, actorFrom(ctx), req.Body.UserId.String(), req.Body.GuardianId.String())
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.LinkParentAccount400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, parentlinks.ErrStudentAccount):
		return gen.LinkParentAccount409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "student_account", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.LinkParentAccount200JSONResponse(toGenParentAccount(acc)), nil
}

// UnlinkParentAccount implements DELETE /parent-links/{userId}.
func (h *Handler) UnlinkParentAccount(ctx context.Context, req gen.UnlinkParentAccountRequestObject) (gen.UnlinkParentAccountResponseObject, error) {
	if h.parentLinks == nil {
		return nil, errNotConfigured
	}
	err := h.parentLinks.Unlink(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, parentlinks.ErrNotFound):
		return gen.UnlinkParentAccount404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: parentLinkNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.UnlinkParentAccount204Response{}, nil
}

// SetParentChildAccess implements PUT /parent-links/children/{linkId}/access.
func (h *Handler) SetParentChildAccess(ctx context.Context, req gen.SetParentChildAccessRequestObject) (gen.SetParentChildAccessResponseObject, error) {
	if h.parentLinks == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.SetParentChildAccess400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	c, err := h.parentLinks.SetAccess(ctx, actorFrom(ctx), req.LinkId.String(), req.Body.NoAccess)
	switch {
	case errors.Is(err, parentlinks.ErrNotFound):
		return gen.SetParentChildAccess404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: parentLinkNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetParentChildAccess200JSONResponse(toGenParentChild(c)), nil
}

// ListMyChildren implements GET /me/children.
func (h *Handler) ListMyChildren(ctx context.Context, _ gen.ListMyChildrenRequestObject) (gen.ListMyChildrenResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.parentLinks == nil {
		return nil, errNotConfigured
	}
	list, err := h.parentLinks.MyChildren(ctx, actorFrom(ctx), p.UserID)
	if err != nil {
		return nil, err
	}
	out := gen.ListMyChildren200JSONResponse{Items: make([]gen.MyChild, len(list))}
	for i, c := range list {
		out.Items[i] = gen.MyChild{StudentId: mustUUID(c.StudentID), Name: c.Name, RegistrationCode: c.RegistrationCode, Relation: gen.MyChildRelation(c.Relation)}
	}
	return out, nil
}
