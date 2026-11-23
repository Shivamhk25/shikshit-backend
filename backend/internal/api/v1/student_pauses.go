package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/pauses"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// StudentPauseService is what the student pause operations need (pauses.Service satisfies it).
type StudentPauseService interface {
	ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f pauses.Filter) (crud.Page[pauses.Pause], error)
	View(ctx context.Context, a audit.Actor, id string) (pauses.Pause, error)
	Create(ctx context.Context, a audit.Actor, in pauses.Input) (pauses.Pause, error)
	Update(ctx context.Context, a audit.Actor, id string, in pauses.Input) (pauses.Pause, error)
	End(ctx context.Context, a audit.Actor, id string, endedOn, note *string) (pauses.Pause, error)
	Cancel(ctx context.Context, a audit.Actor, id string, note *string) (pauses.Pause, error)
}

// WithStudentPauses enables the /student-pauses operations.
func WithStudentPauses(s StudentPauseService) Option {
	return func(h *Handler) { h.studentPauses = s }
}

func init() {
	for op, perm := range map[string]string{
		"ListStudentPauses": "stu.student_pauses.view", "GetStudentPause": "stu.student_pauses.view",
		"CreateStudentPause": "stu.student_pauses.create", "UpdateStudentPause": "stu.student_pauses.edit",
		"EndStudentPause": "stu.student_pauses.edit", "CancelStudentPause": "stu.student_pauses.edit",
	} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
		// why a child is away (health, money) is not for platform staff on a read-only support visit
		documentOperations[op] = true
	}
}

// pauseFail is the error answer shared by the pause operations.
type pauseFail gen.Problem

func (f pauseFail) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(f.Status)
	return json.NewEncoder(w).Encode(f)
}
func (f pauseFail) VisitListStudentPausesResponse(w http.ResponseWriter) error  { return f.write(w) }
func (f pauseFail) VisitCreateStudentPauseResponse(w http.ResponseWriter) error { return f.write(w) }
func (f pauseFail) VisitGetStudentPauseResponse(w http.ResponseWriter) error    { return f.write(w) }
func (f pauseFail) VisitUpdateStudentPauseResponse(w http.ResponseWriter) error { return f.write(w) }
func (f pauseFail) VisitEndStudentPauseResponse(w http.ResponseWriter) error    { return f.write(w) }
func (f pauseFail) VisitCancelStudentPauseResponse(w http.ResponseWriter) error { return f.write(w) }

// failPause turns a service error into its problem answer; unknown errors come back as ok=false.
func failPause(err error) (pauseFail, bool) {
	var fe crud.FieldErrors
	var ce pauses.ConflictError
	switch {
	case errors.Is(err, resource.ErrNotFound):
		return pauseFail(problemBody(http.StatusNotFound, "not_found", "")), true
	case errors.As(err, &fe):
		return pauseFail(validationProblem(fe)), true
	case errors.As(err, &ce):
		return pauseFail(problemBody(http.StatusConflict, ce.Code, ce.Message)), true
	}
	return pauseFail{}, false
}

func toGenPause(x pauses.Pause) gen.StudentPause {
	return gen.StudentPause{
		Id: mustUUID(x.ID), Student: gen.StudentPauseStudent{Id: mustUUID(x.Student.ID), Name: x.Student.Name, RegistrationCode: x.Student.RegistrationCode},
		ClassName: x.ClassName, SectionName: x.SectionName, YearName: x.YearName, PausedFrom: apiDate(x.PausedFrom), Days: x.Days,
		ExpectedBack: apiDatePtr(x.ExpectedBack), Overdue: x.Overdue, Reason: gen.StudentPauseReason(x.Reason), ReasonNote: x.ReasonNote,
		Status: gen.StudentPauseStatus(x.Status), EndedOn: apiDatePtr(x.EndedOn), CloseNote: x.CloseNote,
		Figures: gen.StudentPauseFigures{
			Owed:   gen.StudentPauseFigure{Status: gen.StudentPauseFigureStatus(x.Figures.Owed.Status), Note: x.Figures.Owed.Note},
			Missed: gen.StudentPauseFigure{Status: gen.StudentPauseFigureStatus(x.Figures.Missed.Status), Note: x.Figures.Missed.Note},
		},
		CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt,
	}
}

