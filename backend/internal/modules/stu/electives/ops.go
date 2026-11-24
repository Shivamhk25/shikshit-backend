package electives

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Sources of a change in the history.
const (
	SourceStaff         = "staff"
	SourceChangeRequest = "change_request"
)

// Input is what creating a choice carries.
type Input struct {
	StudentID, YearID, GroupID string
	SubjectIDs                 []string
	Status                     string // "chosen" when empty
}

// Update is what replacing a choice carries. An empty Status keeps the current one.
type Update struct {
	SubjectIDs []string
	Status     string
}

// HistoryEntry is one row of the history of a choice.
type HistoryEntry struct {
	ChoiceID, StudentID, YearID, GroupID string
	Action                               string // create, replace, swap, status or delete
	Before, After                        []string
	StatusBefore, StatusAfter            *string
	Source                               string
	ChangeID, ApprovalRequestID          *string
	ChangedBy                            string
}

// RecordHistory adds a row to the history of a choice inside tx. Rows are only ever added.
func RecordHistory(ctx context.Context, tx pgx.Tx, h HistoryEntry) error {
	if h.Before == nil {
		h.Before = []string{}
	}
	if h.After == nil {
		h.After = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO student_elective_choice_history (tenant_id, choice_id, student_id, year_id, group_id, action, subjects_before, subjects_after,
			status_before, status_after, source, change_id, approval_request_id, changed_by)
		VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6::uuid[], $7::uuid[], $8, $9, $10, $11::uuid, $12::uuid, $13::uuid)`,
		h.ChoiceID, h.StudentID, h.YearID, h.GroupID, h.Action, h.Before, h.After, h.StatusBefore, h.StatusAfter, h.Source, h.ChangeID, h.ApprovalRequestID, h.ChangedBy)
	return err
}

// SetOf returns the subject ids of a choice, sorted.
func SetOf(ctx context.Context, tx pgx.Tx, choiceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT subject_id::text FROM student_elective_choice_subjects WHERE choice_id::text = $1 ORDER BY subject_id::text`, choiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// writeSet makes the subjects of a choice exactly ids.
func writeSet(ctx context.Context, tx pgx.Tx, a audit.Actor, choiceID string, ids []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM student_elective_choice_subjects WHERE choice_id::text = $1 AND NOT (subject_id::text = ANY($2::text[]))`, choiceID, ids); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO student_elective_choice_subjects (tenant_id, choice_id, subject_id, created_by)
		SELECT app_current_tenant(), $1::uuid, s::uuid, $3::uuid FROM unnest($2::text[]) AS s ON CONFLICT (choice_id, subject_id) DO NOTHING`, choiceID, ids, a.UserID)
	return err
}

func sorted(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func same(a, b []string) bool { return slices.Equal(sorted(a), sorted(b)) }

func normaliseStatus(fe *crud.FieldErrors, status, def string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return def
	}
	fe.Choice("status", status, Statuses...)
	return status
}

func uuidField(fe *crud.FieldErrors, field, label, v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "":
		fe.Add(field, "required", "Choose "+label+".")
	case !refs.IsUUID(v):
		fe.Add(field, "invalid", "Choose "+label+" from the list.")
	}
	return v
}

func choiceExists() ConflictError {
	return ConflictError{"choice_exists", "This student already has a choice for this group and year. Change that one instead."}
}

// insertChoice adds a choice with its subjects and history row. The unique index settles two simultaneous creates.
func insertChoice(ctx context.Context, tx pgx.Tx, a audit.Actor, studentID, yearID, groupID string, ids []string, status string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO student_elective_choices (tenant_id, student_id, year_id, group_id, status, created_by, updated_by)
		VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $5::uuid) RETURNING id::text`, studentID, yearID, groupID, status, a.UserID).Scan(&id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "student_elective_choices_uq" {
		return "", choiceExists()
	}
	if err != nil {
		return "", err
	}
	if err := writeSet(ctx, tx, a, id, ids); err != nil {
		return "", err
	}
	return id, RecordHistory(ctx, tx, HistoryEntry{ChoiceID: id, StudentID: studentID, YearID: yearID, GroupID: groupID, Action: "create", After: ids,
		StatusAfter: &status, Source: SourceStaff, ChangedBy: a.UserID})
}

// changeChoice replaces the subjects and status of a choice and records it. It returns false when nothing differed.
func changeChoice(ctx context.Context, tx pgx.Tx, a audit.Actor, before Choice, ids []string, status string) (bool, error) {
	old := make([]string, len(before.Subjects))
	for i, s := range before.Subjects {
		old[i] = s.ID
	}
	setSame, statusSame := same(old, ids), before.Status == status
	if setSame && statusSame {
		return false, nil
	}
	if !setSame {
		if err := writeSet(ctx, tx, a, before.ID, ids); err != nil {
			return false, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE student_elective_choices SET status = $2, updated_by = $3::uuid WHERE id::text = $1`, before.ID, status, a.UserID); err != nil {
		return false, err
	}
	action := "replace"
	if setSame {
		action = "status"
	}
	return true, RecordHistory(ctx, tx, HistoryEntry{ChoiceID: before.ID, StudentID: before.Student.ID, YearID: before.YearID, GroupID: before.Group.ID, Action: action,
		Before: old, After: ids, StatusBefore: &before.Status, StatusAfter: &status, Source: SourceStaff, ChangedBy: a.UserID})
}

