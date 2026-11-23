package files

// Student documents (STU-03): a birth certificate, a transfer certificate, a parent's ID proof and the like. They reuse
// the upload flow of photos (signed PUT link, confirm, bytes checked) with these differences:
//   - allowed types are PDF, JPEG and PNG, at most 10 MB. PDF is recognised by its first bytes (`%PDF-`); HTML, SVG, GIF,
//     WebP, Office files and anything else are refused, so a document can never carry a script;
//   - confirming also records what the document is (type, title, optional valid-until date), in one step;
//   - a download link pins the content type and the disposition: a PDF is always an attachment, an image is inline;
//   - every list and every download is audited; a delete removes the object from storage.
// Who may call these is decided by the API (tenant-wide stu.student_documents.* only); this package only works inside
// the caller's institute.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// MaxDocumentSize is 10 MB: a multi-page scan or a phone photo of an A4 sheet is 1 to 6 MB, so this fits normal use and
// still bounds storage cost and a hostile upload.
const MaxDocumentSize = 10 << 20

// StudentDocument is the purpose of a document kept on a student's record.
var StudentDocument = Purpose{Types: []string{"application/pdf", "image/jpeg", "image/png"}, MaxSize: MaxDocumentSize}

// DocumentTypes are the kinds of document a school keeps. Caste, category and religion certificates are not collected
// (decision 0009); a disability certificate waits for the founder's answer.
var DocumentTypes = []string{
	"birth_certificate", "transfer_certificate", "previous_marksheet", "migration_certificate",
	"medical_certificate", "parent_id_proof", "address_proof", "other",
}

var (
	// ErrDocumentNotAllowed is returned for a type or size documents do not accept; its message is shown to the user.
	ErrDocumentNotAllowed = errors.New("Upload a PDF, JPEG or PNG file of at most 10 MB.")
	// ErrBadDocument is returned when the uploaded bytes are not what was asked for, or nothing was uploaded.
	ErrBadDocument = errors.New("The upload did not arrive, or it is not a PDF, JPEG or PNG file of at most 10 MB. Try again.")
	// ErrNoDispositionStore is returned when the storage cannot pin Content-Disposition; documents are not served then.
	ErrNoDispositionStore = errors.New("files: the storage cannot pin the download disposition")
)

// DocumentInput is what the school says a document is.
type DocumentInput struct {
	DocumentType string
	Title        string
	ValidUntil   *time.Time // a date; nil when the document does not expire
}

// Document is one document on a student's record.
type Document struct {
	ID           string
	StudentID    string
	DocumentType string
	Title        string
	ContentType  string
	SizeBytes    int64
	ValidUntil   *time.Time
	UploadedAt   time.Time
	UploadedBy   string // the uploader's name; empty when the account is gone
	VerifiedAt   *time.Time
	VerifiedBy   string
}

// DocumentDownload is a short-lived link to a document.
type DocumentDownload struct {
	URL         string
	ContentType string
	Disposition string // "attachment" or "inline"
	ExpiresAt   time.Time
}

func isDocumentType(t string) bool { return contains(DocumentTypes, t) }

func (in DocumentInput) validate() (DocumentInput, error) {
	var errs crud.FieldErrors
	in.DocumentType = strings.TrimSpace(in.DocumentType)
	in.Title = strings.TrimSpace(in.Title)
	if !isDocumentType(in.DocumentType) {
		errs = append(errs, crud.FieldError{Field: "documentType", Code: "invalid", Message: "Choose the kind of document."})
	}
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > 120 {
		errs = append(errs, crud.FieldError{Field: "title", Code: "invalid", Message: "Give the document a title of up to 120 characters."})
	}
	if in.ValidUntil != nil {
		y := in.ValidUntil.Year()
		if y < 1990 || y > 2100 {
			errs = append(errs, crud.FieldError{Field: "validUntil", Code: "invalid", Message: "Enter a valid date."})
		}
	}
	if len(errs) > 0 {
		return in, errs
	}
	return in, nil
}

// sniffDocument returns the content type the first bytes show: application/pdf, image/jpeg or image/png, or "" for
// anything else (HTML, SVG, scripts, archives, other images).
func sniffDocument(head []byte) string {
	if bytes.HasPrefix(head, []byte("%PDF-")) {
		return "application/pdf"
	}
	switch t := http.DetectContentType(head); t {
	case "image/jpeg", "image/png":
		return t
	}
	return ""
}

// DocumentDisposition says how a browser must treat a download: a PDF is always saved (never rendered in the school's
// or the storage's page); an image may be shown. The file name is built from the type, never from user text.
func DocumentDisposition(contentType, documentType string) string {
	ext, kind := "bin", "attachment"
	switch contentType {
	case "application/pdf":
		ext = "pdf"
	case "image/jpeg":
		ext, kind = "jpg", "inline"
	case "image/png":
		ext, kind = "png", "inline"
	}
	if !isDocumentType(documentType) {
		documentType = "document"
	}
	return kind + `; filename="` + documentType + "." + ext + `"`
}

