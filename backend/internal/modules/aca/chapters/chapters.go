// Package chapters implements ACA-27 Textbook Chapters: the chapters of a textbook.
package chapters

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the chapter does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Chapter is a chapter as the API shows it.
type Chapter struct {
	ID            string    `json:"id"`
	TextbookID    string    `json:"textbookId"`
	TextbookCode  string    `json:"textbookCode"`
	TextbookTitle string    `json:"textbookTitle"`
	SortOrder     int       `json:"sortOrder"`
	ChapterNo     *string   `json:"chapterNo"`
	Title         string    `json:"title"`
	Periods       *int      `json:"periods"`
	FromPage      *int      `json:"fromPage"`
	ToPage        *int      `json:"toPage"`
	IsOptional    bool      `json:"isOptional"`
	Summary       *string   `json:"summary"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	TextbookID string
	SortOrder  int
	ChapterNo  *string
	Title      string
	Periods    *int
	FromPage   *int
	ToPage     *int
	IsOptional bool
	Summary    *string
}

// Filter narrows a list.
type Filter struct{ TextbookID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"sortOrder": "sort_order", "title": "title", "periods": "periods", "createdAt": "created_at"}

// DefaultSort follows the book's own order.
const DefaultSort = "sortOrder,createdAt"

const columns = `id::text, textbook_id::text,
	(SELECT b.code FROM textbooks b WHERE b.tenant_id = textbook_chapters.tenant_id AND b.id = textbook_chapters.textbook_id) AS textbook_code,
	(SELECT b.title FROM textbooks b WHERE b.tenant_id = textbook_chapters.tenant_id AND b.id = textbook_chapters.textbook_id) AS textbook_title,
	sort_order, chapter_no, title, periods, from_page, to_page, is_optional, summary, created_at, updated_at`

func scan(r resource.Scanner) (Chapter, error) {
	var c Chapter
	err := r.Scan(&c.ID, &c.TextbookID, &c.TextbookCode, &c.TextbookTitle, &c.SortOrder, &c.ChapterNo, &c.Title, &c.Periods, &c.FromPage, &c.ToPage, &c.IsOptional, &c.Summary, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func inRange(v *int, lo, hi int) bool { return v == nil || (*v >= lo && *v <= hi) }

// Normalise trims, defaults and validates one chapter on its own; the textbook is checked in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.TextbookID, in.Title = strings.TrimSpace(in.TextbookID), strings.TrimSpace(in.Title)
	in.ChapterNo, in.Summary = crud.TrimPtr(in.ChapterNo), crud.TrimPtr(in.Summary)
	refs.Require(&errs, "textbookId", "a textbook", in.TextbookID)
	errs.Required("title", "a title", in.Title, 200)
	errs.Optional("chapterNo", "chapter number", in.ChapterNo, 20)
	errs.Optional("summary", "summary", in.Summary, 1000)
	errs.Range("sortOrder", "order", in.SortOrder, 0, 100000)
	if !inRange(in.Periods, 0, 200) {
		errs.Add("periods", "out_of_range", "Periods must be between 0 and 200.")
	}
	if !inRange(in.FromPage, 1, 10000) {
		errs.Add("fromPage", "out_of_range", "A page must be between 1 and 10000.")
	}
	if !inRange(in.ToPage, 1, 10000) {
		errs.Add("toPage", "out_of_range", "A page must be between 1 and 10000.")
	}
	if in.FromPage != nil && in.ToPage != nil && *in.ToPage < *in.FromPage {
		errs.Add("toPage", "before_from", "The last page cannot come before the first page.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Chapter, Input]{
	Table: "textbook_chapters", Action: "textbook_chapters", ResourceType: "textbook_chapter",
	Columns: columns, Scan: scan, ID: func(c Chapter) string { return c.ID },
	WriteColumns: []string{"textbook_id", "sort_order", "chapter_no", "title", "periods", "from_page", "to_page", "is_optional", "summary"},
	Args: func(in Input) []any {
		return []any{in.TextbookID, in.SortOrder, in.ChapterNo, in.Title, in.Periods, in.FromPage, in.ToPage, in.IsOptional, in.Summary}
	},
	SearchColumns: []string{"title", "chapter_no"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		if err := refs.Live(ctx, tx, &fe, "textbookId", "textbooks", in.TextbookID); err != nil {
			return err
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"textbook_chapters_no_uq":                      {Field: "chapterNo", Code: "duplicate", Message: "Another chapter of this book already has this number."},
		"textbook_chapters_tenant_id_textbook_id_fkey": {Field: "textbookId", Code: "not_found", Message: "This textbook does not exist."},
	},
}

// Service reads and writes chapters.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Chapter, Input] { return s.store() }

func (s Service) store() resource.Store[Chapter, Input] {
	return resource.Store[Chapter, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of chapters.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Chapter], error) {
	var conds []resource.Cond
	if f.TextbookID != "" {
		conds = append(conds, resource.Cond{SQL: "textbook_id::text = ?", Args: []any{f.TextbookID}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one chapter.
func (s Service) Get(ctx context.Context, id string) (Chapter, error) { return s.store().Get(ctx, id) }

// Create adds a chapter.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Chapter, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a chapter's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Chapter, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a chapter.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
