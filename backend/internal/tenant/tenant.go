package tenant

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/pie360/backend/internal/auth"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/db"
	"github.com/pie360/backend/internal/shared/errors"
	"github.com/pie360/backend/internal/shared/logger"
)

// Context keys for tenant/user info
const (
	ContextKeyTenantID     = "tenant_id"
	ContextKeyUserID       = "user_id"
	ContextKeyUserRole     = "user_role"
	ContextKeyUserRoles    = "user_roles"
	ContextKeyUserEmail    = "user_email"
	ContextKeySessionID    = "session_id"
	ContextKeyClaims       = "claims"
	ContextKeyTenantConfig = "tenant_config"
)

// TenantConfig holds institute-specific configuration
type TenantConfig struct {
	ID             string                 `json:"id"`
	Code           string                 `json:"code"`
	Name           string                 `json:"name"`
	Domain         string                 `json:"domain"`
	Timezone       string                 `json:"timezone"`
	Locale         string                 `json:"locale"`
	Settings       map[string]interface{} `json:"settings"`
	SubscriptionTier string               `json:"subscription_tier"`
	MaxUsers       int                    `json:"max_users"`
	MaxStorageMB   int                    `json:"max_storage_mb"`
	IsActive       bool                   `json:"is_active"`
}

// TenantMiddleware handles multi-tenancy
type TenantMiddleware struct {
	db         db.QueryExecutor
	auth       *auth.AuthService
	sessionMgr *auth.SessionManager
	config     *config.Config
	logger     *logger.Logger
}

// NewTenantMiddleware creates a new tenant middleware
func NewTenantMiddleware(db db.QueryExecutor, authSvc *auth.AuthService, sessionMgr *auth.SessionManager, cfg *config.Config, log *logger.Logger) *TenantMiddleware {
	return &TenantMiddleware{
		db:         db,
		auth:       authSvc,
		sessionMgr: sessionMgr,
		config:     cfg,
		logger:     log.WithComponent("tenant"),
	}
}

// RequireTenant middleware that requires valid tenant context
func (m *TenantMiddleware) RequireTenant() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Try to get tenant from header (for API calls)
			tenantID := c.Request().Header.Get("X-Institute-ID")
			
			// If not in header, try to get from JWT claims
			if tenantID == "" {
				if claims, ok := c.Get(ContextKeyClaims).(*auth.Claims); ok {
					tenantID = claims.InstituteID
				}
			}
			
			// If still not found, try subdomain
			if tenantID == "" {
				tenantID = m.extractTenantFromSubdomain(c)
			}
			
			if tenantID == "" {
				return errors.NewForbidden("Tenant context required")
			}
			
			// Validate tenant exists and is active
			tenantConfig, err := m.getTenantConfig(c.Request().Context(), tenantID)
			if err != nil {
				m.logger.Warn().Err(err).Str("tenant_id", tenantID).Msg("Failed to get tenant config")
				return errors.NewForbidden("Invalid tenant")
			}
			
			if !tenantConfig.IsActive {
				return errors.NewForbidden("Tenant is inactive")
			}
			
			// Set tenant context in request
			c.Set(ContextKeyTenantID, tenantID)
			c.Set(ContextKeyTenantConfig, tenantConfig)
			
			// Set database session variable for RLS
			if err := m.setTenantContext(c.Request().Context(), tenantID); err != nil {
				m.logger.Warn().Err(err).Str("tenant_id", tenantID).Msg("Failed to set tenant context")
			}
			
			return next(c)
		}
	}
}

// RequireAuth middleware that requires valid authentication
func (m *TenantMiddleware) RequireAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Get token from Authorization header
			authHeader := c.Request().Header.Get("Authorization")
			token, err := auth.ExtractTokenFromHeader(authHeader)
			if err != nil {
				return err
			}
			
			// Validate token
			claims, err := m.auth.ValidateAccessToken(token)
			if err != nil {
				return err
			}
			
			// Set user context
			c.Set(ContextKeyUserID, claims.UserID)
			c.Set(ContextKeyUserRole, claims.Role)
			c.Set(ContextKeyUserRoles, claims.Roles)
			c.Set(ContextKeyUserEmail, claims.Email)
			c.Set(ContextKeySessionID, claims.SessionID)
			c.Set(ContextKeyClaims, claims)
			
			// If tenant not already set, use from claims
			if c.Get(ContextKeyTenantID) == nil && claims.InstituteID != "" {
				c.Set(ContextKeyTenantID, claims.InstituteID)
				
				tenantConfig, err := m.getTenantConfig(c.Request().Context(), claims.InstituteID)
				if err == nil {
					c.Set(ContextKeyTenantConfig, tenantConfig)
					m.setTenantContext(c.Request().Context(), claims.InstituteID)
				}
			}
			
			return next(c)
		}
	}
}

