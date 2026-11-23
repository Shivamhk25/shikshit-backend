package pauses

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ListAudited returns one page. The read is audited with the paging and filters, never the search text (a child's name).
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Pause], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	if f.ClassID != "" {
		conds = append(conds, resource.Cond{SQL: enrolSub("e.class_id", "") + " = ?::uuid", Args: []any{f.ClassID}})
	}
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "status": f.Status, "classId": f.ClassID}, q, conds...)
}

// View returns one pause; the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Pause, error) {
	return s.store().View(ctx, a, id)
}

var errActive = ConflictError{"already_paused", "This student already has an active pause. End it first."}

// constraintError turns a violated database rule into the answer the client should get. The unique index is what
// keeps two simultaneous pauses of one student from both being saved.
func constraintError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "student_pauses_active_uq" {
		return errActive
	}
	return err
}

// setStudentStatus moves the student between active and paused and audits it as a student change. It does nothing
// when the student is not in the "from" status (for example an edit made on the student form meanwhile).
func setStudentStatus(ctx context.Context, tx pgx.Tx, a audit.Actor, studentID, from, to, pauseID string) error {
	tag, err := tx.Exec(ctx, `UPDATE students SET status = $3, updated_by = $4::uuid WHERE id::text = $1 AND status = $2 AND deleted_at IS NULL`, studentID, from, to, a.UserID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, a, audit.Entry{Action: "students.update", ResourceType: "student", ResourceID: studentID,
		Before: map[string]any{"status": from}, After: map[string]any{"status": to, "pauseId": pauseID}})
}

// Create pauses a student: it saves the pause and sets the student "paused" in one transaction, so a failure keeps
// neither. A student with an active pause, or one who is transferred, alumni or removed, is refused.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Pause, error) {
	in, err := normalise(in, Today(time.Now()), true)
	if err != nil {
		return Pause{}, err
	}
	var out Pause
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		var admitted *string
		err := tx.QueryRow(ctx, `SELECT status, admission_date::text FROM students WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, in.StudentID).Scan(&status, &admitted)
		if errors.Is(err, pgx.ErrNoRows) {
			return crud.FieldErrors{{Field: "studentId", Code: "not_found", Message: "This student was not found."}}
		}
		if err != nil {
			return err
		}
		if status != "active" && status != "paused" {
			return crud.FieldErrors{{Field: "studentId", Code: "not_pausable", Message: "Only an active student can be paused. This student is " + status + "."}}
		}
		if admitted != nil && *in.PausedFrom < *admitted {
			return crud.FieldErrors{{Field: "pausedFrom", Code: "before_admission", Message: "The pause cannot start before the admission date (" + *admitted + ")."}}
		}
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM student_pauses WHERE student_id::text = $1 AND status = 'active' AND deleted_at IS NULL)`, in.StudentID).Scan(&open); err != nil {
			return err
		}
		if open {
			return errActive
		}
		// the class the student is in now; none is fine
		var enrolment *string
		err = tx.QueryRow(ctx, `SELECT e.id::text FROM student_enrollments e JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id
			WHERE e.student_id::text = $1 AND e.status = 'enrolled' AND e.deleted_at IS NULL ORDER BY y.start_date DESC, e.created_at DESC LIMIT 1`, in.StudentID).Scan(&enrolment)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tn, ok := tenancy.FromContext(ctx)
		if !ok {
			return tenancy.ErrNoTenant
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO student_pauses (tenant_id, student_id, enrolment_id, paused_from, expected_back, reason, reason_note, created_by, updated_by)
			VALUES ($1, $2::uuid, $3::uuid, $4::date, $5::date, $6, $7, $8::uuid, $8::uuid) RETURNING id::text`,
			tn.ID, in.StudentID, enrolment, *in.PausedFrom, in.ExpectedBack, in.Reason, in.ReasonNote, a.UserID).Scan(&id); err != nil {
			return err
		}
		if err := setStudentStatus(ctx, tx, a, in.StudentID, "active", "paused", id); err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_pauses.create", ResourceType: "student_pause", ResourceID: id, After: out})
	})
	if err != nil {
		return Pause{}, constraintError(err)
	}
	return out, nil
}

// lock reads a live pause and takes the row locks of its student and then of the pause. Every path that changes a
// student and a pause takes them in this order (student first), so two changes never wait on each other in a circle.
func lock(ctx context.Context, tx pgx.Tx, id string) (status, studentID, pausedFrom string, err error) {
	err = tx.QueryRow(ctx, `SELECT student_id::text FROM student_pauses WHERE id::text = $1 AND deleted_at IS NULL`, id).Scan(&studentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM students WHERE id::text = $1 FOR UPDATE`, studentID); err != nil {
		return
	}
	err = tx.QueryRow(ctx, `SELECT status, paused_from::text FROM student_pauses WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&status, &pausedFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func notActive(status string) error {
	return ConflictError{"not_active", "Only an active pause can be changed. This one is " + status + "."}
}

// Update changes the expected back date, reason and note of an active pause. The student and the start date cannot
// be changed: to correct them, cancel the pause and record a new one.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Pause, error) {
	in, err := normalise(in, Today(time.Now()), false)
	if err != nil {
		return Pause{}, err
	}
	var out Pause
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		status, _, from, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != StatusActive {
			return notActive(status)
		}
		if in.ExpectedBack != nil && *in.ExpectedBack < from {
			return crud.FieldErrors{{Field: "expectedBack", Code: "before_start", Message: "The expected back date cannot be before the pause starts (" + from + ")."}}
		}
		before, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE student_pauses SET expected_back = $2::date, reason = $3, reason_note = $4, updated_by = $5::uuid WHERE id::text = $1`,
			id, in.ExpectedBack, in.Reason, in.ReasonNote, a.UserID); err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_pauses.update", ResourceType: "student_pause", ResourceID: id, Before: before, After: out})
	})
	return out, err
}

