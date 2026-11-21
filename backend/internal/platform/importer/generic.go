package importer

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
)

// Importable turns a resource Spec into an importable Entity. T is the stored row, I the service input.
type Importable[T, I any] struct {
	EntityKey, EntityTitle, EntityDomain string
	Version                              int
	Cols                                 []Column

	Store resource.Store[T, I]
	// Build maps a staged row to the service input, reporting bad cells on c.
	Build func(ctx context.Context, tx pgx.Tx, c *Cells) I
	// KeyOf is the natural key of a row (what makes it "the same record"), lower-cased; KeyColumn is the column it
	// comes from. FindWhere is the SQL condition that finds an existing record by it (one $1 parameter).
	KeyOf     func(I) string
	KeyColumn string
	FindWhere string
	// FieldColumn maps a service field name to the file column when they differ (divisionId -> division).
	FieldColumn map[string]string
	ToInput     func(T) I
	ID          func(T) string
	Stamp       func(T) time.Time
}

// Key implements Entity.
func (e Importable[T, I]) Key() string { return e.EntityKey }

// Title implements Entity.
func (e Importable[T, I]) Title() string { return e.EntityTitle }

// Domain implements Entity.
func (e Importable[T, I]) Domain() string { return e.EntityDomain }

// Columns implements Entity.
func (e Importable[T, I]) Columns() []Column { return e.Cols }

// TemplateVersion implements Entity.
func (e Importable[T, I]) TemplateVersion() int { return e.Version }

func hasError(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity != "warning" {
			return true
		}
	}
	return false
}

func (e Importable[T, I]) problemsFrom(err error) ([]Problem, bool) {
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		out := make([]Problem, len(fe))
		for i, f := range fe {
			col := f.Field
			if m, ok := e.FieldColumn[f.Field]; ok {
				col = m
			}
			out[i] = Problem{Column: col, Code: f.Code, Message: f.Message, Severity: "error"}
		}
		return out, true
	}
	var inUse resource.InUseError
	if errors.As(err, &inUse) {
		return []Problem{{Code: "in_use", Message: inUse.Message, Severity: "error"}}, true
	}
	return nil, false
}

// plan builds the input and decides create or update; it returns problems instead of an action when the row cannot
// be applied.
func (e Importable[T, I]) plan(ctx context.Context, tx pgx.Tx, row Row, strategy string) (in I, key, action string, existing T, probs []Problem, err error) {
	c := NewCells(row)
	in = e.Build(ctx, tx, c)
	probs = c.Problems()
	key = e.KeyOf(in)
	if hasError(probs) {
		return
	}
	var found bool
	existing, found, err = e.Store.FindIn(ctx, tx, e.FindWhere, key)
	if err != nil {
		return
	}
	if found && strategy != "insert" {
		in = e.keepBlanks(in, e.ToInput(existing), row)
	}
	switch {
	case found && strategy == "insert":
		probs = append(probs, Problem{Column: e.KeyColumn, Code: "exists", Message: "A record with this " + e.KeyColumn + " already exists. Choose update or upsert to change it.", Severity: "error"})
	case !found && strategy == "update":
		probs = append(probs, Problem{Column: e.KeyColumn, Code: "not_found", Message: "No existing record has this " + e.KeyColumn + ", so there is nothing to update.", Severity: "error"})
	case found:
		action = "update"
	default:
		action = "create"
	}
	return
}

// columnOf names the file column a service field is read from: the field name in camelCase ("DivisionID" becomes
// "divisionId"), unless FieldColumn says it comes from another column (divisionId is read from "division").
func (e Importable[T, I]) columnOf(field string) string {
	name := strings.ToLower(field[:1]) + field[1:]
	if strings.HasSuffix(name, "ID") {
		name = strings.TrimSuffix(name, "ID") + "Id"
	}
	if col, ok := e.FieldColumn[name]; ok {
		return col
	}
	return name
}

