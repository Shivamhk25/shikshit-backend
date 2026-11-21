package apiv1

import (
	"context"
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/rooms"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// RoomsService is what the rooms operations need (rooms.Service satisfies it).
type RoomsService interface {
	List(ctx context.Context, q crud.ListQuery, f rooms.Filter) (crud.Page[rooms.Room], error)
	Get(ctx context.Context, id string) (rooms.Room, error)
	Create(ctx context.Context, actor audit.Actor, in rooms.Input) (rooms.Room, error)
	Update(ctx context.Context, actor audit.Actor, id string, in rooms.Input) (rooms.Room, error)
	Delete(ctx context.Context, actor audit.Actor, id string) error
}

// WithRooms enables the /rooms operations.
func WithRooms(s RoomsService) Option { return func(h *Handler) { h.rooms = s } }

// actorFrom describes who is making this request for the audit trail.
func actorFrom(ctx context.Context) audit.Actor {
	a := audit.Actor{Source: "api"}
	if p, ok := PrincipalFrom(ctx); ok {
		a.UserID = p.UserID
	}
	if m := metaFrom(ctx); m != nil {
		a.IP, a.UserAgent, a.RequestID = m.ip, m.userAgent, m.requestID
		switch m.client {
		case "web", "ios", "android":
			a.Source = m.client
		}
	}
	return a
}

// validationProblem renders field errors as a 400 problem document the form can attach to its fields.
func validationProblem(err crud.FieldErrors) gen.Problem {
	p := problemBody(http.StatusBadRequest, "validation_failed", "Some fields need your attention.")
	list := make([]gen.ProblemFieldError, len(err))
	for i, f := range err {
		list[i] = gen.ProblemFieldError{Field: f.Field, Code: f.Code, Message: f.Message}
	}
	p.Errors = &list
	return p
}

func toGenRoom(r rooms.Room) gen.Room {
	out := gen.Room{
		AvailableForTimetable: r.AvailableForTimetable, Building: r.Building, Capacity: r.Capacity, Code: r.Code,
		CreatedAt: r.CreatedAt, Description: r.Description, DisplayOrder: r.DisplayOrder, Floor: r.Floor,
		Name: r.Name, Status: gen.RoomStatus(r.Status), UpdatedAt: r.UpdatedAt,
	}
	out.Id = mustUUID(r.ID)
	if r.Type != nil {
		t := gen.RoomType(*r.Type)
		out.Type = &t
	}
	return out
}

func roomInput(b gen.RoomInput) rooms.Input {
	in := rooms.Input{
		Code: b.Code, Name: b.Name, Capacity: b.Capacity, Building: b.Building, Floor: b.Floor,
		AvailableForTimetable: b.AvailableForTimetable, Description: b.Description,
	}
	if b.DisplayOrder != nil {
		in.DisplayOrder = *b.DisplayOrder
	}
	if b.Status != nil {
		in.Status = string(*b.Status)
	}
	if b.Type != nil {
		t := string(*b.Type)
		in.Type = &t
	}
	return in
}

func mustUUID(s string) openapi_types.UUID {
	var u openapi_types.UUID
	_ = u.UnmarshalText([]byte(s))
	return u
}

// ListRooms implements GET /rooms.
func (h *Handler) ListRooms(ctx context.Context, req gen.ListRoomsRequestObject) (gen.ListRoomsResponseObject, error) {
	if h.rooms == nil {
		return nil, errNotConfigured
	}
	var page, size int
	// ParseList treats 0 as "not given"; a client that sends 0 explicitly is wrong, so make that fail validation.
	if req.Params.Page != nil {
		if page = int(*req.Params.Page); page == 0 {
			page = -1
		}
	}
	if req.Params.PageSize != nil {
		if size = int(*req.Params.PageSize); size == 0 {
			size = -1
		}
	}
	sort, search := "", ""
	if req.Params.Sort != nil {
		sort = *req.Params.Sort
	}
	if req.Params.Q != nil {
		search = string(*req.Params.Q)
	}
	q, err := crud.ParseList(page, size, sort, search, rooms.SortKeys, rooms.DefaultSort)
	if err != nil {
		return gen.ListRooms400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(err.(crud.FieldErrors)))}, nil
	}
	f := rooms.Filter{AvailableForTimetable: req.Params.AvailableForTimetable}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		if f.Status != "active" && f.Status != "inactive" {
			var fe crud.FieldErrors
			fe.Add("status", "invalid_choice", "Status must be active or inactive.")
			return gen.ListRooms400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
	}
	p, err := h.rooms.List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := gen.RoomPage{Items: make([]gen.Room, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, r := range p.Items {
		out.Items[i] = toGenRoom(r)
	}
	return gen.ListRooms200JSONResponse(out), nil
}

// GetRoom implements GET /rooms/{roomId}.
func (h *Handler) GetRoom(ctx context.Context, req gen.GetRoomRequestObject) (gen.GetRoomResponseObject, error) {
	if h.rooms == nil {
		return nil, errNotConfigured
	}
	r, err := h.rooms.Get(ctx, req.RoomId.String())
	if errors.Is(err, rooms.ErrNotFound) {
		return gen.GetRoom404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetRoom200JSONResponse(toGenRoom(r)), nil
}

// CreateRoom implements POST /rooms.
func (h *Handler) CreateRoom(ctx context.Context, req gen.CreateRoomRequestObject) (gen.CreateRoomResponseObject, error) {
	if h.rooms == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.CreateRoom400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	r, err := h.rooms.Create(ctx, actorFrom(ctx), roomInput(*req.Body))
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.CreateRoom400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateRoom201JSONResponse(toGenRoom(r)), nil
}

// UpdateRoom implements PUT /rooms/{roomId}.
func (h *Handler) UpdateRoom(ctx context.Context, req gen.UpdateRoomRequestObject) (gen.UpdateRoomResponseObject, error) {
	if h.rooms == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.UpdateRoom400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	r, err := h.rooms.Update(ctx, actorFrom(ctx), req.RoomId.String(), roomInput(*req.Body))
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.UpdateRoom400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, rooms.ErrNotFound):
		return gen.UpdateRoom404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case err != nil:
		return nil, err
	}
	return gen.UpdateRoom200JSONResponse(toGenRoom(r)), nil
}

// DeleteRoom implements DELETE /rooms/{roomId}.
func (h *Handler) DeleteRoom(ctx context.Context, req gen.DeleteRoomRequestObject) (gen.DeleteRoomResponseObject, error) {
	if h.rooms == nil {
		return nil, errNotConfigured
	}
	err := h.rooms.Delete(ctx, actorFrom(ctx), req.RoomId.String())
	if errors.Is(err, rooms.ErrNotFound) {
		return gen.DeleteRoom404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	var inUse resource.InUseError
	if errors.As(err, &inUse) {
		return gen.DeleteRoom409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(
			problemBody(http.StatusConflict, "in_use", inUse.Message))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteRoom204Response{}, nil
}
