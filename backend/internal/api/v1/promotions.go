package apiv1

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/promotions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// PromotionService is what the promotions operations need (promotions.Service satisfies it).
type PromotionService interface {
	List(ctx context.Context, actor audit.Actor, q crud.ListQuery, f promotions.Filter) (crud.Page[promotions.Batch], error)
	View(ctx context.Context, actor audit.Actor, id string) (promotions.Detail, error)
	Candidates(ctx context.Context, actor audit.Actor, fromYearID, toYearID, classID, sectionID string) (promotions.Proposal, error)
	Create(ctx context.Context, actor audit.Actor, in promotions.Input) (promotions.Detail, error)
	Update(ctx context.Context, actor audit.Actor, id string, in promotions.UpdateInput) (promotions.Detail, error)
	Discard(ctx context.Context, actor audit.Actor, id string) error
	Preview(ctx context.Context, actor audit.Actor, id string) (promotions.Outcome, error)
	Commit(ctx context.Context, actor audit.Actor, id string, override *promotions.DuesOverride) (promotions.Outcome, error)
	Undo(ctx context.Context, actor audit.Actor, id, reason string) (promotions.Outcome, error)
}

// WithPromotions enables the /promotions operations.
func WithPromotions(s PromotionService) Option { return func(h *Handler) { h.promotions = s } }

// mayPromote says whether the caller holds the promotions permission institute-wide. A promotion moves whole classes,
// so a class-section grant is not enough. The permission itself (view, create, edit, delete) is checked per operation
// from the contract.
func mayPromote(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && contains(a.Effective.Scopes["stu.student_promotions"], authz.ScopeTenant)
}

// mayChangeStudents says whether the caller may do what a commit or an undo does besides the batch itself: create
// and remove enrolments and change a student's status, institute-wide, as the students screen requires.
func mayChangeStudents(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && a.Effective.Can("stu.students.create") && a.Effective.Can("stu.students.edit") && contains(a.Effective.Scopes["stu.students"], authz.ScopeTenant)
}

func badBody(msg string) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", msg))
}

func notFoundBody() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

func toGenPromotionBatch(b promotions.Batch) gen.PromotionBatch {
	return gen.PromotionBatch{
		Id: mustUUID(b.ID), FromYearId: mustUUID(b.FromYearID), FromYearName: b.FromYearName, ToYearId: mustUUID(b.ToYearID), ToYearName: b.ToYearName,
		FromClassId: mustUUID(b.FromClassID), FromClassName: b.FromClassName, FromSectionId: mustUUID(b.FromSectionID), FromSectionName: b.FromSectionName,
		RollMode: gen.PromotionBatchRollMode(b.RollMode), Note: b.Note, Status: gen.PromotionBatchStatus(b.Status),
		StudentCount: b.Students, PromoteCount: b.Promote, RepeatCount: b.Repeat, GraduateCount: b.Graduate, LeaveOutCount: b.LeaveOut,
		CommittedAt: b.CommittedAt, CommittedByName: b.CommittedByName, UndoUntil: b.UndoUntil, CanUndo: b.CanUndo, DuesOverrideReason: b.DuesOverrideReason,
		UndoneAt: b.UndoneAt, UndoneByName: b.UndoneByName, UndoReason: b.UndoReason, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt,
	}
}

func toGenPromotionDetail(d promotions.Detail) gen.PromotionDetail {
	out := gen.PromotionDetail{Batch: toGenPromotionBatch(d.Batch), Rows: make([]gen.PromotionRow, len(d.Rows))}
	for i, r := range d.Rows {
		out.Rows[i] = gen.PromotionRow{
			StudentId: mustUUID(r.StudentID), RegistrationCode: r.RegistrationCode, StudentName: r.StudentName, StudentStatus: gen.PromotionRowStudentStatus(r.StudentStatus),
			EnrollmentId: mustUUID(r.EnrollmentID), RollNumber: r.RollNumber, Decision: gen.PromotionRowDecision(r.Decision),
			ToClassId: uuidPtr(r.ToClassID), ToClassName: r.ToClassName, ToSectionId: uuidPtr(r.ToSectionID), ToSectionName: r.ToSectionName, NewRollNumber: r.NewRollNumber,
			NewEnrollmentId: uuidPtr(r.NewEnrollmentID), EnrolledClassName: r.EnrolledClass, EnrolledSectionName: r.EnrolledSection, EnrolledRollNumber: r.EnrolledRoll,
		}
	}
	return out
}