// BeginStudentDocument records a pending upload and returns the signed link to send it to.
func (s Service) BeginStudentDocument(ctx context.Context, a audit.Actor, studentID, contentType string, size int64) (Upload, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Upload{}, tenancy.ErrNoTenant
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if !StudentDocument.allows(contentType, size) {
		return Upload{}, ErrDocumentNotAllowed
	}
	id := uuid.NewString()
	key := "t/" + t.ID + "/student_document/" + id
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := studentExists(ctx, tx, studentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO files (id, tenant_id, purpose, subject_id, object_key, content_type, size_bytes, created_by)
			VALUES ($1::uuid, $2::uuid, 'student_document', $3::uuid, $4, $5, $6, NULLIF($7, '')::uuid)`, id, t.ID, studentID, key, contentType, size, a.UserID)
		return err
	})
	if err != nil {
		return Upload{}, err
	}
	u, headers, err := s.Store.PresignPut(ctx, key, contentType, UploadTTL)
	if err != nil {
		return Upload{}, err
	}
	return Upload{FileID: id, URL: u, Headers: headers, ExpiresAt: s.now().Add(UploadTTL)}, nil
}

const documentColumns = `d.id::text, d.student_id::text, d.document_type, d.title, f.content_type, f.size_bytes, d.valid_until, d.uploaded_at,
	COALESCE(uu.display_name, ''), d.verified_at, COALESCE(vu.display_name, '')`

const documentFrom = ` FROM student_documents d
	JOIN files f ON f.tenant_id = d.tenant_id AND f.id = d.file_id AND f.status = 'ready'
	LEFT JOIN users uu ON uu.id = d.uploaded_by
	LEFT JOIN users vu ON vu.id = d.verified_by`

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	err := row.Scan(&d.ID, &d.StudentID, &d.DocumentType, &d.Title, &d.ContentType, &d.SizeBytes, &d.ValidUntil, &d.UploadedAt, &d.UploadedBy, &d.VerifiedAt, &d.VerifiedBy)
	return d, err
}

// AddStudentDocument checks the uploaded bytes and, if they pass, records the document on the student. The input is
// checked first, so a typo leaves the upload pending and the client can confirm again. A file that fails the byte
// checks is marked rejected and removed from storage.
func (s Service) AddStudentDocument(ctx context.Context, a audit.Actor, studentID, fileID string, in DocumentInput) (Document, error) {
	in, err := in.validate()
	if err != nil {
		return Document{}, err
	}
	if _, err := uuid.Parse(fileID); err != nil {
		return Document{}, ErrNotFound
	}
	var (
		key, declared string
		rejected      bool
		out           Document
	)
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := studentExists(ctx, tx, studentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		err = tx.QueryRow(ctx, `SELECT object_key, content_type FROM files
			WHERE id = $1::uuid AND purpose = 'student_document' AND subject_id::text = $2 AND status = 'pending'
			  AND created_by IS NOT DISTINCT FROM NULLIF($3, '')::uuid AND created_at > $4 FOR UPDATE`,
			fileID, studentID, a.UserID, s.now().Add(-UploadTTL-time.Minute)).Scan(&key, &declared)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		size, head, herr := s.Store.Head(ctx, key, 512)
		if herr != nil && !errors.Is(herr, storage.ErrNotFound) {
			return herr
		}
		if herr != nil || sniffDocument(head) != declared || !StudentDocument.allows(declared, size) {
			rejected = true
			_, err := tx.Exec(ctx, `UPDATE files SET status = 'rejected' WHERE id = $1::uuid`, fileID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET status = 'ready', size_bytes = $2, ready_at = now() WHERE id = $1::uuid`, fileID, size); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO student_documents (tenant_id, student_id, document_type, title, file_id, valid_until, uploaded_by)
			VALUES (app_current_tenant(), $1::uuid, $2, $3, $4::uuid, $5, NULLIF($6, '')::uuid) RETURNING id::text`,
			studentID, in.DocumentType, in.Title, fileID, in.ValidUntil, a.UserID).Scan(&id); err != nil {
			return err
		}
		if out, err = scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+documentFrom+` WHERE d.id = $1::uuid`, id)); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_documents.upload", ResourceType: "student_document", ResourceID: id,
			After: map[string]string{"studentId": studentID, "documentType": in.DocumentType, "fileId": fileID}})
	})
	if err != nil {
		return Document{}, err
	}
	if rejected {
		_ = s.Store.Remove(ctx, key)
		return Document{}, ErrBadDocument
	}
	return out, nil
}

// ListStudentDocuments returns the student's documents, newest first, and records the read. The caller must already be
// allowed to read documents (the API checks that, and that the student is in the caller's institute).
func (s Service) ListStudentDocuments(ctx context.Context, a audit.Actor, studentID string) ([]Document, error) {
	if _, err := uuid.Parse(studentID); err != nil {
		return nil, ErrNotFound
	}
	out := []Document{}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := studentExists(ctx, tx, studentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `SELECT `+documentColumns+documentFrom+` WHERE d.student_id = $1::uuid AND d.deleted_at IS NULL ORDER BY d.uploaded_at DESC, d.id`, studentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDocument(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_documents.list", ResourceType: "student", ResourceID: studentID})
	})
	return out, err
}

// StudentDocumentLink returns a 5-minute link to one document and records the download.
func (s Service) StudentDocumentLink(ctx context.Context, a audit.Actor, studentID, documentID string) (DocumentDownload, error) {
	ds, ok := s.Store.(storage.DispositionStore)
	if !ok {
		return DocumentDownload{}, ErrNoDispositionStore
	}
	if _, err := uuid.Parse(studentID); err != nil {
		return DocumentDownload{}, ErrNotFound
	}
	if _, err := uuid.Parse(documentID); err != nil {
		return DocumentDownload{}, ErrNotFound
	}
	var key, contentType, docType string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT f.object_key, f.content_type, d.document_type FROM student_documents d
			JOIN files f ON f.tenant_id = d.tenant_id AND f.id = d.file_id AND f.status = 'ready'
			WHERE d.id = $1::uuid AND d.student_id = $2::uuid AND d.deleted_at IS NULL`, documentID, studentID).Scan(&key, &contentType, &docType)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_documents.download", ResourceType: "student_document", ResourceID: documentID,
			After: map[string]string{"studentId": studentID, "documentType": docType}})
	})
	if err != nil {
		return DocumentDownload{}, err
	}
	disposition := DocumentDisposition(contentType, docType)
	u, err := ds.PresignGetAs(ctx, key, contentType, disposition, DownloadTTL)
	if err != nil {
		return DocumentDownload{}, err
	}
	return DocumentDownload{URL: u, ContentType: contentType, Disposition: strings.SplitN(disposition, ";", 2)[0], ExpiresAt: s.now().Add(DownloadTTL)}, nil
}

