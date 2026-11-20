package apiv1_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/pie360/backend/internal/api/v1"
)

const cookieName = "shikshit_rt"

func cookieStack(t *testing.T) stack {
	return newStackWith(t, apiv1.WithWebRefreshCookie(apiv1.CookieConfig{Name: cookieName, Secure: true}))
}

func findCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

func (s stack) postWithCookie(path, body, cookie string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, apiv1.BasePath+path, strings.NewReader(body))
	req.Host = "alpha.shikshit.test"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

func TestWebLoginPutsTheRefreshTokenInAnHttpOnlyCookieNotTheBody(t *testing.T) {
	s := cookieStack(t)
	rec := s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "web")
	if rec.Code != 200 {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "refreshToken\":\"") {
		t.Fatalf("web response body must not carry the refresh token: %s", rec.Body)
	}
	ck := findCookie(rec)
	if ck == nil || ck.Value == "" {
		t.Fatal("no refresh cookie")
	}
	if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != apiv1.BasePath+"/auth" {
		t.Errorf("cookie attributes: %+v", ck)
	}
	if ck.Expires.IsZero() {
		t.Error("cookie must carry the refresh expiry")
	}
}

func TestMobileLoginStillGetsTheTokenInTheBodyAndNoCookie(t *testing.T) {
	s := cookieStack(t)
	for _, client := range []string{"ios", "android"} {
		rec := s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", client)
		if decode[tokenBody](t, rec).RefreshToken == "" || findCookie(rec) != nil {
			t.Errorf("%s: body token missing or cookie set", client)
		}
	}
}

func TestWebRefreshWithCookieRotatesIt(t *testing.T) {
	s := cookieStack(t)
	first := findCookie(s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "web"))

	rec := s.postWithCookie("/auth/refresh", "", first.Value, "X-Client", "web")
	if rec.Code != 200 {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body)
	}
	next := findCookie(rec)
	if next == nil || next.Value == first.Value {
		t.Fatal("cookie was not rotated")
	}
	if decode[tokenBody](t, rec).AccessToken == "" {
		t.Error("no access token")
	}
}

func TestCookieIsIgnoredWithoutTheClientHeader(t *testing.T) {
	s := cookieStack(t)
	first := findCookie(s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "web"))
	// A cross-site form post can send the cookie but cannot add a custom header.
	if rec := s.postWithCookie("/auth/refresh", "", first.Value); rec.Code != http.StatusBadRequest {
		t.Fatalf("cookie without X-Client = %d, want 400", rec.Code)
	}
	if rec := s.postWithCookie("/auth/refresh", "", first.Value, "X-Client", "android"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cookie from a non-web client = %d, want 400", rec.Code)
	}
}

func TestReusedCookieEndsTheSessionAndClearsIt(t *testing.T) {
	s := cookieStack(t)
	first := findCookie(s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "web"))
	s.postWithCookie("/auth/refresh", "", first.Value, "X-Client", "web")
	rec := s.postWithCookie("/auth/refresh", "", first.Value, "X-Client", "web")
	if rec.Code != 401 || decode[problemBody](t, rec).Code != "refresh_reuse" {
		t.Fatalf("replay = %d %s", rec.Code, rec.Body)
	}
	if ck := findCookie(rec); ck == nil || ck.Value != "" || ck.MaxAge >= 0 {
		t.Errorf("cookie must be cleared on reuse: %+v", ck)
	}
}

func TestLogoutClearsTheCookieAndRevokesTheSession(t *testing.T) {
	s := cookieStack(t)
	first := findCookie(s.post("alpha.shikshit.test", "/auth/login", login("user-alpha", password), "X-Client", "web"))
	rec := s.postWithCookie("/auth/logout", "", first.Value)
	if rec.Code != 204 {
		t.Fatalf("logout = %d", rec.Code)
	}
	if ck := findCookie(rec); ck == nil || ck.MaxAge >= 0 {
		t.Errorf("cookie not cleared: %+v", ck)
	}
	if rec := s.postWithCookie("/auth/refresh", "", first.Value, "X-Client", "web"); rec.Code != 401 {
		t.Errorf("refresh after logout = %d", rec.Code)
	}
}

func TestLogoutWithNothingIsStillSuccessful(t *testing.T) {
	s := cookieStack(t)
	rec := s.postWithCookie("/auth/logout", "", "")
	if rec.Code != 204 {
		t.Fatalf("logout without a token = %d, want 204 (idempotent)", rec.Code)
	}
}

func TestRefreshWithNoTokenAnywhereIsABadRequest(t *testing.T) {
	s := cookieStack(t)
	if rec := s.postWithCookie("/auth/refresh", "", "", "X-Client", "web"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