func (h *Handler) pausesReady() error {
	if h.studentPauses == nil {
		return errNotConfigured
	}
	return nil
}

// ListStudentPauses implements GET /student-pauses.
func (h *Handler) ListStudentPauses(ctx context.Context, req gen.ListStudentPausesRequestObject) (gen.ListStudentPausesResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, pauses.SortKeys, pauses.DefaultSort)
	if qerr != nil {
		return pauseFail(validationProblem(qerr)), nil
	}
	var f pauses.Filter
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		var fe crud.FieldErrors
		oneOf(&fe, "status", f.Status, pauses.StatusActive, pauses.StatusEnded, pauses.StatusCancelled)
		if len(fe) > 0 {
			return pauseFail(validationProblem(fe)), nil
		}
	}
	f.ClassID = derefString(uuidStrPtr(req.Params.ClassId))
	p, err := h.studentPauses.ListAudited(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.StudentPausePage{Items: make([]gen.StudentPause, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenPause(it)
	}
	return gen.ListStudentPauses200JSONResponse(out), nil
}

// CreateStudentPause implements POST /student-pauses.
func (h *Handler) CreateStudentPause(ctx context.Context, req gen.CreateStudentPauseRequestObject) (gen.CreateStudentPauseResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return pauseFail(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required.")), nil
	}
	b := req.Body
	x, err := h.studentPauses.Create(ctx, actorFrom(ctx), pauses.Input{StudentID: b.StudentId.String(), PausedFrom: domainDatePtr(b.PausedFrom),
		ExpectedBack: domainDatePtr(b.ExpectedBack), Reason: string(b.Reason), ReasonNote: b.ReasonNote})
	if f, ok := failPause(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateStudentPause201JSONResponse(toGenPause(x)), nil
}

// GetStudentPause implements GET /student-pauses/{pauseId}.
func (h *Handler) GetStudentPause(ctx context.Context, req gen.GetStudentPauseRequestObject) (gen.GetStudentPauseResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	x, err := h.studentPauses.View(ctx, actorFrom(ctx), req.PauseId.String())
	if f, ok := failPause(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentPause200JSONResponse(toGenPause(x)), nil
}

// UpdateStudentPause implements PUT /student-pauses/{pauseId}.
func (h *Handler) UpdateStudentPause(ctx context.Context, req gen.UpdateStudentPauseRequestObject) (gen.UpdateStudentPauseResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return pauseFail(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required.")), nil
	}
	b := req.Body
	x, err := h.studentPauses.Update(ctx, actorFrom(ctx), req.PauseId.String(), pauses.Input{ExpectedBack: domainDatePtr(b.ExpectedBack), Reason: string(b.Reason), ReasonNote: b.ReasonNote})
	if f, ok := failPause(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateStudentPause200JSONResponse(toGenPause(x)), nil
}

// EndStudentPause implements POST /student-pauses/{pauseId}/end.
func (h *Handler) EndStudentPause(ctx context.Context, req gen.EndStudentPauseRequestObject) (gen.EndStudentPauseResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	var on, note *string
	if req.Body != nil {
		on, note = domainDatePtr(req.Body.EndedOn), req.Body.Note
	}
	x, err := h.studentPauses.End(ctx, actorFrom(ctx), req.PauseId.String(), on, note)
	if f, ok := failPause(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.EndStudentPause200JSONResponse(toGenPause(x)), nil
}

// CancelStudentPause implements POST /student-pauses/{pauseId}/cancel.
func (h *Handler) CancelStudentPause(ctx context.Context, req gen.CancelStudentPauseRequestObject) (gen.CancelStudentPauseResponseObject, error) {
	if err := h.pausesReady(); err != nil {
		return nil, err
	}
	var note *string
	if req.Body != nil {
		note = req.Body.Note
	}
	x, err := h.studentPauses.Cancel(ctx, actorFrom(ctx), req.PauseId.String(), note)
	if f, ok := failPause(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CancelStudentPause200JSONResponse(toGenPause(x)), nil
}
