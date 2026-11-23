package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/files"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// PhotoService is what the student photo operations need (files.Service satisfies it).
type PhotoService interface {
	BeginStudentPhoto(ctx context.Context, a audit.Actor, studentID, contentType string, size int64) (files.Upload, error)
	ConfirmStudentPhoto(ctx context.Context, a audit.Actor, studentID, fileID string) error
	StudentPhoto(ctx context.Context, a audit.Actor, studentID string) (files.Download, error)
	RemoveStudentPhoto(ctx context.Context, a audit.Actor, studentID string) error
	DocumentService // student documents (STU-03) ride on the same file service
}

// WithFiles enables the file operations (FND-17). Without it they answer 503: file storage is not set up.
func WithFiles(s PhotoService) Option { return func(h *Handler) { h.files = s } }

func init() {
	for op, perm := range map[string]string{"BeginStudentPhotoUpload": "stu.students.edit", "GetStudentPhoto": "stu.students.view", "SetStudentPhoto": "stu.students.edit", "RemoveStudentPhoto": "stu.students.edit"} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
	}
}

var errStorageNotConfigured = errors.New("apiv1: file storage is not configured (SHIKSHIT_STORAGE_*)")

// canReadStudent applies the student's own read rules (institute and class-section scope) before any photo work.
func (h *Handler) canReadStudent(ctx context.Context, id string) (bool, error) {
	if h.students == nil {
		return false, errNotConfigured
	}
	_, err := h.students.View(ctx, actorFrom(ctx), id)
	if errors.Is(err, resource.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func photoNotFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

// BeginStudentPhotoUpload implements POST /students/{studentId}/photo/upload.
func (h *Handler) BeginStudentPhotoUpload(ctx context.Context, req gen.BeginStudentPhotoUploadRequestObject) (gen.BeginStudentPhotoUploadResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	if req.Body == nil {
		return gen.BeginStudentPhotoUpload400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "contentType and sizeBytes are required")}, nil
	}
	u, err := h.files.BeginStudentPhoto(ctx, actorFrom(ctx), req.StudentId.String(), string(req.Body.ContentType), req.Body.SizeBytes)
	switch {
	case errors.Is(err, files.ErrNotAllowed):
		return gen.BeginStudentPhotoUpload400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("not_allowed", err.Error())}, nil
	case errors.Is(err, files.ErrNotFound):
		return gen.BeginStudentPhotoUpload404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.BeginStudentPhotoUpload201JSONResponse{FileId: mustUUID(u.FileID), UploadUrl: u.URL, Headers: u.Headers, ExpiresAt: u.ExpiresAt}, nil
}

// SetStudentPhoto implements PUT /students/{studentId}/photo.
func (h *Handler) SetStudentPhoto(ctx context.Context, req gen.SetStudentPhotoRequestObject) (gen.SetStudentPhotoResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	if req.Body == nil {
		return gen.SetStudentPhoto400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("validation_failed", "fileId is required")}, nil
	}
	err := h.files.ConfirmStudentPhoto(ctx, actorFrom(ctx), req.StudentId.String(), req.Body.FileId.String())
	switch {
	case errors.Is(err, files.ErrBadUpload):
		return gen.SetStudentPhoto400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: mfaBadRequest("bad_upload", err.Error())}, nil
	case errors.Is(err, files.ErrNotFound):
		return gen.SetStudentPhoto404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetStudentPhoto204Response{}, nil
}

// GetStudentPhoto implements GET /students/{studentId}/photo.
func (h *Handler) GetStudentPhoto(ctx context.Context, req gen.GetStudentPhotoRequestObject) (gen.GetStudentPhotoResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	ok, err := h.canReadStudent(ctx, req.StudentId.String())
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetStudentPhoto404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	d, err := h.files.StudentPhoto(ctx, actorFrom(ctx), req.StudentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.GetStudentPhoto404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentPhoto200JSONResponse{Url: d.URL, ExpiresAt: d.ExpiresAt}, nil
}

// RemoveStudentPhoto implements DELETE /students/{studentId}/photo.
func (h *Handler) RemoveStudentPhoto(ctx context.Context, req gen.RemoveStudentPhotoRequestObject) (gen.RemoveStudentPhotoResponseObject, error) {
	if h.files == nil {
		return nil, errStorageNotConfigured
	}
	err := h.files.RemoveStudentPhoto(ctx, actorFrom(ctx), req.StudentId.String())
	if errors.Is(err, files.ErrNotFound) {
		return gen.RemoveStudentPhoto404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RemoveStudentPhoto204Response{}, nil
}
