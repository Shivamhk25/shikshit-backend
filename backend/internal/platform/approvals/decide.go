package approvals

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var auditAction = map[string]string{"approved": "approval.approve", "rejected": "approval.reject"}

// DecideInput is one approver's decision. Step, when not zero, is the step the approver was looking at; a request that
// has moved on since is refused instead of deciding the wrong step.
type DecideInput struct {
	Approve bool
	Comment string
	Step    int
}

// Decide approves or rejects the current step of a request as the owner of access. A rejection needs a comment. The
// last approval or any rejection ends the request and runs the type's hook in the same transaction; a hook error
// undoes the decision. Decisions on one request run one at a time (row lock), so the second of two simultaneous
// deciders is told the request is no longer open.
func (s Service) Decide(ctx context.Context, actor audit.Actor, a authz.Access, id string, in DecideInput) (Detail, error) {
	in.Comment = strings.TrimSpace(in.Comment)
	var fe crud.FieldErrors
	if !in.Approve {
		fe.Required("comment", "a reason", in.Comment, maxComment)
	} else if utf8.RuneCountInString(in.Comment) > maxComment {
		fe.Add("comment", "too_long", "The comment is at most 1000 characters.")
	}
	if err := fe.OrNil(); err != nil {
		return Detail{}, err
	}
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		r, steps, _, err := s.load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		grants, err := s.activeGrants(ctx, tx, a.UserID)
		if err != nil {
			return err
		}
		rt := decisionRights(a, grants, r, steps)
		if !rt.sees {
			return ErrNotFound
		}
		switch {
		case r.RequesterID == a.UserID:
			return ForbiddenError{"You made this request, so someone else has to decide it."}
		case r.Status != StatusPending:
			return ConflictError{"This request is no longer open. It was " + r.Status + "."}
		case in.Step != 0 && in.Step != r.CurrentStep:
			return ConflictError{"This request has moved on to another step."}
		case !rt.canDecide():
			return ForbiddenError{"You may not decide this step."}
		}
		// Own access first. Only when that does not cover the step does the delegator's access decide, and then the
		// decision is recorded for both.
		var onBehalfOf *string
		auditAfter := map[string]any{"type": r.TypeKey, "subject": r.SubjectID, "comment": in.Comment}
		if !rt.own {
			onBehalfOf = &rt.via.ID
			auditAfter["onBehalfOf"] = rt.via.ID
		}
		step := r.CurrentStep
		stepStatus, action := StatusApproved, "approved"
		if !in.Approve {
			stepStatus, action = StatusRejected, "rejected"
		}
		if _, err := tx.Exec(ctx, `UPDATE approval_steps SET status = $3, decided_by = $4::uuid, decided_at = now() WHERE request_id = $1::uuid AND step_no = $2`,
			id, step, stepStatus, actor.UserID); err != nil {
			return err
		}
		if err := s.event(ctx, tx, id, &step, actor.UserID, action, in.Comment, onBehalfOf); err != nil {
			return err
		}
		auditAfter["step"] = step
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: auditAction[action], ResourceType: "approval_request", ResourceID: id, After: auditAfter}); err != nil {
			return err
		}
		final := !in.Approve || step == r.StepCount
		if !final {
			if _, err := tx.Exec(ctx, `UPDATE approval_requests SET current_step = current_step + 1 WHERE id = $1::uuid`, id); err != nil {
				return err
			}
			next := steps[step].Permission
			r.CurrentStep, r.StepName = step+1, steps[step].Name
			if err := s.notifyApprovers(ctx, tx, r, next); err != nil {
				return err
			}
		} else {
			status := StatusApproved
			if !in.Approve {
				status = StatusRejected
			}
			if err := s.finish(ctx, tx, actor, r, status, in.Comment); err != nil {
				return err
			}
		}
		r, steps, events, err := s.load(ctx, tx, id, false)
		if err != nil {
			return err
		}
		after := decisionRights(a, grants, r, steps)
		if !after.own {
			r.DelegatedFrom = after.via
		}
		out = Detail{Request: r, Steps: steps, Events: events, CanDecide: after.canDecide(), CanCancel: false}
		return nil
	})
	return out, err
}

