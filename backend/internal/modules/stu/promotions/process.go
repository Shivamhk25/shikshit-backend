package promotions

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/enrollments"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Row outcomes of a preview or a commit.
const (
	OutcomeEnrol    = "enrol"     // a new-year enrolment is (or would be) made
	OutcomeGraduate = "graduate"  // the student is (or would be) marked alumni
	OutcomeLeaveOut = "leave_out" // nothing is done for this student
	OutcomeProblem  = "problem"   // the row cannot be applied; Problems says why
)

// OutcomeRow is the result for one student.
type OutcomeRow struct {
	StudentID        string
	RegistrationCode string
	StudentName      string
	Decision         string
	Outcome          string
	ToClassID        *string
	ToClassName      *string
	ToSectionID      *string
	ToSectionName    *string
	// RollNumber is the roll number the student gets (or would get) in the new year.
	RollNumber *string
	// EnrollmentID is the enrolment the commit made.
	EnrollmentID *string
	Problems     crud.FieldErrors
}

// Outcome is the answer of a preview, a commit or an undo.
type Outcome struct {
	Batch            Batch
	Rows             []OutcomeRow
	Committed        bool // this call committed the batch
	AlreadyCommitted bool // the batch was committed before; nothing was written
	AlreadyUndone    bool // the batch was undone before; nothing was changed
	Undone           bool // this call undid the batch
	Enrolled         int
	Graduated        int
	LeftOut          int
	ProblemCount     int
	Warnings         []string
	DuesOverridden   bool
}

var errPreviewDone = errors.New("promotions: preview rolled back")

var rollRE = regexp.MustCompile(`^[0-9]{1,9}$`)

// planRow is one student of a batch while a preview or commit works on it.
type planRow struct {
	decision                                 string
	studentID, code, name, firstName         string
	lastName, studentStatus                  string
	studentRemoved                           bool
	sourceID                                 string
	srcLive                                  bool
	srcStatus, srcYear, srcClass, srcSection string
	srcRoll                                  *string
	wantClass, wantSection, typedRoll        *string
	toClass, toSection                       string
	roll                                     *string
	enrolmentID                              *string
	problems                                 crud.FieldErrors
}

func (r *planRow) add(field, code, msg string) { r.problems.Add(field, code, msg) }

func (r *planRow) enrols() bool { return r.decision == DecisionPromote || r.decision == DecisionRepeat }

func loadPlanRows(ctx context.Context, tx pgx.Tx, batchID string) ([]*planRow, error) {
	rows, err := tx.Query(ctx, `SELECT r.decision, s.id::text, s.registration_code, btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')),
			s.first_name, COALESCE(s.last_name, ''), s.status, s.deleted_at IS NOT NULL,
			e.id::text, e.deleted_at IS NULL, e.status, e.year_id::text, e.class_id::text, e.section_id::text, e.roll_number,
			r.to_class_id::text, r.to_section_id::text, r.roll_number
		FROM promotion_batch_students r
		JOIN students s ON s.tenant_id = r.tenant_id AND s.id = r.student_id
		JOIN student_enrollments e ON e.tenant_id = r.tenant_id AND e.id = r.source_enrollment_id
		WHERE r.batch_id::text = $1`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*planRow
	for rows.Next() {
		p := &planRow{}
		if err := rows.Scan(&p.decision, &p.studentID, &p.code, &p.name, &p.firstName, &p.lastName, &p.studentStatus, &p.studentRemoved,
			&p.sourceID, &p.srcLive, &p.srcStatus, &p.srcYear, &p.srcClass, &p.srcSection, &p.srcRoll, &p.wantClass, &p.wantSection, &p.typedRoll); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if x, y := strings.ToLower(a.firstName), strings.ToLower(b.firstName); x != y {
			return x < y
		}
		if x, y := strings.ToLower(a.lastName), strings.ToLower(b.lastName); x != y {
			return x < y
		}
		return strings.ToLower(a.code) < strings.ToLower(b.code)
	})
	return out, rows.Err()
}

