// Package jobs is a small Postgres-backed background job queue (FND-18, minimum). Jobs are enqueued inside the
// caller's transaction, so a job exists if and only if the change that needs it was committed. Workers claim jobs with
// FOR UPDATE SKIP LOCKED, so any number of workers can run side by side without running a job twice at once.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Job is a claimed job as a handler sees it.
type Job struct {
	ID       string
	TenantID string
	Kind     string
	Payload  json.RawMessage
	Attempt  int // 1 on the first run
}

// Handler runs one job. The context carries the job's institute (tenancy.FromContext). A returned error retries the
// job with backoff until MaxAttempts, then marks it failed. Never put personal data in an error message: it is stored.
type Handler func(ctx context.Context, j Job) error

// Enqueue adds a job inside tx, which must be bound to an institute (tenancy.WithTenantTx). runAfter zero means now.
func Enqueue(ctx context.Context, tx pgx.Tx, kind string, payload any, runAfter time.Time) error {
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return tenancy.ErrNoTenant
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	_, err = tx.Exec(ctx, `INSERT INTO jobs (tenant_id, kind, payload, run_after) VALUES ($1, $2, $3, $4)`, t.ID, kind, b, runAfter)
	return err
}

// EnqueuePlatform adds a job that belongs to no institute (platform work, such as an email to a platform user). tx
// must be a platform-tier transaction; row-level security refuses the row otherwise.
func EnqueuePlatform(ctx context.Context, tx pgx.Tx, kind string, payload any, runAfter time.Time) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if runAfter.IsZero() {
		runAfter = time.Now()
	}
	_, err = tx.Exec(ctx, `INSERT INTO jobs (tenant_id, kind, payload, run_after) VALUES (NULL, $1, $2, $3)`, kind, b, runAfter)
	return err
}

// ErrPermanent marks a failure that retrying cannot fix (for example a mail server refusing the address). A handler
// returns an error wrapping it (Permanent) and the job is marked failed at once.
var ErrPermanent = errors.New("permanent failure")

// Permanent wraps err so the worker does not retry the job.
func Permanent(err error) error { return fmt.Errorf("%w: %w", ErrPermanent, err) }

// Worker claims and runs jobs.
type Worker struct {
	DB       tenancy.Beginner
	Handlers map[string]Handler
	// LockTimeout is how long a running job may hold its lock before it is considered abandoned (a crashed worker)
	// and requeued. Default 15 minutes; it must exceed the longest legitimate job.
	LockTimeout time.Duration
	// Backoff is the wait before retry n (1-based). Default 30s, 2m, 10m...
	Backoff func(attempt int) time.Duration
	Now     func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) lockTimeout() time.Duration {
	if w.LockTimeout > 0 {
		return w.LockTimeout
	}
	return 15 * time.Minute
}

func (w *Worker) backoff(attempt int) time.Duration {
	if w.Backoff != nil {
		return w.Backoff(attempt)
	}
	d := 30 * time.Second
	for i := 1; i < attempt && d < time.Hour; i++ {
		d *= 4
	}
	return d
}

// RunOnce claims and runs at most one job. It reports whether it ran one.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	var j Job
	var max int
	claimed := false
	err := tenancy.WithTx(ctx, w.DB, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		// Requeue jobs whose worker vanished.
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status = 'queued', locked_at = NULL WHERE status = 'running' AND locked_at < $1`, w.now().Add(-w.lockTimeout())); err != nil {
			return err
		}
		var tenant *string
		err := tx.QueryRow(ctx, `
			SELECT id::text, tenant_id::text, kind, payload, attempts + 1, max_attempts
			FROM jobs WHERE status = 'queued' AND run_after <= $1
			ORDER BY run_after, created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, w.now()).Scan(&j.ID, &tenant, &j.Kind, &j.Payload, &j.Attempt, &max)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if tenant != nil {
			j.TenantID = *tenant
		}
		claimed = true
		_, err = tx.Exec(ctx, `UPDATE jobs SET status = 'running', locked_at = $2, attempts = attempts + 1 WHERE id = $1::uuid`, j.ID, w.now())
		return err
	})
	if err != nil || !claimed {
		return false, err
	}

	runErr := w.run(ctx, j)

	return true, tenancy.WithTx(ctx, w.DB, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		switch {
		case runErr == nil:
			_, err := tx.Exec(ctx, `UPDATE jobs SET status = 'done', locked_at = NULL, last_error = NULL, finished_at = $2 WHERE id = $1::uuid`, j.ID, w.now())
			return err
		case j.Attempt >= max || errors.Is(runErr, ErrPermanent):
			_, err := tx.Exec(ctx, `UPDATE jobs SET status = 'failed', locked_at = NULL, last_error = $3, finished_at = $2 WHERE id = $1::uuid`, j.ID, w.now(), truncate(runErr.Error()))
			return err
		default:
			_, err := tx.Exec(ctx, `UPDATE jobs SET status = 'queued', locked_at = NULL, last_error = $3, run_after = $2 WHERE id = $1::uuid`, j.ID, w.now().Add(w.backoff(j.Attempt)), truncate(runErr.Error()))
			return err
		}
	})
}

func (w *Worker) run(ctx context.Context, j Job) (err error) {
	h, ok := w.Handlers[j.Kind]
	if !ok {
		return fmt.Errorf("no handler for job kind %q", j.Kind)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("job panicked: %v", r)
		}
	}()
	if j.TenantID != "" {
		ctx = tenancy.WithTenant(ctx, tenancy.Tenant{ID: j.TenantID})
	}
	return h(ctx, j)
}

// Run processes jobs until ctx is cancelled, sleeping poll between empty checks.
func (w *Worker) Run(ctx context.Context, poll time.Duration, onError func(error)) {
	for ctx.Err() == nil {
		ran, err := w.RunOnce(ctx)
		if err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		if ran && err == nil {
			continue // more may be waiting
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// Retention of finished jobs. Failed jobs are kept longer so a failure can still be looked at.
const (
	KeepDone   = 7 * 24 * time.Hour
	KeepFailed = 30 * 24 * time.Hour
)

// PurgeFinished deletes jobs that finished before the cut-off for their status (done: now-KeepDone, failed:
// now-KeepFailed) and returns how many went. Queued and running jobs are never touched. It works across institutes, so
// it needs a platform-tier connection.
func PurgeFinished(ctx context.Context, db tenancy.Beginner, now time.Time) (int64, error) {
	var n int64
	err := tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM jobs WHERE (status = 'done' AND finished_at < $1) OR (status = 'failed' AND finished_at < $2)`,
			now.Add(-KeepDone), now.Add(-KeepFailed))
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
