// Package apiv1 implements the HTTP contract defined in backend/api/openapi.
//
// The interface it satisfies is generated (internal/api/gen). Adding or changing an
// operation in the OpenAPI spec and regenerating makes this package stop compiling until
// the handler matches, so the implementation cannot silently drift from the contract.
package apiv1

import (
	"context"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/pie360/backend/internal/api/gen"
)

// BasePath is the URL prefix every contract operation is served under.
const BasePath = "/api/v1"

// Handler implements gen.StrictServerInterface. Operations that are declared in the
// contract but not built yet answer 501 until the work item that owns them lands.
type Handler struct {
	version string
	now     func() time.Time
	auth    Authenticator
	access  AccessResolver
	cookie  *CookieConfig
}

// Option configures optional collaborators of a Handler.
type Option func(*Handler)

// WithAccess enables GET /me/access. Without it the operation answers 501.
func WithAccess(r AccessResolver) Option { return func(h *Handler) { h.access = r } }

// WithAuth enables the /auth operations. Without it they answer 501.
func WithAuth(a Authenticator) Option { return func(h *Handler) { h.auth = a } }

// Compile-time guard: the handler must implement every operation in the contract.
var _ gen.StrictServerInterface = (*Handler)(nil)

// New returns a Handler that reports version in health responses.
func New(version string, opts ...Option) *Handler {
	h := &Handler{version: version, now: time.Now}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Register mounts every contract operation on e under BasePath.
// It is not called from the application yet; wiring happens with FND-06.
func Register(e *echo.Echo, h *Handler) {
	gen.RegisterHandlersWithBaseURL(e, gen.NewStrictHandler(h, []gen.StrictMiddlewareFunc{h.withClientMeta, h.authenticate}), BasePath)
}

// GetHealthz implements GET /healthz. It takes no input and always reports ok.
func (h *Handler) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{
		Status:  gen.Ok,
		Version: h.version,
		Time:    h.now().UTC(),
	}, nil
}
