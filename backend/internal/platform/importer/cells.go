package importer

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/platform/crud"
)

// Cells reads typed values out of a staged row and collects the problems it finds, so an entity's Build function reads
// like a plain mapping and every bad cell is reported at once.
type Cells struct {
	Row      Row
	problems []Problem
}

// NewCells wraps a row.
func NewCells(r Row) *Cells { return &Cells{Row: r} }

// Problems returns what was found so far.
func (c *Cells) Problems() []Problem { return c.problems }

// Fail records an error on a column.
func (c *Cells) Fail(col, code, msg string) {
	c.problems = append(c.problems, Problem{Column: col, Code: code, Message: msg, Severity: "error"})
}

// Text is the trimmed cell, empty when blank.
func (c *Cells) Text(key string) string { return strings.TrimSpace(c.Row[key]) }

// Opt is the trimmed cell, or nil when blank.
func (c *Cells) Opt(key string) *string {
	if v := c.Text(key); v != "" {
		return &v
	}
	return nil
}

// Int reads a whole number; a blank cell gives def. "12.0" (how spreadsheets often store numbers) is accepted.
func (c *Cells) Int(key string, def int) int {
	v := c.Text(key)
	if v == "" {
		return def
	}
	n, ok := parseInt(v)
	if !ok {
		c.Fail(key, "invalid_number", "Enter a whole number.")
		return def
	}
	return n
}

// OptInt is Int with nil for a blank cell.
func (c *Cells) OptInt(key string) *int {
	if c.Text(key) == "" {
		return nil
	}
	n := c.Int(key, 0)
	return &n
}

// Float reads a decimal number; a blank cell gives def.
func (c *Cells) Float(key string, def float64) float64 {
	v := c.Text(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		c.Fail(key, "invalid_number", "Enter a number.")
		return def
	}
	return f
}

// Bool reads yes/no, true/false, y/n, 1/0 (any case); a blank cell gives def.
func (c *Cells) Bool(key string, def bool) bool {
	switch strings.ToLower(c.Text(key)) {
	case "":
		return def
	case "yes", "y", "true", "1":
		return true
	case "no", "n", "false", "0":
		return false
	}
	c.Fail(key, "invalid_choice", "Enter yes or no.")
	return def
}

// Date reads 2026-04-01 or 01/04/2026 (day first) and returns the ISO form; blank gives "".
func (c *Cells) Date(key string) string {
	v := c.Text(key)
	if v == "" {
		return ""
	}
	for _, layout := range []string{crud.DateLayout, "02/01/2006", "02-01-2006", "2/1/2006"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format(crud.DateLayout)
		}
	}
	c.Fail(key, "invalid_date", "Use a date like 2026-04-01 or 01/04/2026.")
	return ""
}

func parseInt(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f == math.Trunc(f) && math.Abs(f) < 1e9 {
		return int(f), true
	}
	return 0, false
}

// lookup tables a reference column may point at. Fixed in code: a cell never chooses the table or column.
var lookups = map[string]struct {
	table   string
	columns []string
}{
	"school_divisions": {"school_divisions", []string{"code", "name"}},
	"academic_years":   {"academic_years", []string{"code", "name"}},
	"classes":          {"classes", []string{"code", "name"}},
	"sections":         {"sections", []string{"code", "name"}},
	"subjects":         {"subjects", []string{"code", "name"}},
	"rooms":            {"rooms", []string{"code", "name"}},
	"textbooks":        {"textbooks", []string{"code", "title"}},
	"students":         {"students", []string{"registration_code"}},
}

// Ref resolves a reference cell (a division by its code or name) to an id inside the institute. A blank cell gives
// "" without a problem; an unknown or ambiguous value is reported on the column.
func (c *Cells) Ref(ctx context.Context, tx pgx.Tx, key, entity, label string) string {
	v := c.Text(key)
	if v == "" {
		return ""
	}
	l, ok := lookups[entity]
	if !ok {
		c.Fail(key, "internal", "This column cannot be matched.")
		return ""
	}
	cond := make([]string, len(l.columns))
	for i, col := range l.columns {
		cond[i] = "lower(" + col + ") = lower($1)"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id::text FROM %s WHERE deleted_at IS NULL AND (%s) LIMIT 2`, l.table, strings.Join(cond, " OR ")), v)
	if err != nil {
		c.Fail(key, "internal", "This value could not be checked.")
		return ""
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	switch len(ids) {
	case 0:
		c.Fail(key, "not_found", fmt.Sprintf("No %s matches %q. Add it first, or fix the spelling.", label, v))
		return ""
	case 1:
		return ids[0]
	default:
		c.Fail(key, "ambiguous", fmt.Sprintf("More than one %s matches %q. Use its code.", label, v))
		return ""
	}
}
