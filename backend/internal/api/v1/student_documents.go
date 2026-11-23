package apiv1

import (
	"context"
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/files"
)

// DocumentService is what the student document operations need (files.Service satisfies it).
type DocumentService interface {
	BeginStudentDocument(ctx context.Context, a audit.Actor, studentID, contentType string, size int64) (files.Upload, error)
	AddStudentDocument(ctx context.Context, a audit.Actor, studentID, fileID string, in files.DocumentInput) (files.Document, error)
	ListStudentDocuments(ctx context.Context, a audit.Actor, studentID string) ([]files.Document, error)
	StudentDocumentLink(ctx context.Context, a audit.Actor, studentID, documentID string) (files.DocumentDownload, error)
	VerifyStudentDocument(ctx context.Context, a audit.Actor, studentID, documentID string) (files.Document, error)
	DeleteStudentDocument(ctx context.Context, a audit.Actor, studentID, documentID string) error
}

func init() {
	for op, perm := range map[string]string{
		"BeginStudentDocumentUpload": "stu.student_documents.create", "AddStudentDocument": "stu.student_documents.create",
		"ListStudentDocuments": "stu.student_documents.view", "GetStudentDocumentDownload": "stu.student_documents.view",
		"VerifyStudentDocument": "stu.student_documents.edit", "DeleteStudentDocument": "stu.student_documents.delete",
	} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
	}
}

// documentOperations are the operations that touch a child's identity documents. A read-only support visit (decision
// 0018) may not call them even though some are ".view": platform staff do not read certificates and parents' ID proofs.
var documentOperations = map[string]bool{
	"BeginStudentDocumentUpload": true, "AddStudentDocument": true, "ListStudentDocuments": true,
	"GetStudentDocumentDownload": true, "VerifyStudentDocument": true, "DeleteStudentDocument": true,
}

func toGenDocument(d files.Document) gen.StudentDocument {
	out := gen.StudentDocument{
		Id: mustUUID(d.ID), StudentId: mustUUID(d.StudentID), DocumentType: gen.StudentDocumentType(d.DocumentType), Title: d.Title,
		ContentType: gen.StudentDocumentContentType(d.ContentType), SizeBytes: d.SizeBytes, UploadedAt: d.UploadedAt, VerifiedAt: d.VerifiedAt,
	}
	if d.ValidUntil != nil {
		v := openapi_types.Date{Time: *d.ValidUntil}
		out.ValidUntil = &v
	}
	if d.UploadedBy != "" {
		out.UploadedBy = &d.UploadedBy
	}
	if d.VerifiedBy != "" {
		out.VerifiedBy = &d.VerifiedBy
	}
	return out
}

// BeginStudentDocumentUpload implements POST /students/{studentId}/documents/upload.
func (h *Handler) BeginStudentDocumentUpload(ctx context.Context, req gen.BeginStudentDocumentUploadRequestObject) (gen.BeginStudentDocumentUploadResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	if req.Body == nil {
		return gen.BeginStudentDocumentUpload400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "contentType and sizeBytes are required")}, nil
	}
	u, err := h.files.BeginStudentDocument(ctx, actorFrom(ctx), req.StudentId.String(), string(req.Body.ContentType), req.Body.SizeBytes)
	switch {
	case errors.Is(err, files.ErrDocumentNotAllowed):
		return gen.BeginStudentDocumentUpload400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("not_allowed", err.Error())}, nil
	case errors.Is(err, files.ErrNotFound):
		return gen.BeginStudentDocumentUpload404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.BeginStudentDocumentUpload201JSONResponse{FileId: mustUUID(u.FileID), UploadUrl: u.URL, Headers: u.Headers, ExpiresAt: u.ExpiresAt}, nil
}

// AddStudentDocument implements POST /students/{studentId}/documents.
func (h *Handler) AddStudentDocument(ctx context.Context, req gen.AddStudentDocumentRequestObject) (gen.AddStudentDocumentResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	if req.Body == nil {
		return gen.AddStudentDocument400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "fileId, documentType and title are required")}, nil
	}
	in := files.DocumentInput{DocumentType: string(req.Body.DocumentType), Title: req.Body.Title}
	if req.Body.ValidUntil != nil {
		v := req.Body.ValidUntil.Time
		in.ValidUntil = &v
	}
	d, err := h.files.AddStudentDocument(ctx, actorFrom(ctx), req.StudentId.String(), req.Body.FileId.String(), in)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.AddStudentDocument400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, files.ErrBadDocument):
		return gen.AddStudentDocument400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("bad_upload", err.Error())}, nil
	case errors.Is(err, files.ErrNotFound):
		return gen.AddStudentDocument404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.AddStudentDocument201JSONResponse(toGenDocument(d)), nil
}

// ListStudentDocuments implements GET /students/{studentId}/documents.
func (h *Handler) ListStudentDocuments(ctx context.Context, req gen.ListStudentDocumentsRequestObject) (gen.ListStudentDocumentsResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	list, err := h.files.ListStudentDocuments(ctx, actorFrom(ctx), req.StudentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.ListStudentDocuments404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	out := gen.ListStudentDocuments200JSONResponse{Items: make([]gen.StudentDocument, len(list))}
	for i, d := range list {
		out.Items[i] = toGenDocument(d)
	}
	return out, nil
}

// GetStudentDocumentDownload implements GET /students/{studentId}/documents/{documentId}/download.
func (h *Handler) GetStudentDocumentDownload(ctx context.Context, req gen.GetStudentDocumentDownloadRequestObject) (gen.GetStudentDocumentDownloadResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	d, err := h.files.StudentDocumentLink(ctx, actorFrom(ctx), req.StudentId.String(), req.DocumentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.GetStudentDocumentDownload404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentDocumentDownload200JSONResponse{Url: d.URL, ContentType: d.ContentType, Disposition: gen.StudentDocumentLinkDisposition(d.Disposition), ExpiresAt: d.ExpiresAt}, nil
}

// VerifyStudentDocument implements POST /students/{studentId}/documents/{documentId}/verify.
func (h *Handler) VerifyStudentDocument(ctx context.Context, req gen.VerifyStudentDocumentRequestObject) (gen.VerifyStudentDocumentResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	d, err := h.files.VerifyStudentDocument(ctx, actorFrom(ctx), req.StudentId.String(), req.DocumentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.VerifyStudentDocument404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.VerifyStudentDocument200JSONResponse(toGenDocument(d)), nil
}

// DeleteStudentDocument implements DELETE /students/{studentId}/documents/{documentId}.
func (h *Handler) DeleteStudentDocument(ctx context.Context, req gen.DeleteStudentDocumentRequestObject) (gen.DeleteStudentDocumentResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	err := h.files.DeleteStudentDocument(ctx, actorFrom(ctx), req.StudentId.String(), req.DocumentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.DeleteStudentDocument404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.DeleteStudentDocument204Response{}, nil
}