func toGenPromotionOutcome(o promotions.Outcome) gen.PromotionOutcome {
	out := gen.PromotionOutcome{
		Batch: toGenPromotionBatch(o.Batch), Rows: make([]gen.PromotionOutcomeRow, len(o.Rows)), Committed: o.Committed, AlreadyCommitted: o.AlreadyCommitted,
		Undone: o.Undone, AlreadyUndone: o.AlreadyUndone, EnrolledCount: o.Enrolled, GraduatedCount: o.Graduated, LeftOutCount: o.LeftOut, ProblemCount: o.ProblemCount,
		Warnings: o.Warnings, DuesOverridden: o.DuesOverridden,
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	for i, r := range o.Rows {
		row := gen.PromotionOutcomeRow{
			StudentId: mustUUID(r.StudentID), RegistrationCode: r.RegistrationCode, StudentName: r.StudentName, Decision: gen.PromotionOutcomeRowDecision(r.Decision),
			Outcome: gen.PromotionOutcomeRowOutcome(r.Outcome), ToClassId: uuidPtr(r.ToClassID), ToClassName: r.ToClassName, ToSectionId: uuidPtr(r.ToSectionID),
			ToSectionName: r.ToSectionName, RollNumber: r.RollNumber, EnrollmentId: uuidPtr(r.EnrollmentID), Problems: make([]gen.PromotionProblem, len(r.Problems)),
		}
		for j, p := range r.Problems {
			row.Problems[j] = gen.PromotionProblem{Field: p.Field, Code: p.Code, Message: p.Message}
		}
		out.Rows[i] = row
	}
	return out
}

func promotionRowInputs(rows []gen.PromotionRowInput) []promotions.RowInput {
	out := make([]promotions.RowInput, len(rows))
	for i, r := range rows {
		out[i] = promotions.RowInput{StudentID: r.StudentId.String(), Decision: string(r.Decision), ToClassID: uuidStrPtr(r.ToClassId), ToSectionID: uuidStrPtr(r.ToSectionId), RollNumber: r.RollNumber}
	}
	return out
}

func rollModeOf[T ~string](m *T) string {
	if m == nil {
		return ""
	}
	return string(*m)
}

func tooManyStudents() gen.BadRequestApplicationProblemPlusJSONResponse {
	return badBody(fmt.Sprintf("Send between 1 and %d students.", promotions.MaxRows))
}

// promotionConflict answers the errors of a promotion action that are a 409, with the reasons as field errors.
func promotionConflict(err error) (gen.Problem, bool) {
	var nr promotions.NotReady
	var ur promotions.UndoRefused
	switch {
	case errors.Is(err, promotions.ErrNotDraft):
		return problemBody(http.StatusConflict, "not_draft", "This promotion is no longer a draft, so it cannot be changed. Undo it if it was a mistake."), true
	case errors.Is(err, promotions.ErrUndone):
		return problemBody(http.StatusConflict, "undone", "This promotion was undone. Start a new one for the class section."), true
	case errors.Is(err, promotions.ErrNotCommitted):
		return problemBody(http.StatusConflict, "not_committed", "This promotion has not been committed, so there is nothing to undo."), true
	case errors.As(err, &nr):
		p := problemBody(http.StatusConflict, "has_problems", fmt.Sprintf("%d %s a problem. Nothing was changed. Preview the promotion, then fix the rows or leave those students out.", nr.Outcome.ProblemCount, problemWord(nr.Outcome.ProblemCount)))
		list := []gen.ProblemFieldError{}
		for _, r := range nr.Outcome.Rows {
			for _, pr := range r.Problems {
				list = append(list, gen.ProblemFieldError{Field: r.StudentID, Code: pr.Code, Message: r.StudentName + ": " + pr.Message})
			}
		}
		p.Errors = &list
		return p, true
	case errors.As(err, &ur):
		p := problemBody(http.StatusConflict, "cannot_undo", "This promotion can no longer be undone. Nothing was changed.")
		list := make([]gen.ProblemFieldError, len(ur.Reasons))
		for i, r := range ur.Reasons {
			list[i] = gen.ProblemFieldError{Field: r.StudentID, Code: r.Code, Message: r.Message}
		}
		p.Errors = &list
		return p, true
	}
	return gen.Problem{}, false
}

func problemWord(n int) string {
	if n == 1 {
		return "student has"
	}
	return "students have"
}

// ListPromotions implements GET /promotions.
func (h *Handler) ListPromotions(ctx context.Context, req gen.ListPromotionsRequestObject) (gen.ListPromotionsResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.ListPromotions403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, nil, promotions.SortKeys, promotions.DefaultSort)
	if qerr != nil {
		return gen.ListPromotions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	var f promotions.Filter
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		if f.Status != promotions.StatusDraft && f.Status != promotions.StatusCommitted && f.Status != promotions.StatusUndone {
			var fe crud.FieldErrors
			fe.Add("status", "invalid", "Status must be draft, committed or undone.")
			return gen.ListPromotions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
	}
	f.FromYearID, f.ToYearID, f.ClassID = derefString(uuidStrPtr(req.Params.FromYearId)), derefString(uuidStrPtr(req.Params.ToYearId)), derefString(uuidStrPtr(req.Params.ClassId))
	p, err := h.promotions.List(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.PromotionBatchPage{Items: make([]gen.PromotionBatch, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, b := range p.Items {
		out.Items[i] = toGenPromotionBatch(b)
	}
	return gen.ListPromotions200JSONResponse(out), nil
}

// GetPromotionProposal implements GET /promotions/proposal.
func (h *Handler) GetPromotionProposal(ctx context.Context, req gen.GetPromotionProposalRequestObject) (gen.GetPromotionProposalResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.GetPromotionProposal403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	p := req.Params
	prop, err := h.promotions.Candidates(ctx, actorFrom(ctx), p.FromYearId.String(), p.ToYearId.String(), p.ClassId.String(), p.SectionId.String())
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.GetPromotionProposal400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.PromotionProposal{
		FromYearId: mustUUID(prop.FromYearID), FromYearName: prop.FromYearName, ToYearId: mustUUID(prop.ToYearID), ToYearName: prop.ToYearName,
		ClassId: mustUUID(prop.ClassID), ClassName: prop.ClassName, SectionId: mustUUID(prop.SectionID), SectionName: prop.SectionName,
		NextClassId: uuidPtr(prop.NextClassID), NextClassName: prop.NextClassName, IsLastClass: prop.IsLastClass, ExistingBatchId: uuidPtr(prop.ExistingBatchID),
		Candidates: make([]gen.PromotionCandidate, len(prop.Candidates)),
	}
	if prop.ExistingBatchStatus != nil {
		st := gen.PromotionProposalExistingBatchStatus(*prop.ExistingBatchStatus)
		out.ExistingBatchStatus = &st
	}
	for i, c := range prop.Candidates {
		out.Candidates[i] = gen.PromotionCandidate{StudentId: mustUUID(c.StudentID), RegistrationCode: c.RegistrationCode, StudentName: c.StudentName,
			StudentStatus: gen.PromotionCandidateStudentStatus(c.StudentStatus), EnrollmentId: mustUUID(c.EnrollmentID), RollNumber: c.RollNumber,
			Decision: gen.PromotionCandidateDecision(c.Decision), ToClassId: uuidPtr(c.ToClassID), ToClassName: c.ToClassName}
	}
	return gen.GetPromotionProposal200JSONResponse(out), nil
}

// CreatePromotion implements POST /promotions.
func (h *Handler) CreatePromotion(ctx context.Context, req gen.CreatePromotionRequestObject) (gen.CreatePromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.CreatePromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.CreatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	b := req.Body
	d, err := h.promotions.Create(ctx, actorFrom(ctx), promotions.Input{FromYearID: b.FromYearId.String(), ToYearID: b.ToYearId.String(), FromClassID: b.FromClassId.String(),
		FromSectionID: b.FromSectionId.String(), RollMode: rollModeOf(b.RollMode), Note: b.Note, Rows: promotionRowInputs(b.Rows)})
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.CreatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, promotions.ErrTooMany):
		return gen.CreatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyStudents()}, nil
	case err != nil:
		return nil, err
	}
	return gen.CreatePromotion201JSONResponse(toGenPromotionDetail(d)), nil
}

// GetPromotion implements GET /promotions/{batchId}.
func (h *Handler) GetPromotion(ctx context.Context, req gen.GetPromotionRequestObject) (gen.GetPromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.GetPromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	d, err := h.promotions.View(ctx, actorFrom(ctx), req.BatchId.String())
	if errors.Is(err, resource.ErrNotFound) {
		return gen.GetPromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetPromotion200JSONResponse(toGenPromotionDetail(d)), nil
}

// UpdatePromotion implements PUT /promotions/{batchId}.
func (h *Handler) UpdatePromotion(ctx context.Context, req gen.UpdatePromotionRequestObject) (gen.UpdatePromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.UpdatePromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.UpdatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	b := req.Body
	d, err := h.promotions.Update(ctx, actorFrom(ctx), req.BatchId.String(), promotions.UpdateInput{RollMode: rollModeOf(b.RollMode), Note: b.Note, Rows: promotionRowInputs(b.Rows)})
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.UpdatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, promotions.ErrTooMany):
		return gen.UpdatePromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyStudents()}, nil
	case errors.Is(err, resource.ErrNotFound):
		return gen.UpdatePromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if p, ok := promotionConflict(err); ok {
		return gen.UpdatePromotion409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdatePromotion200JSONResponse(toGenPromotionDetail(d)), nil
}

// DiscardPromotion implements DELETE /promotions/{batchId}.
func (h *Handler) DiscardPromotion(ctx context.Context, req gen.DiscardPromotionRequestObject) (gen.DiscardPromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.DiscardPromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	err := h.promotions.Discard(ctx, actorFrom(ctx), req.BatchId.String())
	if errors.Is(err, resource.ErrNotFound) {
		return gen.DiscardPromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if p, ok := promotionConflict(err); ok {
		return gen.DiscardPromotion409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DiscardPromotion204Response{}, nil
}

// PreviewPromotion implements POST /promotions/{batchId}/preview.
func (h *Handler) PreviewPromotion(ctx context.Context, req gen.PreviewPromotionRequestObject) (gen.PreviewPromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) {
		return gen.PreviewPromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	o, err := h.promotions.Preview(ctx, actorFrom(ctx), req.BatchId.String())
	if errors.Is(err, resource.ErrNotFound) {
		return gen.PreviewPromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if p, ok := promotionConflict(err); ok {
		return gen.PreviewPromotion409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.PreviewPromotion200JSONResponse(toGenPromotionOutcome(o)), nil
}

// CommitPromotion implements POST /promotions/{batchId}/commit.
func (h *Handler) CommitPromotion(ctx context.Context, req gen.CommitPromotionRequestObject) (gen.CommitPromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) || !mayChangeStudents(ctx) {
		return gen.CommitPromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	// the dues override is not offered over HTTP until the fee module adds its permission
	o, err := h.promotions.Commit(ctx, actorFrom(ctx), req.BatchId.String(), nil)
	if errors.Is(err, resource.ErrNotFound) {
		return gen.CommitPromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if p, ok := promotionConflict(err); ok {
		return gen.CommitPromotion409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CommitPromotion200JSONResponse(toGenPromotionOutcome(o)), nil
}

// UndoPromotion implements POST /promotions/{batchId}/undo.
func (h *Handler) UndoPromotion(ctx context.Context, req gen.UndoPromotionRequestObject) (gen.UndoPromotionResponseObject, error) {
	if h.promotions == nil {
		return nil, errNotConfigured
	}
	if !mayPromote(ctx) || !mayChangeStudents(ctx) {
		return gen.UndoPromotion403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.UndoPromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badBody("A request body is required.")}, nil
	}
	o, err := h.promotions.Undo(ctx, actorFrom(ctx), req.BatchId.String(), req.Body.Reason)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.UndoPromotion400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, resource.ErrNotFound):
		return gen.UndoPromotion404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFoundBody()}, nil
	}
	if p, ok := promotionConflict(err); ok {
		return gen.UndoPromotion409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UndoPromotion200JSONResponse(toGenPromotionOutcome(o)), nil
}
