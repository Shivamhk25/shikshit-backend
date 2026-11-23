package files

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/storage"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Defaults for CleanupPending.
const (
	// PendingMaxAge is how long an upload may stay unconfirmed. It must stay well above UploadTTL (10 minutes) and the
	// confirm window (UploadTTL plus a minute), so a client that is still allowed to upload or confirm is never cut off.
	PendingMaxAge = time.Hour
	// CleanupBatch is how many files one query and one round of storage removals handle.
	CleanupBatch = 100
	// CleanupMaxPerRun caps the files looked at in one run; the rest are taken by the next run.
	CleanupMaxPerRun = 1000
)

// CleanupResult says what a run did.
type CleanupResult struct {
	Removed int // rows marked deleted (their objects are gone from storage)
	Failed  int // objects that could not be removed; the rows stay pending for the next run
}

// CleanupPending removes uploads that were never confirmed: files still `pending` after olderThan (PendingMaxAge when
// zero). For each it removes the object from storage (an object that is not there counts as removed) and then marks the
// row `deleted`, the same status a removed photo gets. A storage error on one object leaves that row pending, so the
// next run tries again, and does not stop the others. At most maxFiles are looked at per run (CleanupMaxPerRun when
// zero), in batches of batch (CleanupBatch when zero), oldest first.
//
// files has no platform-wide policy on purpose: the platform tier still needs an institute context to see institute
// data. So the run lists institutes with a platform-tier transaction and then works inside each one as that institute.
// No migration is needed. Several instances may run it at once: removing is idempotent and the update only touches
// rows that are still pending.
func CleanupPending(ctx context.Context, db tenancy.Beginner, store storage.Store, now time.Time, olderThan time.Duration, batch, maxFiles int) (CleanupResult, error) {
	if olderThan <= 0 {
		olderThan = PendingMaxAge
	}
	if batch <= 0 {
		batch = CleanupBatch
	}
	if maxFiles <= 0 {
		maxFiles = CleanupMaxPerRun
	}
	cutoff := now.Add(-olderThan)

	var tenants []string
	err := tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			tenants = append(tenants, id)
		}
		return rows.Err()
	})
	if err != nil {
		return CleanupResult{}, err
	}

	var res CleanupResult
	left := maxFiles
	for _, tenant := range tenants {
		if left <= 0 {
			break
		}
		r, seen, err := cleanupTenant(ctx, db, store, tenant, cutoff, batch, left)
		res.Removed += r.Removed
		res.Failed += r.Failed
		left -= seen
		if err != nil {
			return res, err
		}
	}
	return res, nil
}

type pendingFile struct{ id, key string }

// cleanupTenant works through one institute's stale pending files, oldest first, and returns what it did and how many
// files it looked at. It pages by (created_at, id) so a file that cannot be removed is passed over, not retried in a
// loop within the same run.
func cleanupTenant(ctx context.Context, db tenancy.Beginner, store storage.Store, tenant string, cutoff time.Time, batch, limit int) (CleanupResult, int, error) {
	var (
		res    CleanupResult
		seen   int
		lastAt time.Time
		lastID = "00000000-0000-0000-0000-000000000000"
	)
	for seen < limit {
		n := batch
		if limit-seen < n {
			n = limit - seen
		}
		var (
			list []pendingFile
			at   []time.Time
		)
		err := tenancy.WithTx(ctx, db, tenant, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text, object_key, created_at FROM files
				WHERE status = 'pending' AND created_at < $1 AND (created_at, id) > ($2, $3::uuid)
				ORDER BY created_at, id LIMIT $4`, cutoff, lastAt, lastID, n)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var f pendingFile
				var c time.Time
				if err := rows.Scan(&f.id, &f.key, &c); err != nil {
					return err
				}
				list, at = append(list, f), append(at, c)
			}
			return rows.Err()
		})
		if err != nil {
			return res, seen, err
		}
		if len(list) == 0 {
			break
		}
		seen += len(list)
		lastAt, lastID = at[len(at)-1], list[len(list)-1].id

		var done []string
		for _, f := range list {
			if err := store.Remove(ctx, f.key); err != nil && !errors.Is(err, storage.ErrNotFound) {
				res.Failed++
				continue
			}
			done = append(done, f.id)
		}
		if len(done) > 0 {
			err := tenancy.WithTx(ctx, db, tenant, tenancy.TierTenant, func(ctx context.Context, tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, `UPDATE files SET status = 'deleted', deleted_at = now() WHERE id = ANY($1::uuid[]) AND status = 'pending'`, done)
				res.Removed += int(tag.RowsAffected())
				return err
			})
			if err != nil {
				return res, seen, err
			}
		}
		if len(list) < n {
			break
		}
	}
	return res, seen, nil
}
