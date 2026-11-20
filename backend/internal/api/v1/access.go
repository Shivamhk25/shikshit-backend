package apiv1

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/pie360/backend/internal/api/gen"
	"github.com/pie360/backend/internal/api/problem"
	"github.com/pie360/backend/internal/platform/authz"
	"github.com/pie360/backend/internal/platform/identity"
)

// AccessResolver builds a user's effective access (authz.Service satisfies it).
type AccessResolver interface {
	Resolve(ctx context.Context, userID string) (authz.Access, error)
}

// publicOperations are the operations that need no bearer token. A contract test keeps this set equal to the
// operations marked x-public in the OpenAPI spec, so a new operation is protected unless the contract says otherwise.
var publicOperations = map[string]bool{
	"GetHealthz":   true,
	"Login":        true,
	"RefreshToken": true,
	"Logout":       true,
}

// PublicOperations returns a copy of the set of operations served without a bearer token (Go operation names).
func PublicOperations() map[string]bool {
	out := make(map[string]bool, len(publicOperations))
	for k, v := range publicOperations {
		out[k] = v
	}
	return out
}

type principalKey struct{}

// PrincipalFrom returns the authenticated caller stored by the authentication middleware.
func PrincipalFrom(ctx context.Context) (identity.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identity.Principal)
	return p, ok
}

// authenticate requires a valid bearer token on every operation that is not public, then stores the Principal on the
// request context and advertises the caller's access version so clients know when to refetch /me/access.
func (h *Handler) authenticate(next gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	if publicOperations[operationID] {
		return next
	}
	return func(c echo.Context, request any) (any, error) {
		token, ok := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
		if !ok || h.auth == nil {
			return nil, problem.Write(c, http.StatusUnauthorized, "unauthenticated", "")
		}
		p, err := h.auth.Authenticate(c.Request().Context(), token)
		if err != nil {
			if err == identity.ErrUnauthenticated {
				c.Response().Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				return nil, problem.Write(c, http.StatusUnauthorized, "unauthenticated", "")
			}
			return nil, err
		}
		c.Response().Header().Set("X-Access-Version", itoa(p.AccessVersion))
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), principalKey{}, p)))
		return next(c, request)
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>" (the scheme is case-insensitive).
func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// GetMyAccess implements GET /me/access: everything web and mobile need to decide what to show.
func (h *Handler) GetMyAccess(ctx context.Context, _ gen.GetMyAccessRequestObject) (gen.GetMyAccessResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.access == nil {
		return nil, errAuthNotConfigured
	}
	a, err := h.access.Resolve(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return gen.GetMyAccess200JSONResponse{Body: toAccessPayload(a), Headers: gen.GetMyAccess200ResponseHeaders{XAccessVersion: int(a.Version)}}, nil
}

func toAccessPayload(a authz.Access) gen.AccessPayload {
	var out gen.AccessPayload
	out.User = gen.AccessUser{Id: openapi_types.UUID(uuid.MustParse(a.UserID)), Name: a.UserName, Tier: gen.AccessUserTier("tenant")}
	t := gen.AccessTenant{Id: openapi_types.UUID(uuid.MustParse(a.Tenant.ID)), Name: a.Tenant.Name, Modules: nonNil(a.Modules)}
	if primary, ok := a.Tenant.Brand["primary"].(string); ok {
		t.Brand.Primary = primary
	}
	out.Tenant = &t
	out.Permissions = nonNil(a.Effective.Permissions)
	out.Scopes = a.Effective.Scopes
	out.FieldMasks = map[string][]string{} // field masks arrive with IAM-07
	out.Workspace.Home = a.Home
	out.Workspace.Navigation = make([]gen.NavItem, 0, len(a.Screens))
	for _, s := range a.Screens {
		icon := s.Icon
		item := gen.NavItem{ScreenId: s.ID, Label: s.Label, Order: s.Order}
		if icon != "" {
			item.Icon = &icon
		}
		out.Workspace.Navigation = append(out.Workspace.Navigation, item)
	}
	out.Mobile.Tabs = authz.MobileTabs(a.Screens)
	out.Mobile.Features = []string{}
	out.Approvals.CanApprove = []string{} // approval types arrive with the workflow engine
	out.Version = int(a.Version)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
