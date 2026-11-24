package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electivechanges"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// StudentElectiveService is what the student elective operations need (electives.Service satisfies it).
type StudentElectiveService interface {
	List(ctx context.Context, q crud.ListQuery, f electives.Filter) (crud.Page[electives.Choice], error)
	Get(ctx context.Context, id string) (electives.Choice, error)
	Create(ctx context.Context, a audit.Actor, in electives.Input) (electives.Choice, error)
	Replace(ctx context.Context, a audit.Actor, id string, in electives.Update) (electives.Choice, error)
	Delete(ctx context.Context, a audit.Actor, id string) error
	History(ctx context.Context, id string) ([]electives.HistoryItem, error)
	Roster(ctx context.Context, yearID, classID, sectionID, groupID string) (electives.Roster, error)
	Bulk(ctx context.Context, a audit.Actor, in electives.BulkInput) (electives.BulkResult, error)
}

// StudentElectiveChangeService is what the elective change request operations need (electivechanges.Service satisfies it).
type StudentElectiveChangeService interface {
	List(ctx context.Context, q crud.ListQuery, f electivechanges.Filter) (crud.Page[electivechanges.Change], error)
	Get(ctx context.Context, id string) (electivechanges.Change, error)
	Create(ctx context.Context, a audit.Actor, in electivechanges.Input) (electivechanges.Change, error)
	Cancel(ctx context.Context, a audit.Actor, access authz.Access, id, comment string) (electivechanges.Change, error)
}

// WithStudentElectives enables the /student-electives operations.
func WithStudentElectives(s StudentElectiveService) Option {
	return func(h *Handler) { h.studentElectives = s }
}

// WithStudentElectiveChanges enables the /student-elective-changes operations.
func WithStudentElectiveChanges(s StudentElectiveChangeService) Option {
	return func(h *Handler) { h.studentElectiveChanges = s }
}

func init() {
	for op, perm := range map[string]string{
		"ListStudentElectives": "stu.student_electives.view", "GetStudentElective": "stu.student_electives.view",
		"GetStudentElectiveHistory": "stu.student_electives.view", "GetStudentElectiveRoster": "stu.student_electives.view",
		"CreateStudentElective": "stu.student_electives.create", "ReplaceStudentElective": "stu.student_electives.edit",
		"SaveStudentElectiveRoster": "stu.student_electives.edit", "DeleteStudentElective": "stu.student_electives.delete",
		"ListStudentElectiveChanges": "stu.student_elective_approvals.view", "GetStudentElectiveChange": "stu.student_elective_approvals.view",
		"CreateStudentElectiveChange": "stu.student_elective_approvals.create", "CancelStudentElectiveChange": "stu.student_elective_approvals.create",
	} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
	}
}

// electiveFail is the error answer shared by the elective operations.
type electiveFail gen.Problem

func (f electiveFail) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(f.Status)
	return json.NewEncoder(w).Encode(f)
}
func (f electiveFail) VisitListStudentElectivesResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitCreateStudentElectiveResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitGetStudentElectiveResponse(w http.ResponseWriter) error { return f.write(w) }
func (f electiveFail) VisitReplaceStudentElectiveResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitDeleteStudentElectiveResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitGetStudentElectiveHistoryResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitGetStudentElectiveRosterResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitSaveStudentElectiveRosterResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitListStudentElectiveChangesResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitCreateStudentElectiveChangeResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitGetStudentElectiveChangeResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f electiveFail) VisitCancelStudentElectiveChangeResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// failElective turns a service error into its problem answer; unknown errors come back as ok=false.
func failElective(err error) (electiveFail, bool) {
	var fe crud.FieldErrors
	var ce electives.ConflictError
	var cce electivechanges.ConflictError
	var ace approvals.ConflictError
	var afo approvals.ForbiddenError
	switch {
	case errors.Is(err, resource.ErrNotFound), errors.Is(err, approvals.ErrNotFound):
		return electiveFail(problemBody(http.StatusNotFound, "not_found", "")), true
	case errors.As(err, &fe):
		return electiveFail(validationProblem(fe)), true
	case errors.As(err, &ce):
		return electiveFail(problemBody(http.StatusConflict, ce.Code, ce.Message)), true
	case errors.As(err, &cce):
		return electiveFail(problemBody(http.StatusConflict, cce.Code, cce.Message)), true
	case errors.As(err, &ace):
		return electiveFail(problemBody(http.StatusConflict, "not_cancellable", ace.Message)), true
	case errors.As(err, &afo):
		return electiveFail(problemBody(http.StatusForbidden, "permission_denied", afo.Message)), true
	}
	return electiveFail{}, false
}

