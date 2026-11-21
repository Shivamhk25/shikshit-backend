package apiv1

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/importer"
)

// ImportService is what the onboarding operations need (importer.Service satisfies it).
type ImportService interface {
	Upload(ctx context.Context, actor audit.Actor, in importer.UploadInput) (importer.Batch, error)
	Get(ctx context.Context, id string) (importer.Batch, error)
	List(ctx context.Context, q crud.ListQuery, f importer.ListFilter) (crud.Page[importer.Batch], error)
	Rows(ctx context.Context, id, status string, q crud.ListQuery) (crud.Page[importer.RowView], error)
	FileProblems(ctx context.Context, id string) ([]importer.Problem, error)
	Template(entity string) ([]byte, error)
	PackStatus(ctx context.Context, key string) (importer.PackStatus, error)
	ErrorsCSV(ctx context.Context, id string) ([]byte, error)
	Commit(ctx context.Context, actor audit.Actor, id string) (importer.Batch, error)
	Rollback(ctx context.Context, actor audit.Actor, id string) (importer.Batch, error)
	Approve(ctx context.Context, actor audit.Actor, id string) (importer.Batch, error)
	Reject(ctx context.Context, actor audit.Actor, id, reason string) (importer.Batch, error)
}

// WithImports enables the /onboarding operations. reg lists the entities that can be imported.
func WithImports(s ImportService, reg *importer.Registry) Option {
	return func(h *Handler) { h.imports, h.importReg = s, reg }
}

// importAction is a permission action of an onboarding domain (onb.<domain>.<action>).
const (
	importView     = "view"
	importImport   = "import"
	importCommit   = "commit"
	importRollback = "rollback"
	importReview   = "review"
)

// canDo reports whether the caller holds onb.<domain>.<action> for the entity's domain.
func canDo(ctx context.Context, ent importer.Entity, action string) bool {
	a, ok := AccessFrom(ctx)
	return ok && a.Effective.Can("onb."+ent.Domain()+"."+action)
}

func notFound() gen.NotFoundApplicationProblemPlusJSONResponse {
	return gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))
}

func forbidden() gen.ForbiddenApplicationProblemPlusJSONResponse {
	return gen.ForbiddenApplicationProblemPlusJSONResponse(problemBody(http.StatusForbidden, "permission_denied", "You do not have permission to do this."))
}

func conflict(msg string) gen.ConflictApplicationProblemPlusJSONResponse {
	return gen.ConflictApplicationProblemPlusJSONResponse(problemBody(http.StatusConflict, "conflict", msg))
}

func badRequest(fe crud.FieldErrors) gen.BadRequestApplicationProblemPlusJSONResponse {
	return gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))
}

func toGenProblems(in []importer.Problem) []gen.ImportProblem {
	out := make([]gen.ImportProblem, len(in))
	for i, p := range in {
		out[i] = gen.ImportProblem{Code: p.Code, Message: p.Message, Severity: gen.ImportProblemSeverity(p.Severity)}
		if p.Column != "" {
			c := p.Column
			out[i].Column = &c
		}
	}
	return out
}

func toGenBatch(b importer.Batch) gen.ImportBatch {
	out := gen.ImportBatch{
		CommitMode: gen.ImportBatchCommitMode(b.CommitMode), CommittedAt: b.CommittedAt, CommittedRows: b.CommittedRows,
		CreatedAt: b.CreatedAt, Entity: b.Entity, ErrorRows: b.ErrorRows, Failure: b.Failure, FileName: b.FileName,
		ProgressDone: b.ProgressDone, ProgressTotal: b.ProgressTotal, RevertedRows: b.RevertedRows, RollbackDeadline: b.RollbackDeadline,
		RolledBackAt: b.RolledBackAt, Status: gen.ImportBatchStatus(b.Status), Strategy: gen.ImportBatchStrategy(b.Strategy),
		TemplateVersion: b.TemplateVersion, TotalRows: b.TotalRows, ValidRows: b.ValidRows, ValidatedAt: b.ValidatedAt, WarningRows: b.WarningRows,
		ApprovalRequired: b.ApprovalRequired, ApprovedAt: b.ApprovedAt, RejectedAt: b.RejectedAt, RejectReason: b.RejectReason,
	}
	out.ApprovedBy, out.RejectedBy = optUUID(b.ApprovedBy), optUUID(b.RejectedBy)
	out.Id = mustUUID(b.ID)
	if b.CreatedBy != nil {
		u := mustUUID(*b.CreatedBy)
		out.CreatedBy = &u
	}
	return out
}