// Cancel withdraws the requester's own open request and runs the type's OnCancelled hook in the same transaction.
func (s Service) Cancel(ctx context.Context, actor audit.Actor, a authz.Access, id, comment string) (Detail, error) {
	comment = strings.TrimSpace(comment)
	var fe crud.FieldErrors
	if utf8.RuneCountInString(comment) > maxComment {
		fe.Add("comment", "too_long", "The comment is at most 1000 characters.")
	}
	if err := fe.OrNil(); err != nil {
		return Detail{}, err
	}
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		r, steps, _, err := s.load(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if !involved(a, r, steps) {
			return ErrNotFound
		}
		switch {
		case r.RequesterID != a.UserID:
			return ForbiddenError{"Only the person who made this request can cancel it."}
		case r.Status != StatusPending:
			return ConflictError{"This request is no longer open. It was " + r.Status + "."}
		}
		if err := s.event(ctx, tx, id, nil, actor.UserID, "cancelled", comment, nil); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "approval.cancel", ResourceType: "approval_request", ResourceID: id,
			After: map[string]any{"type": r.TypeKey, "subject": r.SubjectID, "comment": comment}}); err != nil {
			return err
		}
		if err := s.finish(ctx, tx, actor, r, StatusCancelled, comment); err != nil {
			return err
		}
		r, steps, events, err := s.load(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out = Detail{Request: r, Steps: steps, Events: events}
		return nil
	})
	return out, err
}

// finish closes the request with its final status, runs the type's hook for it and tells the requester (unless they
// are the one who ended it).
func (s Service) finish(ctx context.Context, tx pgx.Tx, actor audit.Actor, r Request, status, comment string) error {
	if _, err := tx.Exec(ctx, `UPDATE approval_requests SET status = $2, decided_at = now() WHERE id = $1::uuid`, r.ID, status); err != nil {
		return err
	}
	r.Status = status
	if t, ok := s.Reg.Get(r.TypeKey); ok {
		hook := map[string]Hook{StatusApproved: t.OnApproved, StatusRejected: t.OnRejected, StatusCancelled: t.OnCancelled}[status]
		if hook != nil {
			if err := hook(ctx, tx, actor, r); err != nil {
				return err
			}
		}
	}
	if status == StatusCancelled {
		return nil
	}
	body := trunc(r.Summary, 100)
	if status == StatusRejected {
		body = trunc("Reason: "+comment, 400)
	}
	return notify.Notify(ctx, tx, []string{r.RequesterID}, notify.Notification{
		Kind: "approval." + status, Title: trunc(r.TypeTitle+": "+status, notify.MaxTitle), Body: body, Link: "/approvals/" + r.ID})
}

// notifyApprovers tells everyone who may decide the step (except the requester) that a request waits for them.
func (s Service) notifyApprovers(ctx context.Context, tx pgx.Tx, r Request, permission string) error {
	if s.Access == nil {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT u.id::text FROM users u WHERE u.status = 'active' AND u.deleted_at IS NULL AND u.id <> $1::uuid
		AND (EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id) OR EXISTS (SELECT 1 FROM user_permission_overrides o WHERE o.user_id = u.id))
		ORDER BY u.id LIMIT $2`, r.RequesterID, maxApprovers)
	if err != nil {
		return err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var who []string
	for _, id := range candidates {
		a, err := s.Access.Resolve(ctx, id)
		if err != nil {
			continue // a user who cannot be resolved (for example deactivated meanwhile) is not an approver
		}
		if MayDecide(a, permission, r.ScopeType, r.ScopeRef) {
			who = append(who, id)
		}
	}
	if err := notify.Notify(ctx, tx, who, notify.Notification{
		Kind: "approval.requested", Title: trunc("Approval needed: "+r.TypeTitle, notify.MaxTitle), Body: trunc(r.Summary, notify.MaxBody), Link: "/approvals/" + r.ID}); err != nil {
		return err
	}
	// People holding a delegation from one of those approvers are told too, once, unless they are approvers already.
	// The requester is never told: a delegation does not let them decide their own request.
	told := map[string]bool{r.RequesterID: true}
	for _, id := range who {
		told[id] = true
	}
	delegates, err := s.delegatesOf(ctx, tx, who, r.TypeKey, told)
	if err != nil {
		return err
	}
	return notify.Notify(ctx, tx, delegates, notify.Notification{
		Kind: "approval.requested", Title: trunc("Approval needed (delegated to you): "+r.TypeTitle, notify.MaxTitle), Body: trunc(r.Summary, notify.MaxBody), Link: "/approvals/" + r.ID})
}
