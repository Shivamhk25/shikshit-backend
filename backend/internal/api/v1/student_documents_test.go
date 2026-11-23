package apiv1_test

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/files"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

// PresignGetAs makes the fake store a storage.DispositionStore, like the real one.
func (f *fakeStore) PresignGetAs(_ context.Context, key, contentType, disposition string, ttl time.Duration) (string, error) {
	return "https://storage.test/" + key + "?op=get&ct=" + url.QueryEscape(contentType) + "&cd=" + url.QueryEscape(disposition) + "&ttl=" + ttl.String(), nil
}

var (
	pdfBytes = append([]byte("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"), make([]byte, 700)...)
	pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 700)...)
)

type docOut struct {
	ID           string  `json:"id"`
	StudentID    string  `json:"studentId"`
	DocumentType string  `json:"documentType"`
	Title        string  `json:"title"`
	ContentType  string  `json:"contentType"`
	SizeBytes    int64   `json:"sizeBytes"`
	ValidUntil   *string `json:"validUntil"`
	UploadedBy   *string `json:"uploadedBy"`
	VerifiedAt   *string `json:"verifiedAt"`
	VerifiedBy   *string `json:"verifiedBy"`
}

type docListOut struct {
	Items []docOut `json:"items"`
}

type docLinkOut struct {
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Disposition string `json:"disposition"`
}

func (s usersStack) beginDoc(t *testing.T, tok, student, contentType string, size int) uploadOut {
	t.Helper()
	rec := s.do(http.MethodPost, alphaHost, "/students/"+student+"/documents/upload", tok, `{"contentType":"`+contentType+`","sizeBytes":`+strconv.Itoa(size)+`}`)
	want(t, rec, http.StatusCreated, "")
	return decode[uploadOut](t, rec)
}

// uploadDoc plays the whole flow for one file and returns the confirmed document.
func (s usersStack) uploadDoc(t *testing.T, tok, student, contentType string, content []byte, docType, title string) docOut {
	t.Helper()
	up := s.beginDoc(t, tok, student, contentType, len(content))
	s.store.put(t, up.UploadURL, content)
	rec := s.do(http.MethodPost, alphaHost, "/students/"+student+"/documents", tok, `{"fileId":"`+up.FileID+`","documentType":"`+docType+`","title":"`+title+`","validUntil":"2030-03-31"}`)
	want(t, rec, http.StatusCreated, "")
	return decode[docOut](t, rec)
}

// docUser adds an institute user holding exactly the given permissions (code -> scope).
func (s usersStack) docUser(t *testing.T, name string, grants map[string]string) string {
	t.Helper()
	hash, err := identity.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	alpha := s.tenant["alpha"]
	var uid, rid string
	if err := s.adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status, password_hash) VALUES ($1, $2::text, $2::text, 'active', $3) RETURNING id::text`, alpha, name, hash).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := s.adm.QueryRow(bg, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, alpha, "r_"+name).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	for code, scope := range grants {
		if _, err := s.adm.Exec(bg, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, alpha, rid, code, scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, alpha, uid, rid); err != nil {
		t.Fatal(err)
	}
	if scope := grants["stu.student_documents.view"]; scope == "class_section" {
		if _, err := s.adm.Exec(bg, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', 'cs-1')`, alpha, uid); err != nil {
			t.Fatal(err)
		}
	}
	return s.signIn(t, alphaHost, name, password)
}

