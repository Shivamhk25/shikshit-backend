package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// A slow upload must survive the server-wide read timeout on the upload route only.
func TestSlowUploadOutlivesTheServerReadTimeoutOnlyOnTheUploadRoute(t *testing.T) {
	e := echo.New()
	e.Use(bodyLimits()...)
	h := func(c echo.Context) error { _, err := readAll(c); return err }
	e.POST("/api/v1/onboarding/imports", func(c echo.Context) error {
		if _, err := readAll(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
	e.POST("/api/v1/rooms", func(c echo.Context) error {
		if err := h(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(e)
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	slow := func(path string) (int, error) {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 4; i++ {
				_, _ = pw.Write([]byte("chunk"))
				time.Sleep(150 * time.Millisecond)
			}
			_ = pw.Close()
		}()
		resp, err := http.Post(srv.URL+path, "application/octet-stream", pr)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := slow("/api/v1/onboarding/imports"); err != nil || code != 200 {
		t.Errorf("upload route: %d %v", code, err)
	}
	if code, err := slow("/api/v1/rooms"); err == nil && code == 200 {
		t.Errorf("a slow body on an ordinary route outlived the read timeout (%d)", code)
	}
}