func sectionNames(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, name FROM sections WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		m[id] = name
	}
	return m, rows.Err()
}

func tenantKey(ctx context.Context) string {
	if t, ok := tenancy.FromContext(ctx); ok {
		return t.ID
	}
	return ""
}

// planned is what the rules decided before anything is written.
type planned struct {
	rows       []*planRow
	toStart    string
	warnings   []string
	overridden bool
}

// plan applies every rule to every row and assigns roll numbers, writing nothing except advisory locks. Problems are
// collected on the rows so the registrar sees all of them at once.
func (s Service) plan(ctx context.Context, tx pgx.Tx, b batchLock, override *DuesOverride) (planned, error) {
	var pl planned
	rows, err := loadPlanRows(ctx, tx, b.id)
	if err != nil {
		return pl, err
	}
	pl.rows = rows
	var fe crud.FieldErrors
	_, toStart, ok, err := checkSource(ctx, tx, b.fromYear, b.toYear, b.fromClass, b.fromSection, &fe)
	if err != nil {
		return pl, err
	}
	if !ok {
		for _, r := range rows {
			for _, e := range fe {
				r.add(e.Field, e.Code, e.Message)
			}
		}
		return pl, nil
	}
	pl.toStart = toStart
	_, byID, err := loadClasses(ctx, tx)
	if err != nil {
		return pl, err
	}
	secNames, err := sectionNames(ctx, tx)
	if err != nil {
		return pl, err
	}
	// the sections each class has in the new year, with their capacity
	mapped := map[string]*int{}
	mrows, err := tx.Query(ctx, `SELECT class_id::text, section_id::text, capacity FROM class_section_mappings WHERE year_id::text = $1 AND deleted_at IS NULL`, b.toYear)
	if err != nil {
		return pl, err
	}
	for mrows.Next() {
		var c, sec string
		var capacity *int
		if err := mrows.Scan(&c, &sec, &capacity); err != nil {
			mrows.Close()
			return pl, err
		}
		mapped[c+"|"+sec] = capacity
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return pl, err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.studentID
	}
	already := map[string]bool{}
	arows, err := tx.Query(ctx, `SELECT student_id::text FROM student_enrollments WHERE year_id::text = $1 AND deleted_at IS NULL AND student_id::text = ANY($2)`, b.toYear, ids)
	if err != nil {
		return pl, err
	}
	for arows.Next() {
		var id string
		if err := arows.Scan(&id); err != nil {
			arows.Close()
			return pl, err
		}
		already[id] = true
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return pl, err
	}

	src := byID[b.fromClass]
	for _, r := range rows {
		switch {
		case r.studentRemoved:
			r.add("studentId", "student_removed", "This student has been removed.")
			continue
		case !r.srcLive || r.srcStatus != "enrolled" || r.srcYear != b.fromYear || r.srcClass != b.fromClass || r.srcSection != b.fromSection:
			r.add("studentId", "source_changed", "This student's enrolment in the year being closed has changed since the decisions were saved. Save the decisions again.")
			continue
		}
		if r.decision == DecisionLeaveOut {
			continue
		}
		if r.studentStatus != "active" {
			r.add("studentId", "student_not_active", fmt.Sprintf("This student's status is %s. Only an active student can be promoted, repeated or graduated. Leave this student out.", r.studentStatus))
			continue
		}
		if r.decision == DecisionGraduate {
			continue
		}
		// promote or repeat: where to
		toClass := r.wantClass
		if r.decision == DecisionRepeat {
			c := b.fromClass
			toClass = &c
		}
		if toClass == nil {
			r.add("toClassId", "class_required", "Choose the class to promote this student to.")
			continue
		}
		cls, found := byID[*toClass]
		switch {
		case !found:
			r.add("toClassId", "class_not_found", "This class does not exist.")
			continue
		case cls.status != "active":
			r.add("toClassId", "class_inactive", fmt.Sprintf("%s is not active.", cls.name))
			continue
		case r.decision == DecisionPromote && *toClass == b.fromClass:
			r.add("toClassId", "same_class", "Promoting to the same class is a repeat. Choose repeat, or another class.")
			continue
		case r.decision == DecisionPromote && cls.rank < src.rank:
			r.add("toClassId", "not_forward", fmt.Sprintf("%s comes before %s. A promotion moves up.", cls.name, src.name))
			continue
		}
		r.toClass = *toClass
		section, explicit := b.fromSection, false
		if r.wantSection != nil {
			section, explicit = *r.wantSection, true
		}
		if _, ok := mapped[r.toClass+"|"+section]; !ok {
			if explicit {
				r.add("toSectionId", "section_not_mapped", fmt.Sprintf("Section %s is not set up for %s in the new academic year. Map it under Class sections.", secNames[section], cls.name))
			} else {
				r.add("toSectionId", "section_required", fmt.Sprintf("Section %s is not set up for %s in the new academic year. Choose a section.", secNames[section], cls.name))
			}
			continue
		}
		r.toSection = section
		if already[r.studentID] {
			r.add("studentId", "already_enrolled", "This student already has an enrolment in the new academic year.")
		}
	}

	// dues: every gate, for every student who is moving or graduating
	var movers []string
	for _, r := range rows {
		if r.decision != DecisionLeaveOut && r.studentStatus == "active" {
			movers = append(movers, r.studentID)
		}
	}
	if len(movers) > 0 {
		for _, g := range s.Gates {
			blocked, err := g.Blocked(ctx, tx, b.fromYear, movers)
			if err != nil {
				return pl, err
			}
			for _, r := range rows {
				reason, isBlocked := blocked[r.studentID]
				if !isBlocked {
					continue
				}
				if override != nil {
					pl.overridden = true
					continue
				}
				r.add("dues", "dues_pending", reason)
			}
		}
	}

	if err := s.assignRolls(ctx, tx, b, rows, mapped, secNames, byID, &pl); err != nil {
		return pl, err
	}

	var notIn int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM student_enrollments e JOIN students s ON s.tenant_id = e.tenant_id AND s.id = e.student_id AND s.deleted_at IS NULL
		WHERE e.year_id::text = $1 AND e.class_id::text = $2 AND e.section_id::text = $3 AND e.status = 'enrolled' AND e.deleted_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM promotion_batch_students r WHERE r.batch_id::text = $4 AND r.student_id = e.student_id)`, b.fromYear, b.fromClass, b.fromSection, b.id).Scan(&notIn); err != nil {
		return pl, err
	}
	if notIn > 0 {
		pl.warnings = append(pl.warnings, fmt.Sprintf("%d %s enrolled in this class section but not in this batch, and will not be promoted.", notIn, plural(notIn, "student is", "students are")))
	}
	return pl, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// assignRolls gives each enrolling student a roll number in the target section and warns about capacity. Students
// are numbered in the order of their names, continuing after the highest numeric roll the section already has, so
// two batches into one section (5A and 5B into 6A) never clash. A roll typed for a student wins; "keep" carries the
// old roll; "blank" leaves it empty to fill in later.
func (s Service) assignRolls(ctx context.Context, tx pgx.Tx, b batchLock, rows []*planRow, caps map[string]*int, secNames map[string]string, classes map[string]classRef, pl *planned) error {
	groups := map[string][]*planRow{}
	for _, r := range rows {
		if r.enrols() && r.toClass != "" && r.toSection != "" {
			k := r.toClass + "|" + r.toSection
			groups[k] = append(groups[k], r)
		}
	}
	for _, k := range sortedKeys(groups) {
		g := groups[k]
		cls, sec, _ := strings.Cut(k, "|")
		// two commits into the same section take turns
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "promo|"+tenantKey(ctx)+"|"+b.toYear+"|"+k); err != nil {
			return err
		}
		taken := map[string]bool{}
		var maxNum int64
		var existing int
		rrows, err := tx.Query(ctx, `SELECT roll_number FROM student_enrollments WHERE year_id::text = $1 AND class_id::text = $2 AND section_id::text = $3 AND deleted_at IS NULL`, b.toYear, cls, sec)
		if err != nil {
			return err
		}
		for rrows.Next() {
			var roll *string
			if err := rrows.Scan(&roll); err != nil {
				rrows.Close()
				return err
			}
			existing++
			if roll != nil {
				claim(taken, &maxNum, *roll)
			}
		}
		rrows.Close()
		if err := rrows.Err(); err != nil {
			return err
		}
		secName := secNames[sec]
		// typed rolls first, then the carried or numbered ones
		for _, r := range g {
			if r.typedRoll == nil {
				continue
			}
			roll := strings.TrimSpace(*r.typedRoll)
			if taken[strings.ToLower(roll)] {
				r.add("rollNumber", "roll_taken", fmt.Sprintf("Roll number %s is already used in %s section %s.", roll, classes[cls].name, secName))
				continue
			}
			claim(taken, &maxNum, roll)
			r.roll = &roll
		}
		for _, r := range g {
			if r.typedRoll != nil {
				continue
			}
			switch b.rollMode {
			case RollKeep:
				if r.srcRoll == nil {
					continue
				}
				roll := *r.srcRoll
				if taken[strings.ToLower(roll)] {
					r.add("rollNumber", "roll_taken", fmt.Sprintf("Keeping roll number %s clashes with another student in %s section %s. Renumber the batch or type a roll number.", roll, classes[cls].name, secName))
					continue
				}
				claim(taken, &maxNum, roll)
				r.roll = &roll
			}
		}
		if b.rollMode == RollAlphabetical {
			next := maxNum
			for _, r := range g {
				if r.typedRoll != nil {
					continue
				}
				for {
					next++
					if !taken[strconv.FormatInt(next, 10)] {
						break
					}
				}
				roll := strconv.FormatInt(next, 10)
				taken[roll] = true
				r.roll = &roll
			}
		}
		if capacity := caps[k]; capacity != nil && existing+len(g) > *capacity {
			pl.warnings = append(pl.warnings, fmt.Sprintf("%s section %s would have %d students; its capacity is %d.", classes[cls].name, secName, existing+len(g), *capacity))
		}
	}
	return nil
}

// claim marks a roll number as used and tracks the highest purely numeric one.
func claim(taken map[string]bool, maxNum *int64, roll string) {
	taken[strings.ToLower(strings.TrimSpace(roll))] = true
	if rollRE.MatchString(roll) {
		if n, err := strconv.ParseInt(roll, 10, 64); err == nil && n > *maxNum {
			*maxNum = n
		}
	}
}

// apply makes the changes of the plan, each row in its own savepoint so one row's failure is reported against that
// row and the others are still tried. The caller rolls everything back when any row failed.
func (s Service) apply(ctx context.Context, tx pgx.Tx, actor audit.Actor, b batchLock, pl planned) error {
	store := s.enrolmentStore()
	for _, r := range pl.rows {
		if len(r.problems) > 0 {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		fail := func(err error) (bool, error) {
			_ = sp.Rollback(ctx)
			var fe crud.FieldErrors
			if errors.As(err, &fe) {
				r.problems = append(r.problems, fe...)
				return true, nil
			}
			return false, err
		}
		switch {
		case r.enrols():
			date := pl.toStart
			e, err := store.CreateIn(ctx, sp, actor, enrollments.Input{StudentID: r.studentID, YearID: b.toYear, ClassID: r.toClass, SectionID: r.toSection, RollNumber: r.roll, EnrollmentDate: &date, Status: "enrolled"})
			if err != nil {
				handled, ferr := fail(err)
				if !handled {
					return ferr
				}
				continue
			}
			if _, err := sp.Exec(ctx, `UPDATE promotion_batch_students SET new_enrollment_id = $3::uuid WHERE batch_id::text = $1 AND student_id::text = $2`, b.id, r.studentID, e.ID); err != nil {
				return err
			}
			r.enrolmentID = &e.ID
		case r.decision == DecisionGraduate:
			var prev string
			err := sp.QueryRow(ctx, `UPDATE students SET status = 'alumni', updated_by = NULLIF($2, '')::uuid WHERE id::text = $1 AND status = 'active' AND deleted_at IS NULL
				RETURNING 'active'`, r.studentID, actor.UserID).Scan(&prev)
			if errors.Is(err, pgx.ErrNoRows) {
				var fe crud.FieldErrors
				fe.Add("studentId", "student_not_active", "This student is no longer active.")
				if _, ferr := fail(fe); ferr != nil {
					return ferr
				}
				continue
			}
			if err != nil {
				return err
			}
			if _, err := sp.Exec(ctx, `UPDATE promotion_batch_students SET previous_student_status = $3 WHERE batch_id::text = $1 AND student_id::text = $2`, b.id, r.studentID, prev); err != nil {
				return err
			}
			if err := audit.Record(ctx, sp, actor, audit.Entry{Action: "students.update", ResourceType: "student", ResourceID: r.studentID,
				Before: map[string]any{"status": prev}, After: map[string]any{"status": "alumni", "reason": "promotion batch " + b.id}}); err != nil {
				return err
			}
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) outcome(ctx context.Context, tx pgx.Tx, b batchLock, pl planned, classNames map[string]string, secNames map[string]string) (Outcome, error) {
	batch, err := getBatch(ctx, tx, b.id)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Batch: batch, Rows: make([]OutcomeRow, 0, len(pl.rows)), Warnings: pl.warnings, DuesOverridden: pl.overridden}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for _, r := range pl.rows {
		row := OutcomeRow{StudentID: r.studentID, RegistrationCode: r.code, StudentName: r.name, Decision: r.decision, RollNumber: r.roll, EnrollmentID: r.enrolmentID, Problems: r.problems}
		if r.toClass != "" {
			c, sc := r.toClass, r.toSection
			row.ToClassID, row.ToSectionID = &c, &sc
			cn, sn := classNames[c], secNames[sc]
			row.ToClassName, row.ToSectionName = &cn, &sn
		}
		switch {
		case len(r.problems) > 0:
			row.Outcome = OutcomeProblem
			out.ProblemCount++
		case r.decision == DecisionLeaveOut:
			row.Outcome = OutcomeLeaveOut
			out.LeftOut++
		case r.decision == DecisionGraduate:
			row.Outcome = OutcomeGraduate
			out.Graduated++
		default:
			row.Outcome = OutcomeEnrol
			out.Enrolled++
		}
		if row.Problems == nil {
			row.Problems = crud.FieldErrors{}
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

func nameMaps(ctx context.Context, tx pgx.Tx) (map[string]string, map[string]string, error) {
	_, byID, err := loadClasses(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	cn := make(map[string]string, len(byID))
	for id, c := range byID {
		cn[id] = c.name
	}
	sn, err := sectionNames(ctx, tx)
	return cn, sn, err
}

// storedOutcome describes a batch already committed from what it stored.
func storedOutcome(d Detail) Outcome {
	out := Outcome{Batch: d.Batch, Rows: make([]OutcomeRow, 0, len(d.Rows)), Warnings: []string{}}
	for _, r := range d.Rows {
		row := OutcomeRow{StudentID: r.StudentID, RegistrationCode: r.RegistrationCode, StudentName: r.StudentName, Decision: r.Decision, Problems: crud.FieldErrors{},
			ToClassID: r.ToClassID, ToSectionID: r.ToSectionID, EnrollmentID: r.NewEnrollmentID, RollNumber: r.EnrolledRoll, ToClassName: r.EnrolledClass, ToSectionName: r.EnrolledSection}
		switch {
		case r.NewEnrollmentID != nil:
			row.Outcome = OutcomeEnrol
			out.Enrolled++
		case r.Decision == DecisionGraduate:
			row.Outcome = OutcomeGraduate
			out.Graduated++
		default:
			row.Outcome = OutcomeLeaveOut
			out.LeftOut++
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// execute runs the plan and the writes for a preview (commit false: the caller rolls the transaction back) or a
// commit (commit true: any problem refuses the whole batch with NotReady, and the caller's rollback removes
// everything the apply wrote).
func (s Service) execute(ctx context.Context, tx pgx.Tx, actor audit.Actor, id string, commit bool, override *DuesOverride) (Outcome, error) {
	b, err := lockBatch(ctx, tx, id)
	if err != nil {
		return Outcome{}, err
	}
	switch b.status {
	case StatusUndone:
		return Outcome{}, ErrUndone
	case StatusCommitted:
		if !commit {
			return Outcome{}, ErrNotDraft
		}
		d, err := getDetail(ctx, tx, id)
		if err != nil {
			return Outcome{}, err
		}
		out := storedOutcome(d)
		out.AlreadyCommitted = true
		return out, nil
	}
	pl, err := s.plan(ctx, tx, b, override)
	if err != nil {
		return Outcome{}, err
	}
	hasProblem := false
	for _, r := range pl.rows {
		hasProblem = hasProblem || len(r.problems) > 0
	}
	if !hasProblem {
		if err := s.apply(ctx, tx, actor, b, pl); err != nil {
			return Outcome{}, err
		}
	}
	cn, sn, err := nameMaps(ctx, tx)
	if err != nil {
		return Outcome{}, err
	}
	if !commit {
		return s.outcome(ctx, tx, b, pl, cn, sn)
	}
	out, err := s.outcome(ctx, tx, b, pl, cn, sn)
	if err != nil {
		return Outcome{}, err
	}
	if out.ProblemCount > 0 {
		return out, NotReady{Outcome: out}
	}
	var reason *string
	if pl.overridden && override != nil {
		reason = &override.Reason
	}
	if _, err := tx.Exec(ctx, `UPDATE promotion_batches SET status = 'committed', committed_at = now(), committed_by = NULLIF($2, '')::uuid, undo_until = now() + $3::interval,
			dues_override_reason = $4, updated_by = NULLIF($2, '')::uuid WHERE id::text = $1`, id, actor.UserID, fmt.Sprintf("%d seconds", int(UndoWindow.Seconds())), reason); err != nil {
		return Outcome{}, err
	}
	if out.Batch, err = getBatch(ctx, tx, id); err != nil {
		return Outcome{}, err
	}
	out.Committed = true
	after := map[string]any{"enrolled": out.Enrolled, "graduated": out.Graduated, "leftOut": out.LeftOut, "rollMode": b.rollMode, "fromYearId": b.fromYear, "toYearId": b.toYear,
		"classId": b.fromClass, "sectionId": b.fromSection}
	if pl.overridden {
		after["duesOverrideReason"] = override.Reason
	}
	if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.commit", ResourceType: "promotion_batch", ResourceID: id, Before: map[string]any{"status": StatusDraft}, After: after}); err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// Preview says what committing the draft would do, row by row, without keeping anything. It runs the commit's own
// code inside a transaction that is rolled back, so the roll numbers, the problems and the counts are the ones the
// commit gives.
func (s Service) Preview(ctx context.Context, actor audit.Actor, id string) (Outcome, error) {
	var out Outcome
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		if out, err = s.execute(ctx, tx, actor, id, false, nil); err != nil {
			return err
		}
		return errPreviewDone
	})
	if err != nil && !errors.Is(err, errPreviewDone) {
		return Outcome{}, err
	}
	s.recordSeparately(ctx, actor, audit.Entry{Action: "promotions.preview", ResourceType: "promotion_batch", ResourceID: id,
		After: map[string]any{"enrolled": out.Enrolled, "graduated": out.Graduated, "leftOut": out.LeftOut, "problems": out.ProblemCount}})
	return out, nil
}

// Commit makes the new-year enrolments of the draft and marks its graduates, all or nothing. A batch with a problem
// row is refused with NotReady and nothing is written. Committing a batch that is already committed returns what it
// made with AlreadyCommitted set and writes nothing. override lets the commit pass the students a dues gate blocked;
// it needs a reason and is for users who hold the override permission.
func (s Service) Commit(ctx context.Context, actor audit.Actor, id string, override *DuesOverride) (Outcome, error) {
	if override != nil {
		override.Reason = strings.TrimSpace(override.Reason)
		if override.Reason == "" || len(override.Reason) > 500 {
			var fe crud.FieldErrors
			fe.Add("reason", "required", "Give a reason of up to 500 characters for overriding the dues check.")
			return Outcome{}, fe
		}
	}
	var out Outcome
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		out, err = s.execute(ctx, tx, actor, id, true, override)
		return err
	})
	var nr NotReady
	if errors.As(err, &nr) {
		s.recordSeparately(ctx, actor, audit.Entry{Action: "promotions.commit_refused", ResourceType: "promotion_batch", ResourceID: id,
			After: map[string]any{"problems": nr.Outcome.ProblemCount}})
		return nr.Outcome, err
	}
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// Undo takes back a committed batch inside the undo window: the enrolments it made are removed and graduates return
// to the status they had. It is refused (UndoRefused, nothing changed) once the window has closed or when anything
// has changed what the batch made. Undoing a batch that is already undone changes nothing.
func (s Service) Undo(ctx context.Context, actor audit.Actor, id, reason string) (Outcome, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 500 {
		var fe crud.FieldErrors
		fe.Add("reason", "required", "Give a reason of up to 500 characters for undoing this promotion.")
		return Outcome{}, fe
	}
	var out Outcome
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := lockBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		switch b.status {
		case StatusDraft:
			return ErrNotCommitted
		case StatusUndone:
			d, err := getDetail(ctx, tx, id)
			if err != nil {
				return err
			}
			out = storedOutcome(d)
			out.AlreadyUndone = true
			return nil
		}
		d, err := getDetail(ctx, tx, id)
		if err != nil {
			return err
		}
		reasons, err := s.undoReasons(ctx, tx, b)
		if err != nil {
			return err
		}
		if len(reasons) > 0 {
			return UndoRefused{Reasons: reasons}
		}
		store := s.enrolmentStore()
		type grad struct{ studentID, prev string }
		var grads []grad
		rows, err := tx.Query(ctx, `SELECT student_id::text, new_enrollment_id::text, decision, previous_student_status FROM promotion_batch_students WHERE batch_id::text = $1`, id)
		if err != nil {
			return err
		}
		type made struct{ studentID, enrolmentID string }
		var enrolments []made
		for rows.Next() {
			var sid, decision string
			var eid, prev *string
			if err := rows.Scan(&sid, &eid, &decision, &prev); err != nil {
				rows.Close()
				return err
			}
			if eid != nil {
				enrolments = append(enrolments, made{sid, *eid})
			}
			if decision == DecisionGraduate && prev != nil {
				grads = append(grads, grad{sid, *prev})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, m := range enrolments {
			if err := store.DeleteIn(ctx, tx, actor, m.enrolmentID); err != nil {
				return err
			}
		}
		for _, g := range grads {
			if _, err := tx.Exec(ctx, `UPDATE students SET status = $2, updated_by = NULLIF($3, '')::uuid WHERE id::text = $1`, g.studentID, g.prev, actor.UserID); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "students.update", ResourceType: "student", ResourceID: g.studentID,
				Before: map[string]any{"status": "alumni"}, After: map[string]any{"status": g.prev, "reason": "promotion batch " + id + " undone"}}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE promotion_batches SET status = 'undone', undone_at = now(), undone_by = NULLIF($2, '')::uuid, undo_reason = $3, updated_by = NULLIF($2, '')::uuid WHERE id::text = $1`,
			id, actor.UserID, reason); err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "promotions.undo", ResourceType: "promotion_batch", ResourceID: id,
			Before: map[string]any{"status": StatusCommitted}, After: map[string]any{"status": StatusUndone, "reason": reason, "enrolmentsRemoved": len(enrolments), "statusesRestored": len(grads)}}); err != nil {
			return err
		}
		out = storedOutcome(d)
		out.Undone = true
		out.Batch, err = getBatch(ctx, tx, id)
		return err
	})
	var refused UndoRefused
	if errors.As(err, &refused) {
		codes := make([]string, len(refused.Reasons))
		for i, r := range refused.Reasons {
			codes[i] = r.Code
		}
		s.recordSeparately(ctx, actor, audit.Entry{Action: "promotions.undo_refused", ResourceType: "promotion_batch", ResourceID: id, After: map[string]any{"reasons": codes}})
	}
	if err != nil {
		return Outcome{}, err
	}
	return out, nil
}

