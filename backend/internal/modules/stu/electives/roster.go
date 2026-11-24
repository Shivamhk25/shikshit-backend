package electives

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Ref is an id with a name.
type Ref struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Person is a user shown by name.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HistoryItem is one change to a choice.
type HistoryItem struct {
	ID                string       `json:"id"`
	Action            string       `json:"action"`
	SubjectsBefore    []SubjectRef `json:"subjectsBefore"`
	SubjectsAfter     []SubjectRef `json:"subjectsAfter"`
	StatusBefore      *string      `json:"statusBefore"`
	StatusAfter       *string      `json:"statusAfter"`
	Source            string       `json:"source"`
	ChangeID          *string      `json:"changeId"`
	ApprovalRequestID *string      `json:"approvalRequestId"`
	ChangedAt         time.Time    `json:"changedAt"`
	ChangedBy         *Person      `json:"changedBy"`
}

const subjectsOf = `(SELECT COALESCE(json_agg(json_build_object('id', s.id, 'code', s.code, 'name', s.name) ORDER BY s.name, s.id), '[]'::json) FROM subjects s WHERE s.id = ANY(%s))`

// History returns the changes made to a choice, newest first (at most 200).
func (s Service) History(ctx context.Context, id string) ([]HistoryItem, error) {
	out := []HistoryItem{}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := LockChoice(ctx, tx, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT h.id::text, h.action, `+fmt.Sprintf(subjectsOf, "h.subjects_before")+`, `+fmt.Sprintf(subjectsOf, "h.subjects_after")+`,
				h.status_before, h.status_after, h.source, h.change_id::text, h.approval_request_id::text, h.changed_at, h.changed_by::text, u.display_name
			FROM student_elective_choice_history h LEFT JOIN users u ON u.tenant_id = h.tenant_id AND u.id = h.changed_by
			WHERE h.choice_id::text = $1 ORDER BY h.changed_at DESC, h.id DESC LIMIT 200`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HistoryItem
			var before, after []byte
			var by, byName *string
			if err := rows.Scan(&h.ID, &h.Action, &before, &after, &h.StatusBefore, &h.StatusAfter, &h.Source, &h.ChangeID, &h.ApprovalRequestID, &h.ChangedAt, &by, &byName); err != nil {
				return err
			}
			if err := json.Unmarshal(before, &h.SubjectsBefore); err != nil {
				return err
			}
			if err := json.Unmarshal(after, &h.SubjectsAfter); err != nil {
				return err
			}
			if by != nil {
				name := ""
				if byName != nil {
					name = *byName
				}
				h.ChangedBy = &Person{ID: *by, Name: name}
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// RosterStudent is one student of a class section with their pick (if any) for the group.
type RosterStudent struct {
	Student      StudentRef   `json:"student"`
	RollNumber   *string      `json:"rollNumber"`
	ChoiceID     *string      `json:"choiceId"`
	Status       *string      `json:"status"`
	Subjects     []SubjectRef `json:"subjects"`
	WithinLimits bool         `json:"withinLimits"`
	Warnings     []Warning    `json:"warnings"`
}

// Roster is the students of one class section in one year with their picks from one group.
type Roster struct {
	Year     Ref             `json:"year"`
	Class    Ref             `json:"class"`
	Section  Ref             `json:"section"`
	Group    GroupRef        `json:"group"`
	Offered  []SubjectRef    `json:"offered"`
	Students []RosterStudent `json:"students"`
}

// sectionOf checks the class section exists (and is one of the caller's) and returns the names; ErrNotFound otherwise.
func sectionOf(ctx context.Context, tx pgx.Tx, yearID, classID, sectionID string) (Placement, string, error) {
	pl := Placement{YearID: yearID, ClassID: classID, SectionID: sectionID}
	var yearName string
	err := tx.QueryRow(ctx, `SELECT y.name, c.name, sc.name FROM class_section_mappings m
		JOIN academic_years y ON y.tenant_id = m.tenant_id AND y.id = m.year_id
		JOIN classes c ON c.tenant_id = m.tenant_id AND c.id = m.class_id
		JOIN sections sc ON sc.tenant_id = m.tenant_id AND sc.id = m.section_id
		WHERE m.year_id::text = $1 AND m.class_id::text = $2 AND m.section_id::text = $3 AND m.deleted_at IS NULL`, yearID, classID, sectionID).Scan(&yearName, &pl.ClassName, &pl.SectionName)
	if errors.Is(err, pgx.ErrNoRows) {
		return pl, "", ErrNotFound
	}
	if err != nil {
		return pl, "", err
	}
	ok, err := sectionInScope(ctx, tx, yearID, classID, sectionID)
	if err != nil {
		return pl, "", err
	}
	if !ok {
		return pl, "", ErrNotFound
	}
	return pl, yearName, nil
}

// Roster lists the students enrolled in a class section for a year, with their pick from a group; students who have not
// chosen are included. The class section must exist and be one of the caller's (404 otherwise); the group must offer
// subjects to it (400 groupId not_offered).
func (s Service) Roster(ctx context.Context, yearID, classID, sectionID, groupID string) (Roster, error) {
	var out Roster
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		pl, yearName, err := sectionOf(ctx, tx, yearID, classID, sectionID)
		if err != nil {
			return err
		}
		off, err := LoadOffering(ctx, tx, pl, groupID)
		if err != nil {
			return err
		}
		out = Roster{Year: Ref{yearID, yearName}, Class: Ref{classID, pl.ClassName}, Section: Ref{sectionID, pl.SectionName}, Group: off.Group, Offered: off.Subjects, Students: []RosterStudent{}}
		rows, err := tx.Query(ctx, `SELECT st.id::text, btrim(st.first_name || ' ' || COALESCE(st.middle_name || ' ', '') || COALESCE(st.last_name, '')), st.registration_code, e.roll_number,
				c.id::text, c.status,
				(SELECT COALESCE(json_agg(json_build_object('id', s.id, 'code', s.code, 'name', s.name) ORDER BY s.name, s.id), '[]'::json)
				   FROM student_elective_choice_subjects cs JOIN subjects s ON s.tenant_id = cs.tenant_id AND s.id = cs.subject_id WHERE cs.choice_id = c.id)
			FROM student_enrollments e
			JOIN students st ON st.tenant_id = e.tenant_id AND st.id = e.student_id AND st.deleted_at IS NULL
			LEFT JOIN student_elective_choices c ON c.tenant_id = e.tenant_id AND c.student_id = e.student_id AND c.year_id = e.year_id AND c.group_id::text = $4 AND c.deleted_at IS NULL
			WHERE e.year_id::text = $1 AND e.class_id::text = $2 AND e.section_id::text = $3 AND e.status = 'enrolled' AND e.deleted_at IS NULL
			ORDER BY NULLIF(regexp_replace(e.roll_number, '\D', '', 'g'), '')::numeric NULLS LAST, e.roll_number NULLS LAST, 2, st.id`, yearID, classID, sectionID, groupID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RosterStudent
			var subjects []byte
			if err := rows.Scan(&r.Student.ID, &r.Student.Name, &r.Student.RegistrationCode, &r.RollNumber, &r.ChoiceID, &r.Status, &subjects); err != nil {
				return err
			}
			r.Subjects = []SubjectRef{}
			if r.ChoiceID != nil {
				if err := json.Unmarshal(subjects, &r.Subjects); err != nil {
					return err
				}
			}
			r.Warnings = Warnings(off.Group, len(r.Subjects))
			r.WithinLimits = len(r.Warnings) == 0
			out.Students = append(out.Students, r)
		}
		return rows.Err()
	})
	return out, err
}

// BulkRow is one student's pick in a bulk save.
type BulkRow struct {
	StudentID  string
	SubjectIDs []string
	Status     string // keeps the current status of an existing choice, "chosen" for a new one, when empty
}

// BulkInput saves the picks of many students of one class section for one group, in one transaction.
type BulkInput struct {
	YearID, ClassID, SectionID, GroupID string
	Rows                                []BulkRow
	// AllowCreate and AllowDelete say whether the caller may add and remove choices (the API sets them from their
	// permissions; changing an existing choice needs only the edit permission the operation already requires).
	AllowCreate, AllowDelete bool
}

// BulkItem is what happened to one row.
type BulkItem struct {
	StudentID string  `json:"studentId"`
	ChoiceID  *string `json:"choiceId"`
	Result    string  `json:"result"` // created, updated, removed or unchanged
}

// BulkResult counts and lists what a bulk save did.
type BulkResult struct {
	Created, Updated, Removed, Unchanged int
	Items                                []BulkItem
}

// MaxBulkRows is the most rows one bulk save takes.
const MaxBulkRows = 500

type existing struct {
	id, status string
}

// Bulk saves the picks of a class section. A row with subjects creates the student's choice or replaces it; a row with
// no subjects removes an existing choice, or does nothing when there is none. It is all or nothing: every row is checked
// first, and when any row is wrong nothing is written and the errors come back keyed by student, as field
// "rows[<studentId>].<field>" with the same codes the single operations use (plus not_in_section, not_allowed and
// duplicate_student). A student outside the class section, or one the caller may not see, is not_in_section.
func (s Service) Bulk(ctx context.Context, a audit.Actor, in BulkInput) (BulkResult, error) {
	var fe crud.FieldErrors
	in.YearID = uuidField(&fe, "yearId", "the academic year", in.YearID)
	in.ClassID = uuidField(&fe, "classId", "the class", in.ClassID)
	in.SectionID = uuidField(&fe, "sectionId", "the section", in.SectionID)
	in.GroupID = uuidField(&fe, "groupId", "the elective group", in.GroupID)
	if len(in.Rows) == 0 {
		fe.Add("rows", "required", "Add at least one row.")
	}
	if len(in.Rows) > MaxBulkRows {
		fe.Add("rows", "too_many", fmt.Sprintf("Save at most %d students at a time.", MaxBulkRows))
	}
	if err := fe.OrNil(); err != nil {
		return BulkResult{}, err
	}
	var res BulkResult
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		pl, _, err := sectionOf(ctx, tx, in.YearID, in.ClassID, in.SectionID)
		if err != nil {
			return err
		}
		off, err := LoadOffering(ctx, tx, pl, in.GroupID)
		if err != nil {
			return err
		}
		enrolled := map[string]bool{}
		rows, err := tx.Query(ctx, `SELECT student_id::text FROM student_enrollments WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND status = 'enrolled' AND deleted_at IS NULL`,
			in.YearID, in.ClassID, in.SectionID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			enrolled[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		have := map[string]existing{}
		crows, err := tx.Query(ctx, `SELECT student_id::text, id::text, status FROM student_elective_choices WHERE year_id::text = $1 AND group_id::text = $2 AND deleted_at IS NULL
			AND student_id::text = ANY($3::text[]) FOR UPDATE`, in.YearID, in.GroupID, studentIDs(in.Rows))
		if err != nil {
			return err
		}
		for crows.Next() {
			var sid string
			var e existing
			if err := crows.Scan(&sid, &e.id, &e.status); err != nil {
				crows.Close()
				return err
			}
			have[sid] = e
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return err
		}

		type plan struct {
			row    BulkRow
			ids    []string
			status string
			cur    *existing
		}
		var plans []plan
		var errs crud.FieldErrors
		seen := map[string]bool{}
		for _, r := range in.Rows {
			sid := lower(r.StudentID)
			key := func(field string) string { return "rows[" + sid + "]." + field }
			if !refs.IsUUID(sid) {
				errs.Add(key("studentId"), "invalid", "Choose a student from the list.")
				continue
			}
			r.StudentID = sid
			switch {
			case seen[sid]:
				errs.Add(key("studentId"), "duplicate_student", "This student appears twice.")
				continue
			case !enrolled[sid]:
				errs.Add(key("studentId"), "not_in_section", "This student is not enrolled in this class section for this year.")
				continue
			}
			seen[sid] = true
			var one crud.FieldErrors
			status := normaliseStatus(&one, r.Status, "")
			var cur *existing
			if e, ok := have[sid]; ok {
				cur = &e
			}
			if len(r.SubjectIDs) == 0 {
				switch {
				case cur == nil:
					plans = append(plans, plan{row: r})
				case !in.AllowDelete:
					errs.Add(key("subjectIds"), "not_allowed", "You may not remove a choice.")
				default:
					plans = append(plans, plan{row: r, cur: cur})
				}
				for _, f := range one {
					errs.Add(key(f.Field), f.Code, f.Message)
				}
				continue
			}
			ids, _, ferr := off.Check("subjectIds", r.SubjectIDs)
			one = append(one, ferr...)
			if cur == nil && !in.AllowCreate {
				one.Add("subjectIds", "not_allowed", "You may not add a choice.")
			}
			if len(one) > 0 {
				for _, f := range one {
					errs.Add(key(f.Field), f.Code, f.Message)
				}
				continue
			}
			if status == "" {
				status = StatusChosen
				if cur != nil {
					status = cur.status
				}
			}
			plans = append(plans, plan{row: r, ids: ids, status: status, cur: cur})
		}
		if err := errs.OrNil(); err != nil {
			return err
		}

		res = BulkResult{Items: []BulkItem{}}
		for _, p := range plans {
			sid := p.row.StudentID
			switch {
			case len(p.ids) == 0 && p.cur == nil:
				res.Unchanged++
				res.Items = append(res.Items, BulkItem{StudentID: sid, Result: "unchanged"})
			case len(p.ids) == 0:
				before, err := s.store().GetIn(ctx, tx, p.cur.id)
				if err != nil {
					return err
				}
				if err := removeChoice(ctx, tx, a, before); err != nil {
					return bulkConflict(err, sid)
				}
				if err := audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.delete", ResourceType: "student_elective", ResourceID: before.ID, Before: before}); err != nil {
					return err
				}
				res.Removed++
				res.Items = append(res.Items, BulkItem{StudentID: sid, Result: "removed"})
			case p.cur == nil:
				id, err := insertChoice(ctx, tx, a, sid, in.YearID, in.GroupID, p.ids, p.status)
				if err != nil {
					return bulkConflict(err, sid)
				}
				after, err := s.store().GetIn(ctx, tx, id)
				if err != nil {
					return err
				}
				if err := audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.create", ResourceType: "student_elective", ResourceID: id, After: after}); err != nil {
					return err
				}
				res.Created++
				res.Items = append(res.Items, BulkItem{StudentID: sid, ChoiceID: &id, Result: "created"})
			default:
				before, err := s.store().GetIn(ctx, tx, p.cur.id)
				if err != nil {
					return err
				}
				changed, err := changeChoice(ctx, tx, a, before, p.ids, p.status)
				if err != nil {
					return err
				}
				id := p.cur.id
				if !changed {
					res.Unchanged++
					res.Items = append(res.Items, BulkItem{StudentID: sid, ChoiceID: &id, Result: "unchanged"})
					continue
				}
				after, err := s.store().GetIn(ctx, tx, id)
				if err != nil {
					return err
				}
				if err := audit.Record(ctx, tx, a, audit.Entry{Action: "student_electives.update", ResourceType: "student_elective", ResourceID: id, Before: before, After: after}); err != nil {
					return err
				}
				res.Updated++
				res.Items = append(res.Items, BulkItem{StudentID: sid, ChoiceID: &id, Result: "updated"})
			}
		}
		return nil
	})
	return res, err
}

// bulkConflict names the student in a conflict raised while saving one row of a bulk save.
func bulkConflict(err error, studentID string) error {
	var ce ConflictError
	if errors.As(err, &ce) {
		return crud.FieldErrors{{Field: "rows[" + studentID + "].subjectIds", Code: ce.Code, Message: ce.Message}}
	}
	return err
}

func studentIDs(rows []BulkRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, lower(r.StudentID))
	}
	return out
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
