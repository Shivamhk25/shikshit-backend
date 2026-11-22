// Package server assembles the Shikshit HTTP API: institute resolution from the host, the contract operations,
// authentication and authorisation, CORS and basic hardening.
package server

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
)

// Config is read from environment variables so secrets never live in files or code.
type Config struct {
	Addr               string        // SHIKSHIT_ADDR, default :8080
	DatabaseURL        string        // SHIKSHIT_DATABASE_URL: connect as shikshit_api (row-level security applies), never a superuser
	JWTSecret          []byte        // SHIKSHIT_JWT_SECRET: at least 32 bytes
	JWTIssuer          string        // SHIKSHIT_JWT_ISSUER, default "shikshit"
	AccessTTL          time.Duration // SHIKSHIT_ACCESS_TTL, default 15m
	BaseDomains        []string      // SHIKSHIT_BASE_DOMAINS: e.g. shikshit.app; "<code>.shikshit.app" selects an institute
	DefaultTenant      string        // SHIKSHIT_DEFAULT_TENANT: development only; used when the host carries no institute
	TrustForwardedHost bool          // SHIKSHIT_TRUST_FORWARDED_HOST: only behind a proxy that overwrites X-Forwarded-Host
	AllowedOrigins     []string      // SHIKSHIT_ALLOWED_ORIGINS: exact origins allowed by CORS (no wildcard)
	Version            string        // SHIKSHIT_VERSION
	Production         bool          // SHIKSHIT_ENV=production
	// MFAKey encrypts two-step sign-in secrets at rest. SHIKSHIT_MFA_KEY: 32 random bytes, base64. Required in
	// production; in development it is derived from the JWT secret so a fresh checkout works.
	MFAKey []byte
	// MFARequiredForAdmins makes holders of the Admin role set up two-step sign-in (SHIKSHIT_MFA_REQUIRED_FOR_ADMINS,
	// default true; always true in production).
	MFARequiredForAdmins bool
	// Storage is the S3-compatible bucket for uploaded files (FND-17): SHIKSHIT_STORAGE_ENDPOINT (host:port),
	// _BUCKET, _ACCESS_KEY, _SECRET_KEY, _REGION (default us-east-1), _SECURE (default true). Optional in development
	// (file operations then fail); required in production.
	Storage storage.Config
}

// LoadConfig reads and validates the configuration. get is os.Getenv in production.
func LoadConfig(get func(string) string) (Config, error) {
	c := Config{
		Addr:                 orDefault(get("SHIKSHIT_ADDR"), ":8080"),
		DatabaseURL:          get("SHIKSHIT_DATABASE_URL"),
		JWTSecret:            []byte(get("SHIKSHIT_JWT_SECRET")),
		JWTIssuer:            orDefault(get("SHIKSHIT_JWT_ISSUER"), "shikshit"),
		BaseDomains:          split(get("SHIKSHIT_BASE_DOMAINS")),
		DefaultTenant:        strings.TrimSpace(get("SHIKSHIT_DEFAULT_TENANT")),
		AllowedOrigins:       split(get("SHIKSHIT_ALLOWED_ORIGINS")),
		Version:              orDefault(get("SHIKSHIT_VERSION"), "dev"),
		Production:           get("SHIKSHIT_ENV") == "production",
		AccessTTL:            15 * time.Minute,
		MFARequiredForAdmins: true,
	}
	if v := get("SHIKSHIT_ACCESS_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute || d > time.Hour {
			return c, errors.New("SHIKSHIT_ACCESS_TTL must be a duration between 1m and 1h")
		}
		c.AccessTTL = d
	}
	if v := get("SHIKSHIT_TRUST_FORWARDED_HOST"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, errors.New("SHIKSHIT_TRUST_FORWARDED_HOST must be true or false")
		}
		c.TrustForwardedHost = b
	}

	if v := get("SHIKSHIT_MFA_REQUIRED_FOR_ADMINS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, errors.New("SHIKSHIT_MFA_REQUIRED_FOR_ADMINS must be true or false")
		}
		c.MFARequiredForAdmins = b
	}
	if v := strings.TrimSpace(get("SHIKSHIT_MFA_KEY")); v != "" {
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(key) != 32 {
			return c, errors.New("SHIKSHIT_MFA_KEY must be 32 random bytes, base64 encoded (openssl rand -base64 32)")
		}
		c.MFAKey = key
	}

	c.Storage = storage.Config{
		Endpoint:  strings.TrimSpace(get("SHIKSHIT_STORAGE_ENDPOINT")),
		Bucket:    strings.TrimSpace(get("SHIKSHIT_STORAGE_BUCKET")),
		AccessKey: strings.TrimSpace(get("SHIKSHIT_STORAGE_ACCESS_KEY")),
		SecretKey: strings.TrimSpace(get("SHIKSHIT_STORAGE_SECRET_KEY")),
		Region:    orDefault(get("SHIKSHIT_STORAGE_REGION"), "us-east-1"),
		Secure:    true,
	}
	if v := get("SHIKSHIT_STORAGE_SECURE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, errors.New("SHIKSHIT_STORAGE_SECURE must be true or false")
		}
		c.Storage.Secure = b
	}

	var missing []string
	if c.DatabaseURL == "" {
		missing = append(missing, "SHIKSHIT_DATABASE_URL")
	}
	if len(c.JWTSecret) == 0 {
		missing = append(missing, "SHIKSHIT_JWT_SECRET")
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	if len(c.JWTSecret) < 32 {
		return c, errors.New("SHIKSHIT_JWT_SECRET must be at least 32 bytes")
	}
	for _, o := range c.AllowedOrigins {
		if o == "*" {
			return c, errors.New("SHIKSHIT_ALLOWED_ORIGINS must list exact origins; a wildcard is not allowed")
		}
	}
	if c.Production {
		if c.DefaultTenant != "" {
			return c, errors.New("SHIKSHIT_DEFAULT_TENANT is for development only and must be empty in production")
		}
		if len(c.BaseDomains) == 0 {
			return c, errors.New("SHIKSHIT_BASE_DOMAINS is required in production")
		}
		if c.MFAKey == nil {
			return c, errors.New("SHIKSHIT_MFA_KEY is required in production")
		}
		if !c.MFARequiredForAdmins {
			return c, errors.New("SHIKSHIT_MFA_REQUIRED_FOR_ADMINS cannot be false in production")
		}
		if c.Storage.Endpoint == "" || c.Storage.Bucket == "" || c.Storage.AccessKey == "" || c.Storage.SecretKey == "" {
			return c, errors.New("SHIKSHIT_STORAGE_ENDPOINT, _BUCKET, _ACCESS_KEY and _SECRET_KEY are required in production")
		}
		if !c.Storage.Secure {
			return c, errors.New("SHIKSHIT_STORAGE_SECURE cannot be false in production")
		}
	}
	if c.MFAKey == nil {
		sum := sha256.Sum256(append([]byte("shikshit-dev-mfa-key:"), c.JWTSecret...))
		c.MFAKey = sum[:]
	}
	return c, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func split(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