func electiveSubjects(in []electives.SubjectRef) []gen.ElectiveSubject {
	out := make([]gen.ElectiveSubject, len(in))
	for i, s := range in {
		out[i] = gen.ElectiveSubject{Id: mustUUID(s.ID), Code: s.Code, Name: s.Name}
	}
	return out
}

func electiveWarnings(in []electives.Warning) []gen.ElectiveWarning {
	out := make([]gen.ElectiveWarning, len(in))
	for i, w := range in {
		out[i] = gen.ElectiveWarning{Code: gen.ElectiveWarningCode(w.Code), Message: w.Message}
	}
	return out
}

func electiveGroup(g electives.GroupRef) gen.ElectiveGroupLimits {
	return gen.ElectiveGroupLimits{Id: mustUUID(g.ID), Name: g.Name, Code: g.Code, MinSubjects: g.MinSubjects, MaxSubjects: g.MaxSubjects, EnforceLimits: g.EnforceLimits}
}

func electiveStudent(s electives.StudentRef) gen.StudentElectiveStudent {
	return gen.StudentElectiveStudent{Id: mustUUID(s.ID), Name: s.Name, RegistrationCode: s.RegistrationCode}
}

func electivePerson(p *electives.Person) *gen.ElectivePerson {
	if p == nil {
		return nil
	}
	return &gen.ElectivePerson{Id: mustUUID(p.ID), Name: p.Name}
}

func toGenElective(c electives.Choice) gen.StudentElective {
	return gen.StudentElective{
		Id: mustUUID(c.ID), Student: electiveStudent(c.Student), YearId: mustUUID(c.YearID), YearName: c.YearName,
		ClassId: uuidPtr(c.ClassID), ClassName: c.ClassName, SectionId: uuidPtr(c.SectionID), SectionName: c.SectionName,
		Group: electiveGroup(c.Group), Subjects: electiveSubjects(c.Subjects), Offered: electiveSubjects(c.Offered),
		Status: gen.StudentElectiveStatus(c.Status), WithinLimits: c.WithinLimits, Warnings: electiveWarnings(c.Warnings),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func uuidStrings(in []openapi_types.UUID) []string {
	out := make([]string, len(in))
	for i, u := range in {
		out[i] = u.String()
	}
	return out
}

func electiveStatus(s *gen.StudentElectiveStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func (h *Handler) electivesReady() error {
	if h.studentElectives == nil {
		return errNotConfigured
	}
	return nil
}

func (h *Handler) electiveChangesReady() error {
	if h.studentElectiveChanges == nil {
		return errNotConfigured
	}
	return nil
}

func missingElectiveBody() electiveFail {
	return electiveFail(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))
}

// ListStudentElectives implements GET /student-electives.
func (h *Handler) ListStudentElectives(ctx context.Context, req gen.ListStudentElectivesRequestObject) (gen.ListStudentElectivesResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, electives.SortKeys, electives.DefaultSort)
	if qerr != nil {
		return electiveFail(validationProblem(qerr)), nil
	}
	p := req.Params
	f := electives.Filter{YearID: derefString(uuidStrPtr(p.YearId)), ClassID: derefString(uuidStrPtr(p.ClassId)), SectionID: derefString(uuidStrPtr(p.SectionId)),
		GroupID: derefString(uuidStrPtr(p.GroupId)), StudentID: derefString(uuidStrPtr(p.StudentId)), Status: electiveStatus(p.Status)}
	if f.Status != "" {
		var fe crud.FieldErrors
		oneOf(&fe, "status", f.Status, electives.Statuses...)
		if len(fe) > 0 {
			return electiveFail(validationProblem(fe)), nil
		}
	}
	page, err := h.studentElectives.List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := gen.StudentElectivePage{Items: make([]gen.StudentElective, len(page.Items)), Page: gen.PageInfo{Number: page.Page, Size: page.Size, Total: page.Total}}
	for i, it := range page.Items {
		out.Items[i] = toGenElective(it)
	}
	return gen.ListStudentElectives200JSONResponse(out), nil
}

// CreateStudentElective implements POST /student-electives.
func (h *Handler) CreateStudentElective(ctx context.Context, req gen.CreateStudentElectiveRequestObject) (gen.CreateStudentElectiveResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingElectiveBody(), nil
	}
	b := req.Body
	c, err := h.studentElectives.Create(ctx, actorFrom(ctx), electives.Input{StudentID: b.StudentId.String(), YearID: b.YearId.String(), GroupID: b.GroupId.String(),
		SubjectIDs: uuidStrings(b.SubjectIds), Status: electiveStatus(b.Status)})
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateStudentElective201JSONResponse(toGenElective(c)), nil
}

