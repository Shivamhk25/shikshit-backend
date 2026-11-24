package electivechanges

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
)

// Register adds the "student_elective_change" workflow type to the registry the approval service uses. Call it once at
// start-up, with the same registry the Service's Approvals field holds.
func Register(reg *approvals.Registry) error {
	return reg.Register(approvals.Type{
		Key: TypeKey, Title: "Elective subject change",
		Steps:       []approvals.Step{{Name: "Approval", Permission: Permission}},
		Scope:       scopeOf,
		OnApproved:  onApproved,
		OnRejected:  onRejected,
		OnCancelled: onCancelled,
	})
}

// scopeOf names the class section the request belongs to: the section the student is enrolled in for the year of the
// choice. Approvers with a class-section grant for that very section may decide it, and approvers with an institute-wide
// grant may decide any. A student without a section gets a scope no class-section grant can match, so only
// institute-wide approvers may decide it.
func scopeOf(ctx context.Context, tx pgx.Tx, subjectID string) (string, string, error) {
	var mapping *string
	err := tx.QueryRow(ctx, `SELECT (SELECT m.id::text FROM student_enrollments e
			JOIN class_section_mappings m ON m.tenant_id = e.tenant_id AND m.year_id = e.year_id AND m.class_id = e.class_id AND m.section_id = e.section_id AND m.deleted_at IS NULL
			WHERE e.tenant_id = c.tenant_id AND e.student_id = c.student_id AND e.year_id = c.year_id AND e.status = 'enrolled' AND e.deleted_at IS NULL LIMIT 1)
		FROM student_elective_changes c WHERE c.id::text = $1`, subjectID).Scan(&mapping)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if mapping == nil {
		return "student_elective_change", subjectID, nil
	}
	return "class_section", *mapping, nil
}

type openRow struct{ id, choiceID, fromID, toID string }

// lockOpen takes the row lock of the request the approval is about and returns it; false when the request is no longer
// "requested" (a hook that runs twice, or on a request changed meanwhile, then writes nothing).
func lockOpen(ctx context.Context, tx pgx.Tx, r approvals.Request) (openRow, bool, error) {
	var o openRow
	var status string
	err := tx.QueryRow(ctx, `SELECT id::text, choice_id::text, from_subject_id::text, to_subject_id::text, status FROM student_elective_changes
		WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, r.SubjectID).Scan(&o.id, &o.choiceID, &o.fromID, &o.toID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return o, false, ConflictError{"change_missing", "The change request no longer exists."}
	}
	if err != nil {
		return o, false, err
	}
	return o, status == StatusRequested, nil
}

// closeChange moves a requested change to its next state. setSQL may use $1 (the id), $2 (the new status) and $3 (the actor).
func closeChange(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request, status, setSQL, action string) error {
	tag, err := tx.Exec(ctx, `UPDATE student_elective_changes SET status = $2, `+setSQL+`, updated_by = $3::uuid WHERE id::text = $1 AND status = 'requested' AND deleted_at IS NULL`,
		r.SubjectID, status, actor.UserID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, actor, audit.Entry{Action: action, ResourceType: "student_elective_change", ResourceID: r.SubjectID,
		Before: map[string]any{"status": StatusRequested}, After: map[string]any{"status": status, "approvalRequestId": r.ID}})
}

// onApproved applies the swap. If it no longer fits it refuses the approval (409 with the reason) so that nothing is
// half done; the decision, the swap, the history row and the status change commit or roll back together.
func onApproved(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	o, open, err := lockOpen(ctx, tx, r)
	var ce ConflictError
	if errors.As(err, &ce) {
		return approvals.ConflictError{Message: ce.Message}
	}
	if err != nil || !open {
		return err
	}
	ch, err := electives.LockChoice(ctx, tx, o.choiceID, true)
	if errors.Is(err, electives.ErrNotFound) {
		return approvals.ConflictError{Message: "The student's elective choice no longer exists, so this change cannot be applied. Reject the request."}
	}
	if err != nil {
		return err
	}
	plan, problems, err := electives.PlanSwap(ctx, tx, ch, o.fromID, o.toID)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		msgs := make([]string, len(problems))
		for i, p := range problems {
			msgs[i] = p.Message
		}
		return approvals.ConflictError{Message: "This change can no longer be applied: " + strings.Join(msgs, " ") + " Reject the request instead."}
	}
	if err := electives.ApplySwap(ctx, tx, actor, plan, electives.SwapInput{ChangeID: o.id, ApprovalRequestID: r.ID}); err != nil {
		return err
	}
	return closeChange(ctx, tx, actor, r, StatusApproved, "decided_at = now(), decided_by = $3::uuid", "student_elective_changes.approve")
}

func onRejected(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	return closeChange(ctx, tx, actor, r, StatusRejected, "decided_at = now(), decided_by = $3::uuid", "student_elective_changes.reject")
}

func onCancelled(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	return closeChange(ctx, tx, actor, r, StatusCancelled, "cancelled_at = now(), cancelled_by = $3::uuid", "student_elective_changes.cancel")
}
