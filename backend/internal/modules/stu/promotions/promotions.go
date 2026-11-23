// Package promotions implements STU-07 Student promotions: at the end of an academic year each class section is moved
// up, repeated or graduated, in two steps.
//
//  1. Promote: for one source class section (from year, to year, class, section) decide, per student, whether to
//     promote (default: the next class by display order), repeat the class, graduate (default for the last class) or
//     leave the student out. The decisions are a draft batch.
//  2. Enroll: choose the section (the same section by default, when it is mapped to the target class in the new year)
//     and the roll numbers (keep, renumber alphabetically, or blank), preview, and commit. Committing creates the
//     new-year enrolments and marks graduates as alumni.
//
// A batch commits all-or-nothing. It is one class section, so it is small enough to read as a whole; a half-promoted
// class is harder to explain and to undo than a batch that says what to fix first, and the preview shows every
// problem at once. Each problem can be fixed on its row or the student left out. Committing the same batch again
// changes nothing. The preview runs the commit's own code in a transaction that is rolled back.
//
// A committed batch can be undone for UndoWindow, but only while nothing has changed what it made: every enrolment is
// as the batch created it, nobody has been promoted onward from it, and graduates are still alumni. Otherwise the undo
// is refused with the reasons and nothing changes.
//
// Fees are not built. Each fee check that blocks a promotion while the previous year's dues are open will register a
// Gate on Service.Gates; every gate runs in the preview and the commit. The commit takes an optional DuesOverride for
// the user who holds the override permission (to be added with the fee module); the HTTP API does not offer it yet.
//
// The enrolments are written through the enrollments package, so each rule of the enrolments screen applies and each
// enrolment has its own audit row. Nothing about the year being closed is changed: its enrolments stay as they were.
package promotions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the batch does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

var (
	// ErrNotDraft means the batch is committed or undone, so its decisions can no longer be changed or discarded.
	ErrNotDraft = errors.New("promotions: the batch is not a draft")
	// ErrUndone means the batch was undone; a new batch is needed to promote the class section again.
	ErrUndone = errors.New("promotions: the batch was undone")
	// ErrNotCommitted means there is nothing to undo yet.
	ErrNotCommitted = errors.New("promotions: the batch is not committed")
	// ErrTooMany means the batch has no students or more than MaxRows.
	ErrTooMany = errors.New("promotions: a batch needs between 1 and MaxRows students")
)

// NotReady is returned by Commit when a row has a problem. Nothing was written. Outcome lists every row.
type NotReady struct{ Outcome Outcome }

func (e NotReady) Error() string { return "promotions: the batch has problems" }

// UndoReason is one reason an undo is refused.
type UndoReason struct {
	StudentID   string
	StudentName string
	Code        string
	Message     string
}

// UndoRefused is returned by Undo when the batch can no longer be undone safely. Nothing was changed.
type UndoRefused struct{ Reasons []UndoReason }

func (e UndoRefused) Error() string { return "promotions: the batch cannot be undone" }

// MaxRows is the most students one batch may hold. A class section is far smaller; this is a guard.
const MaxRows = 300

// UndoWindow is how long after the commit a batch can be undone.
const UndoWindow = 7 * 24 * time.Hour

// Decisions, roll modes and states.
const (
	DecisionPromote  = "promote"
	DecisionRepeat   = "repeat"
	DecisionGraduate = "graduate"
	DecisionLeaveOut = "leave_out"

	RollKeep         = "keep"
	RollAlphabetical = "alphabetical"
	RollBlank        = "blank"

	StatusDraft     = "draft"
	StatusCommitted = "committed"
	StatusUndone    = "undone"
)

// Decisions and RollModes are the allowed values.
var (
	Decisions = []string{DecisionPromote, DecisionRepeat, DecisionGraduate, DecisionLeaveOut}
	RollModes = []string{RollKeep, RollAlphabetical, RollBlank}
)