func (s usersStack) auditCount(t *testing.T, action, resource string) int {
	t.Helper()
	var n int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1 AND resource_id = $2`, action, resource).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestADocumentIsUploadedListedDownloadedVerifiedAndDeleted(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")

	// an empty list is an empty list, and the read is audited
	rec := s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")
	want(t, rec, http.StatusOK, "")
	if got := decode[docListOut](t, rec); len(got.Items) != 0 {
		t.Fatalf("items = %+v", got.Items)
	}
	if n := s.auditCount(t, "student_documents.list", st); n != 1 {
		t.Fatalf("list audited %d times", n)
	}

	up := s.beginDoc(t, admin, st, "application/pdf", len(pdfBytes))
	if up.Headers["Content-Type"] != "application/pdf" || !strings.Contains(up.UploadURL, "t/"+s.tenant["alpha"]+"/student_document/"+up.FileID) || !strings.Contains(up.UploadURL, "ttl=10m0s") {
		t.Fatalf("upload = %+v", up)
	}
	// nothing is listed before it is confirmed
	s.store.put(t, up.UploadURL, pdfBytes)
	rec = s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")
	if got := decode[docListOut](t, rec); len(got.Items) != 0 {
		t.Fatalf("an unconfirmed upload is listed: %+v", got.Items)
	}
	rec = s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, `{"fileId":"`+up.FileID+`","documentType":"birth_certificate","title":"  Birth certificate (original)  ","validUntil":"2030-03-31"}`)
	want(t, rec, http.StatusCreated, "")
	doc := decode[docOut](t, rec)
	if doc.DocumentType != "birth_certificate" || doc.Title != "Birth certificate (original)" || doc.ContentType != "application/pdf" || doc.SizeBytes != int64(len(pdfBytes)) ||
		doc.ValidUntil == nil || *doc.ValidUntil != "2030-03-31" || doc.UploadedBy == nil || *doc.UploadedBy != "admin" || doc.VerifiedAt != nil || doc.VerifiedBy != nil {
		t.Fatalf("document = %+v", doc)
	}
	// a confirmed upload cannot be confirmed again
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, `{"fileId":"`+up.FileID+`","documentType":"other","title":"again"}`), http.StatusNotFound, "not_found")

	img := s.uploadDoc(t, admin, st, "image/png", pngBytes, "parent_id_proof", "Mother's ID")
	list := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")).Items
	if len(list) != 2 || list[0].ID != img.ID || list[1].ID != doc.ID {
		t.Fatalf("list = %+v", list)
	}

	// a PDF is always an attachment, an image is inline; the link pins the content type and lasts 5 minutes
	rec = s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents/"+doc.ID+"/download", admin, "")
	want(t, rec, http.StatusOK, "")
	link := decode[docLinkOut](t, rec)
	if link.Disposition != "attachment" || link.ContentType != "application/pdf" || !strings.Contains(link.URL, "ct=application%2Fpdf") ||
		!strings.Contains(link.URL, "cd=attachment%3B+filename%3D%22birth_certificate.pdf%22") || !strings.Contains(link.URL, "ttl=5m0s") || !strings.Contains(link.URL, "/student_document/") {
		t.Fatalf("pdf link = %+v", link)
	}
	link = decode[docLinkOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents/"+img.ID+"/download", admin, ""))
	if link.Disposition != "inline" || link.ContentType != "image/png" || !strings.Contains(link.URL, "cd=inline%3B+filename%3D%22parent_id_proof.png%22") {
		t.Fatalf("image link = %+v", link)
	}
	if n := s.auditCount(t, "student_documents.download", doc.ID); n != 1 {
		t.Fatalf("downloads audited = %d", n)
	}

	// verify: records who and when; verifying again keeps the first verification and adds no second audit row
	rec = s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents/"+doc.ID+"/verify", admin, "")
	want(t, rec, http.StatusOK, "")
	v := decode[docOut](t, rec)
	if v.VerifiedAt == nil || v.VerifiedBy == nil || *v.VerifiedBy != "admin" {
		t.Fatalf("verified = %+v", v)
	}
	v2 := decode[docOut](t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents/"+doc.ID+"/verify", admin, ""))
	if v2.VerifiedAt == nil || *v2.VerifiedAt != *v.VerifiedAt {
		t.Fatalf("a second verify changed the first: %+v vs %+v", v2, v)
	}
	if n := s.auditCount(t, "student_documents.verify", doc.ID); n != 1 {
		t.Fatalf("verifications audited = %d", n)
	}

	// delete removes the object from storage; the record is gone from the list and the link, but kept as history
	key := strings.SplitN(strings.TrimPrefix(link.URL, "https://storage.test/"), "?", 2)[0]
	want(t, s.do(http.MethodDelete, alphaHost, "/students/"+st+"/documents/"+img.ID, admin, ""), http.StatusNoContent, "")
	if _, _, err := s.store.Head(bg, key, 1); err == nil {
		t.Fatal("a deleted document must be removed from storage")
	}
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents/"+img.ID+"/download", admin, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, alphaHost, "/students/"+st+"/documents/"+img.ID, admin, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents/"+img.ID+"/verify", admin, ""), http.StatusNotFound, "not_found")
	if list := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")).Items; len(list) != 1 || list[0].ID != doc.ID {
		t.Fatalf("list after delete = %+v", list)
	}
	var kept int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM student_documents d JOIN files f ON f.id = d.file_id WHERE d.id = $1::uuid AND d.deleted_at IS NOT NULL AND d.deleted_by IS NOT NULL AND f.status = 'deleted'`, img.ID).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("soft delete kept = %d (%v)", kept, err)
	}
	if n := s.auditCount(t, "student_documents.delete", img.ID); n != 1 {
		t.Fatalf("deletes audited = %d", n)
	}
	// the audit trail never carries the title the school typed
	var leaked int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action LIKE 'student_documents.%' AND (before::text LIKE '%Mother%' OR after::text LIKE '%Mother%' OR after::text LIKE '%Birth certificate%')`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("titles in audit: %d (%v)", leaked, err)
	}
}

func TestOnlyRealPdfJpegAndPngAreAccepted(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")

	// refused before any link is given
	for _, body := range []string{
		`{"contentType":"text/html","sizeBytes":100}`, `{"contentType":"image/svg+xml","sizeBytes":100}`, `{"contentType":"image/gif","sizeBytes":100}`,
		`{"contentType":"image/webp","sizeBytes":100}`, `{"contentType":"application/msword","sizeBytes":100}`, `{"contentType":"application/pdf","sizeBytes":10485761}`, `{"contentType":"application/pdf","sizeBytes":0}`,
	} {
		rec := s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents/upload", admin, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", body, rec.Code, rec.Body)
		}
	}
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents/upload", admin, `{"contentType":"application/pdf","sizeBytes":10485760}`), http.StatusCreated, "")

	// what really arrived is checked, whatever was declared
	reject := func(name, declared string, content []byte) {
		t.Helper()
		up := s.beginDoc(t, admin, st, declared, 1000)
		var key string
		if content != nil {
			key = s.store.put(t, up.UploadURL, content)
		}
		rec := s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, `{"fileId":"`+up.FileID+`","documentType":"other","title":"x"}`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_upload") {
			t.Fatalf("%s = %d %s", name, rec.Code, rec.Body)
		}
		var status string
		if err := s.adm.QueryRow(bg, `SELECT status FROM files WHERE id = $1::uuid`, up.FileID).Scan(&status); err != nil || status != "rejected" {
			t.Fatalf("%s: status = %s (%v)", name, status, err)
		}
		if key != "" {
			if _, ok := s.store.objects[key]; ok {
				t.Fatalf("%s: a rejected upload must be removed from storage", name)
			}
		}
	}
	reject("html as pdf", "application/pdf", []byte("<html><script>alert(1)</script></html>"))
	reject("svg as png", "image/png", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`))
	reject("html renamed jpeg", "image/jpeg", []byte("<!DOCTYPE html><html></html>"))
	reject("png as pdf", "application/pdf", pngBytes)
	reject("pdf as jpeg", "image/jpeg", pdfBytes)
	reject("pdf with leading junk", "application/pdf", append([]byte("   \n"), pdfBytes...))
	reject("gif as png", "image/png", append([]byte("GIF89a"), make([]byte, 700)...))
	reject("nothing uploaded", "application/pdf", nil)
	reject("over 10 MB once uploaded", "application/pdf", append(append([]byte{}, pdfBytes...), make([]byte, files.MaxDocumentSize)...))
	if list := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")).Items; len(list) != 0 {
		t.Fatalf("a rejected upload is listed: %+v", list)
	}
}