// removeChoice soft-deletes a choice. It refuses while a change request for it is waiting for a decision.
func removeChoice(ctx context.Context, tx pgx.Tx, a audit.Actor, before Choice) error {
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM student_elective_changes WHERE choice_id::text = $1 AND status = 'requested' AND deleted_at IS NULL)`, before.ID).Scan(&open); err != nil {
		return err
	}
	if open {
		return ConflictError{"open_change_request", "A change request for this choice is waiting for a decision. Cancel or decide it first."}
	}
	old := make([]string, len(before.Subjects))
	for i, s := range before.Subjects {
		old[i] = s.ID
	}
	if _, err := tx.Exec(ctx, `UPDATE student_elective_choices SET deleted_at = now(), deleted_by = $2::uuid, updated_by = $2::uuid WHERE id::text = $1`, before.ID, a.UserID); err != nil {
		return err
	}
	return RecordHistory(ctx, tx, HistoryEntry{ChoiceID: before.ID, StudentID: before.Student.ID, YearID: before.YearID, GroupID: before.Group.ID, Action: "delete",
		Before: old, StatusBefore: &before.Status, Source: SourceStaff, ChangedBy: a.UserID})
}

// Create records a student's choice for a group and year.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Choice, error) {
	var fe crud.FieldErrors
	in.StudentID = uuidField(&fe, "studentId", "the student", in.StudentID)
	in.YearID = uuidField(&fe, "yearId", "the academic year", in.YearID)
	in.GroupID = uuidField(&fe, "groupId", "the elective group", in.GroupID)
	in.Status = normaliseStatus(&fe, in.Status, StatusChosen)
	if err := fe.OrNil(); err != nil {
		return Choice{}, err
	}
	var out Choice
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := studentVisible(ctx, tx, &fe, "studentId", in.StudentID); err != nil {
			return err
		}
		if err := refs.Live(ctx, tx, &fe, "yearId", "academic_years", in.YearID); err != nil {
			return err
		}
		if err := fe.OrNil(); err != nil {
			return err
		}
		pl, err := placementOf(ctx, tx, in.StudentID, in.YearID)
		if err != nil {
			return err
		}
		off, err := LoadOffering(ctx, tx, pl, in.GroupID)
		if err != nil {
			return err
		}
		ids, _, ferr := off.Check("subjectIds", in.SubjectIDs)
		if ferr != nil {
			return ferr
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM student_elective_choices WHERE student_id::text = $1 AND year_id::text = $2 AND group_id::text = $3 AND deleted_at IS NULL)`,
			in.StudentID, in.YearID, in.GroupID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return choiceExists()
		}
		id, err := insertChoice(ctx, tx, a, in.StudentID, in.YearID, in.GroupID, ids, in.Status)
		if err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.create", ResourceType: "student_elective", ResourceID: id, After: out})
	})
	return out, err
}

// Replace sets the subjects (and, when given, the status) of a choice. The subjects are checked against what the group
// offers to the student's class section now.
func (s Service) Replace(ctx context.Context, a audit.Actor, id string, in Update) (Choice, error) {
	var fe crud.FieldErrors
	status := normaliseStatus(&fe, in.Status, "")
	if err := fe.OrNil(); err != nil {
		return Choice{}, err
	}
	var out Choice
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		row, err := LockChoice(ctx, tx, id, true)
		if err != nil {
			return err
		}
		before, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		pl, err := placementOf(ctx, tx, row.StudentID, row.YearID)
		if err != nil {
			return err
		}
		off, err := LoadOffering(ctx, tx, pl, row.GroupID)
		if err != nil {
			return err
		}
		ids, _, ferr := off.Check("subjectIds", in.SubjectIDs)
		if ferr != nil {
			return ferr
		}
		if status == "" {
			status = before.Status
		}
		changed, err := changeChoice(ctx, tx, a, before, ids, status)
		if err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil || !changed {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.update", ResourceType: "student_elective", ResourceID: id, Before: before, After: out})
	})
	return out, err
}

// Delete removes a choice from the lists (a soft delete; the history stays).
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := LockChoice(ctx, tx, id, true); err != nil {
			return err
		}
		before, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := removeChoice(ctx, tx, a, before); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.delete", ResourceType: "student_elective", ResourceID: id, Before: before})
	})
}
