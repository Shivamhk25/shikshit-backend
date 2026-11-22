package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the access-token claims. They identify the caller and the institute; permissions are NOT in the
// token (they are resolved per request from the database, so a role change takes effect immediately).
type Claims struct {
	jwt.RegisteredClaims
	TenantID  string `json:"tid"`
	Tier      string `json:"tier"`
	SessionID string `json:"sid"`
}

// Signer issues and verifies access tokens (HS256, key pinned on verify).
type Signer struct {
	key    []byte
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

// MinKeyLen is the shortest signing key accepted (256 bits).
const MinKeyLen = 32

// NewSigner returns a Signer. The key must be at least 32 bytes and come from the secret store, never source code.
func NewSigner(key []byte, issuer string, ttl time.Duration) (*Signer, error) {
	if len(key) < MinKeyLen {
		return nil, fmt.Errorf("identity: signing key must be at least %d bytes", MinKeyLen)
	}
	if ttl <= 0 {
		return nil, errors.New("identity: access token ttl must be positive")
	}
	return &Signer{key: key, issuer: issuer, ttl: ttl, now: time.Now}, nil
}

// Issue returns a signed access token and its expiry.
func (s *Signer) Issue(userID, tenantID, tier, sessionID string) (string, time.Time, error) {
	now := s.now()
	exp := now.Add(s.ttl)
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: s.issuer, Subject: userID, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp),
		},
		TenantID: tenantID, Tier: tier, SessionID: sessionID,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.key)
	return signed, exp, err
}

// ErrInvalidToken is returned for any token that fails verification (signature, algorithm, expiry, issuer).
var ErrInvalidToken = errors.New("identity: invalid token")

// Verify checks signature (HS256 only), expiry and issuer and returns the claims.
func (s *Signer) Verify(token string) (*Claims, error) {
	var c Claims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(s.issuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(s.now))
	if err != nil || !parsed.Valid || c.Subject == "" || c.SessionID == "" {
		return nil, ErrInvalidToken
	}
	// An institute token names its institute; a platform token (decision 0017) names none. Anything else is refused.
	if (c.Tier == "platform") != (c.TenantID == "") {
		return nil, ErrInvalidToken
	}
	return &c, nil
}

// newRefreshToken returns a random opaque token and the hash that is stored in place of it.
func newRefreshToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// hashToken is SHA-256: refresh tokens are 256-bit random, so a fast hash is enough (no brute force to slow down).
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
