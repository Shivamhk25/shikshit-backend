package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/identity"
)

const platformEmail = "ops@shikshit.test"

// addPlatformUser creates a platform user through the same function the operator command uses.
func (s usersStack) addPlatformUser(t *testing.T) {
	t.Helper()
	if _, err := identity.CreatePlatformUser(bg, s.adm, platformEmail, "Platform Ops", password); err != nil {
		t.Fatal(err)
	}
}

// platformSignIn signs the platform user in on a neutral host, setting up the authenticator the first time. It returns
// the access token, the otpauth URI and the platform refresh cookie.
func (s usersStack) platformSignIn(t *testing.T, uri string) (string, string, *http.Cookie) {
	t.Helper()
	rec := s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/login", "", `{"email":"`+platformEmail+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	ch := decode[challengeOut](t, rec)
	var tokens *httptest.ResponseRecorder
	if ch.MfaRequired == "setup" {
		rec = s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/setup", "", `{"challengeToken":"`+ch.ChallengeToken+`"}`)
		want(t, rec, http.StatusOK, "")
		uri = decode[setupOut](t, rec).OtpauthURI
		tokens = s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/setup/confirm", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now())))
	} else {
		tokens = s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now().Add(30*time.Second))))
	}
	want(t, tokens, http.StatusOK, "")
	var cookie *http.Cookie
	for _, c := range tokens.Result().Cookies() {
		if c.Name == "shikshit_prt" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Path != apiv1.BasePath+"/platform/auth" {
		t.Fatalf("platform refresh cookie = %+v", cookie)
	}
	if strings.Contains(tokens.Body.String(), `"refreshToken"`) {
		t.Fatalf("the refresh token must not be in the body: %s", tokens.Body)
	}
	return decode[tokenBody](t, tokens).AccessToken, uri, cookie
}

func TestAPlatformUserAlwaysSignsInWithTwoSteps(t *testing.T) {
	s := newUsersStack(t)
	s.addPlatformUser(t)

	// first sign-in: setup is forced, and a password alone never gives tokens
	access, uri, cookie := s.platformSignIn(t, "")
	rec := s.do(http.MethodGet, "console.shikshit.test", "/platform/me", access, "")
	want(t, rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), platformEmail) {
		t.Fatalf("me = %s", rec.Body)
	}

	// next sign-in asks for a code from the same app
	rec = s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/login", "", `{"email":"`+platformEmail+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	if got := decode[challengeOut](t, rec).MfaRequired; got != "verify" {
		t.Fatalf("second sign-in kind = %q", got)
	}
	ch := decode[challengeOut](t, rec)
	want(t, s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/verify", "", codeBody(ch.ChallengeToken, "000000")), http.StatusUnauthorized, "invalid_code")
	if totpAt(t, uri, time.Now().Add(30*time.Second)) != "000000" {
		want(t, s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now().Add(30*time.Second)))), http.StatusOK, "")
	}

	// refresh with the cookie, only with X-Client: web
	req := httptest.NewRequest(http.MethodPost, apiv1.BasePath+"/platform/auth/refresh", nil)
	req.Host = "console.shikshit.test"
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	want(t, rec, http.StatusBadRequest, "validation_failed")
	req = httptest.NewRequest(http.MethodPost, apiv1.BasePath+"/platform/auth/refresh", nil)
	req.Host = "console.shikshit.test"
	req.Header.Set("X-Client", "web")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	want(t, rec, http.StatusOK, "")

	// logout ends the session: the access token stops working at once
	req = httptest.NewRequest(http.MethodPost, apiv1.BasePath+"/platform/auth/logout", nil)
	req.Host = "console.shikshit.test"
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	want(t, rec, http.StatusNoContent, "")
	want(t, s.do(http.MethodGet, "console.shikshit.test", "/platform/me", access, ""), http.StatusUnauthorized, "unauthenticated")
}

func TestPlatformAndInstituteTokensNeverCross(t *testing.T) {
	s := newUsersStack(t)
	s.addPlatformUser(t)
	platformToken, _, _ := s.platformSignIn(t, "")
	admin := s.signIn(t, alphaHost, "admin", password)

	// an institute Admin's token is not a platform token, on any host
	want(t, s.do(http.MethodGet, alphaHost, "/platform/me", admin, ""), http.StatusUnauthorized, "unauthenticated")
	want(t, s.do(http.MethodGet, "console.shikshit.test", "/platform/me", admin, ""), http.StatusUnauthorized, "unauthenticated")
	// and a platform token opens no institute data
	want(t, s.do(http.MethodGet, alphaHost, "/me/access", platformToken, ""), http.StatusUnauthorized, "unauthenticated")
	want(t, s.do(http.MethodGet, alphaHost, "/users", platformToken, ""), http.StatusUnauthorized, "unauthenticated")
	want(t, s.do(http.MethodGet, "console.shikshit.test", "/platform/me", "", ""), http.StatusUnauthorized, "unauthenticated")
}