// VerifyStudentDocument records that the school has checked the document against the original. Verifying twice keeps
// the first verification.
func (s Service) VerifyStudentDocument(ctx context.Context, a audit.Actor, studentID, documentID string) (Document, error) {
	if _, err := uuid.Parse(studentID); err != nil {
		return Document{}, ErrNotFound
	}
	if _, err := uuid.Parse(documentID); err != nil {
		return Document{}, ErrNotFound
	}
	var out Document
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE student_documents SET verified_by = NULLIF($3, '')::uuid, verified_at = now()
			WHERE id = $1::uuid AND student_id = $2::uuid AND deleted_at IS NULL AND verified_at IS NULL`, documentID, studentID, a.UserID)
		if err != nil {
			return err
		}
		out, err = scanDocument(tx.QueryRow(ctx, `SELECT `+documentColumns+documentFrom+` WHERE d.id = $1::uuid AND d.student_id = $2::uuid AND d.deleted_at IS NULL`, documentID, studentID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_documents.verify", ResourceType: "student_document", ResourceID: documentID,
			After: map[string]string{"studentId": studentID, "documentType": out.DocumentType}})
	})
	return out, err
}

// DeleteStudentDocument removes a document from the student: the row is kept as history (soft delete) and the file is
// removed from storage.
func (s Service) DeleteStudentDocument(ctx context.Context, a audit.Actor, studentID, documentID string) error {
	if _, err := uuid.Parse(studentID); err != nil {
		return ErrNotFound
	}
	if _, err := uuid.Parse(documentID); err != nil {
		return ErrNotFound
	}
	var key string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var fileID, docType string
		err := tx.QueryRow(ctx, `SELECT d.file_id::text, d.document_type, f.object_key FROM student_documents d
			JOIN files f ON f.tenant_id = d.tenant_id AND f.id = d.file_id
			WHERE d.id = $1::uuid AND d.student_id = $2::uuid AND d.deleted_at IS NULL FOR UPDATE OF d`, documentID, studentID).Scan(&fileID, &docType, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE student_documents SET deleted_at = now(), deleted_by = NULLIF($2, '')::uuid WHERE id = $1::uuid`, documentID, a.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`, fileID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "student_documents.delete", ResourceType: "student_document", ResourceID: documentID,
			Before: map[string]string{"studentId": studentID, "documentType": docType, "fileId": fileID}})
	})
	if err != nil {
		return err
	}
	_ = s.Store.Remove(ctx, key) // the row is already retired; a leftover object is private and the object name is unguessable
	return nil
}
