package apiv1

import (
	"context"
	"errors"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/admissions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// AdmissionService is what the admissions operations need (admissions.Service satisfies it).
type AdmissionService interface {
	ListAudited(ctx context.Context, actor audit.Actor, q crud.ListQuery, f admissions.Filter) (crud.Page[admissions.Admission], error)
	View(ctx context.Context, actor audit.Actor, id string) (admissions.Admission, error)
	Admit(ctx context.Context, actor audit.Actor, registrationID string, in admissions.Input) (admissions.Result, error)
	Preview(ctx context.Context, actor audit.Actor, items []admissions.Item) ([]admissions.Row, error)
	AdmitMany(ctx context.Context, actor audit.Actor, items []admissions.Item) ([]admissions.Row, error)
}

// WithAdmissions enables the /admissions operations.
func WithAdmissions(s AdmissionService) Option { return func(h *Handler) { h.admissions = s } }

func forbiddenProblem() gen.ForbiddenApplicationProblemPlusJSONResponse {
	return gen.ForbiddenApplicationProblemPlusJSONResponse(problemBody(http.StatusForbidden, "permission_denied", "You do not have permission to do this."))
}

// mayCreateStudents says whether the caller may do what an admission does besides editing the queue: create students,
// guardians and enrolments. Admitting writes those records, so the right to admit alone is not enough; the caller also
// needs to create students, institute-wide, as the students screen requires.
func mayCreateStudents(ctx context.Context) bool {
	a, ok := AccessFrom(ctx)
	return ok && a.Effective.Can("stu.students.create") && contains(a.Effective.Scopes["stu.students"], authz.ScopeTenant)
}

func admitInput(sectionID string, b admitFields) admissions.Input {
	in := admissions.Input{SectionID: sectionID, RollNumber: b.rollNumber, StudentCode: b.studentCode, Gender: b.gender,
		AdmissionDate: domainDatePtr(b.admissionDate), ParentEmail: b.parentEmail}
	if b.confirmDuplicate != nil {
		in.ConfirmDuplicate = *b.confirmDuplicate
	}
	return in
}

type admitFields struct {
	rollNumber, studentCode, gender, parentEmail *string
	admissionDate                                *openapi_types.Date
	confirmDuplicate                             *bool
}

func toGenAdmission(a admissions.Admission) gen.Admission {
	return gen.Admission{
		Id: mustUUID(a.ID), ApplicationNumber: a.ApplicationNumber, AppliedOn: apiDate(a.AppliedOn), FullName: a.FullName, DateOfBirth: apiDate(a.DateOfBirth),
		Gender: a.Gender, FatherName: a.FatherName, MotherName: a.MotherName, GuardianName: a.GuardianName, GuardianPhone: a.GuardianPhone,
		ClassId: mustUUID(a.ClassID), ClassName: a.ClassName, SectionId: uuidPtr(a.SectionID), SectionName: a.SectionName,
		YearId: mustUUID(a.YearID), YearName: a.YearName, Status: a.Status, Admitted: a.Admitted, AdmittedAt: a.AdmittedAt,
		StudentId: uuidPtr(a.StudentID), StudentCode: a.StudentCode, EnrolledSectionName: a.EnrolledSection, RollNumber: a.RollNumber,
	}
}

func toGenAdmissionResult(r admissions.Result) gen.AdmissionResult {
	gs := make([]gen.AdmissionGuardian, len(r.Guardians))
	for i, g := range r.Guardians {
		gs[i] = gen.AdmissionGuardian{GuardianId: mustUUID(g.GuardianID), Name: g.Name, Relation: gen.AdmissionGuardianRelation(g.Relation), IsPrimary: g.IsPrimary}
	}
	notes := r.Notes
	if notes == nil {
		notes = []string{}
	}
	return gen.AdmissionResult{
		RegistrationId: mustUUID(r.RegistrationID), ApplicationNumber: r.ApplicationNumber, AlreadyAdmitted: r.AlreadyAdmitted,
		Student: gen.AdmissionStudent{Id: mustUUID(r.Student.ID), RegistrationCode: r.Student.RegistrationCode, FullName: r.Student.FullName,
			ClassName: r.Student.CurrentClassName, SectionName: r.Student.CurrentSectionName, RollNumber: r.Student.CurrentRollNumber},
		EnrollmentId: mustUUID(r.Enrollment.ID), YearName: r.Enrollment.YearName, Guardians: gs, Notes: notes,
	}
}

// admitProblem answers an admit error the way the contract says; ok is false for an error the caller must return.
func admitProblem(err error) (gen.Problem, bool) {
	var ne admissions.NotEligible
	switch {
	case errors.As(err, &ne):
		msgs := make([]string, len(ne.Problems))
		for i, p := range ne.Problems {
			msgs[i] = p.Message
		}
		return problemBody(http.StatusConflict, "not_eligible", strings.Join(msgs, " ")), true
	case errors.Is(err, admissions.ErrNotAccepted):
		return problemBody(http.StatusConflict, "not_accepted", "Only an accepted registration can be admitted."), true
	case errors.Is(err, admissions.ErrStudentRemoved):
		return problemBody(http.StatusConflict, "student_removed", "This registration was admitted, but its student has been removed. Ask an administrator."), true
	}
	return gen.Problem{}, false
}

// ListAdmissions implements GET /admissions.
func (h *Handler) ListAdmissions(ctx context.Context, req gen.ListAdmissionsRequestObject) (gen.ListAdmissionsResponseObject, error) {
	if h.admissions == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, admissions.SortKeys, admissions.DefaultSort)
	if qerr != nil {
		return gen.ListAdmissions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(qerr))}, nil
	}
	var f admissions.Filter
	if req.Params.State != nil {
		f.State = string(*req.Params.State)
		if f.State != admissions.StateReady && f.State != admissions.StateAdmitted {
			var fe crud.FieldErrors
			fe.Add("state", "invalid", "State must be ready or admitted.")
			return gen.ListAdmissions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
		}
	}
	f.ClassID, f.YearID = derefString(uuidStrPtr(req.Params.ClassId)), derefString(uuidStrPtr(req.Params.YearId))
	p, err := h.admissions.ListAudited(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.AdmissionPage{Items: make([]gen.Admission, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenAdmission(it)
	}
	return gen.ListAdmissions200JSONResponse(out), nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// GetAdmission implements GET /admissions/{registrationId}.
func (h *Handler) GetAdmission(ctx context.Context, req gen.GetAdmissionRequestObject) (gen.GetAdmissionResponseObject, error) {
	if h.admissions == nil {
		return nil, errNotConfigured
	}
	it, err := h.admissions.View(ctx, actorFrom(ctx), req.RegistrationId.String())
	if errors.Is(err, resource.ErrNotFound) {
		return gen.GetAdmission404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetAdmission200JSONResponse(toGenAdmission(it)), nil
}

// AdmitRegistration implements POST /admissions/{registrationId}/admit.
func (h *Handler) AdmitRegistration(ctx context.Context, req gen.AdmitRegistrationRequestObject) (gen.AdmitRegistrationResponseObject, error) {
	if h.admissions == nil {
		return nil, errNotConfigured
	}
	if !mayCreateStudents(ctx) {
		return gen.AdmitRegistration403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.AdmitRegistration400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(
			problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	b := req.Body
	in := admitInput(b.SectionId.String(), admitFields{b.RollNumber, b.StudentCode, b.Gender, b.ParentEmail, b.AdmissionDate, b.ConfirmDuplicate})
	res, err := h.admissions.Admit(ctx, actorFrom(ctx), req.RegistrationId.String(), in)
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.AdmitRegistration400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if errors.Is(err, resource.ErrNotFound) {
		return gen.AdmitRegistration404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	}
	if p, ok := admitProblem(err); ok {
		return gen.AdmitRegistration409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(p)}, nil
	}
	if err != nil {
		return nil, err
	}
	if res.AlreadyAdmitted {
		return gen.AdmitRegistration200JSONResponse(toGenAdmissionResult(res)), nil
	}
	return gen.AdmitRegistration201JSONResponse(toGenAdmissionResult(res)), nil
}

func batchItems(b gen.AdmitBatch) []admissions.Item {
	items := make([]admissions.Item, len(b.Items))
	for i, it := range b.Items {
		items[i] = admissions.Item{RegistrationID: it.RegistrationId.String(),
			Input: admitInput(it.SectionId.String(), admitFields{it.RollNumber, it.StudentCode, it.Gender, it.ParentEmail, it.AdmissionDate, it.ConfirmDuplicate})}
	}
	return items
}

func toGenAdmissionBatch(rows []admissions.Row) gen.AdmissionBatchResult {
	out := gen.AdmissionBatchResult{Rows: make([]gen.AdmissionBatchRow, len(rows))}
	for i, r := range rows {
		row := gen.AdmissionBatchRow{RegistrationId: mustUUID(r.RegistrationID), ApplicationNumber: r.ApplicationNumber,
			Outcome: gen.AdmissionBatchRowOutcome(r.Outcome), Problems: make([]gen.AdmissionProblem, len(r.Problems))}
		for j, p := range r.Problems {
			row.Problems[j] = gen.AdmissionProblem{Field: p.Field, Code: p.Code, Message: p.Message}
		}
		if r.Result != nil {
			res := toGenAdmissionResult(*r.Result)
			row.Result = &res
		}
		out.Rows[i] = row
		if r.Outcome == admissions.OutcomeProblem {
			out.ProblemCount++
		} else {
			out.OkCount++
		}
	}
	return out
}

func tooManyProblem() gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "Send between 1 and 200 registrations at a time."))
}

// PreviewAdmissions implements POST /admissions/preview.
func (h *Handler) PreviewAdmissions(ctx context.Context, req gen.PreviewAdmissionsRequestObject) (gen.PreviewAdmissionsResponseObject, error) {
	if h.admissions == nil {
		return nil, errNotConfigured
	}
	if !mayCreateStudents(ctx) {
		return gen.PreviewAdmissions403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.PreviewAdmissions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyProblem()}, nil
	}
	rows, err := h.admissions.Preview(ctx, actorFrom(ctx), batchItems(*req.Body))
	if errors.Is(err, admissions.ErrTooMany) {
		return gen.PreviewAdmissions400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.PreviewAdmissions200JSONResponse(toGenAdmissionBatch(rows)), nil
}

// AdmitRegistrations implements POST /admissions/bulk.
func (h *Handler) AdmitRegistrations(ctx context.Context, req gen.AdmitRegistrationsRequestObject) (gen.AdmitRegistrationsResponseObject, error) {
	if h.admissions == nil {
		return nil, errNotConfigured
	}
	if !mayCreateStudents(ctx) {
		return gen.AdmitRegistrations403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbiddenProblem()}, nil
	}
	if req.Body == nil {
		return gen.AdmitRegistrations400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyProblem()}, nil
	}
	rows, err := h.admissions.AdmitMany(ctx, actorFrom(ctx), batchItems(*req.Body))
	if errors.Is(err, admissions.ErrTooMany) {
		return gen.AdmitRegistrations400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: tooManyProblem()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.AdmitRegistrations200JSONResponse(toGenAdmissionBatch(rows)), nil
}
