package importer

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// Job kinds handled by Service.Handlers.
const (
	JobValidate = "import.validate"
	JobCommit   = "import.commit"
	JobRollback = "import.rollback"
)

// ErrNotFound is returned when the batch does not exist in this institute.
var ErrNotFound = errors.New("importer: batch not found")

// ConflictError means the request is not allowed in the batch's current state. The API answers 409.
type ConflictError struct{ Message string }

func (e ConflictError) Error() string { return "importer: " + e.Message }

// Batch is an upload and its progress.
type Batch struct {
	ID               string     `json:"id"`
	Entity           string     `json:"entity"`
	Strategy         string     `json:"strategy"`
	CommitMode       string     `json:"commitMode"`
	Status           string     `json:"status"`
	FileName         string     `json:"fileName"`
	TemplateVersion  int        `json:"templateVersion"`
	TotalRows        int        `json:"totalRows"`
	ValidRows        int        `json:"validRows"`
	WarningRows      int        `json:"warningRows"`
	ErrorRows        int        `json:"errorRows"`
	CommittedRows    int        `json:"committedRows"`
	RevertedRows     int        `json:"revertedRows"`
	ProgressDone     int        `json:"progressDone"`
	ProgressTotal    int        `json:"progressTotal"`
	Failure          *string    `json:"failure"`
	CreatedBy        *string    `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	ValidatedAt      *time.Time `json:"validatedAt"`
	CommittedAt      *time.Time `json:"committedAt"`
	RollbackDeadline *time.Time `json:"rollbackDeadline"`
	RolledBackAt     *time.Time `json:"rolledBackAt"`
}

const batchColumns = `id::text, entity, strategy, commit_mode, status, file_name, template_version, total_rows, valid_rows, warning_rows, error_rows,
	committed_rows, reverted_rows, progress_done, progress_total, failure, created_by::text, created_at, validated_at, committed_at, rollback_deadline, rolled_back_at`

func scanBatch(r pgx.Row) (Batch, error) {
	var b Batch
	err := r.Scan(&b.ID, &b.Entity, &b.Strategy, &b.CommitMode, &b.Status, &b.FileName, &b.TemplateVersion, &b.TotalRows, &b.ValidRows, &b.WarningRows, &b.ErrorRows,
		&b.CommittedRows, &b.RevertedRows, &b.ProgressDone, &b.ProgressTotal, &b.Failure, &b.CreatedBy, &b.CreatedAt, &b.ValidatedAt, &b.CommittedAt, &b.RollbackDeadline, &b.RolledBackAt)
	return b, err
}

// Service runs imports.
type Service struct {
	DB  tenancy.Beginner
	Reg *Registry
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// UploadInput is a file to import.
type UploadInput struct {
	Entity, Strategy, CommitMode, FileName string
	Data                                   []byte
}

// Upload reads and stages a file and queues its validation. Nothing is kept of the original file, only the parsed rows.
// File-level problems come back as crud.FieldErrors on "file", "entity", "strategy" or "commitMode".
func (s Service) Upload(ctx context.Context, actor audit.Actor, in UploadInput) (Batch, error) {
	var fe crud.FieldErrors
	ent, ok := s.Reg.Get(in.Entity)
	if !ok {
		fe.Add("entity", "unknown_entity", "This kind of data cannot be imported.")
	}
	if in.Strategy == "" {
		in.Strategy = "upsert"
	}
	if in.CommitMode == "" {
		in.CommitMode = "all_or_nothing"
	}
	fe.Choice("strategy", in.Strategy, "insert", "update", "upsert")
	fe.Choice("commitMode", in.CommitMode, "all_or_nothing", "skip_invalid")
	if err := fe.OrNil(); err != nil {
		return Batch{}, err
	}

	parsed, err := ParseFile(in.FileName, in.Data)
	if err != nil {
		var f *FileError
		if errors.As(err, &f) {
			fe.Add("file", f.Code, f.Message)
			return Batch{}, fe
		}
		return Batch{}, err
	}
	keys, unknown, err := MapHeader(parsed.Header, ent.Columns())
	if err != nil {
		return Batch{}, err
	}

	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Batch{}, tenancy.ErrNoTenant
	}
	var out Batch
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO import_batches (tenant_id, entity, strategy, commit_mode, file_name, template_version, total_rows, progress_total, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7, NULLIF($8, '')::uuid, $9) RETURNING id::text`,
			t.ID, in.Entity, in.Strategy, in.CommitMode, trimName(in.FileName), ent.TemplateVersion(), len(parsed.Rows), actor.UserID, s.now().Add(Retention)).Scan(&id); err != nil {
			return err
		}
		nos := make([]int32, len(parsed.Rows))
		raws := make([]string, len(parsed.Rows))
		for i, cells := range parsed.Rows {
			raw := Row{}
			for pos, k := range keys {
				if k == "" || pos >= len(cells) {
					continue
				}
				if v := strings.TrimSpace(cells[pos]); v != "" {
					raw[k] = v
				}
			}
			b, _ := json.Marshal(raw)
			nos[i], raws[i] = int32(parsed.Lines[i]), string(b)
		}
		// One INSERT from arrays rather than COPY: COPY FROM is not supported on tables with row-level security.
		if _, err := tx.Exec(ctx, `INSERT INTO import_rows (tenant_id, batch_id, row_no, raw)
			SELECT $1, $2::uuid, u.no, u.raw::jsonb FROM unnest($3::int[], $4::text[]) AS u(no, raw)`, t.ID, id, nos, raws); err != nil {
			return err
		}
		for _, h := range unknown {
			if _, err := tx.Exec(ctx, `INSERT INTO import_errors (tenant_id, batch_id, row_no, column_key, severity, code, message) VALUES ($1, $2, 0, $3, 'warning', 'unknown_column', $4)`,
				t.ID, id, h, fmt.Sprintf("The column %q is not used and was ignored.", h)); err != nil {
				return err
			}
		}
		if err := jobs.Enqueue(ctx, tx, JobValidate, jobPayload{BatchID: id, UserID: actor.UserID, Source: actor.Source, RequestID: actor.RequestID}, time.Time{}); err != nil {
			return err
		}
		out, err = scanBatch(tx.QueryRow(ctx, `SELECT `+batchColumns+` FROM import_batches WHERE id = $1::uuid`, id))
		return err
	})
	return out, err
}