func TestDocumentDetailsAreCheckedAndAMistakeKeepsTheUpload(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	up := s.beginDoc(t, admin, st, "application/pdf", len(pdfBytes))
	s.store.put(t, up.UploadURL, pdfBytes)
	for _, body := range []string{
		`{"fileId":"` + up.FileID + `","documentType":"caste_certificate","title":"x"}`,
		`{"fileId":"` + up.FileID + `","documentType":"disability_certificate","title":"x"}`,
		`{"fileId":"` + up.FileID + `","documentType":"other","title":"   "}`,
		`{"fileId":"` + up.FileID + `","documentType":"other","title":"` + strings.Repeat("a", 121) + `"}`,
		`{"fileId":"` + up.FileID + `","documentType":"other","title":"x","validUntil":"1850-01-01"}`,
		`{"fileId":"` + up.FileID + `","documentType":"other","title":"x","validUntil":"not a date"}`,
	} {
		rec := s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", body, rec.Code, rec.Body)
		}
	}
	// the upload is still pending and can be confirmed with correct details
	rec := s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, `{"fileId":"`+up.FileID+`","documentType":"other","title":"Fixed"}`)
	want(t, rec, http.StatusCreated, "")
	if d := decode[docOut](t, rec); d.ValidUntil != nil {
		t.Fatalf("validUntil = %v", *d.ValidUntil)
	}
}

