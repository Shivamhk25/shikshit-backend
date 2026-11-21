package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/problem"
)

func TestWriteProducesAContractShapedProblemDocument(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
	c.Response().Header().Set(echo.HeaderXRequestID, "req-123")

	if err := problem.Write(c, http.StatusForbidden, "tenant_suspended", "This institute is suspended."); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("content type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"type": "about:blank", "title": "Forbidden", "status": float64(403), "code": "tenant_suspended", "requestId": "req-123"} {
		if body[k] != want {
			t.Errorf("%s = %v, want %v", k, body[k], want)
		}
	}
}