// RequireRole middleware that requires specific role(s)
func (m *TenantMiddleware) RequireRole(roles ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userRole, ok := c.Get(ContextKeyUserRole).(string)
			if !ok {
				return errors.NewForbidden("Role not found in context")
			}
			
			userRoles, _ := c.Get(ContextKeyUserRoles).([]string)
			allRoles := append([]string{userRole}, userRoles...)
			
			// Check if user has any of the required roles
			hasRole := false
			for _, required := range roles {
				for _, userR := range allRoles {
					if userR == required {
						hasRole = true
						break
					}
				}
				if hasRole {
					break
				}
			}
			
			if !hasRole {
				return errors.NewForbidden("Insufficient permissions")
			}
			
			return next(c)
		}
	}
}

// RequirePermission middleware using Casbin
func (m *TenantMiddleware) RequirePermission(enforcer *auth.EnforcerWrapper, resource, action string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			userRole, ok := c.Get(ContextKeyUserRole).(string)
			if !ok {
				return errors.NewForbidden("Role not found in context")
			}
			
			tenantID, ok := c.Get(ContextKeyTenantID).(string)
			if !ok {
				return errors.NewForbidden("Tenant context required")
			}
			
			allowed, err := enforcer.CheckPermission(userRole, resource, action, tenantID)
			if err != nil {
				m.logger.Error().Err(err).Msg("Permission check failed")
				return errors.NewInternal("Permission check failed", err)
			}
			
			if !allowed {
				return errors.NewForbidden("Insufficient permissions for this resource")
			}
			
			return next(c)
		}
	}
}

// OptionalAuth middleware that adds auth context if token is present
func (m *TenantMiddleware) OptionalAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return next(c)
			}
			
			token, err := auth.ExtractTokenFromHeader(authHeader)
			if err != nil {
				return next(c) // Continue without auth
			}
			
			claims, err := m.auth.ValidateAccessToken(token)
			if err != nil {
				return next(c) // Continue without auth
			}
			
			c.Set(ContextKeyUserID, claims.UserID)
			c.Set(ContextKeyUserRole, claims.Role)
			c.Set(ContextKeyUserRoles, claims.Roles)
			c.Set(ContextKeyUserEmail, claims.Email)
			c.Set(ContextKeySessionID, claims.SessionID)
			c.Set(ContextKeyClaims, claims)
			
			if claims.InstituteID != "" {
				c.Set(ContextKeyTenantID, claims.InstituteID)
				tenantConfig, _ := m.getTenantConfig(c.Request().Context(), claims.InstituteID)
				c.Set(ContextKeyTenantConfig, tenantConfig)
				m.setTenantContext(c.Request().Context(), claims.InstituteID)
			}
			
			return next(c)
		}
	}
}