func toGenEntity(e importer.Entity) gen.ImportEntity {
	cols := make([]gen.ImportColumn, len(e.Columns()))
	for i, c := range e.Columns() {
		col := gen.ImportColumn{Key: c.Key, Label: c.Label, Required: c.Required, Kind: gen.ImportColumnKind(c.Kind)}
		if c.Hint != "" {
			h := c.Hint
			col.Hint = &h
		}
		if c.Example != "" {
			x := c.Example
			col.Example = &x
		}
		if len(c.Allowed) > 0 {
			a := c.Allowed
			col.Allowed = &a
		}
		if c.Ref != "" {
			r := c.Ref
			col.Ref = &r
		}
		cols[i] = col
	}
	return gen.ImportEntity{Key: e.Key(), Title: e.Title(), Domain: e.Domain(), TemplateVersion: e.TemplateVersion(), Columns: cols}
}

// ListImportEntities implements GET /onboarding/entities. Only entities whose domain the caller may view are listed.
func (h *Handler) ListImportEntities(ctx context.Context, _ gen.ListImportEntitiesRequestObject) (gen.ListImportEntitiesResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	out := gen.ImportEntityList{Items: []gen.ImportEntity{}}
	for _, e := range h.importReg.All() {
		if canDo(ctx, e, importView) {
			out.Items = append(out.Items, toGenEntity(e))
		}
	}
	return gen.ListImportEntities200JSONResponse(out), nil
}

// DownloadImportTemplate implements GET /onboarding/templates/{entity}.
func (h *Handler) DownloadImportTemplate(ctx context.Context, req gen.DownloadImportTemplateRequestObject) (gen.DownloadImportTemplateResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	ent, ok := h.importReg.Get(req.Entity)
	if !ok {
		return gen.DownloadImportTemplate404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if !canDo(ctx, ent, importImport) {
		return gen.DownloadImportTemplate403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	data, err := h.imports.Template(req.Entity)
	if err != nil {
		return nil, err
	}
	return gen.DownloadImportTemplate200TextcsvResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data))}, nil
}

// ListImportBatches implements GET /onboarding/imports.
func (h *Handler) ListImportBatches(ctx context.Context, req gen.ListImportBatchesRequestObject) (gen.ListImportBatchesResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, nil, importer.BatchSorts, "-createdAt")
	if qerr != nil {
		return gen.ListImportBatches400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(qerr)}, nil
	}
	f := importer.ListFilter{Entities: []string{}}
	for _, e := range h.importReg.All() {
		if canDo(ctx, e, importView) {
			f.Entities = append(f.Entities, e.Key())
		}
	}
	if req.Params.Status != nil {
		f.Status = string(*req.Params.Status)
	}
	if req.Params.Entity != nil {
		f.Entity = *req.Params.Entity
	}
	p, err := h.imports.List(ctx, q, f)
	if err != nil {
		return nil, err
	}
	out := gen.ImportBatchPage{Items: make([]gen.ImportBatch, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, b := range p.Items {
		out.Items[i] = toGenBatch(b)
	}
	return gen.ListImportBatches200JSONResponse(out), nil
}

// uploadFields are read from the multipart form; the file is read last and bounded.
type uploadForm struct {
	entity, strategy, commitMode, fileName string
	data                                   []byte
}

// readUpload reads the form parts. Text fields are capped at 256 bytes; the file at MaxFileBytes (more is reported as
// a file error, never buffered).
func readUpload(r *multipart.Reader) (uploadForm, crud.FieldErrors) {
	var f uploadForm
	var fe crud.FieldErrors
	for {
		part, err := r.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fe.Add("file", "invalid_upload", "The upload could not be read. Try again.")
			return f, fe
		}
		switch part.FormName() {
		case "file":
			b, err := io.ReadAll(io.LimitReader(part, importer.MaxFileBytes+1))
			if err != nil {
				fe.Add("file", "invalid_upload", "The upload could not be read. Try again.")
				return f, fe
			}
			if len(b) > importer.MaxFileBytes {
				fe.Add("file", "file_too_large", "The file is larger than 10 MB.")
				return f, fe
			}
			f.fileName, f.data = part.FileName(), b
		case "entity", "strategy", "commitMode":
			b, _ := io.ReadAll(io.LimitReader(part, 256))
			v := strings.TrimSpace(string(b))
			switch part.FormName() {
			case "entity":
				f.entity = v
			case "strategy":
				f.strategy = v
			default:
				f.commitMode = v
			}
		}
		_ = part.Close()
	}
	if f.data == nil {
		fe.Add("file", "required", "Choose a file to upload.")
	}
	return f, fe
}

