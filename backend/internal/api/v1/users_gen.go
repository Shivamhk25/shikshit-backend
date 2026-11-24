package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// ptr returns a pointer to v, for turning optional values into pointers.
func ptr[T any](v T) *T { return &v }

// toGenUser converts a service user to the generated API type.
func toGenUser(u users.User) gen.User {
	out := gen.User{
		AccessVersion:      int(u.AccessVersion),
		CreatedAt:          u.CreatedAt,
		DisplayName:        u.DisplayName,
		Email:              u.Email,
		FailedLoginCount:   u.FailedLoginCount,
		Id:                 mustUUID(u.ID),
		LastLoginAt:        u.LastLoginAt,
		LockedUntil:        u.LockedUntil,
		MfaEnabled:         u.MFAEnabled,
		MustChangePassword: u.MustChangePassword,
		Phone:              u.Phone,
		Status:             gen.UserStatus(u.Status),
		UpdatedAt:          u.UpdatedAt,
		Username:           u.Username,
	}
	if u.PersonType != nil {
		t := gen.UserPersonType(*u.PersonType)
		out.PersonType = &t
	}
	if u.PersonID != nil {
		id := mustUUID(*u.PersonID)
		out.PersonId = &id
	}
	return out
}

// toGenUserWithInviteLink converts a service user with invite link to the generated API type.
func toGenUserWithInviteLink(u users.UserWithInviteLink) gen.UserWithInviteLink {
	out := gen.UserWithInviteLink{
		AccessVersion:      int(u.User.AccessVersion),
		CreatedAt:          u.User.CreatedAt,
		DisplayName:        u.User.DisplayName,
		Email:              u.User.Email,
		FailedLoginCount:   u.User.FailedLoginCount,
		Id:                 mustUUID(u.User.ID),
		InviteLink:         u.InviteLink,
		EmailQueued:        u.EmailQueued,
		LastLoginAt:        u.User.LastLoginAt,
		LockedUntil:        u.User.LockedUntil,
		MfaEnabled:         u.User.MFAEnabled,
		MustChangePassword: u.User.MustChangePassword,
		Phone:              u.User.Phone,
		Status:             gen.UserWithInviteLinkStatus(u.User.Status),
		UpdatedAt:          u.User.UpdatedAt,
		Username:           u.User.Username,
	}
	if u.User.PersonType != nil {
		t := gen.UserWithInviteLinkPersonType(*u.User.PersonType)
		out.PersonType = &t
	}
	if u.User.PersonID != nil {
		id := mustUUID(*u.User.PersonID)
		out.PersonId = &id
	}
	return out
}

// toGenUserWithResetLink converts a service user with reset link to the generated API type.
func toGenUserWithResetLink(u users.UserWithResetLink) gen.UserWithResetLink {
	out := gen.UserWithResetLink{
		AccessVersion:      int(u.User.AccessVersion),
		CreatedAt:          u.User.CreatedAt,
		DisplayName:        u.User.DisplayName,
		Email:              u.User.Email,
		FailedLoginCount:   u.User.FailedLoginCount,
		Id:                 mustUUID(u.User.ID),
		LastLoginAt:        u.User.LastLoginAt,
		LockedUntil:        u.User.LockedUntil,
		MfaEnabled:         u.User.MFAEnabled,
		MustChangePassword: u.User.MustChangePassword,
		Phone:              u.User.Phone,
		ResetLink:          u.ResetLink,
		EmailQueued:        u.EmailQueued,
		Status:             gen.UserWithResetLinkStatus(u.User.Status),
		UpdatedAt:          u.User.UpdatedAt,
		Username:           u.User.Username,
	}
	if u.User.PersonType != nil {
		t := gen.UserWithResetLinkPersonType(*u.User.PersonType)
		out.PersonType = &t
	}
	if u.User.PersonID != nil {
		id := mustUUID(*u.User.PersonID)
		out.PersonId = &id
	}
	return out
}

// userCreateInput converts the generated create body to the service input.
func userCreateInput(b gen.UserCreateInput) users.CreateInput {
	in := users.CreateInput{
		Username:    b.Username,
		DisplayName: b.DisplayName,
		Email:       b.Email,
		Phone:       b.Phone,
	}
	if b.PersonType != nil {
		s := string(*b.PersonType)
		in.PersonType = &s
	}
	if b.PersonId != nil {
		s := b.PersonId.String()
		in.PersonID = &s
	}
	return in
}

// userUpdateInput converts the generated update body to the service input.
func userUpdateInput(b gen.UserUpdateInput) users.UpdateInput {
	in := users.UpdateInput{
		DisplayName: b.DisplayName,
		Email:       b.Email,
		Phone:       b.Phone,
	}
	if b.PersonType != nil {
		s := string(*b.PersonType)
		in.PersonType = &s
	}
	if b.PersonId != nil {
		s := b.PersonId.String()
		in.PersonID = &s
	}
	return in
}

// hasDuplicate reports whether any field error is a uniqueness conflict.
func hasDuplicate(fe crud.FieldErrors) bool {
	for _, e := range fe {
		if e.Code == "duplicate" {
			return true
		}
	}
	return false
}

