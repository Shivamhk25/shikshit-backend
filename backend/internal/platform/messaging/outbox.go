package messaging

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// JobEmail is the job kind of a queued email.
const JobEmail = "messaging.email"

// Outbox queues emails as background jobs. The message is sealed (AES-256-GCM) before it is stored: it may carry a
// one-time sign-in link, and the database must never hold one in clear text (only a token's SHA-256 is stored
// elsewhere). A nil *Outbox means email is not configured; Queue then does nothing and reports false.
type Outbox struct {
	aead  cipher.AEAD
	Links Links
}

// NewOutbox derives the sealing key from secret (at least 32 bytes; the server passes its MFA key) so mail can never
// be opened with the key that protects two-step sign-in secrets, or the other way round.
func NewOutbox(secret []byte, links Links) (*Outbox, error) {
	if len(secret) < 32 {
		return nil, errors.New("messaging: the sealing secret must be at least 32 bytes")
	}
	key := sha256.Sum256(append([]byte("shikshit-mail-v1:"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Outbox{aead: aead, Links: links}, nil
}

type sealedPayload struct {
	V      int    `json:"v"`
	Sealed []byte `json:"sealed"`
}

// Queue adds e to the queue inside tx and reports whether it did. In an institute transaction the job belongs to
// the institute; in a platform transaction it belongs to no institute. The email is only sent if tx commits.
func (o *Outbox) Queue(ctx context.Context, tx pgx.Tx, e Email) (bool, error) {
	if o == nil {
		return false, nil
	}
	if err := e.validate(); err != nil {
		return false, err
	}
	plain, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	nonce := make([]byte, o.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return false, err
	}
	p := sealedPayload{V: 1, Sealed: o.aead.Seal(nonce, nonce, plain, []byte(JobEmail))}
	if _, ok := tenancy.FromContext(ctx); ok {
		return true, jobs.Enqueue(ctx, tx, JobEmail, p, time.Time{})
	}
	return true, jobs.EnqueuePlatform(ctx, tx, JobEmail, p, time.Time{})
}

func (o *Outbox) open(raw json.RawMessage) (Email, error) {
	var p sealedPayload
	var e Email
	if err := json.Unmarshal(raw, &p); err != nil || p.V != 1 || len(p.Sealed) < o.aead.NonceSize() {
		return e, errors.New("messaging: unreadable queued email")
	}
	n := o.aead.NonceSize()
	plain, err := o.aead.Open(nil, p.Sealed[:n], p.Sealed[n:], []byte(JobEmail))
	if err != nil {
		return e, errors.New("messaging: queued email does not open with this key")
	}
	if err := json.Unmarshal(plain, &e); err != nil {
		return e, errors.New("messaging: unreadable queued email")
	}
	return e, nil
}

// Handlers returns the job handler that sends queued emails through sender.
func (o *Outbox) Handlers(sender Sender) map[string]jobs.Handler {
	return map[string]jobs.Handler{JobEmail: func(ctx context.Context, j jobs.Job) error {
		e, err := o.open(j.Payload)
		if err != nil {
			return jobs.Permanent(err)
		}
		return sender.Send(ctx, e)
	}}
}

// Links builds the absolute addresses put in emails. With a base domain an institute is at https://<code>.<base>
// and the console at https://console.<base>; without one (development) both are on DevOrigin, the console under
// /console, as the web app serves them on localhost.
type Links struct {
	BaseDomain string // for example shikshit.app
	DevOrigin  string // for example http://localhost:5173
}

// School returns the address of path (which starts with "/") on the institute with code.
func (l Links) School(code, path string) string {
	if l.BaseDomain != "" {
		return "https://" + strings.ToLower(code) + "." + l.BaseDomain + path
	}
	return strings.TrimRight(l.DevOrigin, "/") + path
}

// Console returns the address of path on the Super Admin console.
func (l Links) Console(path string) string {
	if l.BaseDomain != "" {
		return "https://console." + l.BaseDomain + path
	}
	return strings.TrimRight(l.DevOrigin, "/") + "/console" + path
}
