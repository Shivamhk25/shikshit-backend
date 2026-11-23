package transfers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Register adds the "student_transfer" workflow type to the registry the approval service uses. Call it once at
// start-up, with the same registry the transfers Service's Approvals field holds.
//
// The approval is scoped to the transfer itself ("student_transfer", <id>): an approver whose grant covers the whole
// institute may decide it, a holder of a narrower grant (a class section) never matches. The requester can never decide
// their own request; that is the engine's rule.
func Register(reg *approvals.Registry) error {
	return reg.Register(approvals.Type{
		Key: TypeKey, Title: "Student transfer",
		Steps: []approvals.Step{{Name: "Approval", Permission: Permission}},
		Scope: func(_ context.Context, _ pgx.Tx, subjectID string) (string, string, error) {
			return "student_transfer", subjectID, nil
		},
		OnApproved:  onApproved,
		OnRejected:  onRejected,
		OnCancelled: onCancelled,
	})
}

// move changes a requested transfer to its next state inside the deciding transaction. It does nothing when the
// transfer is no longer "requested", so a hook that runs twice, or on a transfer changed meanwhile, writes nothing.
func move(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request, status, setSQL, action string, args ...any) error {
	tag, err := tx.Exec(ctx, `UPDATE student_transfers SET status = $2, `+setSQL+`, updated_by = $3::uuid
		WHERE id::text = $1 AND status = 'requested' AND deleted_at IS NULL`, append([]any{r.SubjectID, status, actor.UserID}, args...)...)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return audit.Record(ctx, tx, actor, audit.Entry{Action: action, ResourceType: "student_transfer", ResourceID: r.SubjectID,
		Before: map[string]any{"status": StatusRequested}, After: map[string]any{"status": status, "approvalRequestId": r.ID}})
}

func onApproved(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	return move(ctx, tx, actor, r, StatusApproved, "approved_at = now(), approved_by = $3::uuid", "student_transfers.approve")
}

func onRejected(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	return move(ctx, tx, actor, r, StatusRejected, "rejected_at = now()", "student_transfers.reject")
}

func onCancelled(ctx context.Context, tx pgx.Tx, actor audit.Actor, r approvals.Request) error {
	return move(ctx, tx, actor, r, StatusCancelled, "cancelled_at = now(), cancelled_by = $3::uuid", "student_transfers.cancel")
}

// ListAudited returns one page. The read is audited with the paging and filters, never the search text (a child's name).
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Transfer], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	if f.ClassID != "" {
		conds = append(conds, resource.Cond{SQL: enrolSub("e.class_id", "") + " = ?::uuid", Args: []any{f.ClassID}})
	}
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "status": f.Status, "classId": f.ClassID}, q, conds...)
}

// View returns one transfer; the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Transfer, error) {
	return s.store().View(ctx, a, id)
}

func summary(x Transfer) string {
	v := fmt.Sprintf("Transfer of %s (%s) — %s", x.Student.Name, x.Student.RegistrationCode, ReasonLabel(x.Reason))
	if utf8.RuneCountInString(v) > 300 {
		v = string([]rune(v)[:299]) + "…"
	}
	return v
}

// constraintError turns a violated database rule into the answer the client should get.
func constraintError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.ConstraintName == "student_transfers_open_uq" {
		return ConflictError{"open_transfer", "This student already has a transfer in progress."}
	}
	return err
}

// Create requests a transfer and files its approval request, in one transaction: if either fails neither is kept.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Transfer, error) {
	today := Today(time.Now())
	in, err := normalise(in, today, true)
	if err != nil {
		return Transfer{}, err
	}
	var out Transfer
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.createIn(ctx, tx, a, in)
		return err
	})
	if err != nil {
		return Transfer{}, constraintError(err)
	}
	return out, nil
}