// Gate is one rule that can hold a student back from being promoted (the previous year's fees are not cleared). FEE
// adds its own to Service.Gates; none exist yet. It reads inside the preview or commit transaction and returns the
// reason for each student who is blocked, keyed by student id.
type Gate interface {
	Blocked(ctx context.Context, tx pgx.Tx, fromYearID string, studentIDs []string) (map[string]string, error)
}

// DuesOverride lets a commit go ahead although a gate blocked some students. Only a user who holds the override
// permission may pass one; the reason is kept on the batch and in the audit trail.
type DuesOverride struct{ Reason string }

// Service reads and writes promotion batches.
type Service struct {
	DB tenancy.Beginner
	// Gates are the extra rules that can block a student (previous-year dues). None until FEE exists.
	Gates []Gate
}

// Batch is a promotion batch as the API shows it.
type Batch struct {
	ID                 string
	FromYearID         string
	FromYearName       string
	ToYearID           string
	ToYearName         string
	FromClassID        string
	FromClassName      string
	FromSectionID      string
	FromSectionName    string
	RollMode           string
	Note               *string
	Status             string
	Students           int
	Promote            int
	Repeat             int
	Graduate           int
	LeaveOut           int
	CommittedAt        *time.Time
	CommittedByName    *string
	UndoUntil          *time.Time
	CanUndo            bool
	DuesOverrideReason *string
	UndoneAt           *time.Time
	UndoneByName       *string
	UndoReason         *string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Row is one student of a batch.
type Row struct {
	StudentID        string
	RegistrationCode string
	StudentName      string
	StudentStatus    string
	EnrollmentID     string
	RollNumber       *string // the roll number the student has in the year being closed
	Decision         string
	ToClassID        *string
	ToClassName      *string
	ToSectionID      *string
	ToSectionName    *string
	NewRollNumber    *string // a roll number typed for the new year
	// After a commit: what the batch made.
	NewEnrollmentID *string
	EnrolledClass   *string
	EnrolledSection *string
	EnrolledRoll    *string
}

// Detail is a batch with its students.
type Detail struct {
	Batch
	Rows []Row
}

// RowInput is the decision for one student.
type RowInput struct {
	StudentID   string
	Decision    string
	ToClassID   *string
	ToSectionID *string
	RollNumber  *string
}

// Input creates a batch.
type Input struct {
	FromYearID, ToYearID, FromClassID, FromSectionID string
	RollMode                                         string
	Note                                             *string
	Rows                                             []RowInput
}

// UpdateInput replaces the decisions of a draft.
type UpdateInput struct {
	RollMode string
	Note     *string
	Rows     []RowInput
}

// Filter narrows the list of batches.
type Filter struct{ Status, FromYearID, ToYearID, ClassID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"createdAt": "b.created_at", "status": "b.status", "committedAt": "b.committed_at"}

// DefaultSort lists the newest first.
const DefaultSort = "-createdAt"

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func trim(p *string) *string { return crud.TrimPtr(p) }

// batchColumns reads a batch with the names and counts the screens show. The alias is b.
const batchColumns = `b.id::text, b.from_year_id::text, fy.name, b.to_year_id::text, ty.name, b.from_class_id::text, fc.name, b.from_section_id::text, fs.name,
	b.roll_mode, b.note, b.status, c.total, c.promote, c.repeat, c.graduate, c.leave_out,
	b.committed_at, (SELECT u.display_name FROM users u WHERE u.id = b.committed_by), b.undo_until,
	b.status = 'committed' AND now() <= b.undo_until, b.dues_override_reason,
	b.undone_at, (SELECT u.display_name FROM users u WHERE u.id = b.undone_by), b.undo_reason, b.created_at, b.updated_at`

const batchFrom = ` FROM promotion_batches b
	JOIN academic_years fy ON fy.tenant_id = b.tenant_id AND fy.id = b.from_year_id
	JOIN academic_years ty ON ty.tenant_id = b.tenant_id AND ty.id = b.to_year_id
	JOIN classes fc ON fc.tenant_id = b.tenant_id AND fc.id = b.from_class_id
	JOIN sections fs ON fs.tenant_id = b.tenant_id AND fs.id = b.from_section_id
	LEFT JOIN LATERAL (SELECT count(*) AS total, count(*) FILTER (WHERE r.decision = 'promote') AS promote, count(*) FILTER (WHERE r.decision = 'repeat') AS repeat,
		count(*) FILTER (WHERE r.decision = 'graduate') AS graduate, count(*) FILTER (WHERE r.decision = 'leave_out') AS leave_out
		FROM promotion_batch_students r WHERE r.tenant_id = b.tenant_id AND r.batch_id = b.id) c ON true`

func scanBatch(r pgx.Row) (Batch, error) {
	var b Batch
	err := r.Scan(&b.ID, &b.FromYearID, &b.FromYearName, &b.ToYearID, &b.ToYearName, &b.FromClassID, &b.FromClassName, &b.FromSectionID, &b.FromSectionName,
		&b.RollMode, &b.Note, &b.Status, &b.Students, &b.Promote, &b.Repeat, &b.Graduate, &b.LeaveOut,
		&b.CommittedAt, &b.CommittedByName, &b.UndoUntil, &b.CanUndo, &b.DuesOverrideReason, &b.UndoneAt, &b.UndoneByName, &b.UndoReason, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func getBatch(ctx context.Context, tx pgx.Tx, id string) (Batch, error) {
	b, err := scanBatch(tx.QueryRow(ctx, `SELECT `+batchColumns+batchFrom+` WHERE b.id::text = $1 AND b.deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func getRows(ctx context.Context, tx pgx.Tx, batchID string) ([]Row, error) {
	rows, err := tx.Query(ctx, `SELECT s.id::text, s.registration_code, btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')), s.status,
			r.source_enrollment_id::text, se.roll_number, r.decision, r.to_class_id::text, tc.name, r.to_section_id::text, ts.name, r.roll_number,
			r.new_enrollment_id::text, nc.name, ns.name, ne.roll_number
		FROM promotion_batch_students r
		JOIN students s ON s.tenant_id = r.tenant_id AND s.id = r.student_id
		JOIN student_enrollments se ON se.tenant_id = r.tenant_id AND se.id = r.source_enrollment_id
		LEFT JOIN classes tc ON tc.tenant_id = r.tenant_id AND tc.id = r.to_class_id
		LEFT JOIN sections ts ON ts.tenant_id = r.tenant_id AND ts.id = r.to_section_id
		LEFT JOIN student_enrollments ne ON ne.tenant_id = r.tenant_id AND ne.id = r.new_enrollment_id
		LEFT JOIN classes nc ON nc.tenant_id = r.tenant_id AND nc.id = ne.class_id
		LEFT JOIN sections ns ON ns.tenant_id = r.tenant_id AND ns.id = ne.section_id
		WHERE r.batch_id::text = $1
		ORDER BY lower(s.first_name), lower(COALESCE(s.last_name, '')), lower(s.registration_code)`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.StudentID, &r.RegistrationCode, &r.StudentName, &r.StudentStatus, &r.EnrollmentID, &r.RollNumber, &r.Decision, &r.ToClassID, &r.ToClassName,
			&r.ToSectionID, &r.ToSectionName, &r.NewRollNumber, &r.NewEnrollmentID, &r.EnrolledClass, &r.EnrolledSection, &r.EnrolledRoll); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getDetail(ctx context.Context, tx pgx.Tx, id string) (Detail, error) {
	b, err := getBatch(ctx, tx, id)
	if err != nil {
		return Detail{}, err
	}
	rows, err := getRows(ctx, tx, id)
	return Detail{Batch: b, Rows: rows}, err
}

// ---- reads

// List returns one page of batches, newest first by default. The read is audited with the paging and filters.
func (s Service) List(ctx context.Context, actor audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Batch], error) {
	out := crud.Page[Batch]{Page: q.Page, Size: q.Size, Items: []Batch{}}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		where, args := []string{"b.deleted_at IS NULL"}, []any{}
		for _, c := range []struct{ col, v string }{{"b.status", f.Status}, {"b.from_year_id::text", f.FromYearID}, {"b.to_year_id::text", f.ToYearID}, {"b.from_class_id::text", f.ClassID}} {
			if c.v != "" {
				args = append(args, c.v)
				where = append(where, fmt.Sprintf("%s = $%d", c.col, len(args)))
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM promotion_batches b WHERE `+strings.Join(where, " AND "), args...).Scan(&out.Total); err != nil {
			return err
		}
		args = append(args, q.Size, q.Offset())
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s %s WHERE %s ORDER BY %s, b.id LIMIT $%d OFFSET $%d`, batchColumns, batchFrom, strings.Join(where, " AND "), q.OrderBy, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBatch(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, b)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.list", ResourceType: "promotion_batch",
			After: map[string]any{"page": q.Page, "size": q.Size, "status": f.Status, "fromYearId": f.FromYearID, "toYearId": f.ToYearID, "classId": f.ClassID}})
	})
	return out, err
}

// View returns a batch with its students; the read is audited.
func (s Service) View(ctx context.Context, actor audit.Actor, id string) (Detail, error) {
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		if out, err = getDetail(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.view", ResourceType: "promotion_batch", ResourceID: id})
	})
	return out, err
}

// Candidate is a student of the source class section as the first step proposes them.
type Candidate struct {
	StudentID        string
	RegistrationCode string
	StudentName      string
	StudentStatus    string
	EnrollmentID     string
	RollNumber       *string
	Decision         string
	ToClassID        *string
	ToClassName      *string
}

// Proposal is what the first step shows for one class section: the students and what is proposed for each.
type Proposal struct {
	FromYearID, FromYearName   string
	ToYearID, ToYearName       string
	ClassID, ClassName         string
	SectionID, SectionName     string
	NextClassID, NextClassName *string
	IsLastClass                bool
	ExistingBatchID            *string
	ExistingBatchStatus        *string
	Candidates                 []Candidate
}

// classRef is a live class in promotion order.
type classRef struct {
	id, name, status string
	rank             int
}

// loadClasses reads the live classes in promotion order: display order, then name, then id.
func loadClasses(ctx context.Context, tx pgx.Tx) ([]classRef, map[string]classRef, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name, status FROM classes WHERE deleted_at IS NULL ORDER BY display_order, lower(name), id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var list []classRef
	byID := map[string]classRef{}
	for rows.Next() {
		var c classRef
		if err := rows.Scan(&c.id, &c.name, &c.status); err != nil {
			return nil, nil, err
		}
		c.rank = len(list)
		list = append(list, c)
		byID[c.id] = c
	}
	return list, byID, rows.Err()
}

// nextClass is the first active class after the given one in promotion order.
func nextClass(list []classRef, from classRef) (classRef, bool) {
	for _, c := range list[from.rank+1:] {
		if c.status == "active" {
			return c, true
		}
	}
	return classRef{}, false
}

// Candidates proposes the decisions for a class section: the students enrolled in it in the year being closed, each
// with the default decision (promote to the next class, graduate from the last class, leave out a student who is not
// active). When a live batch exists for the same source and years, its id is returned so the screen opens it instead.
func (s Service) Candidates(ctx context.Context, actor audit.Actor, fromYearID, toYearID, classID, sectionID string) (Proposal, error) {
	var p Proposal
	var fe crud.FieldErrors
	for _, f := range []struct{ field, v string }{{"fromYearId", fromYearID}, {"toYearId", toYearID}, {"classId", classID}, {"sectionId", sectionID}} {
		if !uuidRE.MatchString(f.v) {
			fe.Add(f.field, "invalid", "This is not a valid id.")
		}
	}
	if len(fe) > 0 {
		return p, fe
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, _, _, err := checkSource(ctx, tx, fromYearID, toYearID, classID, sectionID, &fe); err != nil {
			return err
		}
		if len(fe) > 0 {
			return fe
		}
		p = Proposal{FromYearID: fromYearID, ToYearID: toYearID, ClassID: classID, SectionID: sectionID}
		if err := tx.QueryRow(ctx, `SELECT (SELECT name FROM academic_years WHERE id::text = $1), (SELECT name FROM academic_years WHERE id::text = $2),
			(SELECT name FROM classes WHERE id::text = $3), (SELECT name FROM sections WHERE id::text = $4)`, fromYearID, toYearID, classID, sectionID).
			Scan(&p.FromYearName, &p.ToYearName, &p.ClassName, &p.SectionName); err != nil {
			return err
		}
		list, byID, err := loadClasses(ctx, tx)
		if err != nil {
			return err
		}
		if cur, ok := byID[classID]; ok {
			if nx, ok := nextClass(list, cur); ok {
				p.NextClassID, p.NextClassName = &nx.id, &nx.name
			} else {
				p.IsLastClass = true
			}
		}
		var bid, bst string
		if err := tx.QueryRow(ctx, `SELECT id::text, status FROM promotion_batches WHERE from_year_id::text = $1 AND to_year_id::text = $2 AND from_class_id::text = $3
			AND from_section_id::text = $4 AND status <> 'undone' AND deleted_at IS NULL`, fromYearID, toYearID, classID, sectionID).Scan(&bid, &bst); err == nil {
			p.ExistingBatchID, p.ExistingBatchStatus = &bid, &bst
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT s.id::text, s.registration_code, btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')), s.status, e.id::text, e.roll_number
			FROM student_enrollments e JOIN students s ON s.tenant_id = e.tenant_id AND s.id = e.student_id AND s.deleted_at IS NULL
			WHERE e.year_id::text = $1 AND e.class_id::text = $2 AND e.section_id::text = $3 AND e.status = 'enrolled' AND e.deleted_at IS NULL
			ORDER BY lower(s.first_name), lower(COALESCE(s.last_name, '')), lower(s.registration_code)`, fromYearID, classID, sectionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		p.Candidates = []Candidate{}
		for rows.Next() {
			var c Candidate
			if err := rows.Scan(&c.StudentID, &c.RegistrationCode, &c.StudentName, &c.StudentStatus, &c.EnrollmentID, &c.RollNumber); err != nil {
				return err
			}
			switch {
			case c.StudentStatus != "active":
				c.Decision = DecisionLeaveOut
			case p.IsLastClass:
				c.Decision = DecisionGraduate
			case p.NextClassID != nil:
				c.Decision, c.ToClassID, c.ToClassName = DecisionPromote, p.NextClassID, p.NextClassName
			default:
				c.Decision = DecisionRepeat
			}
			p.Candidates = append(p.Candidates, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.candidates", ResourceType: "promotion_batch",
			After: map[string]any{"fromYearId": fromYearID, "toYearId": toYearID, "classId": classID, "sectionId": sectionID, "students": len(p.Candidates)}})
	})
	if err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// checkSource checks the years, class and section of a batch exist and that the new year comes after the old one. It
// adds problems to fe and returns the new year's first day, which the new enrolments carry as their date.
func checkSource(ctx context.Context, tx pgx.Tx, fromYearID, toYearID, classID, sectionID string, fe *crud.FieldErrors) (fromStart, toStart string, ok bool, err error) {
	if fromYearID == toYearID {
		fe.Add("toYearId", "same_year", "Choose a different academic year to promote into.")
		return "", "", false, nil
	}
	var fromFound, toFound bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT start_date::text FROM academic_years WHERE id::text = $1 AND deleted_at IS NULL), ''),
			COALESCE((SELECT start_date::text FROM academic_years WHERE id::text = $2 AND deleted_at IS NULL), '')`, fromYearID, toYearID).Scan(&fromStart, &toStart)
	if err != nil {
		return "", "", false, err
	}
	fromFound, toFound = fromStart != "", toStart != ""
	if !fromFound {
		fe.Add("fromYearId", "not_found", "This academic year does not exist.")
	}
	if !toFound {
		fe.Add("toYearId", "not_found", "This academic year does not exist.")
	}
	if fromFound && toFound && toStart <= fromStart {
		fe.Add("toYearId", "not_after", "The new academic year must start after the year being closed.")
	}
	for _, r := range []struct{ field, table, id string }{{"classId", "classes", classID}, {"sectionId", "sections", sectionID}} {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM `+r.table+` WHERE id::text = $1 AND deleted_at IS NULL`, r.id).Scan(&n); err != nil {
			return "", "", false, err
		}
		if n == 0 {
			fe.Add(r.field, "not_found", "This does not exist.")
		}
	}
	return fromStart, toStart, len(*fe) == 0, nil
}

