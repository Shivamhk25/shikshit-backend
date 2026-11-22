// Package files records uploaded files and checks them (FND-17, decision 0020). The first use is student photos.
//
// Flow: the client asks for an upload (the type and size it intends), gets a signed PUT link that lasts 10 minutes and
// uploads straight to storage; then it confirms. Confirming reads the object back: its real size must be within the
// limit and its first bytes must show an allowed type. Anything else is removed from storage and rejected.
//
// Rules enforced here (each has a test):
//   - only allowed types and sizes are offered a link, and the content type is part of the signature;
//   - confirming checks the real bytes, not what the client said; a mismatch is rejected and the object removed;
//   - an upload can be confirmed once, only by whoever started it, only for the record it was started for;
//   - every query runs inside the caller's institute (row-level security), and object keys start with the institute;
//   - download links last 5 minutes and pin the content type; every photo view is audited.
package files

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Link lifetimes.
const (
	UploadTTL   = 10 * time.Minute
	DownloadTTL = 5 * time.Minute
)

var (
	// ErrNotFound is returned for an unknown record, file or photo.
	ErrNotFound = errors.New("files: not found")
	// ErrNotAllowed is returned for a type or size the purpose does not accept; its message is shown to the user.
	ErrNotAllowed = errors.New("Upload a JPEG, PNG or WebP photo of at most 5 MB.")
	// ErrBadUpload is returned when the uploaded bytes do not match what was asked for, or nothing was uploaded.
	ErrBadUpload = errors.New("The upload did not arrive, or it is not a JPEG, PNG or WebP photo of at most 5 MB. Try again.")
)

// Purpose describes what a kind of file may be.
type Purpose struct {
	Types   []string
	MaxSize int64
}

// StudentPhoto is the purpose of a student's photo.
var StudentPhoto = Purpose{Types: []string{"image/jpeg", "image/png", "image/webp"}, MaxSize: 5 << 20}

// Upload is what the client needs to send the bytes.
type Upload struct {
	FileID    string
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
}

// Download is a short-lived link to a file.
type Download struct {
	URL       string
	ExpiresAt time.Time
}

// Service records files and talks to storage.
type Service struct {
	DB    tenancy.Beginner
	Store storage.Store
	Now   func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (p Purpose) allows(contentType string, size int64) bool {
	return size > 0 && size <= p.MaxSize && contains(p.Types, contentType)
}

func studentExists(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return false, nil
	}
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM students WHERE id = $1::uuid AND deleted_at IS NULL)`, id).Scan(&ok)
	return ok, err
}

// BeginStudentPhoto records a pending upload and returns the signed link to send it to.
func (s Service) BeginStudentPhoto(ctx context.Context, a audit.Actor, studentID, contentType string, size int64) (Upload, error) {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Upload{}, tenancy.ErrNoTenant
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if !StudentPhoto.allows(contentType, size) {
		return Upload{}, ErrNotAllowed
	}
	id := uuid.NewString()
	key := "t/" + t.ID + "/student_photo/" + id
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := studentExists(ctx, tx, studentID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO files (id, tenant_id, purpose, subject_id, object_key, content_type, size_bytes, created_by)
			VALUES ($1::uuid, $2::uuid, 'student_photo', $3::uuid, $4, $5, $6, NULLIF($7, '')::uuid)`, id, t.ID, studentID, key, contentType, size, a.UserID)
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

// sniff returns the content type the first bytes show. WebP is recognised explicitly (older Go versions miss it).
func sniff(head []byte) string {
	if len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP" {
		return "image/webp"
	}
	return http.DetectContentType(head)
}

// ConfirmStudentPhoto checks the uploaded bytes and, if they pass, makes the file the student's photo. The previous
// photo is retired and removed from storage.
func (s Service) ConfirmStudentPhoto(ctx context.Context, a audit.Actor, studentID, fileID string) error {
	if _, err := uuid.Parse(fileID); err != nil {
		return ErrNotFound
	}
	var (
		key, declared, oldKey string
		rejected              bool
	)
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT object_key, content_type FROM files
			WHERE id = $1::uuid AND purpose = 'student_photo' AND subject_id::text = $2 AND status = 'pending'
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
		if herr != nil || sniff(head) != declared || !StudentPhoto.allows(declared, size) {
			rejected = true
			_, err := tx.Exec(ctx, `UPDATE files SET status = 'rejected' WHERE id = $1::uuid`, fileID)
			return err
		}
		var old *string
		if err := tx.QueryRow(ctx, `SELECT f.object_key FROM students st JOIN files f ON f.tenant_id = st.tenant_id AND f.id = st.photo_file_id
			WHERE st.id = $1::uuid`, studentID).Scan(&old); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if old != nil {
			oldKey = *old
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET status = 'deleted', deleted_at = now()
			WHERE id = (SELECT photo_file_id FROM students WHERE id = $1::uuid)`, studentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET status = 'ready', size_bytes = $2, ready_at = now() WHERE id = $1::uuid`, fileID, size); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE students SET photo_file_id = $2::uuid WHERE id = $1::uuid AND deleted_at IS NULL`, studentID, fileID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "students.photo_set", ResourceType: "student", ResourceID: studentID, After: map[string]string{"fileId": fileID}})
	})
	if err != nil {
		return err
	}
	if rejected {
		_ = s.Store.Remove(ctx, key)
		return ErrBadUpload
	}
	if oldKey != "" {
		_ = s.Store.Remove(ctx, oldKey) // the row is already retired; a leftover object is harmless and private
	}
	return nil
}

// StudentPhoto returns a short-lived link to the student's photo and records the view. The caller must already be
// allowed to read the student (the API checks that with the student's own read rules first).
func (s Service) StudentPhoto(ctx context.Context, a audit.Actor, studentID string) (Download, error) {
	if _, err := uuid.Parse(studentID); err != nil {
		return Download{}, ErrNotFound
	}
	var key, contentType string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT f.object_key, f.content_type FROM students st
			JOIN files f ON f.tenant_id = st.tenant_id AND f.id = st.photo_file_id AND f.status = 'ready'
			WHERE st.id = $1::uuid AND st.deleted_at IS NULL`, studentID).Scan(&key, &contentType)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "students.photo_view", ResourceType: "student", ResourceID: studentID})
	})
	if err != nil {
		return Download{}, err
	}
	u, err := s.Store.PresignGet(ctx, key, contentType, DownloadTTL)
	if err != nil {
		return Download{}, err
	}
	return Download{URL: u, ExpiresAt: s.now().Add(DownloadTTL)}, nil
}

// RemoveStudentPhoto takes the photo off the student and removes it from storage.
func (s Service) RemoveStudentPhoto(ctx context.Context, a audit.Actor, studentID string) error {
	if _, err := uuid.Parse(studentID); err != nil {
		return ErrNotFound
	}
	var key string
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var fileID string
		err := tx.QueryRow(ctx, `SELECT f.id::text, f.object_key FROM students st JOIN files f ON f.tenant_id = st.tenant_id AND f.id = st.photo_file_id
			WHERE st.id = $1::uuid AND st.deleted_at IS NULL FOR UPDATE OF st`, studentID).Scan(&fileID, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE students SET photo_file_id = NULL WHERE id = $1::uuid`, studentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET status = 'deleted', deleted_at = now() WHERE id = $1::uuid`, fileID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "students.photo_remove", ResourceType: "student", ResourceID: studentID, Before: map[string]string{"fileId": fileID}})
	})
	if err != nil {
		return err
	}
	_ = s.Store.Remove(ctx, key)
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
