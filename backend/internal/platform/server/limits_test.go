package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestOnlyTheUploadRouteAcceptsALargeBody(t *testing.T) {
	e := echo.New()
	e.Use(bodyLimits()...)
	read := func(c echo.Context) error {
		n, err := readAll(c)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, n)
	}
	e.POST("/api/v1/onboarding/imports", read)
	e.POST("/api/v1/onboarding/imports/:id/commit", read)
	e.POST("/api/v1/rooms", read)

	post := func(path string, size int) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Repeat("x", size)))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	const mb = 1 << 20
	cases := []struct {
		path string
		size int
		want int
	}{
		{"/api/v1/onboarding/imports", 5 * mb, 200},
		{"/api/v1/onboarding/imports", 12 * mb, http.StatusRequestEntityTooLarge},
		{"/api/v1/rooms", 2 * mb, http.StatusRequestEntityTooLarge},
		{"/api/v1/rooms", mb / 2, 200},
		{"/api/v1/onboarding/imports/abc/commit", 2 * mb, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		if got := post(c.path, c.size); got != c.want {
			t.Errorf("POST %s %d bytes = %d, want %d", c.path, c.size, got, c.want)
		}
	}
}

func readAll(c echo.Context) (string, error) {
	b := make([]byte, 32*1024)
	total := 0
	for {
		n, err := c.Request().Body.Read(b)
		total += n
		if err != nil {
			if err.Error() == "EOF" {
				return "ok", nil
			}
			return "", err
		}
	}
}
