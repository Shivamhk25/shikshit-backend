// Package problem writes RFC 9457 error responses (application/problem+json) in the shape defined by the
// OpenAPI contract (components/schemas/Problem). Clients map the stable `code` to a translated message.
package problem

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/api/gen"
)

// ContentType is the media type of every error response.
const ContentType = "application/problem+json"

// Write sends a problem document with the given HTTP status and stable machine code.
// detail is optional human-readable context and must never contain secrets or internals.
func Write(c echo.Context, status int, code, detail string) error {
	p := gen.Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
	}
	if detail != "" {
		p.Detail = &detail
	}
	if id := c.Response().Header().Get(echo.HeaderXRequestID); id != "" {
		p.RequestId = &id
	}
	c.Response().Header().Set(echo.HeaderContentType, ContentType)
	return c.JSON(status, p)
}