// GetStudentElective implements GET /student-electives/{choiceId}.
func (h *Handler) GetStudentElective(ctx context.Context, req gen.GetStudentElectiveRequestObject) (gen.GetStudentElectiveResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	c, err := h.studentElectives.Get(ctx, req.ChoiceId.String())
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentElective200JSONResponse(toGenElective(c)), nil
}

// ReplaceStudentElective implements PUT /student-electives/{choiceId}.
func (h *Handler) ReplaceStudentElective(ctx context.Context, req gen.ReplaceStudentElectiveRequestObject) (gen.ReplaceStudentElectiveResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingElectiveBody(), nil
	}
	c, err := h.studentElectives.Replace(ctx, actorFrom(ctx), req.ChoiceId.String(), electives.Update{SubjectIDs: uuidStrings(req.Body.SubjectIds), Status: electiveStatus(req.Body.Status)})
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ReplaceStudentElective200JSONResponse(toGenElective(c)), nil
}

// DeleteStudentElective implements DELETE /student-electives/{choiceId}.
func (h *Handler) DeleteStudentElective(ctx context.Context, req gen.DeleteStudentElectiveRequestObject) (gen.DeleteStudentElectiveResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	err := h.studentElectives.Delete(ctx, actorFrom(ctx), req.ChoiceId.String())
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteStudentElective204Response{}, nil
}

// GetStudentElectiveHistory implements GET /student-electives/{choiceId}/history.
func (h *Handler) GetStudentElectiveHistory(ctx context.Context, req gen.GetStudentElectiveHistoryRequestObject) (gen.GetStudentElectiveHistoryResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	items, err := h.studentElectives.History(ctx, req.ChoiceId.String())
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.StudentElectiveHistory{Items: make([]gen.StudentElectiveHistoryItem, len(items))}
	for i, it := range items {
		out.Items[i] = gen.StudentElectiveHistoryItem{Id: mustUUID(it.ID), Action: gen.StudentElectiveHistoryItemAction(it.Action),
			SubjectsBefore: electiveSubjects(it.SubjectsBefore), SubjectsAfter: electiveSubjects(it.SubjectsAfter), StatusBefore: it.StatusBefore, StatusAfter: it.StatusAfter,
			Source: gen.StudentElectiveHistoryItemSource(it.Source), ChangeId: uuidPtr(it.ChangeID), ApprovalRequestId: uuidPtr(it.ApprovalRequestID),
			ChangedAt: it.ChangedAt, ChangedBy: electivePerson(it.ChangedBy)}
	}
	return gen.GetStudentElectiveHistory200JSONResponse(out), nil
}

