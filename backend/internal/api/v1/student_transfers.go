package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// StudentTransferService is what the student transfer operations need (transfers.Service satisfies it).
type StudentTransferService interface {
	ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f transfers.Filter) (crud.Page[transfers.Transfer], error)
	View(ctx context.Context, a audit.Actor, id string) (transfers.Transfer, error)
	Create(ctx context.Context, a audit.Actor, in transfers.Input) (transfers.Transfer, error)
	Update(ctx context.Context, a audit.Actor, id string, in transfers.Input) (transfers.Transfer, error)
	Delete(ctx context.Context, a audit.Actor, id string) error
	Complete(ctx context.Context, a audit.Actor, id string, issuedOn *string) (transfers.CompleteResult, error)
	Cancel(ctx context.Context, a audit.Actor, access authz.Access, id, comment string) (transfers.Transfer, error)
	Certificate(ctx context.Context, a audit.Actor, id string) (transfers.Certificate, error)
}

// WithStudentTransfers enables the /student-transfers operations.
func WithStudentTransfers(s StudentTransferService) Option {
	return func(h *Handler) { h.studentTransfers = s }
}

func init() {
	for op, perm := range map[string]string{
		"ListStudentTransfers": "stu.student_transfers.view", "GetStudentTransfer": "stu.student_transfers.view",
		"GetStudentTransferCertificate": "stu.student_transfers.view", "GetStudentTransferCertificatePdf": "stu.student_transfers.view", "CreateStudentTransfer": "stu.student_transfers.create",
		"UpdateStudentTransfer": "stu.student_transfers.edit", "CompleteStudentTransfer": "stu.student_transfers.edit",
		"CancelStudentTransfer": "stu.student_transfers.edit", "DeleteStudentTransfer": "stu.student_transfers.delete",
	} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
		// why a child leaves (health, money) is not for platform staff on a read-only support visit
		documentOperations[op] = true
	}
}

// transferFail is the error answer shared by the transfer operations.
type transferFail gen.Problem

func (f transferFail) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(f.Status)
	return json.NewEncoder(w).Encode(f)
}
func (f transferFail) VisitListStudentTransfersResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitCreateStudentTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitGetStudentTransferResponse(w http.ResponseWriter) error { return f.write(w) }
func (f transferFail) VisitUpdateStudentTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitDeleteStudentTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitCompleteStudentTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitCancelStudentTransferResponse(w http.ResponseWriter) error {
	return f.write(w)
}
func (f transferFail) VisitGetStudentTransferCertificateResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// failTransfer turns a service error into its problem answer; unknown errors come back as ok=false.
func failTransfer(err error) (transferFail, bool) {
	var fe crud.FieldErrors
	var ce transfers.ConflictError
	var ne transfers.NotEligible
	var ace approvals.ConflictError
	var afo approvals.ForbiddenError
	switch {
	case errors.Is(err, resource.ErrNotFound), errors.Is(err, approvals.ErrNotFound):
		return transferFail(problemBody(http.StatusNotFound, "not_found", "")), true
	case errors.As(err, &fe):
		return transferFail(validationProblem(fe)), true
	case errors.As(err, &ce):
		return transferFail(problemBody(http.StatusConflict, ce.Code, ce.Message)), true
	case errors.As(err, &ne):
		msgs := make([]string, len(ne.Problems))
		for i, p := range ne.Problems {
			msgs[i] = p.Message
		}
		return transferFail(problemBody(http.StatusConflict, "not_eligible", strings.Join(msgs, " "))), true
	case errors.As(err, &ace):
		return transferFail(problemBody(http.StatusConflict, "not_cancellable", ace.Message)), true
	case errors.As(err, &afo):
		return transferFail(problemBody(http.StatusForbidden, "permission_denied", afo.Message)), true
	}
	return transferFail{}, false
}

func toGenTransfer(x transfers.Transfer) gen.StudentTransfer {
	return gen.StudentTransfer{
		Id: mustUUID(x.ID), Student: gen.StudentTransferStudent{Id: mustUUID(x.Student.ID), Name: x.Student.Name, RegistrationCode: x.Student.RegistrationCode},
		ClassName: x.ClassName, SectionName: x.SectionName, YearName: x.YearName, RequestedOn: apiDate(x.RequestedOn), Reason: gen.StudentTransferReason(x.Reason),
		ReasonNote: x.ReasonNote, DestinationSchool: x.DestinationSchool, LastAttendanceDate: apiDatePtr(x.LastAttendanceDate), Status: gen.StudentTransferStatus(x.Status),
		ApprovalRequestId: uuidPtr(x.ApprovalRequestID), TcNumber: x.TCNumber, TcIssuedOn: apiDatePtr(x.TCIssuedOn),
		Dues:      gen.StudentTransferDues{Status: gen.StudentTransferDuesStatus(x.Dues.Status), Note: x.Dues.Note},
		CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt,
	}
}

func (h *Handler) transfersReady() error {
	if h.studentTransfers == nil {
		return errNotConfigured
	}
	return nil
}

