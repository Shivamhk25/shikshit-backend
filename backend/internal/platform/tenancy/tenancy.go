// Package tenancy binds every request to exactly one institute.
//
// Rules (docs/plan/01-access-model.md, 03-architecture-contracts.md):
//   - The tenant comes from the request host (subdomain of a base domain, or a custom domain) and nowhere else.
//     A client-supplied header or body field NEVER selects the tenant for an institute user.
//   - The database enforces isolation with row-level security keyed on app.tenant_id, set per transaction
//     by WithTx. A forgotten filter in a query therefore cannot leak another institute's rows.
package tenancy

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
)

// Tier is who is acting: an institute user or the platform (Super Admin) tier.
type Tier string

const (
	// TierTenant is a user of one institute.
	TierTenant Tier = "tenant"
	// TierPlatform is the Super Admin tier. It still needs an explicit tenant context to read institute data.
	TierPlatform Tier = "platform"
)

// Tenant is the public view of an institute resolved from a host.
type Tenant struct {
	ID     string         `json:"id"`
	Code   string         `json:"code"`
	Name   string         `json:"name"`
	Status string         `json:"status"` // onboarding | live | suspended | archived
	Brand  map[string]any `json:"brand,omitempty"`
}

// Errors returned by resolvers.
var (
	// ErrNotFound means the key does not identify an institute.
	ErrNotFound = errors.New("tenancy: institute not found")
)

// Resolver turns a tenant key (institute code or custom domain) into a Tenant.
type Resolver interface {
	Resolve(ctx context.Context, key string) (Tenant, error)
}

// Config controls how a request host maps to a tenant key.
type Config struct {
	// BaseDomains are the domains institutes are served under, e.g. "shikshit.app":
	// "springfield.shikshit.app" resolves the institute with code "springfield".
	BaseDomains []string
	// ReservedLabels can never be an institute code when used as a subdomain (e.g. "www", "api").
	ReservedLabels []string
	// DefaultTenant is a development convenience: the institute used when the host carries no key
	// (e.g. localhost). Leave empty in production.
	DefaultTenant string
	// TrustForwardedHost makes the middleware read X-Forwarded-Host. Enable only behind a proxy that overwrites it.
	TrustForwardedHost bool
}

var validLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// validHost reports whether h is a plausible lower-case DNS name (labels of a-z, 0-9 and hyphen; no empty labels).
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if !validLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// KeyFromHost derives the tenant key from a Host header value. ok is false when the host carries no key
// (the bare base domain, localhost, an IP address, a reserved label or something that is not a valid hostname).
func KeyFromHost(host string, cfg Config) (key string, ok bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	// validHost also rejects IPv6 literals (colons), empty labels ("a..b") and unusual characters.
	if !validHost(h) || isIP(h) || h == "localhost" {
		return "", false
	}
	for _, raw := range cfg.BaseDomains {
		base := strings.ToLower(strings.Trim(strings.TrimSpace(raw), "."))
		if base == "" {
			continue
		}
		if h == base {
			return "", false
		}
		if strings.HasSuffix(h, "."+base) {
			label := strings.TrimSuffix(h, "."+base)
			if strings.Contains(label, ".") || isReserved(label, cfg.ReservedLabels) {
				return "", false
			}
			return label, true
		}
	}
	// Not under a base domain: treat the whole host as a custom domain, provided it looks like one.
	if !strings.Contains(h, ".") || h == "localhost" {
		return "", false
	}
	return h, true
}

func isReserved(label string, reserved []string) bool {
	for _, r := range reserved {
		if strings.EqualFold(label, r) {
			return true
		}
	}
	return false
}

func isIP(h string) bool {
	parts := strings.Split(h, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 || strings.Trim(p, "0123456789") != "" {
			return false
		}
	}
	return true
}

type ctxKey struct{}

// WithTenant returns a context carrying the resolved tenant.
func WithTenant(ctx context.Context, t Tenant) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// FromContext returns the tenant bound to the request, if any.
func FromContext(ctx context.Context) (Tenant, bool) {
	t, ok := ctx.Value(ctxKey{}).(Tenant)
	return t, ok
}