// ---- writes: the draft

// normalise trims and validates the parts of a batch that need no other record; rows are checked against the
// enrolments in prepareRows.
func normaliseRows(rolls string, rows []RowInput) (string, []RowInput, crud.FieldErrors) {
	var fe crud.FieldErrors
	rolls = strings.ToLower(strings.TrimSpace(rolls))
	if rolls == "" {
		rolls = RollAlphabetical
	}
	fe.Choice("rollMode", rolls, RollModes...)
	out := make([]RowInput, len(rows))
	seen := map[string]bool{}
	for i, r := range rows {
		r.StudentID = strings.TrimSpace(r.StudentID)
		r.Decision = strings.ToLower(strings.TrimSpace(r.Decision))
		r.ToClassID, r.ToSectionID, r.RollNumber = trim(r.ToClassID), trim(r.ToSectionID), trim(r.RollNumber)
		field := func(f string) string { return fmt.Sprintf("rows[%d].%s", i, f) }
		switch {
		case !uuidRE.MatchString(r.StudentID):
			fe.Add(field("studentId"), "invalid", "This is not a valid student id.")
		case seen[r.StudentID]:
			fe.Add(field("studentId"), "duplicate", "This student is listed twice.")
		}
		seen[r.StudentID] = true
		fe.Choice(field("decision"), r.Decision, Decisions...)
		for f, v := range map[string]*string{"toClassId": r.ToClassID, "toSectionId": r.ToSectionID} {
			if v != nil && !uuidRE.MatchString(*v) {
				fe.Add(field(f), "invalid", "This is not a valid id.")
			}
		}
		if r.RollNumber != nil && len(*r.RollNumber) > 20 {
			fe.Add(field("rollNumber"), "too_long", "A roll number has at most 20 characters.")
		}
		// graduating and leaving out take no class, section or roll number
		if r.Decision == DecisionGraduate || r.Decision == DecisionLeaveOut {
			r.ToClassID, r.ToSectionID, r.RollNumber = nil, nil, nil
		}
		out[i] = r
	}
	return rolls, out, fe
}

