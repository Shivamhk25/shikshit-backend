package apiv1_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/messaging"
)

// recordingSender keeps the emails the worker would have sent.
type recordingSender struct {
	mu   sync.Mutex
	sent []messaging.Email
}

func (r *recordingSender) Send(_ context.Context, e messaging.Email) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, e)
	return nil
}

// deliver runs the worker until the queue is empty and returns what it sent.
func (s usersStack) deliver(t *testing.T) []messaging.Email {
	t.Helper()
	rec := &recordingSender{}
	w := &jobs.Worker{DB: s.app, Handlers: s.mail.Handlers(rec)}
	for {
		ran, err := w.RunOnce(bg)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			break
		}
	}
	var failed int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM jobs WHERE kind = $1 AND status <> 'done'`, messaging.JobEmail).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("%d email jobs not done", failed)
	}
	return rec.sent
}

// emailJobs returns the institute (empty for none) of each queued email job and fails if a payload shows a token.
func (s usersStack) emailJobs(t *testing.T, secrets ...string) []string {
	t.Helper()
	rows, err := s.adm.Query(bg, `SELECT coalesce(tenant_id::text, ''), payload::text FROM jobs WHERE kind = $1 ORDER BY created_at`, messaging.JobEmail)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tenant, payload string
		if err := rows.Scan(&tenant, &payload); err != nil {
			t.Fatal(err)
		}
		for _, secret := range secrets {
			if strings.Contains(payload, secret) {
				t.Fatalf("a queued email stores %q in clear text", secret)
			}
		}
		out = append(out, tenant)
	}
	return out
}

type emailedUser struct {
	ID          string `json:"id"`
	InviteLink  string `json:"inviteLink"`
	ResetLink   string `json:"resetLink"`
	EmailQueued bool   `json:"emailQueued"`
}

func TestSchoolInviteAndResetLinksAreEmailed(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)

	rec := s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"asha","displayName":"Asha Rao","email":"asha@alpha.test"}`)
	want(t, rec, http.StatusCreated, "")
	invited := decode[emailedUser](t, rec)
	if !invited.EmailQueued || invited.InviteLink == "" {
		t.Fatalf("invite %+v", invited)
	}
	// no address: nothing is queued and the admin shares the link
	rec = s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"ravi","displayName":"Ravi"}`)
	want(t, rec, http.StatusCreated, "")
	if decode[emailedUser](t, rec).EmailQueued {
		t.Fatal("queued an email for a user without an address")
	}
	// a failed create queues nothing (the email is in the same transaction)
	want(t, s.do(http.MethodPost, alphaHost, "/users", admin, `{"username":"asha","displayName":"Again","email":"again@alpha.test"}`), http.StatusBadRequest, "")

	rec = s.do(http.MethodPost, alphaHost, "/users/"+invited.ID+"/reset-password", admin, "")
	want(t, rec, http.StatusOK, "")
	reset := decode[emailedUser](t, rec)
	if !reset.EmailQueued {
		t.Fatal("reset not emailed")
	}

	if got := s.emailJobs(t, tokenOf(invited.InviteLink), tokenOf(reset.ResetLink), "asha@alpha.test"); len(got) != 2 || got[0] != s.tenant["alpha"] || got[1] != s.tenant["alpha"] {
		t.Fatalf("email jobs %v", got)
	}
	sent := s.deliver(t)
	if len(sent) != 2 {
		t.Fatalf("sent %d", len(sent))
	}
	for i, link := range []string{invited.InviteLink, reset.ResetLink} {
		e := sent[i]
		if e.To != "asha@alpha.test" || !strings.Contains(e.Text, "https://alpha.shikshit.test"+link) || !strings.Contains(e.Text, "asha") {
			t.Fatalf("email %d: to %s\n%s", i, e.To, e.Text)
		}
	}
	if !strings.Contains(sent[0].Subject, "Your account") || !strings.Contains(sent[1].Subject, "new password") {
		t.Fatalf("subjects %q %q", sent[0].Subject, sent[1].Subject)
	}
	// the emailed link is the one that works
	want(t, s.do(http.MethodPost, alphaHost, "/auth/invite/accept", "", accept(tokenOf(reset.ResetLink), "a-new-long-password-1")), http.StatusOK, "")
}

func TestPlatformInvitesAndTheFirstAdminAreEmailed(t *testing.T) {
	s := newUsersStack(t)
	tok := s.platformToken(t)

	rec := s.do(http.MethodPost, console, "/platform/users", tok, `{"email":"meera@shikshit.test","name":"Meera Iyer"}`)
	want(t, rec, http.StatusCreated, "")
	colleague := decode[struct {
		Link        string `json:"link"`
		EmailQueued *bool  `json:"emailQueued"`
	}](t, rec)
	if colleague.EmailQueued == nil || !*colleague.EmailQueued {
		t.Fatalf("platform invite %+v", colleague)
	}

	rec = s.do(http.MethodPost, console, "/platform/institutes", tok, newInstituteBody("Gamma", "standard", "meena"))
	want(t, rec, http.StatusCreated, "")
	inst := decode[struct {
		AdminInviteLink  string `json:"adminInviteLink"`
		AdminEmailQueued bool   `json:"adminEmailQueued"`
	}](t, rec)
	if !inst.AdminEmailQueued {
		t.Fatal("first Admin not emailed")
	}

	if got := s.emailJobs(t, linkToken(colleague.Link), tokenOf(inst.AdminInviteLink)); len(got) != 2 || got[0] != "" || got[1] != "" {
		t.Fatalf("email jobs %v (platform emails belong to no institute)", got)
	}
	sent := s.deliver(t)
	if len(sent) != 2 {
		t.Fatalf("sent %d", len(sent))
	}
	if sent[0].To != "meera@shikshit.test" || !strings.Contains(sent[0].Text, "https://console.shikshit.test"+colleague.Link) || !strings.Contains(sent[0].Subject, "Shikshit console") {
		t.Fatalf("platform email %q\n%s", sent[0].Subject, sent[0].Text)
	}
	if sent[1].To != "meena@gamma.test" || !strings.Contains(sent[1].Text, "https://gamma.shikshit.test"+inst.AdminInviteLink) ||
		!strings.Contains(sent[1].Subject, "Gamma Public School") || !strings.Contains(sent[1].Text, "meena") {
		t.Fatalf("first Admin email %q\n%s", sent[1].Subject, sent[1].Text)
	}
}
