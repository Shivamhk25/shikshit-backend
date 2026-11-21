// Package addresses implements a student's home and mailing address (STU-11). One address of each kind per student.
package addresses

import (
	"context"
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

// ErrNotFound is returned when the address does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Kinds are the allowed address kinds.
var Kinds = []string{"home", "mailing"}

// Address is an address as the API shows it.
type Address struct {
	ID               string    `json:"id"`
	StudentID        string    `json:"studentId"`
	RegistrationCode string    `json:"registrationCode"`
	StudentName      string    `json:"studentName"`
	Kind             string    `json:"kind"`
	Line1            string    `json:"line1"`
	Line2            *string   `json:"line2"`
	City             string    `json:"city"`
	State            *string   `json:"state"`
	Postcode         *string   `json:"postcode"`
	Country          string    `json:"country"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	StudentID string
	Kind      string
	Line1     string
	Line2     *string
	City      string
	State     *string
	Postcode  *string
	Country   string
}

// Filter narrows a list.
type Filter struct{ StudentID, Kind string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"studentName": "student_name", "kind": "kind", "city": "lower(city)", "createdAt": "created_at"}

// DefaultSort lists by student, then kind.
const DefaultSort = "studentName,kind"

const t = "student_addresses"

func sub(col string) string {
	return "(SELECT " + col + " FROM students x WHERE x.tenant_id = " + t + ".tenant_id AND x.id = " + t + ".student_id)"
}

var columns = `id::text, student_id::text, ` + sub("x.registration_code") + ` AS registration_code,
	` + sub("btrim(x.first_name || ' ' || COALESCE(x.last_name, ''))") + ` AS student_name,
	kind, line1, line2, city, state, postcode, country, created_at, updated_at`

func scan(r resource.Scanner) (Address, error) {
	var a Address
	err := r.Scan(&a.ID, &a.StudentID, &a.RegistrationCode, &a.StudentName, &a.Kind, &a.Line1, &a.Line2, &a.City, &a.State, &a.Postcode, &a.Country, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

var postcodeRE = regexp.MustCompile(`^[A-Za-z0-9 -]{3,10}$`)

// Normalise trims, defaults and validates one address on its own.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.StudentID, in.Line1, in.City = strings.TrimSpace(in.StudentID), strings.TrimSpace(in.Line1), strings.TrimSpace(in.City)
	in.Kind, in.Country = strings.ToLower(strings.TrimSpace(in.Kind)), strings.TrimSpace(in.Country)
	in.Line2, in.State, in.Postcode = crud.TrimPtr(in.Line2), crud.TrimPtr(in.State), crud.TrimPtr(in.Postcode)
	if in.Country == "" {
		in.Country = "India"
	}
	refs.Require(&errs, "studentId", "a student", in.StudentID)
	errs.Choice("kind", in.Kind, Kinds...)
	errs.Required("line1", "the first address line", in.Line1, 200)
	errs.Optional("line2", "second address line", in.Line2, 200)
	errs.Required("city", "a city", in.City, 100)
	errs.Optional("state", "state", in.State, 100)
	errs.Required("country", "a country", in.Country, 100)
	if in.Postcode != nil && !postcodeRE.MatchString(*in.Postcode) {
		errs.Add("postcode", "invalid", "A postcode has 3 to 10 letters, digits, spaces or hyphens.")
	}
	return in, errs.OrNil()
}

var spec = resource.Spec[Address, Input]{
	Table: t, Action: "student_addresses", ResourceType: "student_address",
	Columns: columns, Scan: scan, ID: func(a Address) string { return a.ID },
	WriteColumns: []string{"student_id", "kind", "line1", "line2", "city", "state", "postcode", "country"},
	Args: func(in Input) []any {
		return []any{in.StudentID, in.Kind, in.Line1, in.Line2, in.City, in.State, in.Postcode, in.Country}
	},
	SearchColumns: []string{sub("x.registration_code"), sub("x.first_name || ' ' || COALESCE(x.last_name, '')"), "city", "postcode"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		if err := refs.Live(ctx, tx, &fe, "studentId", "students", in.StudentID); err != nil {
			return err
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"student_addresses_kind_uq":                   {Field: "kind", Code: "duplicate", Message: "This student already has an address of this kind."},
		"student_addresses_tenant_id_student_id_fkey": {Field: "studentId", Code: "not_found", Message: "This student does not exist."},
	},
}

// Service reads and writes addresses.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Address, Input] { return s.store() }

func (s Service) store() resource.Store[Address, Input] {
	return resource.Store[Address, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"student_id::text", f.StudentID}, {"kind", f.Kind}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// List returns one page of addresses.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Address], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

// ListAudited is List for the API: the read is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Address], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "studentId": f.StudentID, "kind": f.Kind}, q, filterConds(f)...)
}

// Get returns one address.
func (s Service) Get(ctx context.Context, id string) (Address, error) { return s.store().Get(ctx, id) }

// View is Get for the API: the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Address, error) {
	return s.store().View(ctx, a, id)
}

// Create adds an address.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Address, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces an address's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Address, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes an address.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
