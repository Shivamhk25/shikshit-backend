package jobs_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

type fixture struct {
	app, admin   *pgxpool.Pool
	alpha, bravo tenancy.Tenant
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	t.Cleanup(func() { _ = adm.Close(bg) })
	mk := func(code string) tenancy.Tenant {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return tenancy.Tenant{ID: id, Code: code, Status: "live"}
	}
	f := fixture{alpha: mk("alpha"), bravo: mk("bravo")}
	pool := func(dsn string) *pgxpool.Pool {
		cfg, _ := pgxpool.ParseConfig(dsn)
		p, err := pgxpool.NewWithConfig(bg, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	f.app, f.admin = pool(d.AppDSN), pool(d.AdminDSN)
	return f
}

func (f fixture) enqueue(t *testing.T, ten tenancy.Tenant, kind string, payload any) {
	t.Helper()
	ctx := tenancy.WithTenant(bg, ten)
	if err := tenancy.WithTenantTx(ctx, f.app, func(ctx context.Context, tx pgx.Tx) error {
		return jobs.Enqueue(ctx, tx, kind, payload, time.Time{})
	}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) status(t *testing.T) (status string, attempts int, lastErr *string) {
	t.Helper()
	if err := f.admin.QueryRow(bg, `SELECT status, attempts, last_error FROM jobs ORDER BY created_at DESC LIMIT 1`).Scan(&status, &attempts, &lastErr); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRunsAJobInItsOwnInstitute(t *testing.T) {
	f := setup(t)
	f.enqueue(t, f.alpha, "echo", map[string]string{"hello": "world"})
	var gotTenant, gotPayload string
	w := &jobs.Worker{DB: f.app, Handlers: map[string]jobs.Handler{"echo": func(ctx context.Context, j jobs.Job) error {
		ten, _ := tenancy.FromContext(ctx)
		gotTenant, gotPayload = ten.ID, string(j.Payload)
		return nil
	}}}
	ran, err := w.RunOnce(bg)
	if err != nil || !ran {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	if gotTenant != f.alpha.ID || gotPayload != `{"hello": "world"}` {
		t.Errorf("tenant %q payload %q", gotTenant, gotPayload)
	}
	if s, a, _ := f.status(t); s != "done" || a != 1 {
		t.Errorf("status %s attempts %d", s, a)
	}
	if ran, _ := w.RunOnce(bg); ran {
		t.Error("an empty queue reported work")
	}
}

func TestFailureRetriesWithBackoffThenFails(t *testing.T) {
	f := setup(t)
	f.enqueue(t, f.alpha, "boom", nil)
	now := time.Now()
	calls := 0
	w := &jobs.Worker{DB: f.app, Now: func() time.Time { return now }, Backoff: func(int) time.Duration { return time.Minute },
		Handlers: map[string]jobs.Handler{"boom": func(context.Context, jobs.Job) error { calls++; return errors.New("kaput") }}}

	for attempt := 1; attempt <= 3; attempt++ {
		if ran, err := w.RunOnce(bg); err != nil || !ran {
			t.Fatalf("attempt %d: ran=%v err=%v", attempt, ran, err)
		}
		s, a, e := f.status(t)
		if attempt < 3 && (s != "queued" || a != attempt || e == nil) {
			t.Fatalf("after attempt %d: %s/%d/%v", attempt, s, a, e)
		}
		if attempt == 3 && s != "failed" {
			t.Fatalf("after the last attempt: %s", s)
		}
		if attempt < 3 {
			if ran, _ := w.RunOnce(bg); ran {
				t.Fatal("a job ran before its backoff elapsed")
			}
			now = now.Add(2 * time.Minute)
		}
	}
	if calls != 3 {
		t.Errorf("handler ran %d times", calls)
	}
	if ran, _ := w.RunOnce(bg); ran {
		t.Error("a failed job ran again")
	}
}

func TestPanicAndUnknownKindFailTheJobWithoutCrashingTheWorker(t *testing.T) {
	f := setup(t)
	f.enqueue(t, f.alpha, "panic", nil)
	f.enqueue(t, f.alpha, "nobody-handles-this", nil)
	w := &jobs.Worker{DB: f.app, Backoff: func(int) time.Duration { return time.Hour },
		Handlers: map[string]jobs.Handler{"panic": func(context.Context, jobs.Job) error { panic("oh no") }}}
	for i := 0; i < 2; i++ {
		if ran, err := w.RunOnce(bg); err != nil || !ran {
			t.Fatalf("ran=%v err=%v", ran, err)
		}
	}
	var n int
	_ = f.admin.QueryRow(bg, `SELECT count(*) FROM jobs WHERE status = 'queued' AND last_error IS NOT NULL`).Scan(&n)
	if n != 2 {
		t.Errorf("%d jobs recorded their error", n)
	}
}

func TestAbandonedRunningJobIsRequeued(t *testing.T) {
	f := setup(t)
	f.enqueue(t, f.alpha, "echo", nil)
	if _, err := f.admin.Exec(bg, `UPDATE jobs SET status = 'running', locked_at = now() - interval '1 hour', attempts = 1`); err != nil {
		t.Fatal(err)
	}
	ran := false
	w := &jobs.Worker{DB: f.app, LockTimeout: 15 * time.Minute, Handlers: map[string]jobs.Handler{"echo": func(context.Context, jobs.Job) error { ran = true; return nil }}}
	if got, err := w.RunOnce(bg); err != nil || !got || !ran {
		t.Fatalf("got=%v ran=%v err=%v", got, ran, err)
	}
	// a job that is running and recently locked is left alone
	f.enqueue(t, f.alpha, "echo", nil)
	if _, err := f.admin.Exec(bg, `UPDATE jobs SET status = 'running', locked_at = now() WHERE status = 'queued'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.RunOnce(bg); got {
		t.Error("a job another worker is running was taken")
	}
}

func TestManyWorkersNeverRunAJobTwice(t *testing.T) {
	f := setup(t)
	const n = 30
	for i := 0; i < n; i++ {
		f.enqueue(t, f.alpha, "count", nil)
	}
	var ran atomic.Int32
	var mu sync.Mutex
	seen := map[string]int{}
	h := map[string]jobs.Handler{"count": func(_ context.Context, j jobs.Job) error {
		ran.Add(1)
		mu.Lock()
		seen[j.ID]++
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return nil
	}}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &jobs.Worker{DB: f.app, Handlers: h}
			for {
				ok, err := w.RunOnce(bg)
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return
				}
			}
		}()
	}
	wg.Wait()
	if ran.Load() != n || len(seen) != n {
		t.Fatalf("ran %d times for %d distinct jobs, want %d", ran.Load(), len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Errorf("job %s ran %d times", id, c)
		}
	}
}

func TestAJobIsOnlyVisibleToItsOwnInstitute(t *testing.T) {
	f := setup(t)
	f.enqueue(t, f.alpha, "echo", nil)
	var n int
	ctx := tenancy.WithTenant(bg, f.bravo)
	_ = tenancy.WithTenantTx(ctx, f.app, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n)
	})
	if n != 0 {
		t.Fatalf("bravo sees %d of alpha's jobs", n)
	}
}

func TestEnqueueRollsBackWithTheCallersTransaction(t *testing.T) {
	f := setup(t)
	ctx := tenancy.WithTenant(bg, f.alpha)
	_ = tenancy.WithTenantTx(ctx, f.app, func(ctx context.Context, tx pgx.Tx) error {
		if err := jobs.Enqueue(ctx, tx, "echo", nil, time.Time{}); err != nil {
			t.Fatal(err)
		}
		return errors.New("the change failed")
	})
	var n int
	_ = f.admin.QueryRow(bg, `SELECT count(*) FROM jobs`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d jobs survived a rolled-back transaction", n)
	}
}
