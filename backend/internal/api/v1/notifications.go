package apiv1

import (
	"context"
	"errors"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
)

// NotificationService is the subset of notify.Service used by the handler.
type NotificationService interface {
	List(ctx context.Context, userID string, page, size int, unreadOnly bool) (notify.Inbox, error)
	MarkRead(ctx context.Context, userID, id string) error
	MarkAllRead(ctx context.Context, userID string) (int64, error)
}

// WithNotifications enables the notification operations. Without it they answer 501.
func WithNotifications(s NotificationService) Option { return func(h *Handler) { h.notifications = s } }

// ListMyNotifications implements GET /me/notifications.
func (h *Handler) ListMyNotifications(ctx context.Context, req gen.ListMyNotificationsRequestObject) (gen.ListMyNotificationsResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.notifications == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, nil, nil, crud.Sorts{"createdAt": "created_at"}, "-createdAt")
	if qerr != nil {
		return gen.ListMyNotifications400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	in, err := h.notifications.List(ctx, p.UserID, q.Page, q.Size, req.Params.Unread != nil && *req.Params.Unread)
	if err != nil {
		return nil, err
	}
	out := gen.NotificationPage{Items: make([]gen.Notification, len(in.Items)), UnreadCount: in.UnreadCount,
		Page: gen.PageInfo{Number: in.Page, Size: in.Size, Total: in.Total}}
	for i, it := range in.Items {
		out.Items[i] = gen.Notification{Id: mustUUID(it.ID), Kind: it.Kind, Title: it.Title, Body: it.Body, Link: it.Link, CreatedAt: it.CreatedAt, ReadAt: it.ReadAt}
	}
	return gen.ListMyNotifications200JSONResponse(out), nil
}

// MarkMyNotificationRead implements POST /me/notifications/{id}/read.
func (h *Handler) MarkMyNotificationRead(ctx context.Context, req gen.MarkMyNotificationReadRequestObject) (gen.MarkMyNotificationReadResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.notifications == nil {
		return nil, errNotConfigured
	}
	err := h.notifications.MarkRead(ctx, p.UserID, req.Id.String())
	switch {
	case errors.Is(err, notify.ErrNotFound):
		return gen.MarkMyNotificationRead404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.MarkMyNotificationRead204Response{}, nil
}

// MarkAllMyNotificationsRead implements POST /me/notifications/read-all.
func (h *Handler) MarkAllMyNotificationsRead(ctx context.Context, _ gen.MarkAllMyNotificationsReadRequestObject) (gen.MarkAllMyNotificationsReadResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.notifications == nil {
		return nil, errNotConfigured
	}
	if _, err := h.notifications.MarkAllRead(ctx, p.UserID); err != nil {
		return nil, err
	}
	return gen.MarkAllMyNotificationsRead204Response{}, nil
}
