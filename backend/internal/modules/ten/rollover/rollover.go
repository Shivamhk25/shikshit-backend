// Package rollover implements TEN-01 Year Rollover: setting up the next academic year from the one that is ending.
//
// A rollover goes from year A to year B. It is a checklist of steps, each optional and run on its own: copy the terms,
// the class-section mappings, the subject-class mappings, the holidays and the calendar events from A into B, and make
// B the current year. Every step has a dry-run that executes the very same code in a transaction that is rolled back,
// so the preview and the run cannot disagree. A step never touches a row that already exists in B, so running it again
// copies nothing twice. Each step runs in its own transaction, together with its audit row and its log row.
//
// Students are not moved here: promotions (STU-07) enrol them in B once B has its class sections.
package rollover

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Step ids.
const (
	StepTerms          = "terms"
	StepClassSections  = "class_sections"
	StepSubjectClasses = "subject_classes"
	StepHolidays       = "holidays"
	StepCalendarEvents = "calendar_events"
	StepMakeCurrent    = "make_current"
)

// Item statuses.
const (
	ItemCreate   = "create"   // copied, or would be copied by a dry-run
	ItemExists   = "exists"   // B already has it, left alone
	ItemConflict = "conflict" // cannot be copied; the reason says why
	ItemSkipped  = "skipped"  // deliberately not copied (a holiday that repeats every year)
)

// MaxItems is how many items one result lists. The counts always cover every row.
const MaxItems = 300

// StepInfo describes one step of the checklist. The server owns the wording so every client shows the same.
type StepInfo struct {
	ID          string
	Title       string
	Description string
	// DefaultOn says whether the step is ticked when the wizard opens. Calendar events are not: they are one-off plans.
	DefaultOn bool
	// Irreversible marks the step the school cannot simply undo; it needs an explicit confirmation to run.
	Irreversible bool
}

// Steps is the checklist in the order it is shown and should be worked through.
var Steps = []StepInfo{
	{ID: StepTerms, Title: "Terms", DefaultOn: true,
		Description: "Copy each term with its dates moved into the new year. A term is refused when its moved dates fall outside the new year or overlap a term it already has."},
	{ID: StepClassSections, Title: "Class sections", DefaultOn: true,
		Description: "Map the same sections to the same classes, with their capacity and room. Do this before the subject mappings and before promoting students."},
	{ID: StepSubjectClasses, Title: "Class subjects", DefaultOn: true,
		Description: "Map the same subjects to the same classes (and sections). A subject set for one section needs that section mapped to the class in the new year first."},
	{ID: StepHolidays, Title: "Holidays", DefaultOn: true,
		Description: "Copy the holidays with dates moved by the year shift. Festival dates change every year, so public, regional and restricted holidays are marked for you to review. Holidays that repeat every year are not copied; they already apply."},
	{ID: StepCalendarEvents, Title: "Calendar events", DefaultOn: false,
		Description: "Copy the academic calendar events with dates moved by the year shift. Off by default: events are usually planned afresh."},
	{ID: StepMakeCurrent, Title: "Make the new year current", DefaultOn: false, Irreversible: true,
		Description: "Switch the institute to the new year. Attendance, fees, admissions and every screen that defaults to the current year start using it at once. Needs terms and class sections in the new year."},
}

// Notes are shown beside the checklist: what is deliberately not part of it.
var Notes = []string{
	"Elective groups, textbooks and chapters are not tied to an academic year, so there is nothing to copy; they carry over as they are.",
	"Students are not moved here. Promote each class section on the Promotions screen once the new year has its class sections.",
}

// StepByID returns a step's description.
func StepByID(id string) (StepInfo, bool) {
	for _, s := range Steps {
		if s.ID == id {
			return s, true
		}
	}
	return StepInfo{}, false
}

// Item is one row a step looked at.
type Item struct {
	Label   string
	Status  string
	Reason  string // a stable code when Status is conflict or skipped
	Message string
	// Detail says what changes, such as the old and new dates.
	Detail string
	// Review marks a copied row the school should check afterwards (a holiday that may move).
	Review bool
}

// Result is what a step did, or, for a dry-run, would do.
type Result struct {
	Step       string
	DryRun     bool
	FromYearID string
	ToYearID   string
	Created    int
	Existing   int
	Conflicts  int
	Skipped    int
	Review     int
	Items      []Item
	// ItemsTruncated is true when more rows were looked at than Items lists; the counts still cover all of them.
	ItemsTruncated bool
	Warnings       []string
	// Blockers are reasons the step cannot run at all (only the make-current step has any).
	Blockers []string
	Shift    Shift
	// RunID and RanAt are set once the step has run for real.
	RunID *string
	RanAt *time.Time
}

// Input says which step to run between which years.
type Input struct {
	Step       string
	FromYearID string
	ToYearID   string
	// Confirm must be true to run a step marked irreversible.
	Confirm bool
}

// Blocked is returned when a step cannot run, with the reasons.
type Blocked struct{ Reasons []string }

func (b Blocked) Error() string { return "rollover: " + strings.Join(b.Reasons, "; ") }

