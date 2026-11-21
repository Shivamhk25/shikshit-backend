package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// finalAttempt is the attempt after which a failing job gives up (the queue default is 3). The batch is then marked
// failed so it does not sit "validating" forever.
const finalAttempt = 3

var errDryRun = errors.New("importer: dry run")

type stagedRow struct {
	No    int
	Cells Row
}

type rowResult struct {
	No       int
	Status   string
	Action   string
	Problems []Problem
}

func (s Service) load(ctx context.Context, j jobs.Job) (jobPayload, Batch, Entity, error) {
	var p jobPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil {
		return p, Batch{}, nil, err
	}
	var b Batch
	if err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		b, err = getBatch(ctx, tx, p.BatchID)
		return err
	}); err != nil {
		return p, b, nil, err
	}
	ent, ok := s.Reg.Get(b.Entity)
	if !ok {
		return p, b, nil, fmt.Errorf("unknown entity %q", b.Entity)
	}
	return p, b, ent, nil
}

// fail records a terminal failure on the batch (without personal data).
func (s Service) fail(ctx context.Context, id, message string) {
	_ = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE import_batches SET status = 'failed', failure = $2 WHERE id = $1::uuid`, id, message)
		return err
	})
}

func (s Service) setProgress(ctx context.Context, id string, done int) {
	_ = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE import_batches SET progress_done = $2 WHERE id = $1::uuid`, id, done)
		return err
	})
}

