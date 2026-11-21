// Package importer is the import engine (ONB-05): file -> staged rows -> validate (dry run) -> commit as a batch ->
// roll back. Every importable entity is a registration next to its resource Spec, so a row is validated and written by
// the same code the screens use, and each change is audited with the batch that caused it.
package importer

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/audit"
)

// Limits protect the server from oversized uploads.
const (
	MaxFileBytes = 10 << 20
	MaxRows      = 20000
	MaxCellChars = 10000
	// Retention is how long staged rows are kept; RollbackWindow is how long after a commit it can be undone.
	Retention      = 30 * 24 * time.Hour
	RollbackWindow = 7 * 24 * time.Hour
)

// Column describes one column of an entity's import file. The template, the header mapping and the wizard's help all
// come from this list, so they cannot disagree with validation.
type Column struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Kind     string   `json:"kind"` // text | integer | number | boolean | date | choice | reference
	Hint     string   `json:"hint,omitempty"`
	Example  string   `json:"example,omitempty"`
	Allowed  []string `json:"allowed,omitempty"`
	// Ref names the entity a reference column points at ("school_divisions"); cells are matched by code or name.
	Ref string `json:"ref,omitempty"`
}

// Row is one staged row: cell text keyed by column key. Blank cells are absent.
type Row map[string]string

// Problem is something wrong (or worth knowing) about one cell or row.
type Problem struct {
	Column   string `json:"column,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error | warning
}

// Verdict is the result of checking a row without keeping any change.
type Verdict struct {
	Key       string // the natural key value, used to find duplicates inside the file
	KeyColumn string // the column the key came from, for duplicate messages
	Action    string // create | update
	Problems  []Problem
}

// OK reports whether the row has no error-level problem.
func (v Verdict) OK() bool {
	for _, p := range v.Problems {
		if p.Severity != "warning" {
			return false
		}
	}
	return true
}

// Applied is what writing a row did.
type Applied struct {
	Action         string
	RecordID       string
	Before         json.RawMessage // previous state of an updated record
	AfterUpdatedAt time.Time       // the record's update stamp right after the write, to detect later edits
}

// RevertInput identifies what a row did, so it can be undone.
type RevertInput struct {
	Action         string
	RecordID       string
	Before         json.RawMessage
	AfterUpdatedAt time.Time
}

// Entity is an importable kind of record.
type Entity interface {
	Key() string
	Title() string
	// Domain is the data-onboarding domain whose permissions guard it ("academic" -> onb.academic.*).
	Domain() string
	Columns() []Column
	TemplateVersion() int
	// Check validates a row against the database and says what applying it would do. It changes nothing that persists:
	// the caller runs it in a transaction it rolls back.
	Check(ctx context.Context, tx pgx.Tx, actor audit.Actor, row Row, strategy string) (Verdict, error)
	// Apply writes the row. It returns Problems (as a *RowError) when the data is not acceptable.
	Apply(ctx context.Context, tx pgx.Tx, actor audit.Actor, row Row, strategy string) (Applied, error)
	// Revert undoes an applied row. It returns a non-empty reason, and no error, when the row cannot be undone safely.
	Revert(ctx context.Context, tx pgx.Tx, actor audit.Actor, in RevertInput) (reason string, err error)
}

// RowError carries the problems of one row out of Apply.
type RowError struct{ Problems []Problem }

func (e *RowError) Error() string { return "import row rejected" }

// Registry holds the importable entities.
type Registry struct{ byKey map[string]Entity }

// NewRegistry builds a registry.
func NewRegistry(entities ...Entity) *Registry {
	r := &Registry{byKey: map[string]Entity{}}
	for _, e := range entities {
		r.byKey[e.Key()] = e
	}
	return r
}

// Get returns an entity by key.
func (r *Registry) Get(key string) (Entity, bool) { e, ok := r.byKey[key]; return e, ok }

// All lists entities in key order.
func (r *Registry) All() []Entity {
	keys := make([]string, 0, len(r.byKey))
	for k := range r.byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Entity, len(keys))
	for i, k := range keys {
		out[i] = r.byKey[k]
	}
	return out
}