func (s Service) createIn(ctx context.Context, tx pgx.Tx, a audit.Actor, in Input) (Transfer, error) {
	var status string
	var admitted *string
	err := tx.QueryRow(ctx, `SELECT status, admission_date::text FROM students WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, in.StudentID).Scan(&status, &admitted)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, crud.FieldErrors{{Field: "studentId", Code: "not_found", Message: "This student was not found."}}
	}
	if err != nil {
		return Transfer{}, err
	}
	var fe crud.FieldErrors
	if status != "active" && status != "paused" {
		fe.Add("studentId", "not_transferable", "Only an active or paused student can be transferred. This student is "+status+".")
	}
	if in.LastAttendanceDate != nil && admitted != nil && *in.LastAttendanceDate < *admitted {
		fe.Add("lastAttendanceDate", "before_admission", "The last attendance date cannot be before the admission date ("+*admitted+").")
	}
	if err := fe.OrNil(); err != nil {
		return Transfer{}, err
	}
	var open bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM student_transfers WHERE student_id::text = $1 AND status IN ('requested', 'approved') AND deleted_at IS NULL)`, in.StudentID).Scan(&open); err != nil {
		return Transfer{}, err
	}
	if open {
		return Transfer{}, ConflictError{"open_transfer", "This student already has a transfer in progress."}
	}
	// the class the student is in now; none is fine (a student not yet placed can still leave)
	var enrolment *string
	err = tx.QueryRow(ctx, `SELECT e.id::text FROM student_enrollments e JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id
		WHERE e.student_id::text = $1 AND e.status = 'enrolled' AND e.deleted_at IS NULL ORDER BY y.start_date DESC, e.created_at DESC LIMIT 1`, in.StudentID).Scan(&enrolment)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, err
	}
	tn, ok := tenancy.FromContext(ctx)
	if !ok {
		return Transfer{}, tenancy.ErrNoTenant
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO student_transfers (tenant_id, student_id, enrolment_id, requested_on, reason, reason_note, destination_school, last_attendance_date, created_by, updated_by)
		VALUES ($1, $2::uuid, $3::uuid, $4::date, $5, $6, $7, $8::date, $9::uuid, $9::uuid) RETURNING id::text`,
		tn.ID, in.StudentID, enrolment, *in.RequestedOn, in.Reason, in.ReasonNote, in.DestinationSchool, in.LastAttendanceDate, a.UserID).Scan(&id); err != nil {
		return Transfer{}, err
	}
	x, err := s.store().GetIn(ctx, tx, id)
	if err != nil {
		return Transfer{}, err
	}
	req, err := s.Approvals.Submit(ctx, tx, a, approvals.SubmitInput{TypeKey: TypeKey, SubjectID: id, Summary: summary(x)})
	var ce approvals.ConflictError
	if errors.As(err, &ce) {
		return Transfer{}, ConflictError{"open_transfer", ce.Message}
	}
	if err != nil {
		return Transfer{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE student_transfers SET approval_request_id = $2::uuid WHERE id = $1::uuid`, id, req.ID); err != nil {
		return Transfer{}, err
	}
	if x, err = s.store().GetIn(ctx, tx, id); err != nil {
		return Transfer{}, err
	}
	return x, audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.create", ResourceType: "student_transfer", ResourceID: id, After: x})
}

// lock reads a live transfer's state and takes its row lock, so changes to one transfer run one at a time.
func lock(ctx context.Context, tx pgx.Tx, id string) (status, studentID string, requestedOn string, err error) {
	err = tx.QueryRow(ctx, `SELECT status, student_id::text, requested_on::text FROM student_transfers WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&status, &studentID, &requestedOn)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// Update changes the reason, note, destination and last attendance date of a transfer that is still requested or
// approved. The student and the dates of the request cannot be changed.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Transfer, error) {
	in, err := normalise(in, Today(time.Now()), false)
	if err != nil {
		return Transfer{}, err
	}
	var out Transfer
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		status, studentID, _, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != StatusRequested && status != StatusApproved {
			return ConflictError{"not_editable", "A transfer that is " + status + " can no longer be changed."}
		}
		if in.LastAttendanceDate != nil {
			var admitted *string
			if err := tx.QueryRow(ctx, `SELECT admission_date::text FROM students WHERE id::text = $1`, studentID).Scan(&admitted); err != nil {
				return err
			}
			if admitted != nil && *in.LastAttendanceDate < *admitted {
				return crud.FieldErrors{{Field: "lastAttendanceDate", Code: "before_admission", Message: "The last attendance date cannot be before the admission date (" + *admitted + ")."}}
			}
		}
		before, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE student_transfers SET reason = $2, reason_note = $3, destination_school = $4, last_attendance_date = $5::date, updated_by = $6::uuid WHERE id::text = $1`,
			id, in.Reason, in.ReasonNote, in.DestinationSchool, in.LastAttendanceDate, a.UserID); err != nil {
			return err
		}
		if out, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		if out.Status == StatusRequested {
			// the approver decides on this text, so it follows the edit
			if _, err := tx.Exec(ctx, `UPDATE approval_requests SET summary = $2 WHERE id::text = $1 AND status = 'pending'`, derefStr(out.ApprovalRequestID), summary(out)); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.update", ResourceType: "student_transfer", ResourceID: id, Before: before, After: out})
	})
	return out, err
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// CompleteResult is what completing a transfer made (or, for AlreadyCompleted, what the earlier completion made).
type CompleteResult struct {
	Transfer         Transfer
	AlreadyCompleted bool
}

