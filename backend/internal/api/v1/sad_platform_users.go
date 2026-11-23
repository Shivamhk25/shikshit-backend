package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/sad/platformusers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// PlatformUserService is what SAD-04 needs (platformusers.Service satisfies it).
type PlatformUserService interface {
	List(ctx context.Context) ([]platformusers.User, error)
	Create(ctx context.Context, a audit.Actor, email, name string) (platformusers.WithLink, error)
	Disable(ctx context.Context, a audit.Actor, id string) (platformusers.User, error)
	Enable(ctx context.Context, a audit.Actor, id string) (platformusers.User, error)
	ResetMFA(ctx context.Context, a audit.Actor, id string) (platformusers.User, error)
	ResetPassword(ctx context.Context, a audit.Actor, id string) (platformusers.WithLink, error)
	AcceptInvite(ctx context.Context, token, password string) error
}

// WithPlatformUsers enables the SAD-04 operations.
func WithPlatformUsers(s PlatformUserService) Option { return func(h *Handler) { h.platformUsers = s } }

func init() {
	platformOperations["ListPlatformUsers"] = true
	platformOperations["CreatePlatformUser"] = true
	platformOperations["ChangePlatformUser"] = true
	publicOperations["PlatformAcceptInvite"] = true
}

func toGenPlatformAccount(u platformusers.User) gen.PlatformAccount {
	return gen.PlatformAccount{Id: mustUUID(u.ID), Email: u.Email, Name: u.Name, Status: gen.PlatformAccountStatus(u.Status), MfaEnabled: u.MFAEnabled, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt}
}

func toGenPlatformAccountWithLink(u platformusers.User, link string, emailQueued bool) gen.PlatformAccountWithLink {
	out := gen.PlatformAccountWithLink{Id: mustUUID(u.ID), Email: u.Email, Name: u.Name, Status: gen.PlatformAccountWithLinkStatus(u.Status), MfaEnabled: u.MFAEnabled, LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt}
	if link != "" {
		out.Link, out.EmailQueued = &link, &emailQueued
	}
	return out
}

// ListPlatformUsers implements GET /platform/users.
func (h *Handler) ListPlatformUsers(ctx context.Context, _ gen.ListPlatformUsersRequestObject) (gen.ListPlatformUsersResponseObject, error) {
	if h.platformUsers == nil {
		return nil, errNotConfigured
	}
	list, err := h.platformUsers.List(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListPlatformUsers200JSONResponse{Items: make([]gen.PlatformAccount, len(list))}
	for i, u := range list {
		out.Items[i] = toGenPlatformAccount(u)
	}
	return out, nil
}

// CreatePlatformUser implements POST /platform/users.
func (h *Handler) CreatePlatformUser(ctx context.Context, req gen.CreatePlatformUserRequestObject) (gen.CreatePlatformUserResponseObject, error) {
	if h.platformUsers == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.CreatePlatformUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "A request body is required.")}, nil
	}
	u, err := h.platformUsers.Create(ctx, platformActor(ctx), req.Body.Email, req.Body.Name)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.CreatePlatformUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequestFields(fe)}, nil
	case err != nil:
		return nil, err
	}
	return gen.CreatePlatformUser201JSONResponse(toGenPlatformAccountWithLink(u.User, u.Link, u.EmailQueued)), nil
}

// ChangePlatformUser implements POST /platform/users/{userId}/{action}.
func (h *Handler) ChangePlatformUser(ctx context.Context, req gen.ChangePlatformUserRequestObject) (gen.ChangePlatformUserResponseObject, error) {
	if h.platformUsers == nil {
		return nil, errNotConfigured
	}
	a, id := platformActor(ctx), req.UserId.String()
	var (
		u      platformusers.User
		link   string
		queued bool
		err    error
	)
	switch req.Action {
	case "disable":
		u, err = h.platformUsers.Disable(ctx, a, id)
	case "enable":
		u, err = h.platformUsers.Enable(ctx, a, id)
	case "reset-mfa":
		u, err = h.platformUsers.ResetMFA(ctx, a, id)
	case "reset-password":
		var w platformusers.WithLink
		w, err = h.platformUsers.ResetPassword(ctx, a, id)
		u, link, queued = w.User, w.Link, w.EmailQueued
	default:
		return gen.ChangePlatformUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	}
	conflict := func(code string) gen.ChangePlatformUserResponseObject {
		return gen.ChangePlatformUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, code, err.Error()))}
	}
	switch {
	case errors.Is(err, platformusers.ErrNotFound):
		return gen.ChangePlatformUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: instituteNotFound()}, nil
	case errors.Is(err, platformusers.ErrSelf):
		return conflict("self_action"), nil
	case errors.Is(err, platformusers.ErrLastActive):
		return conflict("last_active"), nil
	case errors.Is(err, platformusers.ErrInvalidState):
		return conflict("invalid_state"), nil
	case err != nil:
		return nil, err
	}
	return gen.ChangePlatformUser200JSONResponse(toGenPlatformAccountWithLink(u, link, queued)), nil
}

// PlatformAcceptInvite implements POST /platform/auth/invite/accept.
func (h *Handler) PlatformAcceptInvite(ctx context.Context, req gen.PlatformAcceptInviteRequestObject) (gen.PlatformAcceptInviteResponseObject, error) {
	if h.platformUsers == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.PlatformAcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "token and password are required")}, nil
	}
	err := h.platformUsers.AcceptInvite(ctx, req.Body.Token, req.Body.Password)
	switch {
	case errors.Is(err, platformusers.ErrInvalidToken):
		return gen.PlatformAcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("invalid_token", err.Error())}, nil
	case errors.Is(err, identity.ErrWeakPassword):
		return gen.PlatformAcceptInvite400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("weak_password", err.Error())}, nil
	case err != nil:
		return nil, err
	}
	return gen.PlatformAcceptInvite204Response{}, nil
}
