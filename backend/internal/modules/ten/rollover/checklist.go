package rollover

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// YearSummary is one end of the rollover.
type YearSummary struct {
	ID        string
	Code      string
	Name      string
	StartDate string
	EndDate   string
	IsCurrent bool
	Status    string
}

// LastRun is the latest completed run of a step between the two years.
type LastRun struct {
	ID        string
	RanAt     time.Time
	RunByName *string
	Created   int
	Existing  int
	Skipped   int
	Conflicts int
	Review    int
}

// ChecklistStep is a step with what is in each year and when it last ran.
type ChecklistStep struct {
	StepInfo
	// SourceCount and TargetCount are the live rows the step reads in year A and finds in year B. Make-current has none.
	SourceCount int
	TargetCount int
	LastRun     *LastRun
}

// PromotionProgress says how far the students have been moved into the new year.
type PromotionProgress struct {
	Sections int
	Promoted int
}

// Checklist is everything the wizard shows for a pair of years.
type Checklist struct {
	From       YearSummary
	To         YearSummary
	Shift      Shift
	Steps      []ChecklistStep
	Promotions PromotionProgress
	Notes      []string
}

func summary(y yearRow) YearSummary {
	return YearSummary{ID: y.ID, Code: y.Code, Name: y.Name, StartDate: iso(y.Start), EndDate: iso(y.End), IsCurrent: y.IsCurrent, Status: y.Status}
}

// Checklist returns the steps with row counts for both years, the last run of each, and the promotion progress.
func (s Service) Checklist(ctx context.Context, fromYearID, toYearID string) (Checklist, error) {
	var out Checklist
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		p, err := loadPair(ctx, tx, fromYearID, toYearID)
		if err != nil {
			return err
		}
		out = Checklist{From: summary(p.A), To: summary(p.B), Shift: p.Shift, Notes: Notes}
		counts := map[string][2]int{}
		for _, c := range []struct{ step, table string }{
			{StepTerms, "academic_terms"}, {StepClassSections, "class_section_mappings"}, {StepSubjectClasses, "subject_class_mappings"},
			{StepHolidays, "holidays"}, {StepCalendarEvents, "calendar_events"},
		} {
			var a, b int
			if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE year_id::text = $1), count(*) FILTER (WHERE year_id::text = $2) FROM `+c.table+` WHERE deleted_at IS NULL`,
				p.A.ID, p.B.ID).Scan(&a, &b); err != nil {
				return err
			}
			counts[c.step] = [2]int{a, b}
		}
		last := map[string]*LastRun{}
		if err := queryAll(ctx, tx, `SELECT DISTINCT ON (r.step) r.step, r.id::text, r.run_at, u.display_name, r.created_count, r.existing_count, r.skipped_count, r.conflict_count, r.review_count
			FROM year_rollover_runs r LEFT JOIN users u ON u.tenant_id = r.tenant_id AND u.id = r.run_by
			WHERE r.from_year_id::text = $1 AND r.to_year_id::text = $2 AND r.status = 'completed'
			ORDER BY r.step, r.run_at DESC`, []any{p.A.ID, p.B.ID}, func(r pgx.Rows) error {
			var step string
			var l LastRun
			err := r.Scan(&step, &l.ID, &l.RanAt, &l.RunByName, &l.Created, &l.Existing, &l.Skipped, &l.Conflicts, &l.Review)
			last[step] = &l
			return err
		}); err != nil {
			return err
		}
		for _, info := range Steps {
			out.Steps = append(out.Steps, ChecklistStep{StepInfo: info, SourceCount: counts[info.ID][0], TargetCount: counts[info.ID][1], LastRun: last[info.ID]})
		}
		out.Promotions.Sections, out.Promotions.Promoted, err = promotionProgress(ctx, tx, p.A.ID, p.B.ID)
		return err
	})
	return out, err
}

// Run is one logged step in the history.
type Run struct {
	ID           string
	FromYearID   string
	FromYearName string
	ToYearID     string
	ToYearName   string
	Step         string
	Status       string
	Created      int
	Existing     int
	Skipped      int
	Conflicts    int
	Review       int
	Warnings     []string
	Error        *string
	RunByName    *string
	RanAt        time.Time
}

// HistoryFilter narrows the history.
type HistoryFilter struct{ FromYearID, ToYearID string }

// HistorySorts are the sort keys clients may use; DefaultHistorySort shows the latest first.
var HistorySorts = crud.Sorts{"ranAt": "r.run_at", "step": "r.step", "status": "r.status"}

// DefaultHistorySort is the newest run first.
const DefaultHistorySort = "-ranAt"

// History lists the logged steps, newest first.
func (s Service) History(ctx context.Context, q crud.ListQuery, f HistoryFilter) (crud.Page[Run], error) {
	out := crud.Page[Run]{Page: q.Page, Size: q.Size, Items: []Run{}}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		where, args := `TRUE`, []any{}
		for _, c := range []struct{ col, v string }{{"r.from_year_id", f.FromYearID}, {"r.to_year_id", f.ToYearID}} {
			if c.v != "" {
				args = append(args, c.v)
				where += " AND " + c.col + "::text = $" + strconv.Itoa(len(args))
			}
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM year_rollover_runs r WHERE `+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		args = append(args, q.Size, q.Offset())
		return queryAll(ctx, tx, `SELECT r.id::text, r.from_year_id::text, fy.name, r.to_year_id::text, ty.name, r.step, r.status, r.created_count, r.existing_count, r.skipped_count,
				r.conflict_count, r.review_count, r.warnings, r.error, u.display_name, r.run_at
			FROM year_rollover_runs r
			JOIN academic_years fy ON fy.tenant_id = r.tenant_id AND fy.id = r.from_year_id
			JOIN academic_years ty ON ty.tenant_id = r.tenant_id AND ty.id = r.to_year_id
			LEFT JOIN users u ON u.tenant_id = r.tenant_id AND u.id = r.run_by
			WHERE `+where+` ORDER BY `+q.OrderBy+`, r.id LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args, func(r pgx.Rows) error {
			var x Run
			err := r.Scan(&x.ID, &x.FromYearID, &x.FromYearName, &x.ToYearID, &x.ToYearName, &x.Step, &x.Status, &x.Created, &x.Existing, &x.Skipped, &x.Conflicts, &x.Review, &x.Warnings, &x.Error, &x.RunByName, &x.RanAt)
			if x.Warnings == nil {
				x.Warnings = []string{}
			}
			out.Items = append(out.Items, x)
			return err
		})
	})
	return out, err
}