// keepBlanks is what makes an update safe: a column that is blank in the row, or left out of the file, keeps the
// record's current value instead of clearing it. Only columns the entity declares are considered, so a service field
// with no column is never touched here. The key column is never blank for a row that found its record.
func (e Importable[T, I]) keepBlanks(in, current I, row Row) I {
	declared := map[string]bool{}
	for _, c := range e.Cols {
		declared[c.Key] = true
	}
	dst, src := reflect.ValueOf(&in).Elem(), reflect.ValueOf(current)
	if dst.Kind() != reflect.Struct {
		return in
	}
	for i := 0; i < dst.NumField(); i++ {
		col := e.columnOf(dst.Type().Field(i).Name)
		if declared[col] && strings.TrimSpace(row[col]) == "" && dst.Field(i).CanSet() {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return in
}

// write performs the create or update inside tx.
func (e Importable[T, I]) write(ctx context.Context, tx pgx.Tx, actor audit.Actor, in I, action string, existing T) (T, error) {
	if action == "update" {
		return e.Store.UpdateIn(ctx, tx, actor, e.ID(existing), in)
	}
	return e.Store.CreateIn(ctx, tx, actor, in)
}

// Check implements Entity. The write is tried inside a savepoint and undone, so every rule the screens enforce
// (unique codes, date overlaps, references) is checked without leaving a trace.
func (e Importable[T, I]) Check(ctx context.Context, tx pgx.Tx, actor audit.Actor, row Row, strategy string) (Verdict, error) {
	in, key, action, existing, probs, err := e.plan(ctx, tx, row, strategy)
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Key: key, KeyColumn: e.KeyColumn, Action: action, Problems: probs}
	if hasError(probs) || action == "" {
		return v, nil
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer func() { _ = sp.Rollback(ctx) }()
	if _, err := e.write(ctx, sp, actor, in, action, existing); err != nil {
		if ps, ok := e.problemsFrom(err); ok {
			v.Problems = append(v.Problems, ps...)
			return v, nil
		}
		return v, err
	}
	return v, nil
}

// Apply implements Entity.
func (e Importable[T, I]) Apply(ctx context.Context, tx pgx.Tx, actor audit.Actor, row Row, strategy string) (Applied, error) {
	in, _, action, existing, probs, err := e.plan(ctx, tx, row, strategy)
	if err != nil {
		return Applied{}, err
	}
	if hasError(probs) {
		return Applied{}, &RowError{Problems: probs}
	}
	var before json.RawMessage
	if action == "update" {
		if before, err = json.Marshal(existing); err != nil {
			return Applied{}, err
		}
	}
	out, err := e.write(ctx, tx, actor, in, action, existing)
	if err != nil {
		if ps, ok := e.problemsFrom(err); ok {
			return Applied{}, &RowError{Problems: ps}
		}
		return Applied{}, err
	}
	return Applied{Action: action, RecordID: e.ID(out), Before: before, AfterUpdatedAt: e.Stamp(out)}, nil
}

// Revert implements Entity. A record that changed after the import is left alone: undoing would throw away someone's
// later edit.
func (e Importable[T, I]) Revert(ctx context.Context, tx pgx.Tx, actor audit.Actor, in RevertInput) (string, error) {
	current, err := e.Store.GetIn(ctx, tx, in.RecordID)
	if errors.Is(err, resource.ErrNotFound) {
		return "The record no longer exists.", nil
	}
	if err != nil {
		return "", err
	}
	if !e.Stamp(current).Equal(in.AfterUpdatedAt) {
		return "The record was changed after the import, so it was left as it is.", nil
	}
	if in.Action == "create" {
		if err := e.Store.DeleteIn(ctx, tx, actor, in.RecordID); err != nil {
			if ps, ok := e.problemsFrom(err); ok {
				return ps[0].Message, nil
			}
			return "", err
		}
		return "", nil
	}
	var before T
	if err := json.Unmarshal(in.Before, &before); err != nil {
		return "", err
	}
	if _, err := e.Store.UpdateIn(ctx, tx, actor, in.RecordID, e.ToInput(before)); err != nil {
		if ps, ok := e.problemsFrom(err); ok {
			return ps[0].Message, nil
		}
		return "", err
	}
	return "", nil
}