// Complete finishes an approved transfer in one transaction: it gives the TC number, marks the student transferred and
// closes the student's enrolment. Everything is kept or nothing is. Completing a completed transfer returns it again
// and writes nothing (issuedOn is then ignored). issuedOn defaults to today in India; it cannot be in the future or
// before the request date.
func (s Service) Complete(ctx context.Context, a audit.Actor, id string, issuedOn *string) (CompleteResult, error) {
	var res CompleteResult
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		status, studentID, requestedOn, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		// the row lock is what makes a second, simultaneous completion wait and then find the first one's result
		if status == StatusCompleted {
			res.AlreadyCompleted = true
			res.Transfer, err = s.store().GetIn(ctx, tx, id)
			return err
		}
		if status != StatusApproved {
			return ConflictError{"not_approved", "Only an approved transfer can be completed. This one is " + status + "."}
		}
		today := Today(time.Now())
		issued := today
		if p := crud.TrimPtr(issuedOn); p != nil {
			issued = *p
		}
		var fe crud.FieldErrors
		checkDate(&fe, "issuedOn", "issue date", issued, today)
		if len(fe) == 0 && issued < requestedOn {
			fe.Add("issuedOn", "before_request", "The certificate cannot be issued before the transfer was requested ("+requestedOn+").")
		}
		if err := fe.OrNil(); err != nil {
			return err
		}
		var studentStatus string
		err = tx.QueryRow(ctx, `SELECT status FROM students WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, studentID).Scan(&studentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ConflictError{"student_not_transferable", "The student of this transfer has been removed."}
		}
		if err != nil {
			return err
		}
		if studentStatus != "active" && studentStatus != "paused" {
			return ConflictError{"student_not_transferable", "The student is " + studentStatus + " now, so the transfer cannot be completed."}
		}
		var problems crud.FieldErrors
		for _, g := range s.gates() {
			fe, gerr := g.Check(ctx, tx, id)
			if gerr != nil {
				return gerr
			}
			problems = append(problems, fe...)
		}
		if len(problems) > 0 {
			return NotEligible{Problems: problems}
		}

		// the TC number is given by the database when the status becomes completed
		if _, err := tx.Exec(ctx, `UPDATE student_transfers SET status = 'completed', tc_issued_on = $2::date, completed_at = now(), completed_by = $3::uuid, updated_by = $3::uuid WHERE id::text = $1`,
			id, issued, a.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE students SET status = 'transferred', updated_by = $2::uuid WHERE id::text = $1`, studentID, a.UserID); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, a, audit.Entry{Action: "students.update", ResourceType: "student", ResourceID: studentID,
			Before: map[string]any{"status": studentStatus}, After: map[string]any{"status": "transferred", "transferId": id}}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE student_enrollments SET status = 'withdrawn', updated_by = $2::uuid WHERE student_id::text = $1 AND status = 'enrolled' AND deleted_at IS NULL RETURNING id::text`, studentID, a.UserID)
		if err != nil {
			return err
		}
		var closed []string
		for rows.Next() {
			var eid string
			if err := rows.Scan(&eid); err != nil {
				rows.Close()
				return err
			}
			closed = append(closed, eid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, eid := range closed {
			if err := audit.Record(ctx, tx, a, audit.Entry{Action: "student_enrollments.update", ResourceType: "student_enrollment", ResourceID: eid,
				Before: map[string]any{"status": "enrolled"}, After: map[string]any{"status": "withdrawn", "transferId": id}}); err != nil {
				return err
			}
		}
		if res.Transfer, err = s.store().GetIn(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.complete", ResourceType: "student_transfer", ResourceID: id,
			Before: map[string]any{"status": StatusApproved},
			After:  map[string]any{"status": StatusCompleted, "tcNumber": res.Transfer.TCNumber, "tcIssuedOn": res.Transfer.TCIssuedOn, "enrolmentsClosed": len(closed)}})
	})
	return res, err
}

// Cancel withdraws a transfer. A requested one is cancelled through its approval request, which only the person who
// made the request may do (their refusal is returned as it is); an approved one is cancelled here. Completed, rejected
// and cancelled transfers cannot be cancelled.
func (s Service) Cancel(ctx context.Context, a audit.Actor, access authz.Access, id, comment string) (Transfer, error) {
	var status string
	var reqID *string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT status, approval_request_id::text FROM student_transfers WHERE id::text = $1 AND deleted_at IS NULL`, id).Scan(&status, &reqID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return Transfer{}, err
	}
	switch status {
	case StatusRequested:
		if reqID == nil {
			return Transfer{}, ConflictError{"not_cancellable", "This transfer has no approval request to cancel."}
		}
		if _, err := s.Approvals.Cancel(ctx, a, access, *reqID, comment); err != nil {
			return Transfer{}, err
		}
	case StatusApproved:
		err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
			cur, _, _, err := lock(ctx, tx, id)
			if err != nil {
				return err
			}
			if cur != StatusApproved {
				return ConflictError{"not_cancellable", "This transfer is " + cur + " now and cannot be cancelled."}
			}
			if _, err := tx.Exec(ctx, `UPDATE student_transfers SET status = 'cancelled', cancelled_at = now(), cancelled_by = $2::uuid, updated_by = $2::uuid WHERE id::text = $1`, id, a.UserID); err != nil {
				return err
			}
			return audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.cancel", ResourceType: "student_transfer", ResourceID: id,
				Before: map[string]any{"status": StatusApproved}, After: map[string]any{"status": StatusCancelled, "comment": strings.TrimSpace(comment)}})
		})
		if err != nil {
			return Transfer{}, err
		}
	default:
		return Transfer{}, ConflictError{"not_cancellable", "A transfer that is " + status + " cannot be cancelled."}
	}
	return s.store().Get(ctx, id)
}