func (s Service) loadRows(ctx context.Context, id string, statuses []string, desc bool) ([]stagedRow, error) {
	var out []stagedRow
	order := "ASC"
	if desc {
		order = "DESC"
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT row_no, raw FROM import_rows WHERE batch_id = $1::uuid AND status = ANY($2) ORDER BY row_no `+order, id, statuses)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r stagedRow
			var raw []byte
			if err := rows.Scan(&r.No, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Cells); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ---- validation

func (s Service) runValidate(ctx context.Context, j jobs.Job) error {
	p, b, ent, err := s.load(ctx, j)
	if err != nil {
		if j.Attempt >= finalAttempt && p.BatchID != "" {
			s.fail(ctx, p.BatchID, "The file could not be checked.")
		}
		return err
	}
	if b.Status != "uploaded" && b.Status != "validating" {
		return nil // already handled (a retried job)
	}
	if err := s.validate(ctx, p, b, ent); err != nil {
		if j.Attempt >= finalAttempt {
			s.fail(ctx, b.ID, "The file could not be checked.")
		}
		return err
	}
	return nil
}

func (s Service) validate(ctx context.Context, p jobPayload, b Batch, ent Entity) error {
	if err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE import_batches SET status = 'validating', progress_done = 0 WHERE id = $1::uuid`, b.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM import_errors WHERE batch_id = $1::uuid AND row_no > 0`, b.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_rows SET status = 'pending', action = NULL WHERE batch_id = $1::uuid`, b.ID)
		return err
	}); err != nil {
		return err
	}
	rows, err := s.loadRows(ctx, b.ID, []string{"pending"}, false)
	if err != nil {
		return err
	}

	actor := p.actor()
	results := make([]rowResult, 0, len(rows))
	// The whole check runs in one transaction that is always rolled back: Check tries each write in a savepoint, so
	// nothing is ever kept, and rows that depend on earlier rows of the file are judged against the real data.
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		seen := map[string]int{}
		for i, r := range rows {
			v, err := ent.Check(ctx, tx, actor, r.Cells, b.Strategy)
			if err != nil {
				return err
			}
			if v.OK() && v.Key != "" {
				if first, dup := seen[v.Key]; dup {
					v.Problems = append(v.Problems, Problem{Column: v.KeyColumn, Code: "duplicate_in_file", Message: fmt.Sprintf("This row repeats row %d.", first), Severity: "error"})
				} else {
					seen[v.Key] = r.No
				}
			}
			status := "valid"
			switch {
			case !v.OK():
				status = "error"
			case len(v.Problems) > 0:
				status = "warning"
			}
			results = append(results, rowResult{No: r.No, Status: status, Action: v.Action, Problems: v.Problems})
			if (i+1)%200 == 0 {
				s.setProgress(ctx, b.ID, i+1)
			}
		}
		return errDryRun
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return err
	}

	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		t, _ := tenancy.FromContext(ctx)
		nos, statuses, actions := make([]int32, len(results)), make([]string, len(results)), make([]string, len(results))
		var valid, warn, bad int
		var errNos []int32
		var errCols, errSev, errCodes, errMsgs []string
		for i, r := range results {
			nos[i], statuses[i], actions[i] = int32(r.No), r.Status, r.Action
			switch r.Status {
			case "valid":
				valid++
			case "warning":
				warn++
			default:
				bad++
			}
			for _, pr := range r.Problems {
				errNos, errCols, errSev, errCodes, errMsgs = append(errNos, int32(r.No)), append(errCols, pr.Column), append(errSev, severity(pr)), append(errCodes, pr.Code), append(errMsgs, pr.Message)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE import_rows r SET status = u.status, action = NULLIF(u.action, '')
			FROM unnest($2::int[], $3::text[], $4::text[]) AS u(no, status, action) WHERE r.batch_id = $1::uuid AND r.row_no = u.no`, b.ID, nos, statuses, actions); err != nil {
			return err
		}
		if len(errNos) > 0 {
			// INSERT from arrays: COPY FROM is not supported on tables with row-level security.
			if _, err := tx.Exec(ctx, `INSERT INTO import_errors (tenant_id, batch_id, row_no, column_key, severity, code, message)
				SELECT $1, $2::uuid, u.no, NULLIF(u.col, ''), u.sev, u.code, u.msg
				FROM unnest($3::int[], $4::text[], $5::text[], $6::text[], $7::text[]) AS u(no, col, sev, code, msg)`,
				t.ID, b.ID, errNos, errCols, errSev, errCodes, errMsgs); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE import_batches SET status = 'validated', valid_rows = $2, warning_rows = $3, error_rows = $4, progress_done = progress_total, validated_at = $5 WHERE id = $1::uuid`,
			b.ID, valid, warn, bad, s.now())
		return err
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func severity(p Problem) string {
	if p.Severity == "warning" {
		return "warning"
	}
	return "error"
}

// ---- commit

type rowFailure struct {
	No       int
	Problems []Problem
}

func (e *rowFailure) Error() string { return fmt.Sprintf("row %d rejected", e.No) }

type applied struct {
	No int
	Applied
}

func (s Service) runCommit(ctx context.Context, j jobs.Job) error {
	p, b, ent, err := s.load(ctx, j)
	if err != nil {
		if j.Attempt >= finalAttempt && p.BatchID != "" {
			s.fail(ctx, p.BatchID, "The import could not be run.")
		}
		return err
	}
	if b.Status != "committing" {
		return nil // already finished (a retried job)
	}
	if err := s.commit(ctx, p, b, ent); err != nil {
		if j.Attempt >= finalAttempt {
			s.fail(ctx, b.ID, "The import could not be completed.")
		}
		return err
	}
	return nil
}

func (s Service) commit(ctx context.Context, p jobPayload, b Batch, ent Entity) error {
	rows, err := s.loadRows(ctx, b.ID, []string{"valid", "warning"}, false)
	if err != nil {
		return err
	}
	actor := p.actor()

	if b.CommitMode == "all_or_nothing" {
		err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
			done := make([]applied, 0, len(rows))
			for _, r := range rows {
				a, err := ent.Apply(ctx, tx, actor, r.Cells, b.Strategy)
				if err != nil {
					var re *RowError
					if errors.As(err, &re) {
						return &rowFailure{No: r.No, Problems: re.Problems}
					}
					return err
				}
				done = append(done, applied{r.No, a})
			}
			if err := markCommitted(ctx, tx, b.ID, done); err != nil {
				return err
			}
			return s.finishCommit(ctx, tx, b.ID)
		})
		var rf *rowFailure
		if errors.As(err, &rf) {
			// Nothing was written. Put the batch back to "validated" with the reason so the file can be fixed.
			return s.rejectRow(ctx, b.ID, rf)
		}
		return err
	}

	// skip_invalid: each row in its own transaction, so one bad row never blocks the others.
	for i, r := range rows {
		err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
			a, err := ent.Apply(ctx, tx, actor, r.Cells, b.Strategy)
			if err != nil {
				return err
			}
			return markCommitted(ctx, tx, b.ID, []applied{{r.No, a}})
		})
		var re *RowError
		if errors.As(err, &re) {
			if err := s.markRowError(ctx, b.ID, r.No, re.Problems); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if (i+1)%100 == 0 {
			s.setProgress(ctx, b.ID, i+1)
		}
	}
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error { return s.finishCommit(ctx, tx, b.ID) })
}

func markCommitted(ctx context.Context, tx pgx.Tx, batchID string, done []applied) error {
	n := len(done)
	nos, actions, ids, befores, stamps := make([]int32, n), make([]string, n), make([]string, n), make([]string, n), make([]time.Time, n)
	for i, d := range done {
		nos[i], actions[i], ids[i], stamps[i] = int32(d.No), d.Action, d.RecordID, d.AfterUpdatedAt
		if d.Before != nil {
			befores[i] = string(d.Before)
		}
	}
	_, err := tx.Exec(ctx, `UPDATE import_rows r SET status = 'committed', action = u.action, record_id = u.id::uuid, before_snapshot = NULLIF(u.before, '')::jsonb, after_updated_at = u.stamp
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::timestamptz[]) AS u(no, action, id, before, stamp)
		WHERE r.batch_id = $1::uuid AND r.row_no = u.no`, batchID, nos, actions, ids, befores, stamps)
	return err
}

