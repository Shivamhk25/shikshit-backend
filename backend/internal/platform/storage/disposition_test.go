package storage

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestADocumentLinkPinsTheContentTypeAndTheDisposition(t *testing.T) {
	s, err := New(Config{Endpoint: "storage.test:9000", Bucket: "shikshit", AccessKey: "AKIAEXAMPLE", SecretKey: "secret-example-key", Region: "ap-south-1"})
	if err != nil {
		t.Fatal(err)
	}
	var _ DispositionStore = s
	link, err := s.PresignGetAs(context.Background(), "t/abc/student_document/f1", "application/pdf", `attachment; filename="birth_certificate.pdf"`, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if q.Get("response-content-type") != "application/pdf" || q.Get("response-content-disposition") != `attachment; filename="birth_certificate.pdf"` || q.Get("X-Amz-Expires") != "300" || q.Get("X-Amz-Signature") == "" {
		t.Fatalf("link = %s", link)
	}
}
