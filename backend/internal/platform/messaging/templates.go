package messaging

import (
	"bytes"
	"context"
	"errors"
	htmltemplate "html/template"
	"strconv"
	"strings"
	texttemplate "text/template"

	"github.com/jackc/pgx/v5"
)

// AccountLink is an email carrying a one-time set-password link: an invitation to a new account or a password reset
// that an administrator started.
type AccountLink struct {
	To       string
	Name     string // the person's display name
	Place    string // the institute's name, or "the Shikshit console"
	Username string // shown for school accounts, which sign in with a username; empty for the console
	Link     string // absolute address
	Hours    int    // how long the link works
	Invite   bool   // true: a new account; false: a password reset
}

// validity says how long a link lasts in words.
func validity(hours int) string {
	if hours%24 == 0 && hours >= 48 {
		return strconv.Itoa(hours/24) + " days"
	}
	if hours == 24 {
		return "24 hours"
	}
	return strconv.Itoa(hours) + " hours"
}

type accountView struct {
	AccountLink
	Validity string
	Heading  string
	Lead     string
	Action   string
}

var accountText = texttemplate.Must(texttemplate.New("text").Parse(`Hello {{.Name}},

{{.Lead}}
{{if .Username}}
Your username: {{.Username}}
{{end}}
{{.Action}}:
{{.Link}}

The link works once and expires in {{.Validity}}. If you were not expecting this email, you can ignore it; nothing
changes until the link is used.

Shikshit
`))

var accountHTML = htmltemplate.Must(htmltemplate.New("html").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:0;background:#f4f2ee;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f2ee;padding:32px 12px;">
<tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:520px;background:#ffffff;border-radius:12px;border:1px solid #e4e0d8;">
<tr><td style="padding:32px 32px 8px;font-family:Georgia,'Times New Roman',serif;font-size:22px;color:#1f2a37;">{{.Heading}}</td></tr>
<tr><td style="padding:8px 32px;font-family:Helvetica,Arial,sans-serif;font-size:15px;line-height:1.6;color:#374151;">
<p style="margin:0 0 12px;">Hello {{.Name}},</p>
<p style="margin:0 0 12px;">{{.Lead}}</p>
{{if .Username}}<p style="margin:0 0 12px;">Your username: <strong>{{.Username}}</strong></p>{{end}}
</td></tr>
<tr><td style="padding:8px 32px 16px;">
<a href="{{.Link}}" style="display:inline-block;background:#1f3a5f;color:#ffffff;text-decoration:none;font-family:Helvetica,Arial,sans-serif;font-size:15px;font-weight:bold;padding:12px 22px;border-radius:8px;">{{.Action}}</a>
</td></tr>
<tr><td style="padding:0 32px 32px;font-family:Helvetica,Arial,sans-serif;font-size:13px;line-height:1.6;color:#6b7280;">
<p style="margin:0 0 8px;">The link works once and expires in {{.Validity}}. If you were not expecting this email, you can ignore it; nothing changes until the link is used.</p>
<p style="margin:0;word-break:break-all;">If the button does not work, open this address: {{.Link}}</p>
</td></tr>
</table>
<p style="font-family:Helvetica,Arial,sans-serif;font-size:12px;color:#9ca3af;margin:16px 0 0;">Shikshit</p>
</td></tr></table>
</body></html>
`))

// Render builds the email.
func (a AccountLink) Render() (Email, error) {
	v := accountView{AccountLink: a, Validity: validity(a.Hours)}
	var subject string
	if a.Invite {
		subject = "Your account at " + a.Place
		v.Heading = "Welcome to " + a.Place
		v.Lead = "An account has been created for you at " + a.Place + " on Shikshit. Choose your password to start."
		v.Action = "Set your password"
	} else {
		subject = "Set a new password for " + a.Place
		v.Heading = "Set a new password"
		v.Lead = "Your administrator started a password reset for your account at " + a.Place + ". You have been signed out everywhere; choose a new password to sign in again."
		v.Action = "Choose a new password"
	}
	var text, html bytes.Buffer
	if err := accountText.Execute(&text, v); err != nil {
		return Email{}, err
	}
	if err := accountHTML.Execute(&html, v); err != nil {
		return Email{}, err
	}
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	e := Email{To: a.To, ToName: oneLine(a.Name), Subject: oneLine(subject), Text: text.String(), HTML: html.String()}
	return e, e.validate()
}

// QueueAccountLink renders a and queues it in tx. It reports false, and queues nothing, when email is not configured
// or the person has no usable address; the administrator still has the link to share by hand.
func (o *Outbox) QueueAccountLink(ctx context.Context, tx pgx.Tx, a AccountLink) (bool, error) {
	if o == nil || strings.TrimSpace(a.To) == "" {
		return false, nil
	}
	e, err := a.Render()
	if errors.Is(err, ErrInvalidEmail) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return o.Queue(ctx, tx, e)
}
