package apiv1

import (
	"context"
	"errors"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/ten/rollover"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// YearRolloverService is what the year rollover operations need (rollover.Service satisfies it).
type YearRolloverService interface {
	Checklist(ctx context.Context, fromYearID, toYearID string) (rollover.Checklist, error)
	CreateYear(ctx context.Context, actor audit.Actor, in rollover.NewYearInput) (years.Year, error)
	Preview(ctx context.Context, actor audit.Actor, in rollover.Input) (rollover.Result, error)
	Run(ctx context.Context, actor audit.Actor, in rollover.Input) (rollover.Result, error)
	History(ctx context.Context, q crud.ListQuery, f rollover.HistoryFilter) (crud.Page[rollover.Run], error)
}

// WithYearRollover enables the /year-rollover operations.
func WithYearRollover(s YearRolloverService) Option { return func(h *Handler) { h.yearRollover = s } }

// mayRollover says whether the caller holds the rollover permission institute-wide. A rollover changes the whole
// school, so a narrower grant is not enough. The permission itself is checked per operation from the contract.
func mayRollover(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && contains(a.Effective.Scopes["ten.year_rollover"], authz.ScopeTenant)
}

// mayMakeCurrent says whether the caller may switch the institute's current year, which the make-current step does.
func mayMakeCurrent(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && a.Effective.Can("ten.year_rollover.edit")
}

func dateOf(s string) openapi_types.Date {
	t, _ := time.Parse("2006-01-02", s) // the service returns ISO dates
	return openapi_types.Date{Time: t}
}

func toGenRolloverYear(y rollover.YearSummary) gen.RolloverYear {
	return gen.RolloverYear{Id: mustUUID(y.ID), Code: y.Code, Name: y.Name, StartDate: dateOf(y.StartDate), EndDate: dateOf(y.EndDate), IsCurrent: y.IsCurrent, Status: gen.RolloverYearStatus(y.Status)}
}

func toGenRolloverShift(s rollover.Shift) gen.RolloverShift {
	return gen.RolloverShift{Mode: gen.RolloverShiftMode(s.Mode), Years: s.Years, Days: s.Days}
}

func toGenRolloverResult(r rollover.Result) gen.RolloverStepResult {
	out := gen.RolloverStepResult{
		Step: gen.RolloverStepId(r.Step), DryRun: r.DryRun, FromYearId: mustUUID(r.FromYearID), ToYearId: mustUUID(r.ToYearID),
		CreatedCount: r.Created, ExistingCount: r.Existing, SkippedCount: r.Skipped, ConflictCount: r.Conflicts, ReviewCount: r.Review,
		Items: make([]gen.RolloverItem, len(r.Items)), ItemsTruncated: r.ItemsTruncated, Warnings: r.Warnings, Blockers: r.Blockers,
		Shift: toGenRolloverShift(r.Shift), RanAt: r.RanAt,
	}
	if r.RunID != nil {
		id := mustUUID(*r.RunID)
		out.RunId = &id
	}
	for i, it := range r.Items {
		out.Items[i] = gen.RolloverItem{Label: it.Label, Status: gen.RolloverItemStatus(it.Status), Reason: it.Reason, Message: it.Message, Detail: it.Detail, Review: it.Review}
	}
	return out
}

func rolloverFieldErrors(err error) (gen.BadRequestApplicationProblemPlusJSONResponse, bool) {
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe)), true
	}
	return gen.BadRequestApplicationProblemPlusJSONResponse{}, false
}

// rolloverBlocked answers a step that cannot run, with the reasons.
func rolloverBlocked(err error) (gen.Problem, bool) {
	var b rollover.Blocked
	if !errors.As(err, &b) {
		return gen.Problem{}, false
	}
	p := problemBody(409, "blocked", "This step cannot run yet: "+strings.Join(b.Reasons, " "))
	list := make([]gen.ProblemFieldError, len(b.Reasons))
	for i, r := range b.Reasons {
		list[i] = gen.ProblemFieldError{Field: "step", Code: "blocked", Message: r}
	}
	p.Errors = &list
	return p, true
}

