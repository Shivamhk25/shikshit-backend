package identity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1, which authenticator apps expect; it is not used as a hash.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters every common authenticator app understands (Google Authenticator, Microsoft Authenticator, Authy,
// 1Password): HMAC-SHA1, 6 digits, 30-second steps. A code from the step before or after is also accepted, for
// clock drift.
const (
	totpDigits = 6
	totpPeriod = 30
	totpSkew   = 1
	secretLen  = 20 // 160 bits, as RFC 4226 recommends
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newTOTPSecret returns a fresh random secret.
func newTOTPSecret() ([]byte, error) {
	s := make([]byte, secretLen)
	_, err := rand.Read(s)
	return s, err
}

// totpCode is the code for one time step (RFC 6238 over RFC 4226).
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, n%1_000_000)
}

// verifyTOTP returns the step a code belongs to, if it matches the current step or one either side and is later
// than lastStep (so a code cannot be used twice).
func verifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	for d := -totpSkew; d <= totpSkew; d++ {
		step := current + int64(d)
		if step <= lastStep {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}

// otpauthURI is what an authenticator app reads from a QR code or a tapped link.
func otpauthURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {b32.EncodeToString(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// sealer encrypts TOTP secrets at rest with AES-256-GCM. The institute and user ids are bound in as associated data,
// so a secret copied onto another user's row does not decrypt.
type sealer struct{ aead cipher.AEAD }

var errNoMFAKey = errors.New("identity: two-step sign-in is not configured (no MFA key)")

func newSealer(key []byte) (*sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("identity: the MFA key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealer{aead: aead}, nil
}

func (s *sealer) seal(plain []byte, tenantID, userID string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(tenantID+"/"+userID)), nil
}

func (s *sealer) open(sealed []byte, tenantID, userID string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("identity: sealed secret too short")
	}
	return s.aead.Open(nil, sealed[:n], sealed[n:], []byte(tenantID+"/"+userID))
}