func TestPlatformSignInFailuresAndLock(t *testing.T) {
	s := newUsersStack(t)
	s.addPlatformUser(t)
	login := func(email, pass string) *httptest.ResponseRecorder {
		return s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/login", "", `{"email":"`+email+`","password":"`+pass+`"}`)
	}
	// unknown email and wrong password look the same
	want(t, login("nobody@shikshit.test", password), http.StatusUnauthorized, "invalid_credentials")
	for i := 0; i < 5; i++ {
		want(t, login(platformEmail, "wrong password!"), http.StatusUnauthorized, "invalid_credentials")
	}
	want(t, login(platformEmail, password), http.StatusLocked, "account_locked")

	// a disabled platform user gets nothing, even with the right password
	if _, err := s.adm.Exec(bg, `UPDATE platform_users SET status = 'disabled', locked_until = NULL`); err != nil {
		t.Fatal(err)
	}
	want(t, login(platformEmail, password), http.StatusUnauthorized, "invalid_credentials")
}

func TestResetPlatformMFAForcesSetupAndEndsSessions(t *testing.T) {
	s := newUsersStack(t)
	s.addPlatformUser(t)
	access, _, _ := s.platformSignIn(t, "")
	if err := identity.ResetPlatformMFA(bg, s.adm, platformEmail); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodGet, "console.shikshit.test", "/platform/me", access, ""), http.StatusUnauthorized, "unauthenticated")
	rec := s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/login", "", `{"email":"`+platformEmail+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	if got := decode[challengeOut](t, rec).MfaRequired; got != "setup" {
		t.Fatalf("after a reset the kind = %q, want setup", got)
	}
}

// Every x-platform operation in the contract is guarded by the platform check, and nothing else is.
func TestPlatformOperationsMatchTheContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi/openapi.bundled.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	got := apiv1.PlatformOperations()
	seen := 0
	for path, methods := range spec.Paths {
		for method, raw := range methods {
			op, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := op["operationId"].(string)
			if id == "" {
				continue
			}
			name := strings.ToUpper(id[:1]) + id[1:]
			platform, _ := op["x-platform"].(bool)
			if platform != got[name] {
				t.Errorf("%s %s (%s): contract x-platform=%v, middleware %v", strings.ToUpper(method), path, name, platform, got[name])
			}
			if platform {
				seen++
				if !strings.HasPrefix(path, "/platform/") {
					t.Errorf("%s: platform operations must live under /platform/ (the tenancy middleware skips it)", path)
				}
			}
			if strings.HasPrefix(path, "/platform/") && !platform {
				if public, _ := op["x-public"].(bool); !public {
					t.Errorf("%s %s: under /platform/ but neither x-platform nor x-public", method, path)
				}
			}
		}
	}
	if seen != len(got) {
		t.Errorf("contract has %d platform operations, middleware %d", seen, len(got))
	}
}

// A lock set while a challenge is open stops the second step too, and a disabled user's session cannot be refreshed.
func TestPlatformLockAndDisableReachEveryStep(t *testing.T) {
	s := newUsersStack(t)
	s.addPlatformUser(t)
	_, uri, cookie := s.platformSignIn(t, "")

	rec := s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/login", "", `{"email":"`+platformEmail+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	ch := decode[challengeOut](t, rec)
	if _, err := s.adm.Exec(bg, `UPDATE platform_users SET locked_until = now() + interval '10 minutes'`); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, "console.shikshit.test", "/platform/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now().Add(30*time.Second)))), http.StatusLocked, "account_locked")

	if _, err := s.adm.Exec(bg, `UPDATE platform_users SET status = 'disabled', locked_until = NULL`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, apiv1.BasePath+"/platform/auth/refresh", nil)
	req.Host = "console.shikshit.test"
	req.Header.Set("X-Client", "web")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	want(t, rec, http.StatusUnauthorized, "invalid_refresh_token")
}