func trimName(n string) string {
	n = strings.TrimSpace(n)
	if i := strings.LastIndexAny(n, `/\`); i >= 0 {
		n = n[i+1:]
	}
	if len([]rune(n)) > 200 {
		n = string([]rune(n)[:200])
	}
	return n
}

type jobPayload struct {
	BatchID   string `json:"batchId"`
	UserID    string `json:"userId"`
	Source    string `json:"source"`
	RequestID string `json:"requestId"`
}

func (p jobPayload) actor() audit.Actor {
	return audit.Actor{UserID: p.UserID, Source: p.Source, RequestID: p.RequestID, ImportBatch: p.BatchID}
}

// Get returns one batch.
func (s Service) Get(ctx context.Context, id string) (Batch, error) {
	var b Batch
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		b, err = getBatch(ctx, tx, id)
		return err
	})
	return b, err
}

func getBatch(ctx context.Context, tx pgx.Tx, id string) (Batch, error) {
	b, err := scanBatch(tx.QueryRow(ctx, `SELECT `+batchColumns+` FROM import_batches WHERE id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ListFilter narrows a list of batches. Entities limits it to the kinds the caller may see; nil means all.
type ListFilter struct {
	Status   string
	Entity   string
	Entities []string
}

// BatchSorts are the sort keys of a batch list.
var BatchSorts = crud.Sorts{"createdAt": "created_at", "status": "status", "entity": "entity"}

// List returns one page of batches, newest first by default.
func (s Service) List(ctx context.Context, q crud.ListQuery, f ListFilter) (crud.Page[Batch], error) {
	page := crud.Page[Batch]{Items: []Batch{}, Page: q.Page, Size: q.Size}
	where, args := "TRUE", []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.Replace(cond, "?", fmt.Sprintf("$%d", len(args)), 1)
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.Entity != "" {
		add("entity = ?", f.Entity)
	}
	if f.Entities != nil {
		add("entity = ANY(?)", f.Entities)
	}
	args = append(args, q.Size, q.Offset())
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s, count(*) OVER() FROM import_batches WHERE %s ORDER BY %s, id LIMIT $%d OFFSET $%d`,
			batchColumns, where, q.OrderBy, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Batch
			if err := rows.Scan(&b.ID, &b.Entity, &b.Strategy, &b.CommitMode, &b.Status, &b.FileName, &b.TemplateVersion, &b.TotalRows, &b.ValidRows, &b.WarningRows, &b.ErrorRows,
				&b.CommittedRows, &b.RevertedRows, &b.ProgressDone, &b.ProgressTotal, &b.Failure, &b.CreatedBy, &b.CreatedAt, &b.ValidatedAt, &b.CommittedAt, &b.RollbackDeadline, &b.RolledBackAt, &page.Total); err != nil {
				return err
			}
			page.Items = append(page.Items, b)
		}
		return rows.Err()
	})
	return page, err
}

// RowView is a staged row with what was found about it.
type RowView struct {
	RowNo    int       `json:"rowNo"`
	Status   string    `json:"status"`
	Action   *string   `json:"action"`
	Cells    Row       `json:"cells"`
	Problems []Problem `json:"problems"`
}

// RowSorts are the sort keys of a row list.
var RowSorts = crud.Sorts{"rowNo": "row_no"}

// Rows returns one page of a batch's rows, optionally only those with one status, with their problems.
func (s Service) Rows(ctx context.Context, id, status string, q crud.ListQuery) (crud.Page[RowView], error) {
	page := crud.Page[RowView]{Items: []RowView{}, Page: q.Page, Size: q.Size}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getBatch(ctx, tx, id); err != nil {
			return err
		}
		args := []any{id}
		where := "batch_id::text = $1"
		if status != "" {
			args = append(args, status)
			where += " AND status = $2"
		}
		args = append(args, q.Size, q.Offset())
		order := q.OrderBy
		if order == "" {
			order = "row_no"
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT row_no, status, action, raw, count(*) OVER() FROM import_rows WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
			where, order, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		byNo := map[int]int{}
		for rows.Next() {
			var rv RowView
			var raw []byte
			if err := rows.Scan(&rv.RowNo, &rv.Status, &rv.Action, &raw, &page.Total); err != nil {
				rows.Close()
				return err
			}
			_ = json.Unmarshal(raw, &rv.Cells)
			rv.Problems = []Problem{}
			byNo[rv.RowNo] = len(page.Items)
			page.Items = append(page.Items, rv)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(page.Items) == 0 {
			return err
		}
		nos := make([]int, 0, len(byNo))
		for n := range byNo {
			nos = append(nos, n)
		}
		pr, err := tx.Query(ctx, `SELECT row_no, COALESCE(column_key, ''), code, message, severity FROM import_errors WHERE batch_id::text = $1 AND row_no = ANY($2) ORDER BY id`, id, nos)
		if err != nil {
			return err
		}
		defer pr.Close()
		for pr.Next() {
			var n int
			var p Problem
			if err := pr.Scan(&n, &p.Column, &p.Code, &p.Message, &p.Severity); err != nil {
				return err
			}
			i := byNo[n]
			page.Items[i].Problems = append(page.Items[i].Problems, p)
		}
		return pr.Err()
	})
	return page, err
}

// FileProblems returns the problems that concern the file as a whole (row 0), such as ignored columns.
func (s Service) FileProblems(ctx context.Context, id string) ([]Problem, error) {
	out := []Problem{}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getBatch(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT COALESCE(column_key, ''), code, message, severity FROM import_errors WHERE batch_id::text = $1 AND row_no = 0 ORDER BY id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Problem
			if err := rows.Scan(&p.Column, &p.Code, &p.Message, &p.Severity); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// Template returns the CSV template of an entity: a header row with the column labels, readable by Excel.
func (s Service) Template(entity string) ([]byte, error) {
	ent, ok := s.Reg.Get(entity)
	if !ok {
		return nil, ErrNotFound
	}
	labels := make([]string, len(ent.Columns()))
	for i, c := range ent.Columns() {
		labels[i] = c.Label
	}
	var buf bytes.Buffer
	buf.WriteString("\xef\xbb\xbf")
	w := csv.NewWriter(&buf)
	_ = w.Write(labels)
	w.Flush()
	return buf.Bytes(), w.Error()
}

// ErrorsCSV returns the rows that failed, in the template's columns plus a "Problems" column, so the file can be
// corrected and uploaded again (the extra column is ignored on upload).
func (s Service) ErrorsCSV(ctx context.Context, id string) ([]byte, error) {
	var out []byte
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := getBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		ent, ok := s.Reg.Get(b.Entity)
		if !ok {
			return ErrNotFound
		}
		cols := ent.Columns()
		var buf bytes.Buffer
		buf.WriteString("\xef\xbb\xbf")
		w := csv.NewWriter(&buf)
		head := make([]string, 0, len(cols)+1)
		for _, c := range cols {
			head = append(head, c.Label)
		}
		_ = w.Write(append(head, "Problems"))

		rows, err := tx.Query(ctx, `
			SELECT r.row_no, r.raw, COALESCE((SELECT string_agg(e.message, ' | ' ORDER BY e.id) FROM import_errors e
			                                  WHERE e.batch_id = r.batch_id AND e.row_no = r.row_no AND e.severity = 'error'), '')
			FROM import_rows r WHERE r.batch_id::text = $1 AND r.status = 'error' ORDER BY r.row_no`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var no int
			var raw []byte
			var problems string
			if err := rows.Scan(&no, &raw, &problems); err != nil {
				return err
			}
			cells := Row{}
			_ = json.Unmarshal(raw, &cells)
			rec := make([]string, 0, len(cols)+1)
			for _, c := range cols {
				rec = append(rec, SafeCell(cells[c.Key]))
			}
			_ = w.Write(append(rec, SafeCell(problems)))
		}
		if err := rows.Err(); err != nil {
			return err
		}
		w.Flush()
		out = buf.Bytes()
		return w.Error()
	})
	return out, err
}

// SafeCell stops a spreadsheet from running a cell as a formula (CSV injection): a leading = + @ or a minus that is not a
// number gets a quote in front.
func SafeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '@', '\t', '\r':
		return "'" + s
	case '-':
		if len(s) > 1 && (s[1] < '0' || s[1] > '9') && s[1] != '.' {
			return "'" + s
		}
	}
	return s
}

// Commit queues the commit of a validated batch.
func (s Service) Commit(ctx context.Context, actor audit.Actor, id string) (Batch, error) {
	var out Batch
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := getBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case b.Status != "validated":
			return ConflictError{"Only a validated file can be imported. Its status is " + b.Status + "."}
		case b.ValidRows+b.WarningRows == 0:
			return ConflictError{"No row is ready to import."}
		case b.CommitMode == "all_or_nothing" && b.ErrorRows > 0:
			return ConflictError{fmt.Sprintf("%d rows have errors. Fix them and upload again, or upload with “skip invalid rows”.", b.ErrorRows)}
		}
		tag, err := tx.Exec(ctx, `UPDATE import_batches SET status = 'committing', progress_done = 0, progress_total = valid_rows + warning_rows, failure = NULL WHERE id = $1::uuid AND status = 'validated'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ConflictError{"This file is already being imported."}
		}
		if err := jobs.Enqueue(ctx, tx, JobCommit, jobPayload{BatchID: id, UserID: actor.UserID, Source: actor.Source, RequestID: actor.RequestID}, time.Time{}); err != nil {
			return err
		}
		out, err = getBatch(ctx, tx, id)
		return err
	})
	return out, err
}

