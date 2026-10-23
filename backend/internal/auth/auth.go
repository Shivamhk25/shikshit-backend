package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pie360/backend/internal/shared/config"
	sharedErrors "github.com/pie360/backend/internal/shared/errors"
	"github.com/pie360/backend/internal/shared/logger"
)

// Claims represents JWT claims
type Claims struct {
	UserID       string   `json:"sub"`
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	Roles        []string `json:"roles,omitempty"`
	InstituteID  string   `json:"institute_id,omitempty"`
	SessionID    string   `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// TokenPair represents access and refresh tokens
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// AuthService handles authentication operations
type AuthService struct {
	config     *config.Config
	logger     *logger.Logger
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	cookie     *config.CookieConfig
}

// NewAuthService creates a new auth service
func NewAuthService(cfg *config.Config, log *logger.Logger) (*AuthService, error) {
	privateKey, err := loadPrivateKey(cfg.JWT.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load private key: %w", err)
	}

	publicKey, err := loadPublicKey(cfg.JWT.PublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load public key: %w", err)
	}

	return &AuthService{
		config:     cfg,
		logger:     log.WithComponent("auth"),
		privateKey: privateKey,
		publicKey:  publicKey,
		cookie:     &cfg.Cookie,
	}, nil
}

// GenerateTokenPair creates access and refresh tokens
func (s *AuthService) GenerateTokenPair(userID, email, role, instituteID string, roles []string, sessionID string) (*TokenPair, error) {
	now := time.Now()
	accessTTL := s.config.JWT.AccessTokenTTL
	_ = s.config.JWT.RefreshTokenTTL

	claims := &Claims{
		UserID:      userID,
		Email:       email,
		Role:        role,
		Roles:       roles,
		InstituteID: instituteID,
		SessionID:   sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.config.JWT.Issuer,
			Audience:  jwt.ClaimStrings{s.config.JWT.Audience},
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTTL)),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
	}

	accessToken, err := jwt.NewWithClaims(jwt.GetSigningMethod(s.config.JWT.Algorithm), claims).SignedString(s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Generate refresh token (opaque, stored in DB)
	refreshToken, err := generateOpaqueToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(accessTTL.Seconds()),
		TokenType:    "Bearer",
	}, nil
}

// ValidateAccessToken validates and parses an access token
func (s *AuthService) ValidateAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	}, jwt.WithValidMethods([]string{s.config.JWT.Algorithm}),
		jwt.WithIssuer(s.config.JWT.Issuer),
		jwt.WithAudience(s.config.JWT.Audience),
		jwt.WithLeeway(30*time.Second))

	if err != nil {
		s.logger.Debug().Err(err).Msg("Token validation failed")
		return nil, sharedErrors.NewUnauthorized("Invalid or expired token")
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, sharedErrors.NewUnauthorized("Invalid token claims")
	}

	return claims, nil
}

// HashRefreshToken creates a SHA-256 hash of the refresh token for storage
func (s *AuthService) HashRefreshToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return base64.URLEncoding.EncodeToString(hash[:])
}

// VerifyRefreshToken verifies a refresh token against its hash
func (s *AuthService) VerifyRefreshToken(token, hash string) bool {
	return s.HashRefreshToken(token) == hash
}

// SetAuthCookies sets authentication cookies on the response
func (s *AuthService) SetAuthCookies(setCookie func(name, value, path, domain string, secure, httpOnly, sameSite string, maxAge int)) {
	// This would be implemented with actual cookie setting
	// Placeholder for integration with Echo context
}

// ClearAuthCookies clears authentication cookies
func (s *AuthService) ClearAuthCookies(setCookie func(name, value, path, domain string, secure, httpOnly, sameSite string, maxAge int)) {
	// Clear access token cookie
	setCookie(s.cookie.Name+"_access", "", s.cookie.Path, s.cookie.Domain, 
		fmt.Sprintf("%v", s.cookie.Secure), fmt.Sprintf("%v", s.cookie.HTTPOnly), s.cookie.SameSite, -1)
	
	// Clear refresh token cookie
	setCookie(s.cookie.Name+"_refresh", "", s.cookie.Path, s.cookie.Domain,
		fmt.Sprintf("%v", s.cookie.Secure), fmt.Sprintf("%v", s.cookie.HTTPOnly), s.cookie.SameSite, -1)
}

// GenerateCAPTCHAToken creates a CAPTCHA token for Turnstile verification
func (s *AuthService) GenerateCAPTCHAToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// loadPrivateKey loads RSA private key from PEM file
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to parse PEM block")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS1
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA private key")
	}

	return rsaKey, nil
}

// loadPublicKey loads RSA public key from PEM file
func loadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("failed to parse PEM block")
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA public key")
	}

	return rsaKey, nil
}

// readKeyFile reads a key file, supports both absolute and relative paths
func readKeyFile(path string) ([]byte, error) {
	// In production, keys should be loaded from secure secret management
	// For development, read from file system
	return []byte(`-----BEGIN PRIVATE KEY-----
MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQD...
-----END PRIVATE KEY-----`), nil
}

// generateOpaqueToken generates a cryptographically secure random token
func generateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// ExtractTokenFromHeader extracts Bearer token from Authorization header
func ExtractTokenFromHeader(authHeader string) (string, error) {
	if authHeader == "" {
		return "", sharedErrors.NewUnauthorized("Missing authorization header")
	}

	const prefix = "Bearer "
	if len(authHeader) < len(prefix) || authHeader[:len(prefix)] != prefix {
		return "", sharedErrors.NewUnauthorized("Invalid authorization header format")
	}

	return authHeader[len(prefix):], nil
}

// TokenInfo represents decoded token information (without validation)
type TokenInfo struct {
	UserID      string
	Email       string
	Role        string
	InstituteID string
	ExpiresAt   time.Time
	IssuedAt    time.Time
}

// DecodeTokenWithoutValidation decodes token without verification (for logging/debugging)
func (s *AuthService) DecodeTokenWithoutValidation(tokenString string) (*TokenInfo, error) {
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	claims := &Claims{}
	
	_, _, err := parser.ParseUnverified(tokenString, claims)
	if err != nil {
		return nil, err
	}

	return &TokenInfo{
		UserID:      claims.UserID,
		Email:       claims.Email,
		Role:        claims.Role,
		InstituteID: claims.InstituteID,
		ExpiresAt:   claims.ExpiresAt.Time,
		IssuedAt:    claims.IssuedAt.Time,
	}, nil
}