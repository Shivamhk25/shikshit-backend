package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/roles"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// RoleService is the subset of roles.Service used by the handler. Writes take the caller's effective access so the
// service can refuse grants the caller does not hold.
type RoleService interface {
	Catalog(ctx context.Context) ([]roles.Permission, error)
	List(ctx context.Context) ([]roles.Role, error)
	Get(ctx context.Context, id string) (roles.Detail, error)
	Create(ctx context.Context, a audit.Actor, caller authz.Effective, in roles.Input) (roles.Detail, error)
	Clone(ctx context.Context, a audit.Actor, caller authz.Effective, id, name string) (roles.Detail, error)
	Update(ctx context.Context, a audit.Actor, caller authz.Effective, id string, in roles.Input) (roles.Detail, error)
	Delete(ctx context.Context, a audit.Actor, id string) error
	UserRoles(ctx context.Context, userID string) ([]roles.Role, error)
	SetUserRoles(ctx context.Context, a audit.Actor, caller authz.Effective, userID string, roleIDs []string) ([]roles.Role, error)
}

// WithRoles enables the role operations. Without them they answer 501.
func WithRoles(r RoleService) Option { return func(h *Handler) { h.roles = r } }

// callerAccess is the effective access the access middleware resolved for this request. Every role operation
// declares a permission, so it is always there; its absence is a wiring error, never an empty grant set.
func callerAccess(ctx context.Context) (authz.Effective, error) {
	a, ok := AccessFrom(ctx)
	if !ok {
		return authz.Effective{}, errAuthNotConfigured
	}
	return a.Effective, nil
}

func toGenRole(r roles.Role) gen.Role {
	return gen.Role{Id: mustUUID(r.ID), Code: r.Code, Name: r.Name, Description: r.Description, IsSystem: r.IsSystem,
		HolderCount: r.HolderCount, PermissionCount: r.PermissionCount, UpdatedAt: r.UpdatedAt}
}

func toGenRoles(rs []roles.Role) []gen.Role {
	out := make([]gen.Role, len(rs))
	for i, r := range rs {
		out[i] = toGenRole(r)
	}
	return out
}

func toGenRoleDetail(d roles.Detail) gen.RoleDetail {
	r := toGenRole(d.Role)
	out := gen.RoleDetail{Id: r.Id, Code: r.Code, Name: r.Name, Description: r.Description, IsSystem: r.IsSystem,
		HolderCount: r.HolderCount, PermissionCount: r.PermissionCount, UpdatedAt: r.UpdatedAt, Grants: make([]gen.RoleGrant, len(d.Grants))}
	for i, g := range d.Grants {
		out.Grants[i] = gen.RoleGrant{Permission: g.Permission, Scope: gen.RoleGrantScope(g.Scope)}
	}
	return out
}

func roleInput(b gen.RoleInput) roles.Input {
	in := roles.Input{Name: b.Name, Description: b.Description, Grants: make([]roles.Grant, len(b.Grants))}
	for i, g := range b.Grants {
		in.Grants[i] = roles.Grant{Permission: g.Permission, Scope: string(g.Scope)}
	}
	return in
}

func cannotGrant(err error) (gen.Problem, bool) {
	var cg roles.CannotGrantError
	if !errors.As(err, &cg) {
		return gen.Problem{}, false
	}
	return problemBody(http.StatusForbidden, "cannot_grant", cg.Error()), true
}

func roleNotFound() gen.Problem { return problemBody(http.StatusNotFound, "not_found", "") }

// ListPermissionCatalog implements GET /permissions/catalog.
func (h *Handler) ListPermissionCatalog(ctx context.Context, _ gen.ListPermissionCatalogRequestObject) (gen.ListPermissionCatalogResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	cat, err := h.roles.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := gen.ListPermissionCatalog200JSONResponse{Items: make([]gen.CatalogPermission, len(cat))}
	for i, p := range cat {
		out.Items[i] = gen.CatalogPermission{Code: p.Code, Module: p.Module, Resource: p.Resource, Action: p.Action, Sensitive: p.Sensitive}
	}
	return out, nil
}

// ListRoles implements GET /roles.
func (h *Handler) ListRoles(ctx context.Context, _ gen.ListRolesRequestObject) (gen.ListRolesResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	rs, err := h.roles.List(ctx)
	if err != nil {
		return nil, err
	}
	return gen.ListRoles200JSONResponse{Items: toGenRoles(rs)}, nil
}