// prepareRows checks the rows against the source class section and fills in what the server decides: a repeating
// student's class is the source class, a promoted student without a class goes to the next class. It returns the
// enrolment of each student in the source class section.
func prepareRows(ctx context.Context, tx pgx.Tx, in Input, rows []RowInput) ([]RowInput, map[string]string, error) {
	var fe crud.FieldErrors
	enrol := map[string]string{}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.StudentID
	}
	er, err := tx.Query(ctx, `SELECT e.student_id::text, e.id::text FROM student_enrollments e
		WHERE e.year_id::text = $1 AND e.class_id::text = $2 AND e.section_id::text = $3 AND e.status = 'enrolled' AND e.deleted_at IS NULL AND e.student_id::text = ANY($4)`,
		in.FromYearID, in.FromClassID, in.FromSectionID, ids)
	if err != nil {
		return nil, nil, err
	}
	for er.Next() {
		var sid, eid string
		if err := er.Scan(&sid, &eid); err != nil {
			er.Close()
			return nil, nil, err
		}
		enrol[sid] = eid
	}
	er.Close()
	if err := er.Err(); err != nil {
		return nil, nil, err
	}
	list, byID, err := loadClasses(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	src, srcOK := byID[in.FromClassID]
	next, hasNext := classRef{}, false
	if srcOK {
		next, hasNext = nextClass(list, src)
	}
	out := make([]RowInput, len(rows))
	for i, r := range rows {
		field := func(f string) string { return fmt.Sprintf("rows[%d].%s", i, f) }
		if _, ok := enrol[r.StudentID]; !ok {
			fe.Add(field("studentId"), "not_enrolled", "This student is not enrolled in this class section for the year being closed.")
		}
		switch r.Decision {
		case DecisionRepeat:
			cls := in.FromClassID
			r.ToClassID = &cls
		case DecisionPromote:
			if r.ToClassID == nil && hasNext {
				id := next.id
				r.ToClassID = &id
			}
			if r.ToClassID == nil {
				fe.Add(field("toClassId"), "required", "Choose the class to promote this student to.")
			}
		}
		if r.ToClassID != nil {
			if _, ok := byID[*r.ToClassID]; !ok {
				fe.Add(field("toClassId"), "not_found", "This class does not exist.")
			}
		}
		out[i] = r
	}
	return out, enrol, fe.OrNil()
}