// Rollback queues the undo of a committed batch, within the rollback window.
func (s Service) Rollback(ctx context.Context, actor audit.Actor, id string) (Batch, error) {
	var out Batch
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		b, err := getBatch(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case b.Status != "committed":
			return ConflictError{"Only an imported file can be rolled back. Its status is " + b.Status + "."}
		case b.RollbackDeadline == nil || s.now().After(*b.RollbackDeadline):
			return ConflictError{"The rollback window for this file has ended."}
		}
		tag, err := tx.Exec(ctx, `UPDATE import_batches SET status = 'rolling_back', progress_done = 0, progress_total = committed_rows WHERE id = $1::uuid AND status = 'committed'`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ConflictError{"This file is already being rolled back."}
		}
		if err := jobs.Enqueue(ctx, tx, JobRollback, jobPayload{BatchID: id, UserID: actor.UserID, Source: actor.Source, RequestID: actor.RequestID}, time.Time{}); err != nil {
			return err
		}
		out, err = getBatch(ctx, tx, id)
		return err
	})
	return out, err
}

// PurgeExpired deletes batches (and their staged rows) past their retention. Run it from a scheduler; it works across
// institutes, so it needs a platform-tier connection (tenancy.TierPlatform).
func PurgeExpired(ctx context.Context, db tenancy.Beginner, now time.Time) (int64, error) {
	var n int64
	err := tenancy.WithTx(ctx, db, "", tenancy.TierPlatform, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM import_batches WHERE expires_at < $1`, now)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// Handlers returns the job handlers to register with a jobs.Worker.
func (s Service) Handlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{
		JobValidate: s.runValidate,
		JobCommit:   s.runCommit,
		JobRollback: s.runRollback,
	}
}
