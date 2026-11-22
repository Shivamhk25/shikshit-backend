package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Signing happens offline (the region is set), so these tests need no storage service.
func TestSignedLinksAreScopedToTheObjectAndExpire(t *testing.T) {
	s, err := New(Config{Endpoint: "storage.test:9000", Bucket: "shikshit", AccessKey: "AKIAEXAMPLE", SecretKey: "secret-example-key", Region: "ap-south-1"})
	if err != nil {
		t.Fatal(err)
	}
	put, headers, err := s.PresignPut(context.Background(), "t/abc/student_photo/f1", "image/jpeg", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(put)
	q := u.Query()
	if u.Path != "/shikshit/t/abc/student_photo/f1" || q.Get("X-Amz-Expires") != "600" || q.Get("X-Amz-Signature") == "" || u.Scheme != "http" {
		t.Fatalf("put link = %s", put)
	}
	if !strings.Contains(q.Get("X-Amz-SignedHeaders"), "content-type") || headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("the content type must be signed: %s %v", q.Get("X-Amz-SignedHeaders"), headers)
	}
	if strings.Contains(put, "secret-example-key") {
		t.Fatal("the secret must never appear in a link")
	}
	get, err := s.PresignGet(context.Background(), "t/abc/student_photo/f1", "image/jpeg", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := url.Parse(get); g.Query().Get("X-Amz-Expires") != "300" || g.Query().Get("response-content-type") != "image/jpeg" {
		t.Fatalf("get link = %s", get)
	}
}

func TestConfigIsRequired(t *testing.T) {
	if _, err := New(Config{Endpoint: "x", Bucket: "b"}); err == nil {
		t.Fatal("missing keys must be refused")
	}
}