// GetStudentElectiveRoster implements GET /student-electives/roster.
func (h *Handler) GetStudentElectiveRoster(ctx context.Context, req gen.GetStudentElectiveRosterRequestObject) (gen.GetStudentElectiveRosterResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	p := req.Params
	r, err := h.studentElectives.Roster(ctx, p.YearId.String(), p.ClassId.String(), p.SectionId.String(), p.GroupId.String())
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	named := func(x electives.Ref) gen.ElectiveNamed { return gen.ElectiveNamed{Id: mustUUID(x.ID), Name: x.Name} }
	out := gen.StudentElectiveRoster{Year: named(r.Year), Class: named(r.Class), Section: named(r.Section), Group: electiveGroup(r.Group),
		Offered: electiveSubjects(r.Offered), Students: make([]gen.StudentElectiveRosterStudent, len(r.Students))}
	for i, s := range r.Students {
		out.Students[i] = gen.StudentElectiveRosterStudent{Student: electiveStudent(s.Student), RollNumber: s.RollNumber, ChoiceId: uuidPtr(s.ChoiceID), Status: s.Status,
			Subjects: electiveSubjects(s.Subjects), WithinLimits: s.WithinLimits, Warnings: electiveWarnings(s.Warnings)}
	}
	return gen.GetStudentElectiveRoster200JSONResponse(out), nil
}

// SaveStudentElectiveRoster implements PUT /student-electives/roster.
func (h *Handler) SaveStudentElectiveRoster(ctx context.Context, req gen.SaveStudentElectiveRosterRequestObject) (gen.SaveStudentElectiveRosterResponseObject, error) {
	if err := h.electivesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingElectiveBody(), nil
	}
	access, ok := AccessFrom(ctx)
	if !ok {
		return nil, errNotConfigured
	}
	b := req.Body
	in := electives.BulkInput{YearID: b.YearId.String(), ClassID: b.ClassId.String(), SectionID: b.SectionId.String(), GroupID: b.GroupId.String(),
		AllowCreate: access.Effective.Can("stu.student_electives.create"), AllowDelete: access.Effective.Can("stu.student_electives.delete")}
	for _, r := range b.Rows {
		in.Rows = append(in.Rows, electives.BulkRow{StudentID: r.StudentId.String(), SubjectIDs: uuidStrings(r.SubjectIds), Status: electiveStatus(r.Status)})
	}
	res, err := h.studentElectives.Bulk(ctx, actorFrom(ctx), in)
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.StudentElectiveRosterResult{Created: res.Created, Updated: res.Updated, Removed: res.Removed, Unchanged: res.Unchanged}
	out.Items = make([]struct {
		ChoiceId  *openapi_types.UUID                        `json:"choiceId"`
		Result    gen.StudentElectiveRosterResultItemsResult `json:"result"`
		StudentId openapi_types.UUID                         `json:"studentId"`
	}, len(res.Items))
	for i, it := range res.Items {
		out.Items[i].StudentId, out.Items[i].ChoiceId, out.Items[i].Result = mustUUID(it.StudentID), uuidPtr(it.ChoiceID), gen.StudentElectiveRosterResultItemsResult(it.Result)
	}
	return gen.SaveStudentElectiveRoster200JSONResponse(out), nil
}

