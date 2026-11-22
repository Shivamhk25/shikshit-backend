package identity

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B, SHA-1, secret "12345678901234567890", 8-digit codes. We use 6 digits, which are the last six.
func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := totpCode(secret, tc.unix/30); got != tc.want[2:] {
			t.Errorf("T=%d: code %s, want %s", tc.unix, got, tc.want[2:])
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111111, 0)
	step := now.Unix() / 30
	code := totpCode(secret, step)
	if got, ok := verifyTOTP(secret, code, now, 0); !ok || got != step {
		t.Fatalf("current code refused")
	}
	if _, ok := verifyTOTP(secret, totpCode(secret, step-1), now, 0); !ok {
		t.Error("previous step (clock drift) refused")
	}
	if _, ok := verifyTOTP(secret, totpCode(secret, step-2), now, 0); ok {
		t.Error("code two steps old accepted")
	}
	if _, ok := verifyTOTP(secret, code, now, step); ok {
		t.Error("a code was accepted twice")
	}
	if _, ok := verifyTOTP(secret, code[:3]+" "+code[3:], now, 0); !ok {
		t.Error("a code typed with a space was refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := verifyTOTP(secret, bad, now, 0); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOTPAuthURIAndSealer(t *testing.T) {
	uri := otpauthURI("Shikshit · Alpha", "asha", []byte("12345678901234567890"))
	if !strings.HasPrefix(uri, "otpauth://totp/") || !strings.Contains(uri, "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ") || !strings.Contains(uri, "issuer=") {
		t.Fatalf("uri = %s", uri)
	}
	s, err := newSealer(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.seal([]byte("secret"), "t1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.open(sealed, "t1", "u1"); err != nil || string(got) != "secret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := s.open(sealed, "t1", "u2"); err == nil {
		t.Error("a secret opened for another user")
	}
	if _, err := newSealer([]byte("short")); err == nil {
		t.Error("short key accepted")
	}
}
