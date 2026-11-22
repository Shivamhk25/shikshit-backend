package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strconv"
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
)

var bg = context.Background()

func invite(t *testing.T) Email {
	t.Helper()
	e, err := AccountLink{To: "asha@example.com", Name: "Asha Rao", Place: "Springfield School", Username: "asha.rao",
		Link: "https://springfield.shikshit.app/accept-invite?token=abc123", Hours: 168, Invite: true}.Render()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// parts reads the delivered message and returns its headers and the decoded text and HTML parts.
func parts(t *testing.T, raw string) (*mail.Message, string, string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var text, html string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p) // multipart decodes quoted-printable
		if strings.HasPrefix(p.Header.Get("Content-Type"), "text/html") {
			html = string(b)
		} else {
			text = string(b)
		}
	}
	return m, text, html
}

func TestSendsOverStartTLSWithAuthentication(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.user, f.pass = "mailer", "s3cret" })
	cfg := f.config()
	cfg.Username, cfg.Password = "mailer", "s3cret"
	if err := (SMTPSender{Config: cfg}).Send(bg, invite(t)); err != nil {
		t.Fatal(err)
	}
	got := f.got()
	if len(got) != 1 || !f.sawTLS || !strings.Contains(f.sawAuth, "mailer") {
		t.Fatalf("messages %d, tls %v, auth %q", len(got), f.sawTLS, f.sawAuth)
	}
	m, text, html := parts(t, got[0])
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subject != "Your account at Springfield School" {
		t.Fatalf("subject %q", subject)
	}
	if to := m.Header.Get("To"); !strings.Contains(to, "asha@example.com") {
		t.Fatalf("to %q", to)
	}
	for _, want := range []string{"asha.rao", "https://springfield.shikshit.app/accept-invite?token=abc123", "7 days"} {
		if !strings.Contains(text, want) || !strings.Contains(html, want) {
			t.Fatalf("%q missing from the message", want)
		}
	}
	if !strings.Contains(f.rcptTo, "asha@example.com") || !strings.Contains(f.mailFrom, "no-reply@shikshit.test") {
		t.Fatalf("envelope %q %q", f.mailFrom, f.rcptTo)
	}
}

func TestSendsOverImplicitTLS(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.implicit = true })
	if err := (SMTPSender{Config: f.config()}).Send(bg, invite(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.got()) != 1 {
		t.Fatal("not delivered")
	}
}

func TestRefusesAServerWithoutStartTLS(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.starttls = false })
	err := (SMTPSender{Config: f.config()}).Send(bg, invite(t))
	if err == nil || len(f.got()) != 0 || f.mailFrom != "" {
		t.Fatalf("sent without TLS: %v", err)
	}
	if errors.Is(err, jobs.ErrPermanent) {
		t.Fatal("a server without TLS today may have it tomorrow; the job should retry")
	}
}

func TestAnUntrustedCertificateIsRefused(t *testing.T) {
	f := newFakeSMTP(t, nil)
	cfg := f.config()
	cfg.TLSConfig = nil // system roots do not include the test certificate
	if err := (SMTPSender{Config: cfg}).Send(bg, invite(t)); err == nil || len(f.got()) != 0 {
		t.Fatalf("sent to an untrusted server: %v", err)
	}
}

func TestServerRefusalsKeepTheCodeOnly(t *testing.T) {
	for _, tc := range []struct {
		code      int
		permanent bool
	}{{550, true}, {451, false}} {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.rcptCode = tc.code })
		err := (SMTPSender{Config: f.config()}).Send(bg, invite(t))
		if err == nil {
			t.Fatalf("%d: no error", tc.code)
		}
		if errors.Is(err, jobs.ErrPermanent) != tc.permanent {
			t.Fatalf("%d: permanent=%v", tc.code, !tc.permanent)
		}
		if strings.Contains(err.Error(), "asha") || !strings.Contains(err.Error(), "("+strconv.Itoa(tc.code)+")") {
			t.Fatalf("%d: error %q", tc.code, err)
		}
	}
}

