package apiv1

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/api/problem"
)

// ErrorHandler is an echo.HTTPErrorHandler that answers every error as application/problem+json, so clients see one
// error shape. Unexpected errors become a generic 500: the cause is logged, never sent to the caller.
func ErrorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	var he *echo.HTTPError
	if errors.As(err, &he) && he.Code < http.StatusInternalServerError {
		code := "bad_request"
		switch he.Code {
		case http.StatusNotFound:
			code = "not_found"
		case http.StatusMethodNotAllowed:
			code = "method_not_allowed"
		case http.StatusRequestEntityTooLarge:
			code = "payload_too_large"
		case http.StatusUnauthorized:
			code = "unauthenticated"
		}
		_ = problem.Write(c, he.Code, code, "")
		return
	}
	c.Logger().Errorf("request failed: %v", err)
	_ = problem.Write(c, http.StatusInternalServerError, "internal_error", "")
}
