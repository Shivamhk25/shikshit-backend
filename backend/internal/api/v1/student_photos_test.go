package apiv1_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
)

// fakeStore stands in for object storage: links are plain URLs naming the key, and "uploading" puts bytes in a map.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	removed []string
}

func newFakeStore() *fakeStore { return &fakeStore{objects: map[string][]byte{}} }

func (f *fakeStore) PresignPut(_ context.Context, key, contentType string, ttl time.Duration) (string, map[string]string, error) {
	return "https://storage.test/" + key + "?op=put&ttl=" + ttl.String(), map[string]string{"Content-Type": contentType}, nil
}

func (f *fakeStore) PresignGet(_ context.Context, key, contentType string, ttl time.Duration) (string, error) {
	return "https://storage.test/" + key + "?op=get&ct=" + url.QueryEscape(contentType) + "&ttl=" + ttl.String(), nil
}

func (f *fakeStore) Head(_ context.Context, key string, n int64) (int64, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return 0, nil, storage.ErrNotFound
	}
	if int64(len(b)) > n {
		return int64(len(b)), b[:n], nil
	}
	return int64(len(b)), b, nil
}

func (f *fakeStore) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.removed = append(f.removed, key)
	return nil
}

// put plays the client's PUT to a signed link.
func (f *fakeStore) put(t *testing.T, link string, body []byte) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Host != "storage.test" {
		t.Fatalf("link = %s", link)
	}
	key := strings.TrimPrefix(u.Path, "/")
	f.mu.Lock()
	f.objects[key] = body
	f.mu.Unlock()
	return key
}

var jpeg = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00}, make([]byte, 600)...)

type uploadOut struct {
	FileID    string            `json:"fileId"`
	UploadURL string            `json:"uploadUrl"`
	Headers   map[string]string `json:"headers"`
}

func (s usersStack) student(t *testing.T, tenant, code string) string {
	t.Helper()
	var id string
	if err := s.adm.QueryRow(bg, `INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth) VALUES ($1, $2::text, 'Asha', 'female', DATE '2015-04-01') RETURNING id::text`,
		s.tenant[tenant], code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (s usersStack) beginPhoto(t *testing.T, host, tok, student, body string) uploadOut {
	t.Helper()
	rec := s.do(http.MethodPost, host, "/students/"+student+"/photo/upload", tok, body)
	want(t, rec, http.StatusCreated, "")
	return decode[uploadOut](t, rec)
}

func TestAStudentPhotoIsUploadedCheckedAndShownByShortLinks(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")

	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", admin, ""), http.StatusNotFound, "not_found")
	up := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
	if up.Headers["Content-Type"] != "image/jpeg" || !strings.Contains(up.UploadURL, "t/"+s.tenant["alpha"]+"/student_photo/"+up.FileID) || !strings.Contains(up.UploadURL, "ttl=10m0s") {
		t.Fatalf("upload = %+v", up)
	}
	key := s.store.put(t, up.UploadURL, jpeg)
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNoContent, "")
	// a confirmed upload cannot be confirmed again
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")

	rec := s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", admin, "")
	want(t, rec, http.StatusOK, "")
	link := decode[struct {
		URL string `json:"url"`
	}](t, rec).URL
	if !strings.Contains(link, key) || !strings.Contains(link, "ct=image%2Fjpeg") || !strings.Contains(link, "ttl=5m0s") {
		t.Fatalf("download link = %s", link)
	}
	var views int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'students.photo_view' AND resource_id = $1`, st).Scan(&views); err != nil || views != 1 {
		t.Fatalf("photo views audited = %d (%v)", views, err)
	}

	// a new photo replaces the old one, whose object is removed
	up2 := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
	s.store.put(t, up2.UploadURL, jpeg)
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up2.FileID+`"}`), http.StatusNoContent, "")
	if _, _, err := s.store.Head(bg, key, 1); err == nil {
		t.Fatal("the replaced photo must be removed from storage")
	}
	// and removing the photo removes it
	want(t, s.do(http.MethodDelete, alphaHost, "/students/"+st+"/photo", admin, ""), http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", admin, ""), http.StatusNotFound, "not_found")
	if len(s.store.objects) != 0 {
		t.Fatalf("objects left in storage: %v", s.store.objects)
	}
}

func TestOnlyRealAllowedImagesAreAccepted(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")

	for _, body := range []string{`{"contentType":"image/gif","sizeBytes":100}`, `{"contentType":"text/html","sizeBytes":100}`, `{"contentType":"image/jpeg","sizeBytes":5242881}`, `{"contentType":"image/jpeg","sizeBytes":0}`} {
		want(t, s.do(http.MethodPost, alphaHost, "/students/"+st+"/photo/upload", admin, body), http.StatusBadRequest, "not_allowed")
	}
	reject := func(content []byte) {
		t.Helper()
		up := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
		var key string
		if content != nil {
			key = s.store.put(t, up.UploadURL, content)
		}
		want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusBadRequest, "bad_upload")
		var status string
		if err := s.adm.QueryRow(bg, `SELECT status FROM files WHERE id = $1::uuid`, up.FileID).Scan(&status); err != nil || status != "rejected" {
			t.Fatalf("status = %s (%v)", status, err)
		}
		if key != "" {
			if _, ok := s.store.objects[key]; ok {
				t.Fatal("a rejected upload must be removed from storage")
			}
		}
	}
	reject([]byte("<html><script>alert(1)</script></html>"))                                  // declared JPEG, really HTML
	reject(nil)                                                                               // nothing uploaded
	reject(append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 100)...)) // a PNG declared as JPEG
	reject(append(jpeg, make([]byte, 5<<20)...))                                              // over 5 MB once uploaded
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", admin, ""), http.StatusNotFound, "not_found")
}