func mapWriteError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "promotion_batches_source_uq" {
		var fe crud.FieldErrors
		fe.Add("fromSectionId", "duplicate", "A promotion for this class section and these academic years already exists. Open it instead.")
		return fe
	}
	return err
}

func insertRows(ctx context.Context, tx pgx.Tx, batchID string, rows []RowInput, enrol map[string]string) error {
	for _, r := range rows {
		if _, err := tx.Exec(ctx, `INSERT INTO promotion_batch_students (tenant_id, batch_id, student_id, source_enrollment_id, decision, to_class_id, to_section_id, roll_number)
			VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, $7)`, batchID, r.StudentID, enrol[r.StudentID], r.Decision, r.ToClassID, r.ToSectionID, r.RollNumber); err != nil {
			return err
		}
	}
	return nil
}

func counts(rows []RowInput) map[string]int {
	m := map[string]int{"students": len(rows)}
	for _, r := range rows {
		m[r.Decision]++
	}
	return m
}

// Create saves a new draft batch for one source class section. The first step's decisions are stored as given: the
// section and roll numbers can still be empty, they are checked by Preview and Commit. At most one live batch exists
// for a source class section and pair of years; a second answers a field error on fromSectionId.
func (s Service) Create(ctx context.Context, actor audit.Actor, in Input) (Detail, error) {
	var fe crud.FieldErrors
	in.FromYearID, in.ToYearID, in.FromClassID, in.FromSectionID = strings.TrimSpace(in.FromYearID), strings.TrimSpace(in.ToYearID), strings.TrimSpace(in.FromClassID), strings.TrimSpace(in.FromSectionID)
	for _, f := range []struct{ field, v string }{{"fromYearId", in.FromYearID}, {"toYearId", in.ToYearID}, {"fromClassId", in.FromClassID}, {"fromSectionId", in.FromSectionID}} {
		if !uuidRE.MatchString(f.v) {
			fe.Add(f.field, "invalid", "This is not a valid id.")
		}
	}
	note := trim(in.Note)
	if note != nil && len(*note) > 500 {
		fe.Add("note", "too_long", "A note has at most 500 characters.")
	}
	if len(in.Rows) == 0 || len(in.Rows) > MaxRows {
		return Detail{}, ErrTooMany
	}
	mode, rows, rfe := normaliseRows(in.RollMode, in.Rows)
	fe = append(fe, rfe...)
	if len(fe) > 0 {
		return Detail{}, fe
	}
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var sfe crud.FieldErrors
		if _, _, ok, err := checkSource(ctx, tx, in.FromYearID, in.ToYearID, in.FromClassID, in.FromSectionID, &sfe); err != nil {
			return err
		} else if !ok {
			return sfe
		}
		prepared, enrol, err := prepareRows(ctx, tx, in, rows)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO promotion_batches (tenant_id, from_year_id, to_year_id, from_class_id, from_section_id, roll_mode, note, created_by, updated_by)
			VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, NULLIF($7, '')::uuid, NULLIF($7, '')::uuid) RETURNING id::text`,
			in.FromYearID, in.ToYearID, in.FromClassID, in.FromSectionID, mode, note, actor.UserID).Scan(&id); err != nil {
			return mapWriteError(err)
		}
		if err := insertRows(ctx, tx, id, prepared, enrol); err != nil {
			return err
		}
		if out, err = getDetail(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.create", ResourceType: "promotion_batch", ResourceID: id,
			After: map[string]any{"fromYearId": in.FromYearID, "toYearId": in.ToYearID, "classId": in.FromClassID, "sectionId": in.FromSectionID, "rollMode": mode, "counts": counts(prepared)}})
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// lockBatch reads a batch's own columns under a row lock, which serialises a commit, an undo and an edit of the same
// batch.
type batchLock struct {
	id, fromYear, toYear, fromClass, fromSection, rollMode, status string
	expired                                                        bool
}

func lockBatch(ctx context.Context, tx pgx.Tx, id string) (batchLock, error) {
	var b batchLock
	err := tx.QueryRow(ctx, `SELECT id::text, from_year_id::text, to_year_id::text, from_class_id::text, from_section_id::text, roll_mode, status,
			COALESCE(now() > undo_until, false)
		FROM promotion_batches WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, id).
		Scan(&b.id, &b.fromYear, &b.toYear, &b.fromClass, &b.fromSection, &b.rollMode, &b.status, &b.expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// Update replaces the decisions, the roll mode and the note of a draft. The source class section and the years of a
// batch do not change; start another batch for those.
func (s Service) Update(ctx context.Context, actor audit.Actor, id string, in UpdateInput) (Detail, error) {
	note := trim(in.Note)
	var fe crud.FieldErrors
	if note != nil && len(*note) > 500 {
		fe.Add("note", "too_long", "A note has at most 500 characters.")
	}
	if len(in.Rows) == 0 || len(in.Rows) > MaxRows {
		return Detail{}, ErrTooMany
	}
	mode, rows, rfe := normaliseRows(in.RollMode, in.Rows)
	fe = append(fe, rfe...)
	if len(fe) > 0 {
		return Detail{}, fe
	}
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := lockBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		if b.status != StatusDraft {
			return ErrNotDraft
		}
		before, err := getBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		prepared, enrol, err := prepareRows(ctx, tx, Input{FromYearID: b.fromYear, ToYearID: b.toYear, FromClassID: b.fromClass, FromSectionID: b.fromSection}, rows)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM promotion_batch_students WHERE batch_id::text = $1`, id); err != nil {
			return err
		}
		if err := insertRows(ctx, tx, id, prepared, enrol); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE promotion_batches SET roll_mode = $2, note = $3, updated_by = NULLIF($4, '')::uuid WHERE id::text = $1`, id, mode, note, actor.UserID); err != nil {
			return err
		}
		if out, err = getDetail(ctx, tx, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.update", ResourceType: "promotion_batch", ResourceID: id,
			Before: map[string]any{"rollMode": before.RollMode, "students": before.Students, "promote": before.Promote, "repeat": before.Repeat, "graduate": before.Graduate, "leaveOut": before.LeaveOut},
			After:  map[string]any{"rollMode": mode, "counts": counts(prepared)}})
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// Discard removes a draft. A committed batch is never discarded (undo it instead); an undone one is history.
func (s Service) Discard(ctx context.Context, actor audit.Actor, id string) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := lockBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		if b.status != StatusDraft {
			return ErrNotDraft
		}
		if _, err := tx.Exec(ctx, `UPDATE promotion_batches SET deleted_at = now(), deleted_by = NULLIF($2, '')::uuid, updated_by = NULLIF($2, '')::uuid WHERE id::text = $1`, id, actor.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.discard", ResourceType: "promotion_batch", ResourceID: id,
			Before: map[string]any{"status": StatusDraft}})
	})
}

// recordSeparately writes an audit row in its own transaction, for the steps whose own transaction is rolled back (a
// preview, a commit or an undo that was refused) but that the trail must still show.
func (s Service) recordSeparately(ctx context.Context, actor audit.Actor, e audit.Entry) {
	_ = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error { return audit.Record(ctx, tx, actor, e) })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s Service) enrolmentStore() resource.Store[enrollments.Enrollment, enrollments.Input] {
	return enrollments.Service{DB: s.DB}.Store()
}