// undoReasons lists why a committed batch cannot be undone now, locking the enrolments it made so nobody edits them
// while the undo decides.
func (s Service) undoReasons(ctx context.Context, tx pgx.Tx, b batchLock) ([]UndoReason, error) {
	var reasons []UndoReason
	if b.expired {
		reasons = append(reasons, UndoReason{Code: "window_closed", Message: fmt.Sprintf("The undo window of %d days has closed.", int(UndoWindow.Hours()/24))})
		return reasons, nil
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM student_enrollments WHERE id IN (SELECT new_enrollment_id FROM promotion_batch_students WHERE batch_id::text = $1 AND new_enrollment_id IS NOT NULL) FOR UPDATE`, b.id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text, btrim(s.first_name || ' ' || COALESCE(s.last_name, '')), r.decision, r.new_enrollment_id IS NOT NULL,
			COALESCE(ne.deleted_at IS NOT NULL, false), COALESCE(ne.updated_at <> ne.created_at, false), s.status, s.deleted_at IS NOT NULL,
			EXISTS (SELECT 1 FROM promotion_batch_students x JOIN promotion_batches xb ON xb.tenant_id = x.tenant_id AND xb.id = x.batch_id
				WHERE x.source_enrollment_id = r.new_enrollment_id AND xb.status = 'committed' AND xb.deleted_at IS NULL AND xb.id <> r.batch_id)
		FROM promotion_batch_students r
		JOIN students s ON s.tenant_id = r.tenant_id AND s.id = r.student_id
		LEFT JOIN student_enrollments ne ON ne.tenant_id = r.tenant_id AND ne.id = r.new_enrollment_id
		WHERE r.batch_id::text = $1
		ORDER BY lower(s.first_name), lower(COALESCE(s.last_name, '')), s.id`, b.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid, name, decision, status string
		var hasNew, removed, edited, studentGone, onward bool
		if err := rows.Scan(&sid, &name, &decision, &hasNew, &removed, &edited, &status, &studentGone, &onward); err != nil {
			return nil, err
		}
		add := func(code, msg string) {
			reasons = append(reasons, UndoReason{StudentID: sid, StudentName: name, Code: code, Message: msg})
		}
		switch {
		case studentGone:
			add("student_removed", name+" has been removed.")
		case hasNew && removed:
			add("enrolment_removed", "The new enrolment of "+name+" has been removed.")
		case hasNew && edited:
			add("enrolment_changed", "The new enrolment of "+name+" has been edited since the promotion.")
		case hasNew && onward:
			add("promoted_onward", name+" has already been promoted again from the new enrolment.")
		case decision == DecisionGraduate && status != "alumni":
			add("status_changed", "The status of "+name+" has changed from alumni since the promotion.")
		}
	}
	return reasons, rows.Err()
}
