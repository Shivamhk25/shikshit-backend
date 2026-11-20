// Package crud holds the pieces every list-and-form resource shares: paging and sorting that cannot be abused, search
// that cannot inject wildcards, and field-level validation errors the API turns into a 400 problem document.
package crud

import (
	"fmt"
	"strings"
)

// FieldError is one validation failure on one field.
type FieldError struct {
	Field   string
	Code    string // stable machine code, translated by clients
	Message string
}

// FieldErrors is a validation failure. It is an error so services can return it; the HTTP layer renders it.
type FieldErrors []FieldError

func (e FieldErrors) Error() string {
	parts := make([]string, len(e))
	for i, f := range e {
		parts[i] = f.Field + ": " + f.Code
	}
	return "validation failed: " + strings.Join(parts, ", ")
}

// Add appends a failure.
func (e *FieldErrors) Add(field, code, message string) {
	*e = append(*e, FieldError{field, code, message})
}

// OrNil returns nil when there are no failures, so a function can `return errs.OrNil()`.
func (e FieldErrors) OrNil() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

// ListQuery is the parsed paging, sorting and search of a list request.
type ListQuery struct {
	Page, Size int
	OrderBy    string // a ready SQL fragment built only from the allow-list, never from user text
	Search     string // raw search text; use LikePattern to match it
}

// Offset is the number of rows to skip.
func (q ListQuery) Offset() int { return (q.Page - 1) * q.Size }

// Sorts maps the sort keys a client may use to SQL column expressions. Anything else is rejected.
type Sorts map[string]string

// DefaultSize and MaxSize bound a page.
const (
	DefaultSize = 50
	MaxSize     = 200
)

// ParseList validates paging and sorting. sort looks like "name" or "-capacity" (leading minus = descending) and may
// hold several comma-separated keys. An empty sort falls back to defaultSort.
func ParseList(page, size int, sort, search string, allowed Sorts, defaultSort string) (ListQuery, error) {
	var errs FieldErrors
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = DefaultSize
	}
	if page < 1 {
		errs.Add("page", "invalid_page", "Page must be 1 or more.")
	}
	if size < 1 || size > MaxSize {
		errs.Add("size", "invalid_size", fmt.Sprintf("Size must be between 1 and %d.", MaxSize))
	}
	if sort == "" {
		sort = defaultSort
	}
	var order []string
	for _, key := range strings.Split(sort, ",") {
		key = strings.TrimSpace(key)
		dir := "ASC"
		if strings.HasPrefix(key, "-") {
			dir, key = "DESC", key[1:]
		}
		col, ok := allowed[key]
		if !ok {
			errs.Add("sort", "invalid_sort", "Cannot sort by "+key+".")
			continue
		}
		order = append(order, col+" "+dir)
	}
	if len(search) > 100 {
		errs.Add("q", "too_long", "Search text is too long.")
	}
	if err := errs.OrNil(); err != nil {
		return ListQuery{}, err
	}
	return ListQuery{Page: page, Size: size, OrderBy: strings.Join(order, ", "), Search: strings.TrimSpace(search)}, nil
}

// LikePattern turns user text into a contains-pattern for ILIKE, escaping % _ and \ so they match literally.
// Use it with `ILIKE $n ESCAPE '\'`.
func LikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// Page is one page of a list plus the total number of matching rows.
type Page[T any] struct {
	Items []T
	Total int
	Page  int
	Size  int
}

// TrimPtr trims a pointer to text and turns blank into nil, so optional fields are stored as NULL, not "".
func TrimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

// Date is a calendar date as the API shows it ("2026-04-01").
const DateLayout = "2006-01-02"
