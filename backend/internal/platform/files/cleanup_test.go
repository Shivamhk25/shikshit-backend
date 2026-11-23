package files_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/files"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

// store is an in-memory object store; keys in fail return an error from Remove.
type store struct {
	mu       sync.Mutex
	objects  map[string]bool
	fail     map[string]bool
	removed  []string
	onRemove func(key string)
}

func (s *store) PresignPut(context.Context, string, string, time.Duration) (string, map[string]string, error) {
	return "", nil, nil
}
func (s *store) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}
func (s *store) Head(context.Context, string, int64) (int64, []byte, error) {
	return 0, nil, storage.ErrNotFound
}
func (s *store) Remove(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[key] {
		return errors.New("storage down")
	}
	if s.onRemove != nil {
		s.onRemove(key)
	}
	delete(s.objects, key)
	s.removed = append(s.removed, key)
	return nil
}

type fixture struct {
	app, admin   *pgxpool.Pool
	alpha, bravo string
	store        *store
	now          time.Time
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.New(t)
	pool := func(dsn string) *pgxpool.Pool {
		cfg, _ := pgxpool.ParseConfig(dsn)
		p, err := pgxpool.NewWithConfig(bg, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	f := fixture{app: pool(d.AppDSN), admin: pool(d.AdminDSN), store: &store{objects: map[string]bool{}, fail: map[string]bool{}}, now: time.Now()}
	mk := func(code string) string {
		var id string
		if err := f.admin.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.alpha, f.bravo = mk("alpha"), mk("bravo")
	return f
}

// file inserts a file row of the given status and age, with an object in the store, and returns its key.
func (f fixture) file(t *testing.T, tenant, status string, age time.Duration) string {
	t.Helper()
	id := uuid.NewString()
	key := "t/" + tenant + "/student_photo/" + id
	if _, err := f.admin.Exec(bg, `INSERT INTO files (id, tenant_id, purpose, subject_id, object_key, content_type, size_bytes, status, created_at)
		VALUES ($1, $2, 'student_photo', gen_random_uuid(), $3, 'image/jpeg', 100, $4, $5)`, id, tenant, key, status, f.now.Add(-age)); err != nil {
		t.Fatal(err)
	}
	f.store.objects[key] = true
	return key
}

func (f fixture) status(t *testing.T, key string) string {
	t.Helper()
	var s string
	if err := f.admin.QueryRow(bg, `SELECT status FROM files WHERE object_key = $1`, key).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f fixture) run(t *testing.T, batch, max int) files.CleanupResult {
	t.Helper()
	r, err := files.CleanupPending(bg, f.app, f.store, f.now, 0, batch, max)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (f fixture) expect(t *testing.T, key, status string, objectKept bool) {
	t.Helper()
	if got := f.status(t, key); got != status {
		t.Errorf("%s: status = %s, want %s", key, got, status)
	}
	if f.store.objects[key] != objectKept {
		t.Errorf("%s: object kept = %v, want %v", key, f.store.objects[key], objectKept)
	}
}

func TestOldPendingUploadsAreRemovedAndYoungOnesKept(t *testing.T) {
	f := setup(t)
	old := f.file(t, f.alpha, "pending", 2*time.Hour)
	young := f.file(t, f.alpha, "pending", 30*time.Minute)
	justUnder := f.file(t, f.alpha, "pending", files.PendingMaxAge-time.Minute)
	r := f.run(t, 0, 0)
	if r.Removed != 1 || r.Failed != 0 {
		t.Fatalf("result = %+v", r)
	}
	f.expect(t, old, "deleted", false)
	f.expect(t, young, "pending", true)
	f.expect(t, justUnder, "pending", true)
	var at *time.Time
	if err := f.admin.QueryRow(bg, `SELECT deleted_at FROM files WHERE object_key = $1`, old).Scan(&at); err != nil || at == nil {
		t.Fatalf("deleted_at = %v, %v", at, err)
	}
}

func TestReadyRejectedAndDeletedFilesAreUntouched(t *testing.T) {
	f := setup(t)
	ready := f.file(t, f.alpha, "ready", 48*time.Hour)
	rejected := f.file(t, f.alpha, "rejected", 48*time.Hour)
	deleted := f.file(t, f.alpha, "deleted", 48*time.Hour)
	if r := f.run(t, 0, 0); r.Removed != 0 {
		t.Fatalf("result = %+v", r)
	}
	f.expect(t, ready, "ready", true)
	f.expect(t, rejected, "rejected", true)
	f.expect(t, deleted, "deleted", true)
	if len(f.store.removed) != 0 {
		t.Fatalf("removed = %v", f.store.removed)
	}
}

func TestAStorageFailureLeavesThatRowPendingAndDoesNotStopTheOthers(t *testing.T) {
	f := setup(t)
	bad := f.file(t, f.alpha, "pending", 3*time.Hour)
	good1 := f.file(t, f.alpha, "pending", 2*time.Hour)
	good2 := f.file(t, f.alpha, "pending", time.Hour+time.Minute)
	f.store.fail[bad] = true
	r := f.run(t, 0, 0)
	if r.Removed != 2 || r.Failed != 1 {
		t.Fatalf("result = %+v", r)
	}
	f.expect(t, bad, "pending", true)
	f.expect(t, good1, "deleted", false)
	f.expect(t, good2, "deleted", false)

	delete(f.store.fail, bad) // storage recovers: the next run finishes the job
	if r := f.run(t, 0, 0); r.Removed != 1 || r.Failed != 0 {
		t.Fatalf("second result = %+v", r)
	}
	f.expect(t, bad, "deleted", false)
}

func TestAnObjectThatIsAlreadyGoneStillClearsTheRow(t *testing.T) {
	f := setup(t)
	key := f.file(t, f.alpha, "pending", 2*time.Hour)
	delete(f.store.objects, key)
	f.store.fail[key] = false
	gone := &missingStore{store: f.store}
	r, err := files.CleanupPending(bg, f.app, gone, f.now, 0, 0, 0)
	if err != nil || r.Removed != 1 || r.Failed != 0 {
		t.Fatalf("result = %+v, %v", r, err)
	}
	if f.status(t, key) != "deleted" {
		t.Fatal("row not deleted")
	}
}

type missingStore struct{ *store }

func (missingStore) Remove(context.Context, string) error {
	return fmt.Errorf("wrapped: %w", storage.ErrNotFound)
}

func TestItWorksAcrossInstitutesAndKeepsEachOnesObjects(t *testing.T) {
	f := setup(t)
	a := f.file(t, f.alpha, "pending", 2*time.Hour)
	b := f.file(t, f.bravo, "pending", 2*time.Hour)
	bYoung := f.file(t, f.bravo, "pending", time.Minute)
	bReady := f.file(t, f.bravo, "ready", 5*time.Hour)
	if r := f.run(t, 0, 0); r.Removed != 2 {
		t.Fatalf("result = %+v", r)
	}
	f.expect(t, a, "deleted", false)
	f.expect(t, b, "deleted", false)
	f.expect(t, bYoung, "pending", true)
	f.expect(t, bReady, "ready", true)
}

func TestTheLimitBoundsARunAndLaterRunsFinishTheBacklog(t *testing.T) {
	f := setup(t)
	var keys []string
	for i := 0; i < 7; i++ {
		keys = append(keys, f.file(t, f.alpha, "pending", time.Duration(20-i)*time.Hour))
	}
	keys = append(keys, f.file(t, f.alpha, "pending", 2*time.Hour))
	// batches of 2, at most 5 files per run
	if r := f.run(t, 2, 5); r.Removed != 5 {
		t.Fatalf("first run = %+v", r)
	}
	for _, k := range keys[:5] { // oldest first
		f.expect(t, k, "deleted", false)
	}
	f.expect(t, keys[5], "pending", true)
	f.expect(t, keys[7], "pending", true)
	if r := f.run(t, 2, 5); r.Removed != 3 {
		t.Fatalf("second run = %+v", r)
	}
	for _, k := range keys {
		f.expect(t, k, "deleted", false)
	}
}

func TestFailedFilesAreSkippedNotRetriedWithinARunSoLaterBatchesStillRun(t *testing.T) {
	f := setup(t)
	var bad, good []string
	for i := 0; i < 3; i++ {
		k := f.file(t, f.alpha, "pending", time.Duration(20-i)*time.Hour)
		f.store.fail[k] = true
		bad = append(bad, k)
	}
	for i := 0; i < 3; i++ {
		good = append(good, f.file(t, f.alpha, "pending", time.Duration(5-i)*time.Hour))
	}
	r := f.run(t, 2, 100)
	if r.Removed != 3 || r.Failed != 3 {
		t.Fatalf("result = %+v", r)
	}
	for _, k := range bad {
		f.expect(t, k, "pending", true)
	}
	for _, k := range good {
		f.expect(t, k, "deleted", false)
	}
}

func TestAFileConfirmedWhileTheRunIsWorkingIsNotMarkedDeleted(t *testing.T) {
	f := setup(t)
	key := f.file(t, f.alpha, "pending", 2*time.Hour)
	f.store.onRemove = func(k string) {
		if _, err := f.admin.Exec(bg, `UPDATE files SET status = 'ready' WHERE object_key = $1`, k); err != nil {
			t.Error(err)
		}
	}
	if r := f.run(t, 0, 0); r.Removed != 0 {
		t.Fatalf("result = %+v", r)
	}
	if got := f.status(t, key); got != "ready" {
		t.Fatalf("status = %s, want ready", got)
	}
}
