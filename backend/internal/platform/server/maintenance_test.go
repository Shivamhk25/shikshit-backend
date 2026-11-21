package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/platform/server"
	"github.com/pie360/backend/internal/shared/dbtest"
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
		server.RunMaintenance(ctx, pool, 20*time.Millisecond, time.Now, func(err error) { errs = append(errs, err) })
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