func toGenElectiveChange(c electivechanges.Change) gen.StudentElectiveChange {
	person := func(p *electivechanges.Person) *gen.ElectivePerson {
		if p == nil {
			return nil
		}
		return &gen.ElectivePerson{Id: mustUUID(p.ID), Name: p.Name}
	}
	subject := func(s electives.SubjectRef) gen.ElectiveSubject {
		return gen.ElectiveSubject{Id: mustUUID(s.ID), Code: s.Code, Name: s.Name}
	}
	out := gen.StudentElectiveChange{
		Id: mustUUID(c.ID), Student: electiveStudent(c.Student), YearId: mustUUID(c.YearID), YearName: c.YearName, ClassName: c.ClassName, SectionName: c.SectionName,
		Group: gen.ElectiveNamed{Id: mustUUID(c.Group.ID), Name: c.Group.Name}, ChoiceId: mustUUID(c.ChoiceID), FromSubject: subject(c.FromSubject), ToSubject: subject(c.ToSubject),
		WantedFrom: apiDate(c.WantedFrom), Reason: c.Reason, Status: gen.StudentElectiveChangeStatus(c.Status), ApprovalRequestId: uuidPtr(c.ApprovalRequestID),
		RequestedBy: person(c.RequestedBy), CancelledAt: c.CancelledAt, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
	if d := c.Decision; d != nil {
		out.Decision = &gen.StudentElectiveChangeDecision{Outcome: gen.StudentElectiveChangeDecisionOutcome(d.Outcome), DecidedBy: person(d.DecidedBy), DecidedAt: d.DecidedAt, Note: d.Note}
	}
	return out
}

// ListStudentElectiveChanges implements GET /student-elective-changes.
func (h *Handler) ListStudentElectiveChanges(ctx context.Context, req gen.ListStudentElectiveChangesRequestObject) (gen.ListStudentElectiveChangesResponseObject, error) {
	if err := h.electiveChangesReady(); err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, electivechanges.SortKeys, electivechanges.DefaultSort)
	if qerr != nil {
		return electiveFail(validationProblem(qerr)), nil
	}
	p := req.Params
	f := electivechanges.Filter{StudentID: derefString(uuidStrPtr(p.StudentId)), ChoiceID: derefString(uuidStrPtr(p.ChoiceId)), GroupID: derefString(uuidStrPtr(p.GroupId)),
		YearID: derefString(uuidStrPtr(p.YearId)), ClassID: derefString(uuidStrPtr(p.ClassId)), SectionID: derefString(uuidStrPtr(p.SectionId))}
	if p.Status != nil {
		f.Status = string(*p.Status)
		var fe crud.FieldErrors
		oneOf(&fe, "status", f.Status, "requested", "approved", "rejected", "cancelled")
		if len(fe) > 0 {
			return electiveFail(validationProblem(fe)), nil
		}
	}
	page, err := h.studentElectiveChanges.List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := gen.StudentElectiveChangePage{Items: make([]gen.StudentElectiveChange, len(page.Items)), Page: gen.PageInfo{Number: page.Page, Size: page.Size, Total: page.Total}}
	for i, it := range page.Items {
		out.Items[i] = toGenElectiveChange(it)
	}
	return gen.ListStudentElectiveChanges200JSONResponse(out), nil
}

// CreateStudentElectiveChange implements POST /student-elective-changes.
func (h *Handler) CreateStudentElectiveChange(ctx context.Context, req gen.CreateStudentElectiveChangeRequestObject) (gen.CreateStudentElectiveChangeResponseObject, error) {
	if err := h.electiveChangesReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingElectiveBody(), nil
	}
	b := req.Body
	c, err := h.studentElectiveChanges.Create(ctx, actorFrom(ctx), electivechanges.Input{ChoiceID: b.ChoiceId.String(), FromSubjectID: b.FromSubjectId.String(),
		ToSubjectID: b.ToSubjectId.String(), WantedFrom: domainDatePtr(b.WantedFrom), Reason: b.Reason})
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateStudentElectiveChange201JSONResponse(toGenElectiveChange(c)), nil
}

// GetStudentElectiveChange implements GET /student-elective-changes/{changeId}.
func (h *Handler) GetStudentElectiveChange(ctx context.Context, req gen.GetStudentElectiveChangeRequestObject) (gen.GetStudentElectiveChangeResponseObject, error) {
	if err := h.electiveChangesReady(); err != nil {
		return nil, err
	}
	c, err := h.studentElectiveChanges.Get(ctx, req.ChangeId.String())
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentElectiveChange200JSONResponse(toGenElectiveChange(c)), nil
}

// CancelStudentElectiveChange implements POST /student-elective-changes/{changeId}/cancel.
func (h *Handler) CancelStudentElectiveChange(ctx context.Context, req gen.CancelStudentElectiveChangeRequestObject) (gen.CancelStudentElectiveChangeResponseObject, error) {
	if err := h.electiveChangesReady(); err != nil {
		return nil, err
	}
	access, ok := AccessFrom(ctx)
	if !ok {
		return nil, errNotConfigured
	}
	comment := ""
	if req.Body != nil && req.Body.Comment != nil {
		comment = *req.Body.Comment
	}
	c, err := h.studentElectiveChanges.Cancel(ctx, actorFrom(ctx), access, req.ChangeId.String(), comment)
	if f, ok := failElective(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CancelStudentElectiveChange200JSONResponse(toGenElectiveChange(c)), nil
}
