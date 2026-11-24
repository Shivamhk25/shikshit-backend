package rollover

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// flush sends the queued inserts and fails on the first error, so the step's transaction rolls back as a whole.
func flush(ctx context.Context, tx pgx.Tx, b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

// queryAll runs a query and hands every row to scan, closing the rows before it returns so the next query can run.
func queryAll(ctx context.Context, tx pgx.Tx, sql string, args []any, scan func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

type span struct{ start, end time.Time }

func (a span) overlaps(b span) bool { return !a.start.After(b.end) && !b.start.After(a.end) }

func moved(p pair, start, end time.Time) (time.Time, time.Time, string) {
	ns, ne := p.Shift.Apply(start), p.Shift.Apply(end)
	return ns, ne, fmt.Sprintf("%s to %s becomes %s to %s", iso(start), iso(end), iso(ns), iso(ne))
}

func outside(p pair, ns, ne time.Time) bool { return ns.Before(p.B.Start) || ne.After(p.B.End) }

func outsideMessage(p pair) string {
	return fmt.Sprintf("The moved dates fall outside %s (%s to %s).", p.B.Name, iso(p.B.Start), iso(p.B.End))
}

// ---------------------------------------------------------------- terms

func copyTerms(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	type term struct {
		code, title string
		start, end  time.Time
		color       *string
	}
	var src []term
	if err := queryAll(ctx, tx, `SELECT code, title, start_date, end_date, color FROM academic_terms WHERE year_id::text = $1 AND deleted_at IS NULL ORDER BY start_date, lower(code)`,
		[]any{p.A.ID}, func(r pgx.Rows) error {
			var t term
			err := r.Scan(&t.code, &t.title, &t.start, &t.end, &t.color)
			src = append(src, t)
			return err
		}); err != nil {
		return err
	}
	codes, occupied := map[string]bool{}, []span{}
	if err := queryAll(ctx, tx, `SELECT lower(code), start_date, end_date FROM academic_terms WHERE year_id::text = $1 AND deleted_at IS NULL`, []any{p.B.ID}, func(r pgx.Rows) error {
		var c string
		var s span
		err := r.Scan(&c, &s.start, &s.end)
		codes[c] = true
		occupied = append(occupied, s)
		return err
	}); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, t := range src {
		ns, ne, detail := moved(p, t.start, t.end)
		it := Item{Label: t.code + " · " + t.title, Detail: detail}
		switch {
		case codes[strings.ToLower(t.code)]:
			it.Status, it.Message = ItemExists, "A term with this code already exists in "+p.B.Name+"."
		case outside(p, ns, ne):
			it.Status, it.Reason, it.Message = ItemConflict, "outside_year", outsideMessage(p)
		case overlapsAny(occupied, span{ns, ne}):
			it.Status, it.Reason, it.Message = ItemConflict, "overlaps_term", "The moved dates overlap another term of "+p.B.Name+"."
		default:
			it.Status = ItemCreate
			occupied, codes[strings.ToLower(t.code)] = append(occupied, span{ns, ne}), true
			batch.Queue(`INSERT INTO academic_terms (tenant_id, year_id, code, title, start_date, end_date, color, created_by, updated_by)
				VALUES (app_current_tenant(), $1::uuid, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid, NULLIF($7, '')::uuid)`, p.B.ID, t.code, t.title, ns, ne, t.color, actor)
		}
		res.add(it)
	}
	return flush(ctx, tx, batch)
}

func overlapsAny(list []span, s span) bool {
	for _, o := range list {
		if o.overlaps(s) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- class-section mappings

func copyClassSections(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	type mapping struct {
		classID, className, sectionID, sectionName string
		classGone, sectionGone, roomLive           bool
		capacity                                   *int
		roomID, remarks                            *string
	}
	var src []mapping
	if err := queryAll(ctx, tx, `SELECT m.class_id::text, c.name, c.deleted_at IS NOT NULL, m.section_id::text, s.name, s.deleted_at IS NOT NULL,
			m.capacity, m.room_id::text, (r.id IS NOT NULL AND r.deleted_at IS NULL), m.remarks
		FROM class_section_mappings m
		JOIN classes c ON c.tenant_id = m.tenant_id AND c.id = m.class_id
		JOIN sections s ON s.tenant_id = m.tenant_id AND s.id = m.section_id
		LEFT JOIN rooms r ON r.tenant_id = m.tenant_id AND r.id = m.room_id
		WHERE m.year_id::text = $1 AND m.deleted_at IS NULL
		ORDER BY c.display_order, lower(c.name), lower(s.name), m.id`, []any{p.A.ID}, func(r pgx.Rows) error {
		var m mapping
		err := r.Scan(&m.classID, &m.className, &m.classGone, &m.sectionID, &m.sectionName, &m.sectionGone, &m.capacity, &m.roomID, &m.roomLive, &m.remarks)
		src = append(src, m)
		return err
	}); err != nil {
		return err
	}
	have := map[string]bool{}
	if err := queryAll(ctx, tx, `SELECT class_id::text || ':' || section_id::text FROM class_section_mappings WHERE year_id::text = $1 AND deleted_at IS NULL`, []any{p.B.ID}, func(r pgx.Rows) error {
		var k string
		err := r.Scan(&k)
		have[k] = true
		return err
	}); err != nil {
		return err
	}
	batch, noRoom := &pgx.Batch{}, 0
	for _, m := range src {
		key := m.classID + ":" + m.sectionID
		it := Item{Label: m.className + " / " + m.sectionName}
		switch {
		case have[key]:
			it.Status, it.Message = ItemExists, "This section is already mapped to this class in "+p.B.Name+"."
		case m.classGone:
			it.Status, it.Reason, it.Message = ItemConflict, "class_deleted", "The class no longer exists."
		case m.sectionGone:
			it.Status, it.Reason, it.Message = ItemConflict, "section_deleted", "The section no longer exists."
		default:
			it.Status = ItemCreate
			room := m.roomID
			if m.roomID != nil && !m.roomLive {
				room, it.Detail = nil, "The room no longer exists, so it is not set."
				noRoom++
			}
			have[key] = true
			batch.Queue(`INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id, capacity, room_id, remarks, created_by, updated_by)
				VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6, NULLIF($7, '')::uuid, NULLIF($7, '')::uuid)`,
				p.B.ID, m.classID, m.sectionID, m.capacity, room, m.remarks, actor)
		}
		res.add(it)
	}
	if noRoom > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d %s copied without a room because the room no longer exists.", noRoom, plural(noRoom, "mapping was", "mappings were")))
	}
	return flush(ctx, tx, batch)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---------------------------------------------------------------- subject-class mappings

func copySubjectClasses(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	type mapping struct {
		classID, className, subjectID, subjectName string
		sectionID, sectionName                     *string
		classGone, subjectGone, sectionGone        bool
		periods                                    *int
		elective, examined, assessed               bool
		gradeOnly, practical, onReport             bool
	}
	var src []mapping
	if err := queryAll(ctx, tx, `SELECT m.class_id::text, c.name, c.deleted_at IS NOT NULL, m.subject_id::text, s.name, s.deleted_at IS NOT NULL,
			m.section_id::text, sec.name, COALESCE(sec.deleted_at IS NOT NULL, false),
			m.periods_per_week, m.is_elective, m.is_examined, m.is_assessed, m.is_grade_only, m.has_practical, m.on_report_card
		FROM subject_class_mappings m
		JOIN classes c ON c.tenant_id = m.tenant_id AND c.id = m.class_id
		JOIN subjects s ON s.tenant_id = m.tenant_id AND s.id = m.subject_id
		LEFT JOIN sections sec ON sec.tenant_id = m.tenant_id AND sec.id = m.section_id
		WHERE m.year_id::text = $1 AND m.deleted_at IS NULL
		ORDER BY c.display_order, lower(c.name), lower(s.name), lower(sec.name) NULLS FIRST, m.id`, []any{p.A.ID}, func(r pgx.Rows) error {
		var m mapping
		err := r.Scan(&m.classID, &m.className, &m.classGone, &m.subjectID, &m.subjectName, &m.subjectGone, &m.sectionID, &m.sectionName, &m.sectionGone,
			&m.periods, &m.elective, &m.examined, &m.assessed, &m.gradeOnly, &m.practical, &m.onReport)
		src = append(src, m)
		return err
	}); err != nil {
		return err
	}
	have, sections := map[string]bool{}, map[string]bool{}
	if err := queryAll(ctx, tx, `SELECT class_id::text || ':' || subject_id::text || ':' || COALESCE(section_id::text, '') FROM subject_class_mappings WHERE year_id::text = $1 AND deleted_at IS NULL`,
		[]any{p.B.ID}, func(r pgx.Rows) error {
			var k string
			err := r.Scan(&k)
			have[k] = true
			return err
		}); err != nil {
		return err
	}
	if err := queryAll(ctx, tx, `SELECT class_id::text || ':' || section_id::text FROM class_section_mappings WHERE year_id::text = $1 AND deleted_at IS NULL`, []any{p.B.ID}, func(r pgx.Rows) error {
		var k string
		err := r.Scan(&k)
		sections[k] = true
		return err
	}); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, m := range src {
		sec := ""
		if m.sectionID != nil {
			sec = *m.sectionID
		}
		key := m.classID + ":" + m.subjectID + ":" + sec
		label := m.className + " / " + m.subjectName
		if m.sectionName != nil {
			label += " (section " + *m.sectionName + ")"
		}
		it := Item{Label: label}
		switch {
		case have[key]:
			it.Status, it.Message = ItemExists, "This subject is already mapped to this class in "+p.B.Name+"."
		case m.classGone:
			it.Status, it.Reason, it.Message = ItemConflict, "class_deleted", "The class no longer exists."
		case m.subjectGone:
			it.Status, it.Reason, it.Message = ItemConflict, "subject_deleted", "The subject no longer exists."
		case m.sectionID != nil && m.sectionGone:
			it.Status, it.Reason, it.Message = ItemConflict, "section_deleted", "The section no longer exists."
		case m.sectionID != nil && !sections[m.classID+":"+*m.sectionID]:
			it.Status, it.Reason, it.Message = ItemConflict, "section_not_mapped", "This section is not mapped to the class in "+p.B.Name+" yet. Run the class sections step first."
		default:
			it.Status = ItemCreate
			have[key] = true
			batch.Queue(`INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, periods_per_week, is_elective, is_examined, is_assessed, is_grade_only, has_practical, on_report_card, created_by, updated_by)
				VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid, NULLIF($12, '')::uuid)`,
				p.B.ID, m.classID, m.subjectID, m.sectionID, m.periods, m.elective, m.examined, m.assessed, m.gradeOnly, m.practical, m.onReport, actor)
		}
		res.add(it)
	}
	return flush(ctx, tx, batch)
}

// ---------------------------------------------------------------- holidays and calendar events

// festivalTypes are the holiday types whose dates usually move from year to year (festivals follow lunar calendars).
var festivalTypes = map[string]bool{"public": true, "regional": true, "restricted": true}

func dateKey(title, kind string, start, end time.Time) string {
	return strings.ToLower(strings.TrimSpace(title)) + "|" + kind + "|" + iso(start) + "|" + iso(end)
}

func copyHolidays(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	type holiday struct {
		title, kind                string
		start, end                 time.Time
		optional, recurring        bool
		applicableFor, description *string
		status                     string
	}
	var src []holiday
	if err := queryAll(ctx, tx, `SELECT title, holiday_type, start_date, end_date, is_optional, is_recurring_annual, applicable_for, description, status
		FROM holidays WHERE year_id::text = $1 AND deleted_at IS NULL ORDER BY start_date, end_date, lower(title), id`, []any{p.A.ID}, func(r pgx.Rows) error {
		var h holiday
		err := r.Scan(&h.title, &h.kind, &h.start, &h.end, &h.optional, &h.recurring, &h.applicableFor, &h.description, &h.status)
		src = append(src, h)
		return err
	}); err != nil {
		return err
	}
	have := map[string]bool{}
	if err := queryAll(ctx, tx, `SELECT title, holiday_type, start_date, end_date FROM holidays WHERE year_id::text = $1 AND deleted_at IS NULL`, []any{p.B.ID}, func(r pgx.Rows) error {
		var title, kind string
		var s, e time.Time
		err := r.Scan(&title, &kind, &s, &e)
		have[dateKey(title, kind, s, e)] = true
		return err
	}); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, h := range src {
		ns, ne, detail := moved(p, h.start, h.end)
		it := Item{Label: h.title, Detail: detail}
		switch {
		case h.recurring:
			it.Status, it.Reason, it.Message, it.Detail = ItemSkipped, "recurring_annual", "This holiday repeats every year, so it already applies to "+p.B.Name+".", ""
		case have[dateKey(h.title, h.kind, ns, ne)]:
			it.Status, it.Message = ItemExists, "This holiday is already in "+p.B.Name+" on the moved dates."
		case outside(p, ns, ne):
			it.Status, it.Reason, it.Message = ItemConflict, "outside_year", outsideMessage(p)
		default:
			it.Status = ItemCreate
			if festivalTypes[h.kind] {
				it.Review, it.Message = true, "The date of a "+h.kind+" holiday may change from year to year. Check it."
			}
			have[dateKey(h.title, h.kind, ns, ne)] = true
			batch.Queue(`INSERT INTO holidays (tenant_id, year_id, title, holiday_type, start_date, end_date, is_optional, is_recurring_annual, applicable_for, description, status, created_by, updated_by)
				VALUES (app_current_tenant(), $1::uuid, $2, $3, $4, $5, $6, false, $7, $8, $9, NULLIF($10, '')::uuid, NULLIF($10, '')::uuid)`,
				p.B.ID, h.title, h.kind, ns, ne, h.optional, h.applicableFor, h.description, h.status, actor)
		}
		res.add(it)
	}
	return flush(ctx, tx, batch)
}

func copyEvents(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	type event struct {
		title, kind                        string
		start, end                         time.Time
		allDay, instructional, examination bool
		venue, description                 *string
		status                             string
	}
	var src []event
	if err := queryAll(ctx, tx, `SELECT title, event_type, start_date, end_date, is_all_day, is_instructional_day, is_examination_day, venue, description, status
		FROM calendar_events WHERE year_id::text = $1 AND deleted_at IS NULL ORDER BY start_date, end_date, lower(title), id`, []any{p.A.ID}, func(r pgx.Rows) error {
		var e event
		err := r.Scan(&e.title, &e.kind, &e.start, &e.end, &e.allDay, &e.instructional, &e.examination, &e.venue, &e.description, &e.status)
		src = append(src, e)
		return err
	}); err != nil {
		return err
	}
	have := map[string]bool{}
	if err := queryAll(ctx, tx, `SELECT title, event_type, start_date, end_date FROM calendar_events WHERE year_id::text = $1 AND deleted_at IS NULL`, []any{p.B.ID}, func(r pgx.Rows) error {
		var title, kind string
		var s, e time.Time
		err := r.Scan(&title, &kind, &s, &e)
		have[dateKey(title, kind, s, e)] = true
		return err
	}); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, e := range src {
		ns, ne, detail := moved(p, e.start, e.end)
		it := Item{Label: e.title, Detail: detail}
		switch {
		case have[dateKey(e.title, e.kind, ns, ne)]:
			it.Status, it.Message = ItemExists, "This event is already in "+p.B.Name+" on the moved dates."
		case outside(p, ns, ne):
			it.Status, it.Reason, it.Message = ItemConflict, "outside_year", outsideMessage(p)
		default:
			it.Status = ItemCreate
			have[dateKey(e.title, e.kind, ns, ne)] = true
			batch.Queue(`INSERT INTO calendar_events (tenant_id, year_id, title, event_type, start_date, end_date, is_all_day, is_instructional_day, is_examination_day, venue, description, status, created_by, updated_by)
				VALUES (app_current_tenant(), $1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid, NULLIF($12, '')::uuid)`,
				p.B.ID, e.title, e.kind, ns, ne, e.allDay, e.instructional, e.examination, e.venue, e.description, e.status, actor)
		}
		res.add(it)
	}
	return flush(ctx, tx, batch)
}

// ---------------------------------------------------------------- make current

// promotionProgress counts the class sections that have enrolled students in year A and how many of them have a
// committed promotion into year B.
func promotionProgress(ctx context.Context, tx pgx.Tx, fromID, toID string) (sections, promoted int, err error) {
	err = tx.QueryRow(ctx, `WITH src AS (
			SELECT DISTINCT class_id, section_id FROM student_enrollments WHERE year_id::text = $1 AND status = 'enrolled' AND deleted_at IS NULL)
		SELECT count(*), count(*) FILTER (WHERE EXISTS (
			SELECT 1 FROM promotion_batches b WHERE b.from_year_id::text = $1 AND b.to_year_id::text = $2 AND b.from_class_id = src.class_id
				AND b.from_section_id = src.section_id AND b.status = 'committed' AND b.deleted_at IS NULL))
		FROM src`, fromID, toID).Scan(&sections, &promoted)
	return
}

func makeCurrent(ctx context.Context, tx pgx.Tx, p pair, actor string, res *Result) error {
	res.Shift = Shift{}
	if p.B.IsCurrent {
		res.add(Item{Label: p.B.Name + " is already the current year", Status: ItemExists, Message: "Nothing to change."})
		return nil
	}
	var terms, mappings int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM academic_terms WHERE year_id::text = $1 AND deleted_at IS NULL),
		(SELECT count(*) FROM class_section_mappings WHERE year_id::text = $1 AND deleted_at IS NULL)`, p.B.ID).Scan(&terms, &mappings); err != nil {
		return err
	}
	if p.B.Status != "active" {
		res.Blockers = append(res.Blockers, p.B.Name+" is inactive. Make it active on the Academic years screen first.")
	}
	if terms == 0 {
		res.Blockers = append(res.Blockers, p.B.Name+" has no terms. Run the terms step or add them first.")
	}
	if mappings == 0 {
		res.Blockers = append(res.Blockers, p.B.Name+" has no class sections. Run the class sections step or map them first.")
	}
	if len(res.Blockers) > 0 {
		return nil
	}
	var curID, curName *string
	if err := tx.QueryRow(ctx, `SELECT id::text, name FROM academic_years WHERE is_current AND deleted_at IS NULL`).Scan(&curID, &curName); err != nil && err != pgx.ErrNoRows {
		return err
	}
	if curID != nil && *curID != p.A.ID {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s is the current year, not %s. It stops being current.", *curName, p.A.Name))
	}
	var future bool
	if err := tx.QueryRow(ctx, `SELECT $1::date > current_date`, p.B.Start).Scan(&future); err != nil {
		return err
	}
	if future {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s starts on %s, which is still ahead.", p.B.Name, iso(p.B.Start)))
	}
	if sections, promoted, err := promotionProgress(ctx, tx, p.A.ID, p.B.ID); err != nil {
		return err
	} else if promoted < sections {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d of %d class %s with students in %s %s not been promoted into %s yet.",
			sections-promoted, sections, plural(sections, "section", "sections"), p.A.Name, plural(sections-promoted, "has", "have"), p.B.Name))
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('academic_years:' || current_setting('app.tenant_id')))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE academic_years SET is_current = false, updated_by = NULLIF($1, '')::uuid WHERE is_current AND deleted_at IS NULL AND id::text <> $2`, actor, p.B.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE academic_years SET is_current = true, updated_by = NULLIF($1, '')::uuid WHERE id::text = $2`, actor, p.B.ID); err != nil {
		return err
	}
	from := "no year"
	if curName != nil {
		from = *curName
	}
	res.add(Item{Label: "Make " + p.B.Name + " the current year", Status: ItemCreate, Detail: "The current year changes from " + from + " to " + p.B.Name + "."})
	return nil
}
