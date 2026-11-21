// Package guardians implements the parent or guardian of a student (STU-02). A guardian is one person who can be
// linked to several students; the link is in package links.
package guardians

import (
	"context"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/modules/stu/stuscope"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the guardian does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Guardian is a guardian as the API shows it.
type Guardian struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Phone        string    `json:"phone"`
	Email        *string   `json:"email"`
	StudentCount int       `json:"studentCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Name  string
	Phone string
	Email *string
}

// Filter narrows a list.
type Filter struct{ Phone string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"name": "lower(name)", "phone": "phone", "createdAt": "created_at"}

// DefaultSort lists guardians by name.
const DefaultSort = "name"

const columns = `id::text, name, phone, email,
	(SELECT count(*) FROM student_guardians l WHERE l.tenant_id = guardians.tenant_id AND l.guardian_id = guardians.id AND l.deleted_at IS NULL)::int AS student_count,
	created_at, updated_at`

func scan(r resource.Scanner) (Guardian, error) {
	var g Guardian
	err := r.Scan(&g.ID, &g.Name, &g.Phone, &g.Email, &g.StudentCount, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

var phoneRE = regexp.MustCompile(`^[0-9+() -]{7,20}$`)

// Normalise trims and validates one guardian.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Name, in.Phone = strings.TrimSpace(in.Name), strings.TrimSpace(in.Phone)
	in.Email = crud.TrimPtr(in.Email)
	errs.Required("name", "a name", in.Name, 150)
	if in.Phone == "" {
		errs.Add("phone", "required", "Enter a phone number.")
	} else if !phoneRE.MatchString(in.Phone) {
		errs.Add("phone", "invalid", "A phone number has 7 to 20 digits, and may include +, spaces, brackets or hyphens.")
	}
	if in.Email != nil {
		if a, err := mail.ParseAddress(*in.Email); err != nil || a.Address != *in.Email || len(*in.Email) > 254 {
			errs.Add("email", "invalid_email", "Enter a valid email address.")
		}
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Guardian, Input]{
	Table: "guardians", Action: "guardians", ResourceType: "guardian",
	Columns: columns, Scan: scan, ID: func(g Guardian) string { return g.ID },
	WriteColumns:  []string{"name", "phone", "email"},
	Args:          func(in Input) []any { return []any{in.Name, in.Phone, in.Email} },
	SearchColumns: []string{"name", "phone", "email"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		return refs.Used(ctx, tx, id, `SELECT count(*) FROM student_guardians WHERE guardian_id::text = $1 AND deleted_at IS NULL`,
			"This guardian is still linked to a student. Remove the link first.")
	},
	Restrict: stuscope.Guardian,
	Constraints: map[string]crud.FieldError{
		"guardians_phone_name_uq": {Field: "phone", Code: "duplicate", Message: "A guardian with this name and phone number already exists. Link that guardian instead."},
	},
}

// Service reads and writes guardians.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Guardian, Input] { return s.store() }

func (s Service) store() resource.Store[Guardian, Input] {
	return resource.Store[Guardian, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	if f.Phone == "" {
		return nil
	}
	return []resource.Cond{{SQL: "phone = ?", Args: []any{f.Phone}}}
}

// List returns one page of guardians.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Guardian], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

// ListAudited is List for the API: the read is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Guardian], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "byPhone": f.Phone != ""}, q, filterConds(f)...)
}

// Get returns one guardian.
func (s Service) Get(ctx context.Context, id string) (Guardian, error) { return s.store().Get(ctx, id) }

// View is Get for the API: the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Guardian, error) {
	return s.store().View(ctx, a, id)
}

// Create adds a guardian.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Guardian, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a guardian's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Guardian, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a guardian unless a student is still linked.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