func TestWrongCredentialsArePermanent(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.user, f.pass = "mailer", "right" })
	cfg := f.config()
	cfg.Username, cfg.Password = "mailer", "wrong"
	err := (SMTPSender{Config: cfg}).Send(bg, invite(t))
	if !errors.Is(err, jobs.ErrPermanent) || strings.Contains(err.Error(), "example.com") || len(f.got()) != 0 {
		t.Fatalf("error %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConfigRules(t *testing.T) {
	ok := SMTPConfig{Host: "smtp.example.com", Port: 587, From: "no-reply@shikshit.app", TLS: TLSStartTLS}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]SMTPConfig{
		"no host":           {Port: 587, From: ok.From, TLS: TLSStartTLS},
		"bad port":          {Host: ok.Host, Port: 0, From: ok.From, TLS: TLSStartTLS},
		"sender with name":  {Host: ok.Host, Port: 587, From: "Shikshit <no-reply@shikshit.app>", TLS: TLSStartTLS},
		"unknown tls":       {Host: ok.Host, Port: 587, From: ok.From, TLS: "maybe"},
		"password in clear": {Host: ok.Host, Port: 25, From: ok.From, TLS: TLSNone, Username: "u", Password: "p"},
	} {
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMessagesCannotCarryExtraHeaders(t *testing.T) {
	base := invite(t)
	for name, mutate := range map[string]func(*Email){
		"subject": func(e *Email) { e.Subject = "Hi\r\nBcc: everyone@example.com" },
		"name":    func(e *Email) { e.ToName = "Asha\nBcc: x@example.com" },
		"address": func(e *Email) { e.To = "asha@example.com\r\nBcc: x@example.com" },
		"empty":   func(e *Email) { e.Text = " " },
	} {
		e := base
		mutate(&e)
		if e.validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Render flattens a name with a line break instead of failing.
	e, err := AccountLink{To: "a@example.com", Name: "Asha\nBcc: x@example.com", Place: "School", Link: "https://x", Hours: 24}.Render()
	if err != nil || strings.ContainsAny(e.ToName, "\r\n") {
		t.Fatalf("%q %v", e.ToName, err)
	}
}

func TestTemplates(t *testing.T) {
	reset, err := AccountLink{To: "a@example.com", Name: "<b>Asha</b>", Place: "the Shikshit console", Link: "https://console.shikshit.app/accept-invite?token=t", Hours: 24}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if reset.Subject != "Set a new password for the Shikshit console" || !strings.Contains(reset.Text, "24 hours") || strings.Contains(reset.Text, "username") {
		t.Fatalf("reset %q\n%s", reset.Subject, reset.Text)
	}
	if strings.Contains(reset.HTML, "<b>Asha</b>") || !strings.Contains(reset.HTML, "&lt;b&gt;Asha&lt;/b&gt;") {
		t.Fatal("the name is not escaped in HTML")
	}
}

func TestSealedQueuedEmailOpensOnlyWithItsKey(t *testing.T) {
	secret := []byte(strings.Repeat("k", 32))
	o, err := NewOutbox(secret, Links{})
	if err != nil {
		t.Fatal(err)
	}
	e := invite(t)
	nonce := make([]byte, o.aead.NonceSize())
	sealed := o.aead.Seal(nonce, nonce, mustJSON(t, e), []byte(JobEmail))
	raw := mustJSON(t, sealedPayload{V: 1, Sealed: sealed})
	if strings.Contains(string(raw), "abc123") || strings.Contains(string(raw), "asha") {
		t.Fatal("the stored payload shows the message")
	}
	got, err := o.open(raw)
	if err != nil || got.To != e.To || got.Text != e.Text {
		t.Fatalf("open: %v", err)
	}
	other, _ := NewOutbox([]byte(strings.Repeat("z", 32)), Links{})
	if _, err := other.open(raw); err == nil {
		t.Fatal("opened with another key")
	}
	// A handler turns an unreadable job into a permanent failure (retrying cannot help).
	err = other.Handlers(SMTPSender{})[JobEmail](bg, jobs.Job{Payload: raw})
	if !errors.Is(err, jobs.ErrPermanent) {
		t.Fatalf("handler error %v", err)
	}
	if _, err := NewOutbox([]byte("short"), Links{}); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestLinks(t *testing.T) {
	prod := Links{BaseDomain: "shikshit.app", DevOrigin: "http://localhost:5173"}
	if got := prod.School("Springfield", "/accept-invite?token=t"); got != "https://springfield.shikshit.app/accept-invite?token=t" {
		t.Fatal(got)
	}
	if got := prod.Console("/accept-invite?token=t"); got != "https://console.shikshit.app/accept-invite?token=t" {
		t.Fatal(got)
	}
	dev := Links{DevOrigin: "http://localhost:5173/"}
	if dev.School("x", "/a") != "http://localhost:5173/a" || dev.Console("/a") != "http://localhost:5173/console/a" {
		t.Fatal(dev.School("x", "/a"), dev.Console("/a"))
	}
}

func TestNilOutboxQueuesNothing(t *testing.T) {
	var o *Outbox
	if ok, err := o.Queue(bg, nil, invite(t)); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}
