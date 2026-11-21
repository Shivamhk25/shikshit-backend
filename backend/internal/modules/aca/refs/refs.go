// Package refs holds the checks shared by resources that point at other academic records.
package refs

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is written like a UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// Table names that may be checked. They are fixed in code, never taken from a request.
var live = map[string]string{
	"academic_years": "an academic year", "classes": "a class", "sections": "a section", "subjects": "a subject", "rooms": "a room", "textbooks": "a textbook", "students": "a student", "guardians": "a guardian",
}

// Require adds a field error unless id is a UUID. label is shown to people, for example "a class".
func Require(fe *crud.FieldErrors, field, label, id string) bool {
	if !IsUUID(id) {
		fe.Add(field, "required", "Choose "+label+".")
		return false
	}
	return true
}

// Live adds a not_found error on field unless the record exists in this institute and is not deleted. A foreign key
// alone is not enough: it accepts a soft-deleted row. It must run inside the write transaction. A blank id is ignored,
// so optional references need no wrapper.
func Live(ctx context.Context, tx pgx.Tx, fe *crud.FieldErrors, field, table, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || !IsUUID(id) {
		return nil
	}
	label, ok := live[table]
	if !ok {
		return fmt.Errorf("refs: table %q is not checkable", table)
	}
	var n int
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE id::text = $1 AND deleted_at IS NULL`, table), id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		fe.Add(field, "not_found", "This "+strings.TrimPrefix(label, "a ")+" does not exist.")
	}
	return nil
}

// Used returns a resource.InUseError with msg when query, which takes the record's id as $1 and returns a count,
// finds anything pointing at it. Queries are written in code, never taken from a request.
func Used(ctx context.Context, tx pgx.Tx, id, query, msg string) error {
	var n int
	if err := tx.QueryRow(ctx, query, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return resource.InUseError{Message: msg}
	}
	return nil
}