// isDuplicate reports whether any field error is a uniqueness conflict.
func isDuplicate(fe crud.FieldErrors) bool {
	for _, f := range fe {
		if f.Code == "duplicate" {
			return true
		}
	}
	return false
}

// validStatus reports whether s is one of the allowed user statuses.
func validStatus(s string) bool {
	for _, st := range users.Statuses {
		if s == st {
			return true
		}
	}
	return false
}

// ListUsers implements GET /users.
func (h *Handler) ListUsers(ctx context.Context, req gen.ListUsersRequestObject) (gen.ListUsersResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, users.SortKeys, users.DefaultSort)
	if qerr != nil {
		return gen.ListUsers400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	f := users.Filter{}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		if !validStatus(f.Status) {
			var fe crud.FieldErrors
			fe.Add("status", "invalid_choice", "Status must be one of invited, active, locked, disabled.")
			return gen.ListUsers400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
	}
	p, err := h.users.ListAudited(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.UserPage{Items: make([]gen.User, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenUser(it)
	}
	return gen.ListUsers200JSONResponse(out), nil
}

// CreateUser implements POST /users.
func (h *Handler) CreateUser(ctx context.Context, req gen.CreateUserRequestObject) (gen.CreateUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.CreateUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	u, err := h.users.Create(ctx, actorFrom(ctx), userCreateInput(*req.Body))
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		if hasDuplicate(fe) {
			return gen.CreateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
		return gen.CreateUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateUser201JSONResponse(toGenUserWithInviteLink(u)), nil
}

// GetUser implements GET /users/{userId}.
func (h *Handler) GetUser(ctx context.Context, req gen.GetUserRequestObject) (gen.GetUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.View(ctx, actorFrom(ctx), req.UserId.String())
	if errors.Is(err, users.ErrNotFound) {
		return gen.GetUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetUser200JSONResponse(toGenUser(u)), nil
}

// UpdateUser implements PATCH /users/{userId}.
func (h *Handler) UpdateUser(ctx context.Context, req gen.UpdateUserRequestObject) (gen.UpdateUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.UpdateUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	u, err := h.users.Update(ctx, actorFrom(ctx), req.UserId.String(), userUpdateInput(*req.Body))
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		if hasDuplicate(fe) {
			return gen.UpdateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
		return gen.UpdateUser400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, users.ErrNotFound):
		return gen.UpdateUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case err != nil:
		return nil, err
	}
	return gen.UpdateUser200JSONResponse(toGenUser(u)), nil
}

// DeactivateUser implements POST /users/{userId}/deactivate.
func (h *Handler) DeactivateUser(ctx context.Context, req gen.DeactivateUserRequestObject) (gen.DeactivateUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.Deactivate(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.DeactivateUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrSelfAction):
		return gen.DeactivateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "self_action", err.Error()))}, nil
	case errors.Is(err, users.ErrLastAdmin):
		return gen.DeactivateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "last_admin", err.Error()))}, nil
	case errors.Is(err, users.ErrInvalidState):
		return gen.DeactivateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.DeactivateUser200JSONResponse(toGenUser(u)), nil
}

// ReactivateUser implements POST /users/{userId}/reactivate.
func (h *Handler) ReactivateUser(ctx context.Context, req gen.ReactivateUserRequestObject) (gen.ReactivateUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.Reactivate(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.ReactivateUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrInvalidState):
		return gen.ReactivateUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.ReactivateUser200JSONResponse(toGenUser(u)), nil
}

// LockUser implements POST /users/{userId}/lock.
func (h *Handler) LockUser(ctx context.Context, req gen.LockUserRequestObject) (gen.LockUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.Lock(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.LockUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrSelfAction):
		return gen.LockUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "self_action", err.Error()))}, nil
	case errors.Is(err, users.ErrLastAdmin):
		return gen.LockUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "last_admin", err.Error()))}, nil
	case errors.Is(err, users.ErrInvalidState):
		return gen.LockUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.LockUser200JSONResponse(toGenUser(u)), nil
}

// UnlockUser implements POST /users/{userId}/unlock.
func (h *Handler) UnlockUser(ctx context.Context, req gen.UnlockUserRequestObject) (gen.UnlockUserResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.Unlock(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.UnlockUser404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrInvalidState):
		return gen.UnlockUser409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.UnlockUser200JSONResponse(toGenUser(u)), nil
}

// ResetUserPassword implements POST /users/{userId}/reset-password.
func (h *Handler) ResetUserPassword(ctx context.Context, req gen.ResetUserPasswordRequestObject) (gen.ResetUserPasswordResponseObject, error) {
	if h.users == nil {
		return nil, errNotConfigured
	}
	u, err := h.users.ResetPassword(ctx, actorFrom(ctx), req.UserId.String())
	switch {
	case errors.Is(err, users.ErrNotFound):
		return gen.ResetUserPassword404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, users.ErrInvalidState):
		return gen.ResetUserPassword409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "invalid_state", err.Error()))}, nil
	case err != nil:
		return nil, err
	}
	return gen.ResetUserPassword200JSONResponse(toGenUserWithResetLink(u)), nil
}
