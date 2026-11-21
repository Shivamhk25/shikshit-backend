package server

import (
	"context"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/jobs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// RunMaintenance removes data that has outlived its retention: import batches (and their staged rows) past their
// expiry, and finished jobs. It runs once straight away and then every `every` until ctx ends. now is the clock
// (time.Now in production). A failed pass is reported to onError and tried again on the next tick. Several instances
// may run it at once: the deletes are idempotent.
func RunMaintenance(ctx context.Context, db tenancy.Beginner, every time.Duration, now func() time.Time, onError func(error)) {
	pass := func() {
		if _, err := importer.PurgeExpired(ctx, db, now()); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
		if _, err := jobs.PurgeFinished(ctx, db, now()); err != nil && ctx.Err() == nil && onError != nil {
			onError(err)
		}
	}
	pass()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