// Delete removes a cancelled or rejected transfer from the lists (a soft delete: the row and its history stay). A
// transfer that is open or completed is never deleted.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		status, _, _, err := lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != StatusCancelled && status != StatusRejected {
			return ConflictError{"not_deletable", "Only a cancelled or rejected transfer can be deleted. This one is " + status + "."}
		}
		if _, err := tx.Exec(ctx, `UPDATE student_transfers SET deleted_at = now(), deleted_by = $2::uuid, updated_by = $2::uuid WHERE id::text = $1`, id, a.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.delete", ResourceType: "student_transfer", ResourceID: id, Before: map[string]any{"status": status}})
	})
}

// Guardian is a parent or guardian named on the certificate.
type Guardian struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
}

// Certificate is the data of a transfer certificate. The service gives the facts; laying them out is the client's job.
type Certificate struct {
	TCNumber           string     `json:"tcNumber"`
	IssuedOn           string     `json:"issuedOn"`
	SchoolName         string     `json:"schoolName"`
	Student            CertStud   `json:"student"`
	ClassName          *string    `json:"className"`
	SectionName        *string    `json:"sectionName"`
	YearName           *string    `json:"yearName"`
	LastAttendanceDate *string    `json:"lastAttendanceDate"`
	Reason             string     `json:"reason"`
	ReasonLabel        string     `json:"reasonLabel"`
	ReasonNote         *string    `json:"reasonNote"`
	DestinationSchool  *string    `json:"destinationSchool"`
	Guardians          []Guardian `json:"guardians"`
}

// CertStud is the student block of a certificate.
type CertStud struct {
	FullName         string  `json:"fullName"`
	RegistrationCode string  `json:"registrationCode"`
	DateOfBirth      string  `json:"dateOfBirth"`
	Gender           string  `json:"gender"`
	AdmissionDate    *string `json:"admissionDate"`
}

// Certificate returns the certificate data of a completed transfer (409 "not_completed" for any other); the read is audited.
func (s Service) Certificate(ctx context.Context, a audit.Actor, id string) (Certificate, error) {
	var c Certificate
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		x, err := s.store().GetIn(ctx, tx, id)
		if err != nil {
			return err
		}
		if x.Status != StatusCompleted || x.TCNumber == nil || x.TCIssuedOn == nil {
			return ConflictError{"not_completed", "The certificate exists only once the transfer is completed. This one is " + x.Status + "."}
		}
		c = Certificate{TCNumber: *x.TCNumber, IssuedOn: *x.TCIssuedOn, ClassName: x.ClassName, SectionName: x.SectionName, YearName: x.YearName,
			LastAttendanceDate: x.LastAttendanceDate, Reason: x.Reason, ReasonLabel: ReasonLabel(x.Reason), ReasonNote: x.ReasonNote,
			DestinationSchool: x.DestinationSchool, Guardians: []Guardian{}}
		c.Student.RegistrationCode = x.Student.RegistrationCode
		if err := tx.QueryRow(ctx, `SELECT btrim(first_name || ' ' || COALESCE(middle_name || ' ', '') || COALESCE(last_name, '')), date_of_birth::text, gender, admission_date::text
			FROM students WHERE id::text = $1`, x.Student.ID).Scan(&c.Student.FullName, &c.Student.DateOfBirth, &c.Student.Gender, &c.Student.AdmissionDate); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id = app_current_tenant()`).Scan(&c.SchoolName); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT g.name, sg.relation FROM student_guardians sg JOIN guardians g ON g.tenant_id = sg.tenant_id AND g.id = sg.guardian_id
			WHERE sg.student_id::text = $1 AND sg.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY sg.is_primary DESC, g.name`, x.Student.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g Guardian
			if err := rows.Scan(&g.Name, &g.Relation); err != nil {
				return err
			}
			c.Guardians = append(c.Guardians, g)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_transfers.certificate", ResourceType: "student_transfer", ResourceID: id})
	})
	return c, err
}
