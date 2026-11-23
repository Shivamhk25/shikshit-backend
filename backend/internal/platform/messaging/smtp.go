package messaging

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
)

// TLS modes for the connection to the mail server.
const (
	TLSStartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587); refuse servers that cannot
	TLSImplicit = "tls"      // TLS from the first byte (port 465)
	TLSNone     = "none"     // no encryption: development only (a local test mail server)
)

// SMTPConfig is the mail server the worker sends through. Any provider that offers SMTP works (Amazon SES, Zoho,
// Postmark, a school's own server).
type SMTPConfig struct {
	Host     string
	Port     int
	Username string // empty: no authentication (development)
	Password string
	From     string // the sender address, for example no-reply@shikshit.app
	FromName string
	TLS      string // TLSStartTLS, TLSImplicit or TLSNone
	// TLSConfig overrides the TLS settings (tests trust their own certificate with it). nil uses the system roots.
	TLSConfig *tls.Config
	// Timeout covers the whole conversation with the server. Default 30 seconds.
	Timeout time.Duration
}

// Validate checks the configuration.
func (c SMTPConfig) Validate() error {
	if c.Host == "" {
		return errors.New("the mail server host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("the mail server port must be between 1 and 65535")
	}
	if a, err := mail.ParseAddress(c.From); err != nil || a.Address != c.From {
		return errors.New("the sender address must be a plain email address")
	}
	switch c.TLS {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return errors.New("the mail TLS mode must be starttls, tls or none")
	}
	if c.TLS == TLSNone && c.Username != "" {
		return errors.New("a mail password is never sent without TLS")
	}
	return nil
}

// Sender delivers one email.
type Sender interface {
	Send(ctx context.Context, e Email) error
}

// SMTPSender sends through an SMTP server.
type SMTPSender struct {
	Config SMTPConfig
	Now    func() time.Time
}

// Send opens a connection, sends the message and closes it. A refusal by the server (a 5xx reply) is permanent and
// is not retried. Errors carry the server's reply code only: its text can repeat the recipient's address, and job
// errors are stored.
func (s SMTPSender) Send(ctx context.Context, e Email) error {
	c := s.Config
	if err := c.Validate(); err != nil {
		return jobs.Permanent(err)
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	msg, err := e.build(mail.Address{Name: c.FromName, Address: c.From}, now())
	if err != nil {
		return jobs.Permanent(err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tlsConf := c.TLSConfig
	if tlsConf == nil {
		tlsConf = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsConf = tlsConf.Clone()
	tlsConf.ServerName = c.Host

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	var conn net.Conn
	if c.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return errors.New("smtp: could not connect to the mail server")
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return smtpError("greeting", err)
	}
	defer client.Close()

	if c.TLS == TLSStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("smtp: the mail server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConf); err != nil {
			return errors.New("smtp: TLS with the mail server failed")
		}
	}
	if c.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			// Wrong credentials (535) are permanent: retrying would only lock the account. 454 is retried.
			return smtpError("authentication", err)
		}
	}
	if err := client.Mail(c.From); err != nil {
		return smtpError("sender", err)
	}
	if err := client.Rcpt(e.To); err != nil {
		return smtpError("recipient", err)
	}
	w, err := client.Data()
	if err != nil {
		return smtpError("data", err)
	}
	if _, err := w.Write(msg); err != nil {
		return smtpError("data", err)
	}
	if err := w.Close(); err != nil {
		return smtpError("data", err)
	}
	_ = client.Quit()
	return nil
}

// smtpError keeps the server's reply code and drops its text. A 5xx reply is permanent.
func smtpError(step string, err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) {
		e := fmt.Errorf("smtp: %s refused (%d)", step, tp.Code)
		if tp.Code >= 500 {
			return jobs.Permanent(e)
		}
		return e
	}
	return fmt.Errorf("smtp: %s failed", step)
}