// ListStudentTransfers implements GET /student-transfers.
func (h *Handler) ListStudentTransfers(ctx context.Context, req gen.ListStudentTransfersRequestObject) (gen.ListStudentTransfersResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, transfers.SortKeys, transfers.DefaultSort)
	if qerr != nil {
		return transferFail(validationProblem(qerr)), nil
	}
	var f transfers.Filter
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
		var fe crud.FieldErrors
		oneOf(&fe, "status", f.Status, "requested", "approved", "rejected", "cancelled", "completed")
		if len(fe) > 0 {
			return transferFail(validationProblem(fe)), nil
		}
	}
	f.ClassID = derefString(uuidStrPtr(req.Params.ClassId))
	p, err := h.studentTransfers.ListAudited(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.StudentTransferPage{Items: make([]gen.StudentTransfer, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenTransfer(it)
	}
	return gen.ListStudentTransfers200JSONResponse(out), nil
}

func missingBody() transferFail {
	return transferFail(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))
}

// CreateStudentTransfer implements POST /student-transfers.
func (h *Handler) CreateStudentTransfer(ctx context.Context, req gen.CreateStudentTransferRequestObject) (gen.CreateStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingBody(), nil
	}
	b := req.Body
	x, err := h.studentTransfers.Create(ctx, actorFrom(ctx), transfers.Input{StudentID: b.StudentId.String(), RequestedOn: domainDatePtr(b.RequestedOn), Reason: string(b.Reason),
		ReasonNote: b.ReasonNote, DestinationSchool: b.DestinationSchool, LastAttendanceDate: domainDatePtr(b.LastAttendanceDate)})
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CreateStudentTransfer201JSONResponse(toGenTransfer(x)), nil
}

// GetStudentTransfer implements GET /student-transfers/{transferId}.
func (h *Handler) GetStudentTransfer(ctx context.Context, req gen.GetStudentTransferRequestObject) (gen.GetStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	x, err := h.studentTransfers.View(ctx, actorFrom(ctx), req.TransferId.String())
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentTransfer200JSONResponse(toGenTransfer(x)), nil
}

// UpdateStudentTransfer implements PUT /student-transfers/{transferId}.
func (h *Handler) UpdateStudentTransfer(ctx context.Context, req gen.UpdateStudentTransferRequestObject) (gen.UpdateStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	if req.Body == nil {
		return missingBody(), nil
	}
	b := req.Body
	x, err := h.studentTransfers.Update(ctx, actorFrom(ctx), req.TransferId.String(), transfers.Input{Reason: string(b.Reason), ReasonNote: b.ReasonNote,
		DestinationSchool: b.DestinationSchool, LastAttendanceDate: domainDatePtr(b.LastAttendanceDate)})
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UpdateStudentTransfer200JSONResponse(toGenTransfer(x)), nil
}

// DeleteStudentTransfer implements DELETE /student-transfers/{transferId}.
func (h *Handler) DeleteStudentTransfer(ctx context.Context, req gen.DeleteStudentTransferRequestObject) (gen.DeleteStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	err := h.studentTransfers.Delete(ctx, actorFrom(ctx), req.TransferId.String())
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteStudentTransfer204Response{}, nil
}

// CompleteStudentTransfer implements POST /student-transfers/{transferId}/complete.
func (h *Handler) CompleteStudentTransfer(ctx context.Context, req gen.CompleteStudentTransferRequestObject) (gen.CompleteStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	var issued *string
	if req.Body != nil {
		issued = domainDatePtr(req.Body.IssuedOn)
	}
	res, err := h.studentTransfers.Complete(ctx, actorFrom(ctx), req.TransferId.String(), issued)
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CompleteStudentTransfer200JSONResponse(gen.StudentTransferResult{Transfer: toGenTransfer(res.Transfer), AlreadyCompleted: res.AlreadyCompleted}), nil
}

// CancelStudentTransfer implements POST /student-transfers/{transferId}/cancel.
func (h *Handler) CancelStudentTransfer(ctx context.Context, req gen.CancelStudentTransferRequestObject) (gen.CancelStudentTransferResponseObject, error) {
	if err := h.transfersReady(); err != nil {
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
	x, err := h.studentTransfers.Cancel(ctx, actorFrom(ctx), access, req.TransferId.String(), comment)
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CancelStudentTransfer200JSONResponse(toGenTransfer(x)), nil
}

// GetStudentTransferCertificate implements GET /student-transfers/{transferId}/certificate.
func (h *Handler) GetStudentTransferCertificate(ctx context.Context, req gen.GetStudentTransferCertificateRequestObject) (gen.GetStudentTransferCertificateResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	c, err := h.studentTransfers.Certificate(ctx, actorFrom(ctx), req.TransferId.String())
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.StudentTransferCertificate{TcNumber: c.TCNumber, IssuedOn: apiDate(c.IssuedOn), SchoolName: c.SchoolName,
		ClassName: c.ClassName, SectionName: c.SectionName, YearName: c.YearName, LastAttendanceDate: apiDatePtr(c.LastAttendanceDate),
		Reason: gen.StudentTransferReason(c.Reason), ReasonLabel: c.ReasonLabel, ReasonNote: c.ReasonNote, DestinationSchool: c.DestinationSchool,
		Guardians: make([]struct {
			Name     string `json:"name"`
			Relation string `json:"relation"`
		}, len(c.Guardians))}
	out.Student.FullName, out.Student.RegistrationCode, out.Student.DateOfBirth = c.Student.FullName, c.Student.RegistrationCode, apiDate(c.Student.DateOfBirth)
	out.Student.Gender, out.Student.AdmissionDate = c.Student.Gender, apiDatePtr(c.Student.AdmissionDate)
	for i, g := range c.Guardians {
		out.Guardians[i].Name, out.Guardians[i].Relation = g.Name, g.Relation
	}
	return gen.GetStudentTransferCertificate200JSONResponse(out), nil
}
