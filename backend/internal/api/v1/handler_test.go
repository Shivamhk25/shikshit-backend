package apiv1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

func newTestServer() *echo.Echo {
	h := New("test-1.2.3")
	h.now = func() time.Time { return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC) }
	e := echo.New()
	Register(e, h)
	return e
}

func TestGetHealthzReportsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BasePath+"/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Status  string    `json:"status"`
		Version string    `json:"version"`
		Time    time.Time `json:"time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Status != "ok" || body.Version != "test-1.2.3" || body.Time.IsZero() {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestGetMyAccessIsNotImplementedUntilFND06(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, BasePath+"/me/access", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content type = %q, want application/problem+json", ct)
	}
	var p struct {
		Code   string `json:"code"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if p.Code != "not_implemented" || p.Status != http.StatusNotImplemented {
		t.Fatalf("unexpected problem: %+v", p)
	}
}