// Service reads and writes the rollover.
type Service struct{ DB tenancy.Beginner }

type yearRow struct {
	ID, Code, Name, Status string
	Start, End             time.Time
	IsCurrent              bool
}

func loadYear(ctx context.Context, tx pgx.Tx, id string) (yearRow, bool, error) {
	var y yearRow
	err := tx.QueryRow(ctx, `SELECT id::text, code, name, status, start_date, end_date, is_current FROM academic_years WHERE id::text = $1 AND deleted_at IS NULL`, id).
		Scan(&y.ID, &y.Code, &y.Name, &y.Status, &y.Start, &y.End, &y.IsCurrent)
	if errors.Is(err, pgx.ErrNoRows) {
		return y, false, nil
	}
	return y, err == nil, err
}

// pair is the two years of a rollover, checked.
type pair struct {
	A, B  yearRow
	Shift Shift
}

// loadPair checks the ids and the years: both exist, they differ, and B starts after A.
func loadPair(ctx context.Context, tx pgx.Tx, fromID, toID string) (pair, error) {
	var fe crud.FieldErrors
	for _, f := range []struct{ field, v string }{{"fromYearId", fromID}, {"toYearId", toID}} {
		if !refs.IsUUID(f.v) {
			fe.Add(f.field, "invalid", "Choose an academic year.")
		}
	}
	if len(fe) > 0 {
		return pair{}, fe
	}
	if fromID == toID {
		fe.Add("toYearId", "same_year", "Choose a different year to roll over into.")
		return pair{}, fe
	}
	var p pair
	var ok bool
	var err error
	if p.A, ok, err = loadYear(ctx, tx, fromID); err != nil {
		return pair{}, err
	} else if !ok {
		fe.Add("fromYearId", "not_found", "This academic year does not exist.")
	}
	if p.B, ok, err = loadYear(ctx, tx, toID); err != nil {
		return pair{}, err
	} else if !ok {
		fe.Add("toYearId", "not_found", "This academic year does not exist.")
	}
	if len(fe) > 0 {
		return pair{}, fe
	}
	if !p.B.Start.After(p.A.Start) {
		fe.Add("toYearId", "not_after", "The new year must start after the year being rolled over.")
		return pair{}, fe
	}
	p.Shift = ComputeShift(p.A.Start, p.B.Start)
	return p, nil
}

func (r *Result) add(it Item) {
	switch it.Status {
	case ItemCreate:
		r.Created++
		if it.Review {
			r.Review++
		}
	case ItemExists:
		r.Existing++
	case ItemConflict:
		r.Conflicts++
	case ItemSkipped:
		r.Skipped++
	}
	r.Items = append(r.Items, it)
}

// finish orders the items so what needs attention comes first, and caps the list.
func (r *Result) finish() {
	rank := map[string]int{ItemConflict: 0, ItemCreate: 1, ItemSkipped: 2, ItemExists: 3}
	sort.SliceStable(r.Items, func(i, j int) bool {
		a, b := r.Items[i], r.Items[j]
		if rank[a.Status] != rank[b.Status] {
			return rank[a.Status] < rank[b.Status]
		}
		return a.Review && !b.Review
	})
	if len(r.Items) > MaxItems {
		r.Items, r.ItemsTruncated = r.Items[:MaxItems], true
	}
	if r.Items == nil {
		r.Items = []Item{}
	}
	if r.Warnings == nil {
		r.Warnings = []string{}
	}
	if r.Blockers == nil {
		r.Blockers = []string{}
	}
}

