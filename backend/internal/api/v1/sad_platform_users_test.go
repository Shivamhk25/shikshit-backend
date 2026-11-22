package apiv1_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/sad/platformusers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
)

type accountOut struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Link   string `json:"link"`
}

const colleague = "meera@shikshit.test"

func linkToken(link string) string { return strings.TrimPrefix(link, "/accept-invite?token=") }

// inviteColleague invites a second platform user and has them set their password; it returns their id.
func (s usersStack) inviteColleague(t *testing.T, tok string) string {
	t.Helper()
	rec := s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"`+colleague+`","name":"Meera Iyer"}`)
	want(t, rec, http.StatusCreated, "")
	a := decode[accountOut](t, rec)
	if a.Status != "invited" || !strings.HasPrefix(a.Link, "/accept-invite?token=") {
		t.Fatalf("invite = %+v", a)
	}
	want(t, s.do(http.MethodPost, console, "/platform/auth/invite/accept", "", `{"token":"`+linkToken(a.Link)+`","password":"`+password+`"}`), http.StatusNoContent, "")
	return a.ID
}

func TestInvitingAPlatformUser(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	want(t, s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"not-an-email","name":"X"}`), http.StatusBadRequest, "validation_failed")

	rec := s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"`+colleague+`","name":"Meera Iyer"}`)
	want(t, rec, http.StatusCreated, "")
	a := decode[accountOut](t, rec)
	want(t, s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"MEERA@shikshit.test","name":"Again"}`), http.StatusBadRequest, "validation_failed")

	// an invited user cannot sign in yet
	want(t, s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`), http.StatusUnauthorized, "invalid_credentials")
	// the link checks the password, works once, and then sign-in asks for authenticator setup
	want(t, s.do(http.MethodPost, console, "/platform/auth/invite/accept", "", `{"token":"`+linkToken(a.Link)+`","password":"short"}`), http.StatusBadRequest, "weak_password")
	want(t, s.do(http.MethodPost, console, "/platform/auth/invite/accept", "", `{"token":"`+linkToken(a.Link)+`","password":"`+password+`"}`), http.StatusNoContent, "")
	want(t, s.do(http.MethodPost, console, "/platform/auth/invite/accept", "", `{"token":"`+linkToken(a.Link)+`","password":"`+password+`"}`), http.StatusBadRequest, "invalid_token")
	rec = s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	if decode[challengeOut](t, rec).MfaRequired != "setup" {
		t.Fatalf("a new platform user must set up two-step sign-in: %s", rec.Body)
	}

	// an expired link is refused
	rec = s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"late@shikshit.test","name":"Late"}`)
	late := decode[accountOut](t, rec)
	if _, err := s.adm.Exec(bg, `UPDATE platform_user_invites SET expires_at = now() - interval '1 second' WHERE platform_user_id = $1::uuid`, late.ID); err != nil {
		t.Fatal(err)
	}
	want(t, s.do(http.MethodPost, console, "/platform/auth/invite/accept", "", `{"token":"`+linkToken(late.Link)+`","password":"`+password+`"}`), http.StatusBadRequest, "invalid_token")

	// the audit trail has the invite, never the link
	var after string
	if err := s.adm.QueryRow(bg, `SELECT string_agg(COALESCE(after::text, ''), ' ') FROM audit_log WHERE action LIKE 'platform_users.%'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, linkToken(a.Link)) || !strings.Contains(after, colleague) {
		t.Fatalf("audit: %s", after)
	}
}

func TestPlatformUserActionsAndTheirLimits(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	id := s.inviteColleague(t, tok)
	var me string
	if err := s.adm.QueryRow(bg, `SELECT id::text FROM platform_users WHERE email = $1`, platformEmail).Scan(&me); err != nil {
		t.Fatal(err)
	}
	act := func(target, action string) *accountOut {
		rec := s.do(http.MethodPost, console, "/platform/users/"+target+"/"+action, tok, "")
		if rec.Code != http.StatusOK {
			return nil
		}
		a := decode[accountOut](t, rec)
		return &a
	}

	// nobody can act on themselves
	for _, action := range []string{"disable", "reset-mfa", "reset-password"} {
		want(t, s.do(http.MethodPost, console, "/platform/users/"+me+"/"+action, tok, ""), http.StatusConflict, "self_action")
	}

	// the colleague signs in; disabling them ends that session and stops sign-in
	var uri string
	colleagueTok := func() string {
		rec := s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`)
		want(t, rec, http.StatusAccepted, "")
		ch := decode[challengeOut](t, rec)
		rec = s.do(http.MethodPost, console, "/platform/auth/mfa/setup", "", `{"challengeToken":"`+ch.ChallengeToken+`"}`)
		uri = decode[setupOut](t, rec).OtpauthURI
		rec = s.do(http.MethodPost, console, "/platform/auth/mfa/setup/confirm", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now())))
		want(t, rec, http.StatusOK, "")
		return decode[tokenBody](t, rec).AccessToken
	}
	ctok := colleagueTok()
	if a := act(id, "disable"); a == nil || a.Status != "disabled" {
		t.Fatalf("disable = %+v", a)
	}
	want(t, s.do(http.MethodGet, console, "/platform/me", ctok, ""), http.StatusUnauthorized, "unauthenticated")
	var live int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM sessions WHERE platform_user_id = $1::uuid AND revoked_at IS NULL`, id).Scan(&live); err != nil || live != 0 {
		t.Fatalf("disabling must end every session: %d left (%v)", live, err)
	}
	want(t, s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`), http.StatusUnauthorized, "invalid_credentials")
	want(t, s.do(http.MethodPost, console, "/platform/users/"+id+"/disable", tok, ""), http.StatusConflict, "invalid_state")
	if a := act(id, "enable"); a == nil || a.Status != "active" {
		t.Fatalf("enable = %+v", a)
	}

	// resetting the authenticator ends their sessions and forces setup again
	rec := s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`)
	want(t, rec, http.StatusAccepted, "")
	ch := decode[challengeOut](t, rec)
	rec = s.do(http.MethodPost, console, "/platform/auth/mfa/verify", "", codeBody(ch.ChallengeToken, totpAt(t, uri, time.Now().Add(30*time.Second))))
	want(t, rec, http.StatusOK, "")
	ctok = decode[tokenBody](t, rec).AccessToken
	if act(id, "reset-mfa") == nil {
		t.Fatal("reset-mfa refused")
	}
	want(t, s.do(http.MethodGet, console, "/platform/me", ctok, ""), http.StatusUnauthorized, "unauthenticated")
	rec = s.do(http.MethodPost, console, "/platform/auth/login", "", `{"email":"`+colleague+`","password":"`+password+`"}`)
	if decode[challengeOut](t, rec).MfaRequired != "setup" {
		t.Fatalf("after reset-mfa: %s", rec.Body)
	}

	// a password reset gives a new one-time link
	a := act(id, "reset-password")
	if a == nil || !strings.HasPrefix(a.Link, "/accept-invite?token=") {
		t.Fatalf("reset-password = %+v", a)
	}
	want(t, s.do(http.MethodPost, console, "/platform/users/00000000-0000-0000-0000-000000000000/disable", tok, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodPost, console, "/platform/users/"+id+"/delete", tok, ""), http.StatusNotFound, "not_found")
}

// The last active platform user can never be disabled, even when two operators act at once: the check runs under a
// lock, so the second one sees the first one's change.
func TestTheLastActivePlatformUserStays(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)
	id := s.inviteColleague(t, tok)
	var me string
	if err := s.adm.QueryRow(bg, `SELECT id::text FROM platform_users WHERE email = $1`, platformEmail).Scan(&me); err != nil {
		t.Fatal(err)
	}
	svc := platformusers.Service{DB: s.adm}
	// operator "me" disables the colleague, then the colleague (whose request was already authenticated) tries to
	// disable "me": that would leave nobody, so it is refused
	if _, err := svc.Disable(bg, audit.Actor{UserID: me}, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Disable(bg, audit.Actor{UserID: id}, me); !errors.Is(err, platformusers.ErrLastActive) {
		t.Fatalf("err = %v, want ErrLastActive", err)
	}
}