func TestAnUploadBelongsToItsStarterStudentAndSchool(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	other := s.student(t, "alpha", "A-2")
	bst := s.student(t, "beta", "B-1")

	up := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
	s.store.put(t, up.UploadURL, jpeg)
	// another Admin of the same school did not start this upload
	s.grantAdmin(t, s.ids["plain"])
	otherAdmin := s.signIn(t, alphaHost, "plain", password)
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", otherAdmin, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")
	// another student, another school, an unknown student: all not found
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+other+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")
	beta := s.signIn(t, betaHost, "betaadmin", password)
	want(t, s.do(http.MethodPut, betaHost, "/students/"+st+"/photo", beta, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, alphaHost, "/students/"+bst+"/photo/upload", admin, `{"contentType":"image/jpeg","sizeBytes":611}`), http.StatusNotFound, "not_found")
	// a late confirmation (after the link expired) is refused
	if _, err := s.adm.Exec(bg, `UPDATE files SET created_at = now() - interval '1 hour' WHERE id = $1::uuid`, up.FileID); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNotFound, "not_found")
}

func TestPhotoAccessFollowsTheStudentsRules(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	st := s.student(t, "alpha", "A-1")
	up := s.beginPhoto(t, alphaHost, admin, st, `{"contentType":"image/jpeg","sizeBytes":611}`)
	s.store.put(t, up.UploadURL, jpeg)
	want(t, s.do(http.MethodPut, alphaHost, "/students/"+st+"/photo", admin, `{"fileId":"`+up.FileID+`"}`), http.StatusNoContent, "")

	// no permission at all
	plain := s.signIn(t, alphaHost, "plain", password)
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", plain, ""), http.StatusForbidden, "permission_denied")
	// a class-section viewer whose sections do not include the student: as if it did not exist, and cannot change it
	mgr := s.signIn(t, alphaHost, "rolemgr", password)
	want(t, s.do(http.MethodGet, alphaHost, "/students/"+st+"/photo", mgr, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodDelete, alphaHost, "/students/"+st+"/photo", mgr, ""), http.StatusForbidden, "permission_denied")
	// another school's admin sees nothing
	beta := s.signIn(t, betaHost, "betaadmin", password)
	want(t, s.do(http.MethodGet, betaHost, "/students/"+st+"/photo", beta, ""), http.StatusNotFound, "not_found")
}
