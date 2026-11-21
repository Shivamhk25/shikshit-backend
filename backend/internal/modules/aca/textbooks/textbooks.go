// Package textbooks implements ACA-26 Textbooks: the books of a class and subject.
package textbooks

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the textbook does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Textbook is a textbook as the API shows it. ChapterCount and PeriodsNeeded come from its chapters: the periods
// of the chapters that are not marked optional.
type Textbook struct {
	ID            string    `json:"id"`
	Code          string    `json:"code"`
	Title         string    `json:"title"`
	ClassID       string    `json:"classId"`
	ClassCode     string    `json:"classCode"`
	ClassName     string    `json:"className"`
	SubjectID     string    `json:"subjectId"`
	SubjectCode   string    `json:"subjectCode"`
	SubjectName   string    `json:"subjectName"`
	Publisher     *string   `json:"publisher"`
	Author        *string   `json:"author"`
	Isbn          *string   `json:"isbn"`
	BookUrl       *string   `json:"bookUrl"`
	Notes         *string   `json:"notes"`
	Status        string    `json:"status"`
	ChapterCount  int       `json:"chapterCount"`
	PeriodsNeeded int       `json:"periodsNeeded"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code      string
	Title     string
	ClassID   string
	SubjectID string
	Publisher *string
	Author    *string
	Isbn      *string
	BookUrl   *string
	Notes     *string
	Status    string
}

// Filter narrows a list.
type Filter struct{ ClassID, SubjectID, Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "title": "title", "className": "class_name", "subjectName": "subject_name", "createdAt": "created_at"}

// DefaultSort lists books by class, then subject, then title.
const DefaultSort = "className,subjectName,title"

const columns = `id::text, code, title, class_id::text,
	(SELECT c.code FROM classes c WHERE c.tenant_id = textbooks.tenant_id AND c.id = textbooks.class_id) AS class_code,
	(SELECT c.name FROM classes c WHERE c.tenant_id = textbooks.tenant_id AND c.id = textbooks.class_id) AS class_name,
	subject_id::text,
	(SELECT s.code FROM subjects s WHERE s.tenant_id = textbooks.tenant_id AND s.id = textbooks.subject_id) AS subject_code,
	(SELECT s.name FROM subjects s WHERE s.tenant_id = textbooks.tenant_id AND s.id = textbooks.subject_id) AS subject_name,
	publisher, author, isbn, book_url, notes, status,
	(SELECT count(*) FROM textbook_chapters ch WHERE ch.tenant_id = textbooks.tenant_id AND ch.textbook_id = textbooks.id AND ch.deleted_at IS NULL)::int AS chapter_count,
	(SELECT COALESCE(sum(ch.periods), 0) FROM textbook_chapters ch WHERE ch.tenant_id = textbooks.tenant_id AND ch.textbook_id = textbooks.id AND ch.deleted_at IS NULL AND NOT ch.is_optional)::int AS periods_needed,
	created_at, updated_at`

func scan(r resource.Scanner) (Textbook, error) {
	var b Textbook
	err := r.Scan(&b.ID, &b.Code, &b.Title, &b.ClassID, &b.ClassCode, &b.ClassName, &b.SubjectID, &b.SubjectCode, &b.SubjectName,
		&b.Publisher, &b.Author, &b.Isbn, &b.BookUrl, &b.Notes, &b.Status, &b.ChapterCount, &b.PeriodsNeeded, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

var isbnRE = regexp.MustCompile(`^[0-9Xx-]{10,17}$`)

// webURL reports whether s is an http or https address with a host. Anything else (javascript:, data:, file:) is refused,
// because the address is shown to people as a link.
func webURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Normalise trims, defaults and validates one textbook on its own; rules that need other records are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Code, in.Title = strings.TrimSpace(in.Code), strings.TrimSpace(in.Title)
	in.ClassID, in.SubjectID = strings.TrimSpace(in.ClassID), strings.TrimSpace(in.SubjectID)
	in.Publisher, in.Author, in.Isbn, in.BookUrl, in.Notes = crud.TrimPtr(in.Publisher), crud.TrimPtr(in.Author), crud.TrimPtr(in.Isbn), crud.TrimPtr(in.BookUrl), crud.TrimPtr(in.Notes)
	if in.Status == "" {
		in.Status = "active"
	}
	errs.Required("code", "a code", in.Code, 20)
	errs.Required("title", "a title", in.Title, 200)
	refs.Require(&errs, "classId", "a class", in.ClassID)
	refs.Require(&errs, "subjectId", "a subject", in.SubjectID)
	errs.Optional("publisher", "publisher", in.Publisher, 100)
	errs.Optional("author", "author", in.Author, 100)
	if in.Isbn != nil && !isbnRE.MatchString(*in.Isbn) {
		errs.Add("isbn", "invalid", "An ISBN has 10 to 17 digits, hyphens or an X.")
	}
	if in.BookUrl != nil && (len(*in.BookUrl) > 500 || !webURL(*in.BookUrl)) {
		errs.Add("bookUrl", "invalid_url", "Use a web address that starts with http:// or https://.")
	}
	errs.Optional("notes", "notes", in.Notes, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

var spec = resource.Spec[Textbook, Input]{
	Table: "textbooks", Action: "textbooks", ResourceType: "textbook",
	Columns: columns, Scan: scan, ID: func(b Textbook) string { return b.ID },
	WriteColumns: []string{"code", "title", "class_id", "subject_id", "publisher", "author", "isbn", "book_url", "notes", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Title, in.ClassID, in.SubjectID, in.Publisher, in.Author, in.Isbn, in.BookUrl, in.Notes, in.Status}
	},
	SearchColumns: []string{"code", "title", "publisher", "author"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		for _, r := range []struct{ field, table, id string }{{"classId", "classes", in.ClassID}, {"subjectId", "subjects", in.SubjectID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		return fe.OrNil()
	},
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM textbook_chapters WHERE textbook_id::text = $1 AND deleted_at IS NULL`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return resource.InUseError{Message: "This textbook still has chapters. Delete them first."}
		}
		return nil
	},
	Constraints: map[string]crud.FieldError{
		"textbooks_tenant_code_uq":            {Field: "code", Code: "duplicate", Message: "Another textbook already uses this code."},
		"textbooks_tenant_id_class_id_fkey":   {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"textbooks_tenant_id_subject_id_fkey": {Field: "subjectId", Code: "not_found", Message: "This subject does not exist."},
	},
}

// Service reads and writes textbooks.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Textbook, Input] { return s.store() }

func (s Service) store() resource.Store[Textbook, Input] {
	return resource.Store[Textbook, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of textbooks.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Textbook], error) {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"class_id::text", f.ClassID}, {"subject_id::text", f.SubjectID}, {"status", f.Status}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one textbook.
func (s Service) Get(ctx context.Context, id string) (Textbook, error) { return s.store().Get(ctx, id) }

// Create adds a textbook.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Textbook, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a textbook's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Textbook, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a textbook unless it still has chapters.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
