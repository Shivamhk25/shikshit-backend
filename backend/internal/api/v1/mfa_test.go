package apiv1_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// totpAt computes an authenticator code the way a phone app would (RFC 6238, SHA-1, 6 digits, 30 s).
func totpAt(t *testing.T, otpauth string, at time.Time) string {
	t.Helper()
	u, err := url.Parse(otpauth)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(u.Query().Get("secret"))
	if err != nil {
		t.Fatal(err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

type challengeOut struct {
	MfaRequired    string `json:"mfaRequired"`
	ChallengeToken string `json:"challengeToken"`
}

type setupOut struct {
	OtpauthURI    string   `json:"otpauthUri"`
	RecoveryCodes []string `json:"recoveryCodes"`
}

func codeBody(token, code string) string {
	if token == "" {
		return `{"code":"` + code + `"}`
	}
	return `{"challengeToken":"` + token + `","code":"` + code + `"}`
}

// enrol turns two-step sign-in on for a signed-in user and returns the otpauth URI.
func (s usersStack) enrol(t *testing.T, token string) string {
	t.Helper()
	rec := s.do(http.MethodPost, alphaHost, "/me/mfa/setup", token, "")
	want(t, rec, http.StatusOK, "")
	uri := decode[setupOut](t, rec).OtpauthURI
	rec = s.do(http.MethodPost, alphaHost, "/me/mfa/confirm", token, codeBody("", totpAt(t, uri, time.Now())))
	want(t, rec, http.StatusOK, "")
	if n := len(decode[setupOut](t, rec).RecoveryCodes); n != 10 {
		t.Fatalf("recovery codes = %d", n)
	}
	return uri
}

func (s usersStack) challenge(t *testing.T, host, user string) challengeOut {
	t.Helper()
	rec := s.do(http.MethodPost, host, "/auth/login", "", login(user, password))
	want(t, rec, http.StatusAccepted, "")
	return decode[challengeOut](t, rec)
}

func TestMFAEnrolVerifyAndNoReuse(t *testing.T) {
	s := newUsersStackMFA(t, false)
	plain := s.signIn(t, alphaHost, "plain", password)
	want(t, s.do(http.MethodGet, alphaHost, "/me/mfa", plain, ""), http.StatusOK, "")
	uri := s.enrol(t, plain)
	want(t, s.do(http.MethodPost, alphaHost, "/me/mfa/setup", plain, ""), http.StatusConflict, "mfa_already_on")

	// From now on the password alone gives no tokens.
	ch := s.challenge(t, alphaHost, "plain")
	if ch.MfaRequired != "verify" {
		t.Fatalf("challenge = %+v", ch)
	}
	// The code used to confirm the setup cannot be used again; the next step's code works once.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now()))), http.StatusUnauthorized, "invalid_code")
	next := totpAt(t, uri, time.Now().Add(30*time.Second))
	// A challenge works only on its own institute's host.
	want(t, s.do(http.MethodPost, betaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, next)), http.StatusUnauthorized, "challenge_expired")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, next)), http.StatusOK, "")
	ch = s.challenge(t, alphaHost, "plain")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, next)), http.StatusUnauthorized, "invalid_code")

	// Turning it off needs a current code; the secret is never in the audit trail.
	want(t, s.do(http.MethodPost, alphaHost, "/me/mfa/disable", plain, codeBody("", "123456")), http.StatusBadRequest, "invalid_code")
	var leaked int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE after::text ILIKE '%otpauth%' OR after::text ILIKE '%secret%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("secrets in audit = %d, %v", leaked, err)
	}
	var stored []byte
	if err := s.adm.QueryRow(bg, `SELECT mfa_secret_enc FROM users WHERE id = $1::uuid`, s.ids["plain"]).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(uri); strings.Contains(string(stored), u.Query().Get("secret")) {
		t.Fatal("the secret is stored in the clear")
	}
}

func TestMFAWrongCodesLockTheAccountEvenAcrossSignIns(t *testing.T) {
	s := newUsersStackMFA(t, false)
	s.enrol(t, s.signIn(t, alphaHost, "plain", password))
	// Four wrong codes on one challenge, then a fresh sign-in: the count carries over, so the fifth locks.
	ch := s.challenge(t, alphaHost, "plain")
	for i := 0; i < 4; i++ {
		want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, "000000")), http.StatusUnauthorized, "invalid_code")
	}
	ch = s.challenge(t, alphaHost, "plain")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, "000000")), http.StatusUnauthorized, "invalid_code")
	want(t, s.do(http.MethodPost, alphaHost, "/auth/login", "", login("plain", password)), http.StatusLocked, "account_locked")
}

func TestMFAChallengeDiesAfterFiveWrongCodes(t *testing.T) {
	s := newUsersStackMFA(t, false)
	uri := s.enrol(t, s.signIn(t, alphaHost, "plain", password))
	ch := s.challenge(t, alphaHost, "plain")
	if _, err := s.adm.Exec(bg, `UPDATE mfa_challenges SET attempts = 4 WHERE kind = 'verify'`); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, "000000")), http.StatusUnauthorized, "invalid_code")
	// Even the right code no longer helps: sign in again.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now().Add(30*time.Second)))), http.StatusUnauthorized, "challenge_expired")
}

func TestMFARequiredForAdminsAndAdminReset(t *testing.T) {
	s := newUsersStackMFA(t, true)
	// An Admin without two-step sign-in must set it up before getting tokens.
	ch := s.challenge(t, alphaHost, "admin")
	if ch.MfaRequired != "setup" {
		t.Fatalf("admin challenge = %+v", ch)
	}
	// A setup challenge is not a verify challenge.
	want(t, s.do(http.MethodPost, alphaHost, "/auth/mfa/verify", "", codeBody(ch.ChallengeToken, "000000")), http.StatusUnauthorized, "challenge_expired")
	rec := s.do(http.MethodPost, alphaHost, "/auth/mfa/setup", "", `{"challengeToken":"`+ch.ChallengeToken+`"}`)
	want(t, rec, http.StatusOK, "")
	uri := decode[setupOut](t, rec).OtpauthURI
	rec = s.do(http.MethodPost, alphaHost, "/auth/mfa/setup/confirm", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now())))
	want(t, rec, http.StatusOK, "")
	admin := decode[tokenBody](t, rec).AccessToken

	// Admins cannot turn it off; everyone else is not asked.
	want(t, s.do(http.MethodPost, alphaHost, "/me/mfa/disable", admin, codeBody("", totpAt(t, uri, time.Now().Add(30*time.Second)))), http.StatusConflict, "mfa_required")
	plain := s.signIn(t, alphaHost, "plain", password)
	s.enrol(t, plain)

	// An admin can reset someone else's two-step sign-in (lost phone): it is off and they are signed out.
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+s.ids["admin"]+"/reset-mfa", admin, ""), http.StatusConflict, "self_action")
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+s.ids["plain"]+"/reset-mfa", plain, ""), http.StatusForbidden, "")
	want(t, s.do(http.MethodPost, alphaHost, "/users/"+s.ids["plain"]+"/reset-mfa", admin, ""), http.StatusOK, "")
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", plain, ""), http.StatusUnauthorized, "")
	s.signIn(t, alphaHost, "plain", password) // password alone again
}
