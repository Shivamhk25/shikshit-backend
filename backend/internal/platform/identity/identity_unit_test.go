package identity

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected format: %s", h)
	}
	if ok, err := VerifyPassword("correct horse", h); err != nil || !ok {
		t.Fatalf("right password: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword("wrong", h); err != nil || ok {
		t.Fatalf("wrong password: ok=%v err=%v", ok, err)
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$m=0,t=0,p=0$AAAA$AAAA", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if ok, err := VerifyPassword("x", bad); err == nil || ok {
			t.Errorf("hash %q: want error, got ok=%v err=%v", bad, ok, err)
		}
	}
}

func newTestSigner(t *testing.T, now time.Time) *Signer {
	t.Helper()
	s, err := NewSigner([]byte(strings.Repeat("k", 32)), "shikshit", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

func TestSignerRejectsShortKeyAndBadTTL(t *testing.T) {
	if _, err := NewSigner([]byte("short"), "i", time.Minute); err == nil {
		t.Error("short key accepted")
	}
	if _, err := NewSigner([]byte(strings.Repeat("k", 32)), "i", 0); err == nil {
		t.Error("zero ttl accepted")
	}
}

func TestTokenIssueVerify(t *testing.T) {
	now := time.Now()
	s := newTestSigner(t, now)
	tok, exp, err := s.Issue("user-1", "tenant-1", "tenant", "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("expiry = %v", exp)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-1" || c.TenantID != "tenant-1" || c.SessionID != "sess-1" || c.Tier != "tenant" {
		t.Errorf("claims = %+v", c)
	}
}

func TestTokenRejections(t *testing.T) {
	now := time.Now()
	s := newTestSigner(t, now)
	tok, _, _ := s.Issue("u", "t", "tenant", "s")

	expired := newTestSigner(t, now.Add(16*time.Minute))
	if _, err := expired.Verify(tok); err == nil {
		t.Error("expired token accepted")
	}

	otherKey, _ := NewSigner([]byte(strings.Repeat("z", 32)), "shikshit", time.Minute)
	otherKey.now = s.now
	if _, err := otherKey.Verify(tok); err == nil {
		t.Error("token signed with another key accepted")
	}

	otherIssuer, _ := NewSigner([]byte(strings.Repeat("k", 32)), "someone-else", time.Minute)
	otherIssuer.now = s.now
	if _, err := otherIssuer.Verify(tok); err == nil {
		t.Error("token from another issuer accepted")
	}

	// alg=none must never verify
	none := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "shikshit", Subject: "u", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))},
		TenantID:         "t", SessionID: "s",
	})
	raw, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := s.Verify(raw); err == nil {
		t.Error("alg=none token accepted")
	}

	// a different HMAC algorithm must be refused even with the right key
	hs512 := jwt.NewWithClaims(jwt.SigningMethodHS512, Claims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "shikshit", Subject: "u", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))},
		TenantID:         "t", SessionID: "s",
	})
	raw512, _ := hs512.SignedString([]byte(strings.Repeat("k", 32)))
	if _, err := s.Verify(raw512); err == nil {
		t.Error("HS512 token accepted")
	}

	if _, err := s.Verify("garbage"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestRefreshTokenIsRandomAndHashed(t *testing.T) {
	a, ha, _ := newRefreshToken()
	b, hb, _ := newRefreshToken()
	if a == b || ha == hb {
		t.Fatal("tokens must be unique")
	}
	if ha == a || hashToken(a) != ha {
		t.Fatal("stored value must be the hash of the token")
	}
}