// lock serialises rollover work into one year, so two people running the same step cannot both copy.
func lock(ctx context.Context, tx pgx.Tx, toYearID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('year_rollover:' || current_setting('app.tenant_id') || ':' || $1))`, toYearID)
	return err
}

// execute runs one step inside tx. A dry-run calls it too, then rolls the transaction back.
func execute(ctx context.Context, tx pgx.Tx, in Input, actorID string, dry bool) (Result, error) {
	if _, ok := StepByID(in.Step); !ok {
		var fe crud.FieldErrors
		fe.Add("step", "invalid", "Choose one of the steps in the checklist.")
		return Result{}, fe
	}
	p, err := loadPair(ctx, tx, in.FromYearID, in.ToYearID)
	if err != nil {
		return Result{}, err
	}
	if err := lock(ctx, tx, p.B.ID); err != nil {
		return Result{}, err
	}
	res := Result{Step: in.Step, DryRun: dry, FromYearID: p.A.ID, ToYearID: p.B.ID, Shift: p.Shift}
	switch in.Step {
	case StepTerms:
		err = copyTerms(ctx, tx, p, actorID, &res)
	case StepClassSections:
		err = copyClassSections(ctx, tx, p, actorID, &res)
	case StepSubjectClasses:
		err = copySubjectClasses(ctx, tx, p, actorID, &res)
	case StepHolidays:
		err = copyHolidays(ctx, tx, p, actorID, &res)
	case StepCalendarEvents:
		err = copyEvents(ctx, tx, p, actorID, &res)
	case StepMakeCurrent:
		err = makeCurrent(ctx, tx, p, actorID, &res)
	}
	if err != nil {
		return Result{}, err
	}
	res.finish()
	return res, nil
}

var errRollback = errors.New("rollover: dry-run rolled back")

// Preview runs a step as a dry-run: the same code as Run, in a transaction that is rolled back, so nothing is kept.
func (s Service) Preview(ctx context.Context, actor audit.Actor, in Input) (Result, error) {
	var res Result
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if res, err = execute(ctx, tx, in, actor.UserID, true); err != nil {
			return err
		}
		return errRollback
	})
	if errors.Is(err, errRollback) {
		return res, nil
	}
	return Result{}, err
}

// Run runs one step for real, in its own transaction with its log row and its audit row. A step that is marked
// irreversible needs in.Confirm. A step that fails changes nothing; the failure is logged on its own.
func (s Service) Run(ctx context.Context, actor audit.Actor, in Input) (Result, error) {
	if info, ok := StepByID(in.Step); ok && info.Irreversible && !in.Confirm {
		var fe crud.FieldErrors
		fe.Add("confirm", "required", "Confirm that you want to run this step. It cannot be undone from here.")
		return Result{}, fe
	}
	var res Result
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if res, err = execute(ctx, tx, in, actor.UserID, false); err != nil {
			return err
		}
		if len(res.Blockers) > 0 {
			return Blocked{Reasons: res.Blockers}
		}
		warnings := res.Warnings
		var id string
		var at time.Time
		if err := tx.QueryRow(ctx, `INSERT INTO year_rollover_runs (tenant_id, from_year_id, to_year_id, step, status, created_count, existing_count, skipped_count, conflict_count, review_count, warnings, run_by)
			VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3, 'completed', $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid) RETURNING id::text, run_at`,
			res.FromYearID, res.ToYearID, res.Step, res.Created, res.Existing, res.Skipped, res.Conflicts, res.Review, warnings, actor.UserID).Scan(&id, &at); err != nil {
			return err
		}
		res.RunID, res.RanAt = &id, &at
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "year_rollover." + res.Step, ResourceType: "year_rollover", ResourceID: id,
			After: map[string]any{"fromYearId": res.FromYearID, "toYearId": res.ToYearID, "created": res.Created, "existing": res.Existing, "skipped": res.Skipped,
				"conflicts": res.Conflicts, "review": res.Review, "warnings": warnings}})
	})
	if err != nil {
		var fe crud.FieldErrors
		if !errors.As(err, &fe) {
			s.recordFailure(ctx, actor, in, err)
		}
		return Result{}, err
	}
	return res, nil
}

// recordFailure logs a step that did not run, in a transaction of its own because the step's was rolled back. It is
// best effort: when the years do not exist the row cannot be written, and that is fine.
func (s Service) recordFailure(ctx context.Context, actor audit.Actor, in Input, cause error) {
	msg := "The step failed and nothing was changed."
	var b Blocked
	if errors.As(cause, &b) {
		msg = "Blocked: " + strings.Join(b.Reasons, " ")
	}
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_ = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO year_rollover_runs (tenant_id, from_year_id, to_year_id, step, status, error, run_by)
			VALUES (app_current_tenant(), $1::uuid, $2::uuid, $3, 'failed', $4, NULLIF($5, '')::uuid)`, in.FromYearID, in.ToYearID, in.Step, msg, actor.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "year_rollover." + in.Step + ".failed", ResourceType: "year_rollover",
			After: map[string]any{"fromYearId": in.FromYearID, "toYearId": in.ToYearID, "error": msg}})
	})
}

// NewYearInput is what the wizard sends to create the year it rolls into.
type NewYearInput struct {
	FromYearID string
	Code       string
	Name       string
	StartDate  string
	EndDate    string
}

// CreateYear creates the new year inside the rollover: the same rules as the Academic years screen (no overlap with
// another year, valid dates and code), plus the new year must start after the year being rolled over. It is not made
// current; that is a separate step of the checklist.
func (s Service) CreateYear(ctx context.Context, actor audit.Actor, in NewYearInput) (years.Year, error) {
	var out years.Year
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var fe crud.FieldErrors
		if !refs.IsUUID(in.FromYearID) {
			fe.Add("fromYearId", "invalid", "Choose an academic year.")
			return fe
		}
		a, ok, err := loadYear(ctx, tx, in.FromYearID)
		if err != nil {
			return err
		}
		if !ok {
			fe.Add("fromYearId", "not_found", "This academic year does not exist.")
			return fe
		}
		yin, err := years.Normalise(years.Input{Code: in.Code, Name: in.Name, StartDate: in.StartDate, EndDate: in.EndDate})
		if err != nil {
			return err
		}
		if yin.StartDate <= iso(a.Start) {
			fe.Add("startDate", "not_after", fmt.Sprintf("The new year must start after %s, when %s starts.", iso(a.Start), a.Name))
			return fe
		}
		out, err = years.Service{DB: s.DB}.Store().CreateIn(ctx, tx, actor, yin)
		return err
	})
	return out, err
}