func TestAnUploadBelongsToItsStarterStudentPurposeAndSchool(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st, other, bst := s.student(t, "alpha", "A-1"), s.student(t, "alpha", "A-2"), s.student(t, "beta", "B-1")
	body := func(f string) string { return `{"fileId":"` + f + `","documentType":"other","title":"x"}` }

	up := s.beginDoc(t, admin, st, "application/pdf", len(pdfBytes))
	s.store.put(t, up.UploadURL, pdfBytes)
	// another Admin did not start this upload
	s.grantAdmin(t, s.ids["plain"])
	otherAdmin := s.signIn(t, alphaHost, "plain", password)
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", otherAdmin, body(up.FileID)), http.StatusNotFound, "not_found")
	// another student, another school, an unknown student
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+other+"/documents", admin, body(up.FileID)), http.StatusNotFound, "not_found")
	beta := s.signIn(t, betaHost, "betaadmin", password)
	want(t, s.do(http.MethodPost, betaHost, "/students/"+st+"/documents", beta, body(up.FileID)), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+bst+"/documents/upload", admin, `{"contentType":"application/pdf","sizeBytes":611}`), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/students/00000000-0000-0000-0000-000000000000/documents/upload", admin, `{"contentType":"application/pdf","sizeBytes":611}`), http.StatusNotFound, "not_found")
	// a photo upload cannot be confirmed as a document, nor a document upload as a photo
	ph := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
	s.store.put(t, ph.UploadURL, jpeg)
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, body(ph.FileID)), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")
	// a late confirmation (after the link expired) is refused
	if _, err := s.adm.Exec(bg, `UPDATE files SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, up.FileID); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/documents", admin, body(up.FileID)), http.StatusNotFound, "not_found")
}

func TestDocumentsOfOtherStudentsAndSchoolsAreNotReachable(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st, other := s.student(t, "alpha", "A-1"), s.student(t, "alpha", "A-2")
	doc := s.uploadDoc(t, admin, st, "application/pdf", pdfBytes, "birth_certificate", "Birth")
	beta := s.signIn(t, betaHost, "betaadmin", password)
	bst := s.student(t, "beta", "B-1")

	for _, c := range []struct{ method, host, tok, path string }{
		{http.MethodGet, betaHost, beta, "/students/" + st + "/documents"},
		{http.MethodGet, betaHost, beta, "/students/" + bst + "/documents/" + doc.ID + "/download"},
		{http.MethodPost, betaHost, beta, "/students/" + bst + "/documents/" + doc.ID + "/verify"},
		{http.MethodDelete, betaHost, beta, "/students/" + bst + "/documents/" + doc.ID},
		// the right school but the wrong student: the document is not theirs
		{http.MethodGet, alphaHost, admin, "/students/" + other + "/documents/" + doc.ID + "/download"},
		{http.MethodPost, alphaHost, admin, "/students/" + other + "/documents/" + doc.ID + "/verify"},
		{http.MethodDelete, alphaHost, admin, "/students/" + other + "/documents/" + doc.ID},
	} {
		want(t, s.do(c.method, c.host, c.tok, c.path, ""), http.StatusNotFound, "not_found")
	}
	if list := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+other+"/documents", admin, "")).Items; len(list) != 0 {
		t.Fatalf("another student's list = %+v", list)
	}
	// untouched
	if d := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")).Items; len(d) != 1 || d[0].VerifiedAt != nil {
		t.Fatalf("document changed: %+v", d)
	}
}

func TestEachDocumentActionNeedsItsOwnPermissionAtSchoolScope(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	doc := s.uploadDoc(t, admin, st, "application/pdf", pdfBytes, "birth_certificate", "Birth")
	base := "/students/" + st + "/documents"

	viewer := s.docUser(t, "viewer", map[string]string{"stu.student_documents.view": "tenant"})
	creator := s.docUser(t, "creator", map[string]string{"stu.student_documents.create": "tenant"})
	editor := s.docUser(t, "editor", map[string]string{"stu.student_documents.edit": "tenant"})
	deleter := s.docUser(t, "deleter", map[string]string{"stu.student_documents.delete": "tenant"})
	nothing := s.signIn(t, alphaHost, "plain", password)
	// a student viewer is not a document viewer
	studentsOnly := s.docUser(t, "studentsonly", map[string]string{"stu.students.view": "tenant", "stu.students.edit": "tenant"})

	type call struct{ method, path, body string }
	calls := map[string]call{
		"list":     {http.MethodGet, base, ""},
		"download": {http.MethodGet, base + "/" + doc.ID + "/download", ""},
		"begin":    {http.MethodPost, base + "/upload", `{"contentType":"application/pdf","sizeBytes":100}`},
		"add":      {http.MethodPost, base, `{"fileId":"00000000-0000-0000-0000-000000000000","documentType":"other","title":"x"}`},
		"verify":   {http.MethodPost, base + "/" + doc.ID + "/verify", ""},
		"delete":   {http.MethodDelete, base + "/" + doc.ID, ""},
	}
	allowed := map[string]map[string]bool{
		viewer: {"list": true, "download": true}, creator: {"begin": true, "add": true}, editor: {"verify": true}, deleter: {"delete": true},
		nothing: {}, studentsOnly: {},
	}
	// delete last: it removes the document the other calls need
	for _, name := range []string{"list", "download", "begin", "add", "verify", "delete"} {
		c := calls[name]
		for tok, ok := range allowed {
			rec := s.do(c.method, alphaHost, c.path, tok, c.body)
			if ok[name] {
				if rec.Code == http.StatusForbidden {
					t.Fatalf("%s must be allowed: %d %s", name, rec.Code, rec.Body)
				}
				continue
			}
			want(t, rec, http.StatusForbidden, "permission_denied")
		}
	}
}

func TestDocumentsAreNeverServedToAClassSectionScope(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	doc := s.uploadDoc(t, admin, st, "application/pdf", pdfBytes, "parent_id_proof", "ID")

	// a teacher-like grant: documents.view limited to assigned class sections, plus the students view
	teacher := s.docUser(t, "teacher", map[string]string{"stu.student_documents.view": "class_section", "stu.students.view": "class_section"})
	for _, path := range []string{"/students/" + st + "/documents", "/students/" + st + "/documents/" + doc.ID + "/download"} {
		want(t, s.do(http.MethodGet, alphaHost, path, teacher, ""), http.StatusForbidden, "permission_denied")
	}
	if n := s.auditCount(t, "student_documents.download", doc.ID); n != 0 {
		t.Fatalf("a refused download was audited as served: %d", n)
	}
}

func TestSupportVisitsCannotOpenDocuments(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	s.uploadDoc(t, admin, st, "application/pdf", pdfBytes, "birth_certificate", "Birth")
	v := s.startVisit(t, s.platformToken(t), s.tenant["alpha"])
	tok := s.redeem(t, alphaHost, v.Code)
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", tok, ""), http.StatusForbidden, "read_only_visit")
}

func TestBuiltInRolesThatCouldReadEveryStudentRecordCannotReadDocuments(t *testing.T) {
	s := newUsersStack(t)
	grants := func(role string) []string {
		rows, err := s.adm.Query(bg, `SELECT rp.permission_code FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.tenant_id = $1 AND r.code = $2`, s.tenant["alpha"], role)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
	holds := func(role, perm string) bool {
		for _, g := range grants(role) {
			if authz.MatchPermission(g, perm) {
				return true
			}
		}
		return false
	}
	for _, role := range []string{"accountant", "librarian", "transport_manager", "security_guard", "nurse", "counselor"} {
		if holds(role, "stu.student_documents.view") {
			t.Errorf("%s must not read student documents", role)
		}
		if !holds(role, "stu.students.view") || !holds(role, "stu.student_addresses.view") {
			t.Errorf("%s lost its other student reads", role)
		}
	}
	for _, role := range []string{"admin", "registrar", "principal"} {
		if !holds(role, "stu.student_documents.view") {
			t.Errorf("%s must read student documents", role)
		}
	}
	for _, p := range []string{"create", "edit", "delete"} {
		if !holds("registrar", "stu.student_documents."+p) || holds("principal", "stu.student_documents."+p) {
			t.Errorf("registrar must hold, and principal must not hold, stu.student_documents.%s", p)
		}
	}
}

func TestUnconfirmedDocumentUploadsAreCleanedUp(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	old := s.beginDoc(t, admin, st, "application/pdf", len(pdfBytes))
	oldKey := s.store.put(t, old.UploadURL, pdfBytes)
	fresh := s.beginDoc(t, admin, st, "application/pdf", len(pdfBytes))
	freshKey := s.store.put(t, fresh.UploadURL, pdfBytes)
	kept := s.uploadDoc(t, admin, st, "application/pdf", pdfBytes, "other", "kept")
	for _, id := range []string{old.FileID, kept.ID} {
		if _, err := s.adm.Exec(bg, `UPDATE files SET created_at = now() - interval '3 hours' WHERE id = $1::uuid OR id = (SELECT file_id FROM student_documents WHERE id = $1::uuid)`, id); err != nil {
			t.Fatal(err)
		}
	}
	r, err := files.CleanupPending(bg, s.app, s.store, time.Now(), 0, 0, 0)
	if err != nil || r.Removed != 1 || r.Failed != 0 {
		t.Fatalf("cleanup = %+v (%v)", r, err)
	}
	if _, ok := s.store.objects[oldKey]; ok {
		t.Fatal("the stale document upload must be removed from storage")
	}
	if _, ok := s.store.objects[freshKey]; !ok {
		t.Fatal("a young upload must be kept")
	}
	var status string
	if err := s.adm.QueryRow(bg, `SELECT status FROM files WHERE id = $1::uuid`, old.FileID).Scan(&status); err != nil || status != "deleted" {
		t.Fatalf("status = %s (%v)", status, err)
	}
	// a confirmed document is never touched, however old
	if list := decode[docListOut](t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/documents", admin, "")).Items; len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if !bytes.Contains([]byte(oldKey), []byte("student_document")) {
		t.Fatalf("key = %s", oldKey)
	}
}