// GetYearRolloverChecklist implements GET /year-rollover/checklist.
func (h *Handler) GetYearRolloverChecklist(ctx context.Context, req gen.GetYearRolloverChecklistRequestObject) (gen.GetYearRolloverChecklistResponseObject, error) {
	if h.yearRollover == nil {
		return nil, errNotConfigured
	}
	if !mayRollover(ctx) {
		return gen.GetYearRolloverChecklist403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	c, err := h.yearRollover.Checklist(ctx, req.Params.FromYearId.String(), req.Params.ToYearId.String())
	if bad, ok := rolloverFieldErrors(err); ok {
		return gen.GetYearRolloverChecklist400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: bad}, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.RolloverChecklist{From: toGenRolloverYear(c.From), To: toGenRolloverYear(c.To), Shift: toGenRolloverShift(c.Shift), Notes: c.Notes,
		Promotions: gen.RolloverPromotionProgress{Sections: c.Promotions.Sections, Promoted: c.Promotions.Promoted}, Steps: make([]gen.RolloverChecklistStep, len(c.Steps))}
	for i, s := range c.Steps {
		step := gen.RolloverChecklistStep{Id: gen.RolloverStepId(s.ID), Title: s.Title, Description: s.Description, DefaultOn: s.DefaultOn, Irreversible: s.Irreversible,
			SourceCount: s.SourceCount, TargetCount: s.TargetCount}
		if l := s.LastRun; l != nil {
			step.LastRun = &gen.RolloverLastRun{Id: mustUUID(l.ID), RanAt: l.RanAt, RunByName: l.RunByName, CreatedCount: l.Created, ExistingCount: l.Existing,
				SkippedCount: l.Skipped, ConflictCount: l.Conflicts, ReviewCount: l.Review}
		}
		out.Steps[i] = step
	}
	return gen.GetYearRolloverChecklist200JSONResponse(out), nil
}

// CreateRolloverYear implements POST /year-rollover/years.
func (h *Handler) CreateRolloverYear(ctx context.Context, req gen.CreateRolloverYearRequestObject) (gen.CreateRolloverYearResponseObject, error) {
	if h.yearRollover == nil {
		return nil, errNotConfigured
	}
	if !mayRollover(ctx) {
		return gen.CreateRolloverYear403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.CreateRolloverYear400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	b := req.Body
	y, err := h.yearRollover.CreateYear(ctx, actorFrom(ctx), rollover.NewYearInput{FromYearID: b.FromYearId.String(), Code: b.Code, Name: b.Name,
		StartDate: b.StartDate.Format("2006-01-02"), EndDate: b.EndDate.Format("2006-01-02")})
	if bad, ok := rolloverFieldErrors(err); ok {
		return gen.CreateRolloverYear400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: bad}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateRolloverYear201JSONResponse(toGenRolloverYear(rollover.YearSummary{ID: y.ID, Code: y.Code, Name: y.Name, StartDate: y.StartDate, EndDate: y.EndDate, IsCurrent: y.IsCurrent, Status: y.Status})), nil
}

// PreviewRolloverStep implements POST /year-rollover/steps/{step}/preview.
func (h *Handler) PreviewRolloverStep(ctx context.Context, req gen.PreviewRolloverStepRequestObject) (gen.PreviewRolloverStepResponseObject, error) {
	if h.yearRollover == nil {
		return nil, errNotConfigured
	}
	if !mayRollover(ctx) {
		return gen.PreviewRolloverStep403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.PreviewRolloverStep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	r, err := h.yearRollover.Preview(ctx, actorFrom(ctx), rollover.Input{Step: string(req.Step), FromYearID: req.Body.FromYearId.String(), ToYearID: req.Body.ToYearId.String()})
	if bad, ok := rolloverFieldErrors(err); ok {
		return gen.PreviewRolloverStep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: bad}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.PreviewRolloverStep200JSONResponse(toGenRolloverResult(r)), nil
}

// RunRolloverStep implements POST /year-rollover/steps/{step}/run.
func (h *Handler) RunRolloverStep(ctx context.Context, req gen.RunRolloverStepRequestObject) (gen.RunRolloverStepResponseObject, error) {
	if h.yearRollover == nil {
		return nil, errNotConfigured
	}
	if !mayRollover(ctx) || (string(req.Step) == rollover.StepMakeCurrent && !mayMakeCurrent(ctx)) {
		return gen.RunRolloverStep403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.RunRolloverStep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	in := rollover.Input{Step: string(req.Step), FromYearID: req.Body.FromYearId.String(), ToYearID: req.Body.ToYearId.String(), Confirm: req.Body.Confirm != nil && *req.Body.Confirm}
	r, err := h.yearRollover.Run(ctx, actorFrom(ctx), in)
	if bad, ok := rolloverFieldErrors(err); ok {
		return gen.RunRolloverStep400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: bad}, nil
	}
	if p, ok := rolloverBlocked(err); ok {
		return gen.RunRolloverStep409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RunRolloverStep200JSONResponse(toGenRolloverResult(r)), nil
}

// ListYearRolloverRuns implements GET /year-rollover/history.
func (h *Handler) ListYearRolloverRuns(ctx context.Context, req gen.ListYearRolloverRunsRequestObject) (gen.ListYearRolloverRunsResponseObject, error) {
	if h.yearRollover == nil {
		return nil, errNotConfigured
	}
	if !mayRollover(ctx) {
		return gen.ListYearRolloverRuns403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, nil, rollover.HistorySorts, rollover.DefaultHistorySort)
	if qerr != nil {
		return gen.ListYearRolloverRuns400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	p, err := h.yearRollover.History(ctx, q, rollover.HistoryFilter{FromYearID: derefString(uuidStrPtr(req.Params.FromYearId)), ToYearID: derefString(uuidStrPtr(req.Params.ToYearId))})
	if err != nil {
		return nil, err
	}
	out := gen.RolloverRunPage{Items: make([]gen.RolloverRun, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, r := range p.Items {
		out.Items[i] = gen.RolloverRun{Id: mustUUID(r.ID), FromYearId: mustUUID(r.FromYearID), FromYearName: r.FromYearName, ToYearId: mustUUID(r.ToYearID), ToYearName: r.ToYearName,
			Step: gen.RolloverStepId(r.Step), Status: gen.RolloverRunStatus(r.Status), CreatedCount: r.Created, ExistingCount: r.Existing, SkippedCount: r.Skipped,
			ConflictCount: r.Conflicts, ReviewCount: r.Review, Warnings: r.Warnings, Error: r.Error, RunByName: r.RunByName, RanAt: r.RanAt}
	}
	return gen.ListYearRolloverRuns200JSONResponse(out), nil
}
