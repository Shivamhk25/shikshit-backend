package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/server"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMaintenancePurgesExpiredBatchesAndOldJobsThenStopsWithItsContext(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	defer adm.Close(bg)
	var tenant string
	if err := adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ('alpha', 'Alpha') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	batch := func(expires time.Time) {
		if _, err := adm.Exec(bg, `INSERT INTO import_batches (tenant_id, entity, strategy, commit_mode, file_name, template_version, expires_at) VALUES ($1, 'rooms', 'upsert', 'all_or_nothing', 'x.csv', 1, $2)`, tenant, expires); err != nil {
			t.Fatal(err)
		}
	}
	batch(now.Add(-time.Hour)) // expired
	batch(now.Add(time.Hour))  // still kept
	if _, err := adm.Exec(bg, `INSERT INTO jobs (tenant_id, kind, status, finished_at) VALUES ($1, 'x', 'done', $2), ($1, 'x', 'done', $3)`, tenant, now.Add(-jobs.KeepDone-time.Hour), now); err != nil {
		t.Fatal(err)
	}

	cfg, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	var errs []error
	go func() {
		server.RunMaintenance(ctx, pool, nil, 20*time.Millisecond, time.Now, func(err error) { errs = append(errs, err) })
		close(done)
	}()
	count := func(q string) (n int) {
		_ = adm.QueryRow(bg, q).Scan(&n)
		return n
	}
	deadline := time.Now().Add(5 * time.Second)
	for count(`SELECT count(*) FROM import_batches`) != 1 || count(`SELECT count(*) FROM jobs`) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("not purged: batches=%d jobs=%d", count(`SELECT count(*) FROM import_batches`), count(`SELECT count(*) FROM jobs`))
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("maintenance did not stop when its context ended")
	}
	if len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
	// the batch that had not expired is still there
	if count(`SELECT count(*) FROM import_batches WHERE expires_at > now()`) != 1 {
		t.Fatal("a live batch was purged")
	}
}

// objects is a store that records what was removed.
type objects struct {
	mu      sync.Mutex
	removed []string
}

func (o *objects) PresignPut(context.Context, string, string, time.Duration) (string, map[string]string, error) {
	return "", nil, nil
}
func (o *objects) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}
func (o *objects) Head(context.Context, string, int64) (int64, []byte, error) {
	return 0, nil, storage.ErrNotFound
}
func (o *objects) Remove(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.removed = append(o.removed, key)
	return nil
}

func TestMaintenanceRemovesUnconfirmedUploadsWhenStorageIsConfigured(t *testing.T) {
	d := dbtest.New(t)
	adm := d.Admin(t)
	defer adm.Close(bg)
	var tenant string
	if err := adm.QueryRow(bg, `INSERT INTO tenants (code, name) VALUES ('alpha', 'Alpha') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		key string
		age time.Duration
	}{{"old", 3 * time.Hour}, {"young", time.Minute}} {
		if _, err := adm.Exec(bg, `INSERT INTO files (tenant_id, purpose, subject_id, object_key, content_type, size_bytes, created_at)
			VALUES ($1, 'student_photo', gen_random_uuid(), $2::text, 'image/png', 10, now() - $3::interval)`, tenant, c.key, c.age.String()); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := &objects{}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		server.RunMaintenance(ctx, pool, store, time.Hour, time.Now, nil)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		_ = adm.QueryRow(bg, `SELECT count(*) FROM files WHERE status = 'deleted'`).Scan(&n)
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the old unconfirmed upload was not cleaned up")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.removed) != 1 || store.removed[0] != "old" {
		t.Fatalf("removed = %v", store.removed)
	}
}