// GetTenantID extracts tenant ID from context
func GetTenantID(c echo.Context) string {
	if v := c.Get(ContextKeyTenantID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetUserID extracts user ID from context
func GetUserID(c echo.Context) string {
	if v := c.Get(ContextKeyUserID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetUserRole extracts user role from context
func GetUserRole(c echo.Context) string {
	if v := c.Get(ContextKeyUserRole); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetClaims extracts JWT claims from context
func GetClaims(c echo.Context) *auth.Claims {
	if v := c.Get(ContextKeyClaims); v != nil {
		if cl, ok := v.(*auth.Claims); ok {
			return cl
		}
	}
	return nil
}

// GetTenantConfig extracts tenant config from context
func GetTenantConfig(c echo.Context) *TenantConfig {
	if v := c.Get(ContextKeyTenantConfig); v != nil {
		if tc, ok := v.(*TenantConfig); ok {
			return tc
		}
	}
	return nil
}

// extractTenantFromSubdomain extracts tenant from subdomain
func (m *TenantMiddleware) extractTenantFromSubdomain(c echo.Context) string {
	host := c.Request().Host
	host = strings.TrimSpace(host)
	
	// Remove port
	if idx := strings.Index(host, ":"); idx != -1 {
		host = host[:idx]
	}
	
	// Check for subdomain pattern: tenant.domain.com
	parts := strings.Split(host, ".")
	if len(parts) >= 3 {
		subdomain := parts[0]
		// Skip common subdomains
		if subdomain != "www" && subdomain != "api" && subdomain != "app" {
			return subdomain
		}
	}
	
	return ""
}

// getTenantConfig fetches tenant configuration from database
func (m *TenantMiddleware) getTenantConfig(ctx context.Context, tenantID string) (*TenantConfig, error) {
	query := `
		SELECT id, code, name, domain, timezone, locale, settings, 
		       subscription_tier, max_users, max_storage_mb, is_active
		FROM institutes
		WHERE id = $1 AND is_deleted = false
	`
	
	var tc TenantConfig
	var settings []byte
	err := m.db.QueryRow(ctx, query, tenantID).Scan(
		&tc.ID, &tc.Code, &tc.Name, &tc.Domain, &tc.Timezone, &tc.Locale,
		&settings, &tc.SubscriptionTier, &tc.MaxUsers, &tc.MaxStorageMB, &tc.IsActive,
	)
	if err != nil {
		return nil, err
	}
	
	// Parse settings JSON
	if len(settings) > 0 {
		// In production, unmarshal properly
		tc.Settings = make(map[string]interface{})
	}
	
	return &tc, nil
}

// setTenantContext sets the tenant context for RLS
func (m *TenantMiddleware) setTenantContext(ctx context.Context, tenantID string) error {
	query := `SET LOCAL app.current_institute_id = $1`
	_, err := m.db.Exec(ctx, query, tenantID)
	return err
}

// CORSMiddleware configures CORS
func CORSMiddleware(cfg *config.CORSConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			origin := c.Request().Header.Get("Origin")
			
			// Check if origin is allowed
			allowed := false
			for _, o := range cfg.AllowedOrigins {
				if o == "*" || o == origin {
					allowed = true
					break
				}
			}
			
			if allowed {
				c.Response().Header().Set("Access-Control-Allow-Origin", origin)
				c.Response().Header().Set("Access-Control-Allow-Credentials", "true")
			}
			
			c.Response().Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowedMethods, ", "))
			c.Response().Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			c.Response().Header().Set("Access-Control-Max-Age", "86400")
			
			if c.Request().Method == http.MethodOptions {
				return c.NoContent(http.StatusNoContent)
			}
			
			return next(c)
		}
	}
}

// SecurityHeadersMiddleware adds security headers
func SecurityHeadersMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Response().Header().Set("X-Content-Type-Options", "nosniff")
			c.Response().Header().Set("X-Frame-Options", "DENY")
			c.Response().Header().Set("X-XSS-Protection", "1; mode=block")
			c.Response().Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			c.Response().Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; font-src 'self' data:; connect-src 'self' wss: https:;")
			return next(c)
		}
	}
}

// RequestIDMiddleware adds request ID to context
func RequestIDMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			requestID := c.Request().Header.Get("X-Request-ID")
			if requestID == "" {
				requestID = generateRequestID()
			}
			c.Set("request_id", requestID)
			c.Response().Header().Set("X-Request-ID", requestID)
			return next(c)
		}
	}
}

func generateRequestID() string {
	return "req_" + strings.ToLower(generateRandomString(16))
}

func generateRandomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[i%len(letters)]
	}
	return string(b)
}

// RateLimitMiddleware adds rate limiting
func RateLimitMiddleware(redis RedisClient, cfg *config.RateLimitConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !cfg.Enabled {
				return next(c)
			}
			
			_ = "ratelimit:" + c.RealIP()
			
			// Check endpoint-specific limit
			_ = cfg.RequestsPerMin
			if _, ok := cfg.Endpoints[c.Path()]; ok {
				// Parse "N/minute" format
				// Simplified for now
			}
			
			// Implementation would use Redis sliding window
			// Simplified for now
			
			return next(c)
		}
	}
}

// RedisClient interface for rate limiting
type RedisClient interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, expiration time.Duration) error
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error)
}