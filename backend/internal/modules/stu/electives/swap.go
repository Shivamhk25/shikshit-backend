package electives

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// SwapPlan is a checked swap of one subject of a choice for another.
type SwapPlan struct {
	Choice    ChoiceRow
	Offering  Offering
	Before    []string
	After     []string
	FromID    string
	ToID      string
	StatusNow string
}

// PlanSwap checks that subject from can be replaced by subject to in the choice, as things stand now. It is used when a
// change is requested and again when it is approved, so both apply the same rules. The field errors name fromSubjectId,
// toSubjectId, studentId or groupId:
//   - fromSubjectId not_chosen: the student does not have that subject in the choice;
//   - toSubjectId same_subject, already_chosen, not_in_group: the new subject is the old one, is already chosen, or is not
//     offered by the group to the student's class section this year;
//   - studentId not_enrolled and groupId not_found, inactive or not_offered: the student or the group no longer fits;
//   - subjectIds below_minimum or above_maximum: the result would break the limits of a group that enforces them.
//
// The choice must be locked by the caller (LockChoice with forUpdate) so the check and the write see the same set.
func PlanSwap(ctx context.Context, tx pgx.Tx, ch ChoiceRow, from, to string) (SwapPlan, crud.FieldErrors, error) {
	plan := SwapPlan{Choice: ch, FromID: from, ToID: to, StatusNow: ch.Status}
	var fe crud.FieldErrors
	pl, err := placementOf(ctx, tx, ch.StudentID, ch.YearID)
	if err != nil {
		fe, err := split(err)
		return plan, fe, err
	}
	off, err := LoadOffering(ctx, tx, pl, ch.GroupID)
	if err != nil {
		fe, err := split(err)
		return plan, fe, err
	}
	plan.Offering = off
	if plan.Before, err = SetOf(ctx, tx, ch.ID); err != nil {
		return plan, nil, err
	}
	if !slices.Contains(plan.Before, from) {
		fe.Add("fromSubjectId", "not_chosen", "The student has not chosen this subject, so it cannot be replaced.")
	}
	switch {
	case from == to:
		fe.Add("toSubjectId", "same_subject", "Choose a different subject to change to.")
	case slices.Contains(plan.Before, to):
		fe.Add("toSubjectId", "already_chosen", "The student has already chosen this subject.")
	case !off.Has(to):
		fe.Add("toSubjectId", "not_in_group", "This subject is not part of "+off.Group.Name+" for "+pl.ClassName+" "+pl.SectionName+" this year.")
	}
	if len(fe) > 0 {
		return plan, fe, nil
	}
	for _, id := range plan.Before {
		if id == from {
			plan.After = append(plan.After, to)
		} else {
			plan.After = append(plan.After, id)
		}
	}
	if _, _, lim := off.Check("subjectIds", plan.After); lim != nil {
		return plan, lim, nil
	}
	return plan, nil, nil
}

// split separates a failed check (field errors, returned as such) from a failure of the database (returned as an error).
func split(err error) (crud.FieldErrors, error) {
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return fe, nil
	}
	return nil, err
}

// SwapInput says which change is being applied and by which approval.
type SwapInput struct {
	ChangeID, ApprovalRequestID string
}

// ApplySwap carries out a swap inside tx: the subject is replaced in the choice and a history row (source
// change_request) is written. The caller has locked the choice and checked the plan.
func ApplySwap(ctx context.Context, tx pgx.Tx, a audit.Actor, plan SwapPlan, in SwapInput) error {
	if _, err := tx.Exec(ctx, `DELETE FROM student_elective_choice_subjects WHERE choice_id::text = $1 AND subject_id::text = $2`, plan.Choice.ID, plan.FromID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO student_elective_choice_subjects (tenant_id, choice_id, subject_id, created_by) VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid)
		ON CONFLICT (choice_id, subject_id) DO NOTHING`, plan.Choice.ID, plan.ToID, a.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE student_elective_choices SET updated_by = $2::uuid, updated_at = now() WHERE id::text = $1`, plan.Choice.ID, a.UserID); err != nil {
		return err
	}
	status := plan.StatusNow
	if err := RecordHistory(ctx, tx, HistoryEntry{ChoiceID: plan.Choice.ID, StudentID: plan.Choice.StudentID, YearID: plan.Choice.YearID, GroupID: plan.Choice.GroupID,
		Action: "swap", Before: plan.Before, After: plan.After, StatusBefore: &status, StatusAfter: &status, Source: SourceChangeRequest,
		ChangeID: &in.ChangeID, ApprovalRequestID: &in.ApprovalRequestID, ChangedBy: a.UserID}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.swap", ResourceType: "student_elective", ResourceID: plan.Choice.ID,
		Before: map[string]any{"subjects": plan.Before}, After: map[string]any{"subjects": plan.After, "changeId": in.ChangeID, "approvalRequestId": in.ApprovalRequestID}})
}