// UploadImport implements POST /onboarding/imports.
func (h *Handler) UploadImport(ctx context.Context, req gen.UploadImportRequestObject) (gen.UploadImportResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.UploadImport400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A file is required."))}, nil
	}
	form, fe := readUpload(req.Body)
	if len(fe) > 0 {
		return gen.UploadImport400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(fe)}, nil
	}
	// An unknown entity is a field error (400); a known entity the caller may not import is 403.
	ent, ok := h.importReg.Get(form.entity)
	if !ok {
		var e crud.FieldErrors
		e.Add("entity", "unknown_entity", "This kind of data cannot be imported.")
		return gen.UploadImport400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(e)}, nil
	}
	if !canDo(ctx, ent, importImport) {
		return gen.UploadImport403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	b, err := h.imports.Upload(ctx, actorFrom(ctx), importer.UploadInput{
		Entity: form.entity, Strategy: form.strategy, CommitMode: form.commitMode, FileName: form.fileName, Data: form.data,
	})
	var ferr crud.FieldErrors
	if errors.As(err, &ferr) {
		return gen.UploadImport400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(ferr)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.UploadImport202JSONResponse(toGenBatch(b)), nil
}

// visibleBatch loads a batch and its entity and checks the caller may view it. ok is false when the batch must answer
// 404: it does not exist here, or the caller has no view access to its domain (existence is not revealed).
func (h *Handler) visibleBatch(ctx context.Context, id openapi_types.UUID) (b importer.Batch, ent importer.Entity, ok bool, err error) {
	b, err = h.imports.Get(ctx, id.String())
	if errors.Is(err, importer.ErrNotFound) {
		return b, nil, false, nil
	}
	if err != nil {
		return b, nil, false, err
	}
	ent, found := h.importReg.Get(b.Entity)
	if !found || !canDo(ctx, ent, importView) {
		return b, nil, false, nil
	}
	return b, ent, true, nil
}

// GetImportBatch implements GET /onboarding/imports/{batchId}.
func (h *Handler) GetImportBatch(ctx context.Context, req gen.GetImportBatchRequestObject) (gen.GetImportBatchResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, _, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.GetImportBatch404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	out := toGenBatch(b)
	ps, err := h.imports.FileProblems(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	gp := toGenProblems(ps)
	out.FileProblems = &gp
	return gen.GetImportBatch200JSONResponse(out), nil
}

// ListImportRows implements GET /onboarding/imports/{batchId}/rows.
func (h *Handler) ListImportRows(ctx context.Context, req gen.ListImportRowsRequestObject) (gen.ListImportRowsResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, nil, nil, importer.RowSorts, "rowNo")
	if qerr != nil {
		return gen.ListImportRows400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(qerr)}, nil
	}
	b, _, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.ListImportRows404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	status := ""
	if req.Params.Status != nil {
		status = string(*req.Params.Status)
	}
	p, err := h.imports.Rows(ctx, b.ID, status, q)
	if err != nil {
		return nil, err
	}
	out := gen.ImportRowPage{Items: make([]gen.ImportRow, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, r := range p.Items {
		row := gen.ImportRow{RowNo: r.RowNo, Status: gen.ImportRowStatus(r.Status), Cells: map[string]string(r.Cells), Problems: toGenProblems(r.Problems)}
		if row.Cells == nil {
			row.Cells = map[string]string{}
		}
		if r.Action != nil {
			a := gen.ImportRowAction(*r.Action)
			row.Action = &a
		}
		out.Items[i] = row
	}
	return gen.ListImportRows200JSONResponse(out), nil
}

// DownloadImportErrors implements GET /onboarding/imports/{batchId}/errors.csv.
func (h *Handler) DownloadImportErrors(ctx context.Context, req gen.DownloadImportErrorsRequestObject) (gen.DownloadImportErrorsResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, _, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.DownloadImportErrors404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	data, err := h.imports.ErrorsCSV(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	return gen.DownloadImportErrors200TextcsvResponse{Body: bytes.NewReader(data), ContentLength: int64(len(data))}, nil
}

// CommitImportBatch implements POST /onboarding/imports/{batchId}/commit.
func (h *Handler) CommitImportBatch(ctx context.Context, req gen.CommitImportBatchRequestObject) (gen.CommitImportBatchResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, ent, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.CommitImportBatch404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if !canDo(ctx, ent, importCommit) {
		return gen.CommitImportBatch403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	out, err := h.imports.Commit(ctx, actorFrom(ctx), b.ID)
	var ce importer.ConflictError
	if errors.As(err, &ce) {
		return gen.CommitImportBatch409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: conflict(ce.Message)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.CommitImportBatch202JSONResponse(toGenBatch(out)), nil
}

// RollbackImportBatch implements POST /onboarding/imports/{batchId}/rollback.
func (h *Handler) RollbackImportBatch(ctx context.Context, req gen.RollbackImportBatchRequestObject) (gen.RollbackImportBatchResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, ent, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.RollbackImportBatch404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if !canDo(ctx, ent, importRollback) {
		return gen.RollbackImportBatch403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	out, err := h.imports.Rollback(ctx, actorFrom(ctx), b.ID)
	var ce importer.ConflictError
	if errors.As(err, &ce) {
		return gen.RollbackImportBatch409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: conflict(ce.Message)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.RollbackImportBatch202JSONResponse(toGenBatch(out)), nil
}

// GetImportPack implements GET /onboarding/packs/{pack}. A pack whose domain the caller cannot view does not exist
// for them.
func (h *Handler) GetImportPack(ctx context.Context, req gen.GetImportPackRequestObject) (gen.GetImportPackResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	st, err := h.imports.PackStatus(ctx, req.Pack)
	if errors.Is(err, importer.ErrNotFound) {
		return gen.GetImportPack404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	if a, ok := AccessFrom(ctx); !ok || !a.Effective.Can("onb."+st.Domain+"."+importView) {
		return gen.GetImportPack404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	out := gen.ImportPack{Key: st.Key, Title: st.Title, Domain: st.Domain, Ready: st.Ready,
		Steps: make([]gen.ImportPackStep, len(st.Steps)), Rules: make([]gen.ImportPackRule, len(st.Rules))}
	for i, s := range st.Steps {
		out.Steps[i] = gen.ImportPackStep{Entity: s.Entity, Title: s.Title, Order: s.Order, Count: s.Count, State: gen.ImportPackStepState(s.State), Required: s.Required, Requires: s.Requires, Next: s.Next}
	}
	for i, r := range st.Rules {
		rule := gen.ImportPackRule{Key: r.Key, Label: r.Label, Met: r.Met, Checked: r.Checked}
		if r.Detail != "" {
			d := r.Detail
			rule.Detail = &d
		}
		out.Rules[i] = rule
	}
	return gen.GetImportPack200JSONResponse(out), nil
}

// optUUID converts an optional id string to the contract's optional UUID.
func optUUID(s *string) *openapi_types.UUID {
	if s == nil {
		return nil
	}
	u := mustUUID(*s)
	return &u
}

// ApproveImportBatch implements POST /onboarding/imports/{batchId}/approve.
func (h *Handler) ApproveImportBatch(ctx context.Context, req gen.ApproveImportBatchRequestObject) (gen.ApproveImportBatchResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, ent, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.ApproveImportBatch404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if !canDo(ctx, ent, importReview) {
		return gen.ApproveImportBatch403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	out, err := h.imports.Approve(ctx, actorFrom(ctx), b.ID)
	var ce importer.ConflictError
	if errors.As(err, &ce) {
		return gen.ApproveImportBatch409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: conflict(ce.Message)}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.ApproveImportBatch202JSONResponse(toGenBatch(out)), nil
}

// RejectImportBatch implements POST /onboarding/imports/{batchId}/reject.
func (h *Handler) RejectImportBatch(ctx context.Context, req gen.RejectImportBatchRequestObject) (gen.RejectImportBatchResponseObject, error) {
	if h.imports == nil || h.importReg == nil {
		return nil, errNotConfigured
	}
	b, ent, ok, err := h.visibleBatch(ctx, req.BatchId)
	if err != nil {
		return nil, err
	}
	if !ok {
		return gen.RejectImportBatch404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: notFound()}, nil
	}
	if !canDo(ctx, ent, importReview) {
		return gen.RejectImportBatch403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: forbidden()}, nil
	}
	if req.Body == nil {
		return gen.RejectImportBatch400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A reason is required."))}, nil
	}
	out, err := h.imports.Reject(ctx, actorFrom(ctx), b.ID, req.Body.Reason)
	var fe crud.FieldErrors
	var ce importer.ConflictError
	switch {
	case errors.As(err, &fe):
		return gen.RejectImportBatch400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: badRequest(fe)}, nil
	case errors.As(err, &ce):
		return gen.RejectImportBatch409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: conflict(ce.Message)}, nil
	case err != nil:
		return nil, err
	}
	return gen.RejectImportBatch200JSONResponse(toGenBatch(out)), nil
}