// End records that the student is back: the pause becomes "ended" on endedOn (default today in India; not in the
// future, not before the pause began) and the student becomes "active", in one transaction.
func (s Service) End(ctx context.Context, a audit.Actor, id string, endedOn, note *string) (Pause, error) {
	today := Today(time.Now())
	on := today
	if p := crud.TrimPtr(endedOn); p != nil {
		on = *p
	}
	note = crud.TrimPtr(note)
	var fe crud.FieldErrors
	if parseDate(&fe, "endedOn", "date the student came back", on) && on > today {
		fe.Add("endedOn", "in_future", "The date the student came back cannot be in the future.")
	}
	fe.Optional("note", "note", note, 500)
	if err := fe.OrNil(); err != nil {
		return Pause{}, err
	}
	return s.close(ctx, a, id, StatusEnded, "end", &on, note)
}

// Cancel withdraws a pause recorded by mistake: the pause becomes "cancelled" and the student becomes "active" again.
func (s Service) Cancel(ctx context.Context, a audit.Actor, id string, note *string) (Pause, error) {
	note = crud.TrimPtr(note)
	var fe crud.FieldErrors
	fe.Optional("note", "note", note, 500)
	if err := fe.OrNil(); err != nil {
		return Pause{}, err
	}
	return s.close(ctx, a, id, StatusCancelled, "cancel", nil, note)
}

// close moves an active pause to its final status and returns the student to "active", all or nothing.
func (s Service) close(ctx context.Context, a audit.Actor, id, to, action string, endedOn, note *string) (Pause, error) {
	var out Pause
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		status, studentID, from, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != StatusActive {
			return notActive(status)
		}
		if endedOn != nil && *endedOn < from {
			return crud.FieldErrors{{Field: "endedOn", Code: "before_start", Message: "The student cannot be back before the pause began (" + from + ")."}}
		}
		if _, err := tx.Exec(ctx, `UPDATE student_pauses SET status = $2, ended_on = $3::date, closed_at = now(), closed_by = $4::uuid, close_note = $5, updated_by = $4::uuid WHERE id::text = $1`,
			id, to, endedOn, a.UserID, note); err != nil {
			return err
		}
		if err := setStudentStatus(ctx, tx, a, studentID, "paused", "active", id); err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_pauses." + action, ResourceType: "student_pause", ResourceID: id,
			Before: map[string]any{"status": StatusActive}, After: map[string]any{"status": to, "endedOn": endedOn, "note": note}})
	})
	return out, err
}