func (s Service) finishCommit(ctx context.Context, tx pgx.Tx, id string) error {
	now := s.now()
	_, err := tx.Exec(ctx, `UPDATE import_batches b SET status = 'committed', committed_at = $2, rollback_deadline = $3, failure = NULL,
		committed_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'committed'),
		error_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'error'),
		progress_done = progress_total WHERE b.id = $1::uuid`, id, now, now.Add(RollbackWindow))
	return err
}

func (s Service) rejectRow(ctx context.Context, id string, rf *rowFailure) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := addRowProblems(ctx, tx, id, rf.No, rf.Problems); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE import_rows SET status = 'error' WHERE batch_id = $1::uuid AND row_no = $2`, id, rf.No); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_batches b SET status = 'validated', progress_done = 0,
			valid_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'valid'),
			warning_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'warning'),
			error_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'error'),
			failure = $2 WHERE b.id = $1::uuid`, id, fmt.Sprintf("Nothing was imported: row %d could not be saved. Fix it and upload the file again.", rf.No))
		return err
	})
}

func (s Service) markRowError(ctx context.Context, id string, no int, ps []Problem) error {
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := addRowProblems(ctx, tx, id, no, ps); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE import_rows SET status = 'error' WHERE batch_id = $1::uuid AND row_no = $2`, id, no)
		return err
	})
}

func addRowProblems(ctx context.Context, tx pgx.Tx, id string, no int, ps []Problem) error {
	t, _ := tenancy.FromContext(ctx)
	for _, p := range ps {
		if _, err := tx.Exec(ctx, `INSERT INTO import_errors (tenant_id, batch_id, row_no, column_key, severity, code, message) VALUES ($1, $2::uuid, $3, $4, 'error', $5, $6)`,
			t.ID, id, no, nullIfEmpty(p.Column), p.Code, p.Message); err != nil {
			return err
		}
	}
	return nil
}

// ---- rollback

func (s Service) runRollback(ctx context.Context, j jobs.Job) error {
	p, b, ent, err := s.load(ctx, j)
	if err != nil {
		if j.Attempt >= finalAttempt && p.BatchID != "" {
			s.fail(ctx, p.BatchID, "The rollback could not be run.")
		}
		return err
	}
	if b.Status != "rolling_back" {
		return nil
	}
	if err := s.rollback(ctx, p, b, ent); err != nil {
		if j.Attempt >= finalAttempt {
			s.fail(ctx, b.ID, "The rollback could not be completed.")
		}
		return err
	}
	return nil
}

func (s Service) rollback(ctx context.Context, p jobPayload, b Batch, ent Entity) error {
	type rec struct {
		No     int
		Action string
		ID     string
		Before json.RawMessage
		Stamp  time.Time
	}
	var recs []rec
	if err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT row_no, action, record_id::text, before_snapshot, after_updated_at FROM import_rows
			WHERE batch_id = $1::uuid AND status = 'committed' ORDER BY row_no DESC`, b.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.No, &r.Action, &r.ID, &r.Before, &r.Stamp); err != nil {
				return err
			}
			recs = append(recs, r)
		}
		return rows.Err()
	}); err != nil {
		return err
	}

	actor := p.actor()
	for i, r := range recs {
		err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
			reason, err := ent.Revert(ctx, tx, actor, RevertInput{Action: r.Action, RecordID: r.ID, Before: r.Before, AfterUpdatedAt: r.Stamp})
			if err != nil {
				return err
			}
			if reason != "" {
				if err := addRowProblems(ctx, tx, b.ID, r.No, []Problem{{Code: "revert_failed", Message: reason}}); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `UPDATE import_rows SET status = 'revert_failed' WHERE batch_id = $1::uuid AND row_no = $2`, b.ID, r.No)
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE import_rows SET status = 'reverted' WHERE batch_id = $1::uuid AND row_no = $2`, b.ID, r.No)
			return err
		})
		if err != nil {
			return err
		}
		if (i+1)%100 == 0 {
			s.setProgress(ctx, b.ID, i+1)
		}
	}
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE import_batches b SET status = 'rolled_back', rolled_back_at = $2, progress_done = progress_total,
			reverted_rows = (SELECT count(*) FROM import_rows WHERE batch_id = b.id AND status = 'reverted') WHERE b.id = $1::uuid`, b.ID, s.now())
		return err
	})
}

var _ = audit.Actor{}