// GetRole implements GET /roles/{roleId}.
func (h *Handler) GetRole(ctx context.Context, req gen.GetRoleRequestObject) (gen.GetRoleResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	d, err := h.roles.Get(ctx, req.RoleId.String())
	if errors.Is(err, roles.ErrNotFound) {
		return gen.GetRole404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetRole200JSONResponse(toGenRoleDetail(d)), nil
}

// CreateRole implements POST /roles.
func (h *Handler) CreateRole(ctx context.Context, req gen.CreateRoleRequestObject) (gen.CreateRoleResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	caller, err := callerAccess(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.CreateRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	d, err := h.roles.Create(ctx, actorFrom(ctx), caller, roleInput(*req.Body))
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.CreateRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if p, ok := cannotGrant(err); ok {
		return gen.CreateRole403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateRole201JSONResponse(toGenRoleDetail(d)), nil
}

// CloneRole implements POST /roles/{roleId}/clone.
func (h *Handler) CloneRole(ctx context.Context, req gen.CloneRoleRequestObject) (gen.CloneRoleResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	caller, err := callerAccess(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.CloneRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	d, err := h.roles.Clone(ctx, actorFrom(ctx), caller, req.RoleId.String(), req.Body.Name)
	var fe crud.FieldErrors
	switch {
	case errors.Is(err, roles.ErrNotFound):
		return gen.CloneRole404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	case errors.As(err, &fe):
		return gen.CloneRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if p, ok := cannotGrant(err); ok {
		return gen.CloneRole403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CloneRole201JSONResponse(toGenRoleDetail(d)), nil
}

// UpdateRole implements PATCH /roles/{roleId}.
func (h *Handler) UpdateRole(ctx context.Context, req gen.UpdateRoleRequestObject) (gen.UpdateRoleResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	caller, err := callerAccess(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.UpdateRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	d, err := h.roles.Update(ctx, actorFrom(ctx), caller, req.RoleId.String(), roleInput(*req.Body))
	var fe crud.FieldErrors
	switch {
	case errors.Is(err, roles.ErrNotFound):
		return gen.UpdateRole404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	case errors.Is(err, roles.ErrSystemRole):
		return gen.UpdateRole409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "system_role", err.Error()))}, nil
	case errors.As(err, &fe):
		return gen.UpdateRole400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if p, ok := cannotGrant(err); ok {
		return gen.UpdateRole403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateRole200JSONResponse(toGenRoleDetail(d)), nil
}

// DeleteRole implements DELETE /roles/{roleId}.
func (h *Handler) DeleteRole(ctx context.Context, req gen.DeleteRoleRequestObject) (gen.DeleteRoleResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	err := h.roles.Delete(ctx, actorFrom(ctx), req.RoleId.String())
	switch {
	case errors.Is(err, roles.ErrNotFound):
		return gen.DeleteRole404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	case errors.Is(err, roles.ErrSystemRole):
		return gen.DeleteRole409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "system_role", err.Error()))}, nil
	case errors.Is(err, roles.ErrInUse):
		return gen.DeleteRole409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "role_in_use", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.DeleteRole204Response{}, nil
}

// ListUserRoles implements GET /users/{userId}/roles.
func (h *Handler) ListUserRoles(ctx context.Context, req gen.ListUserRolesRequestObject) (gen.ListUserRolesResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	rs, err := h.roles.UserRoles(ctx, req.UserId.String())
	if errors.Is(err, roles.ErrNotFound) {
		return gen.ListUserRoles404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ListUserRoles200JSONResponse{Items: toGenRoles(rs)}, nil
}

// SetUserRoles implements PUT /users/{userId}/roles.
func (h *Handler) SetUserRoles(ctx context.Context, req gen.SetUserRolesRequestObject) (gen.SetUserRolesResponseObject, error) {
	if h.roles == nil {
		return nil, errNotConfigured
	}
	caller, err := callerAccess(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return gen.SetUserRoles400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	ids := make([]string, len(req.Body.RoleIds))
	for i, id := range req.Body.RoleIds {
		ids[i] = id.String()
	}
	rs, err := h.roles.SetUserRoles(ctx, actorFrom(ctx), caller, req.UserId.String(), ids)
	var fe crud.FieldErrors
	switch {
	case errors.Is(err, roles.ErrNotFound):
		return gen.SetUserRoles404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(roleNotFound())}, nil
	case errors.Is(err, users.ErrLastAdmin):
		return gen.SetUserRoles409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "last_admin", err.Error()))}, nil
	case errors.As(err, &fe):
		return gen.SetUserRoles400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if p, ok := cannotGrant(err); ok {
		return gen.SetUserRoles403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.SetUserRoles200JSONResponse{Items: toGenRoles(rs)}, nil
}
