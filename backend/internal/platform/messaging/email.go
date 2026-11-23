// Package messaging sends messages to people (FND-19, decision 0021). Email is the first channel: messages are
// queued in the caller's transaction as background jobs (so an email exists if and only if the change that needs it
// was committed), sealed so the database never holds a usable link in clear text, and sent over SMTP by the worker.
// SMS and WhatsApp will plug into the same queue.
package messaging

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Email is one message to one person.
type Email struct {
	To      string `json:"to"`
	ToName  string `json:"toName,omitempty"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html,omitempty"`
}

// ErrInvalidEmail is returned for a message that cannot be sent as it is (bad address, empty subject or body).
var ErrInvalidEmail = errors.New("messaging: invalid email")

// validate checks the message. The subject and names must be single lines: a line break there would let someone
// add their own mail headers.
func (e Email) validate() error {
	addr, err := mail.ParseAddress(e.To)
	if err != nil || addr.Address != e.To || len(e.To) > 254 {
		return fmt.Errorf("%w: recipient address", ErrInvalidEmail)
	}
	if strings.TrimSpace(e.Subject) == "" || strings.TrimSpace(e.Text) == "" {
		return fmt.Errorf("%w: subject and text are required", ErrInvalidEmail)
	}
	if strings.ContainsAny(e.Subject+e.ToName, "\r\n") {
		return fmt.Errorf("%w: subject and name must be one line", ErrInvalidEmail)
	}
	return nil
}

// build renders the message as RFC 5322 bytes: plain text and, when present, HTML as alternatives.
func (e Email) build(from mail.Address, now time.Time) ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	domain := "shikshit.local"
	if at := strings.LastIndexByte(from.Address, '@'); at >= 0 {
		domain = from.Address[at+1:]
	}
	to := mail.Address{Name: e.ToName, Address: e.To}

	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.QEncoding.Encode("utf-8", e.Subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+id+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")

	if e.HTML == "" {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, e.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	boundary, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ kind, body string }{{"text/plain", e.Text}, {"text/html", e.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + part.kind + "; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
		if err := writeQP(&b, part.body); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func writeQP(b *bytes.Buffer, s string) error {
	w := quotedprintable.NewWriter(b)
	if _, err := w.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return err
	}
	return w.Close()
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
