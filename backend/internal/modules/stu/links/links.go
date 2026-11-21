// Package links implements the link between a student and a guardian (STU-02): the relation, who is primary, who has
// legal custody and who may collect the child.
package links

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

// ErrNotFound is returned when the link does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Relations are the allowed relations, stored as these codes.
var Relations = []string{"father", "mother", "guardian", "grandparent", "sibling", "other"}

// Link is a link as the API shows it.
type Link struct {
	ID               string    `json:"id"`
	StudentID        string    `json:"studentId"`
	RegistrationCode string    `json:"registrationCode"`
	StudentName      string    `json:"studentName"`
	GuardianID       string    `json:"guardianId"`
	GuardianName     string    `json:"guardianName"`
	GuardianPhone    string    `json:"guardianPhone"`
	Relation         string    `json:"relation"`
	IsPrimary        bool      `json:"isPrimary"`
	IsLegalCustody   bool      `json:"isLegalCustody"`
	CanPickup        bool      `json:"canPickup"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	StudentID      string
	GuardianID     string
	Relation       string
	IsPrimary      bool
	IsLegalCustody bool
	CanPickup      bool
}

// Filter narrows a list.
type Filter struct{ StudentID, GuardianID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"studentName": "student_name", "guardianName": "guardian_name", "relation": "relation", "createdAt": "created_at"}

// DefaultSort lists by student, then guardian.
const DefaultSort = "studentName,guardianName"

const t = "student_guardians"

func sub(table, col, alias string) string {
	return "(SELECT " + col + " FROM " + table + " x WHERE x.tenant_id = " + t + ".tenant_id AND x.id = " + t + "." + alias + ")"
}

var columns = `id::text, student_id::text,
	` + sub("students", "x.registration_code", "student_id") + ` AS registration_code,
	` + sub("students", "btrim(x.first_name || ' ' || COALESCE(x.last_name, ''))", "student_id") + ` AS student_name,
	guardian_id::text,
	` + sub("guardians", "x.name", "guardian_id") + ` AS guardian_name,
	` + sub("guardians", "x.phone", "guardian_id") + ` AS guardian_phone,
	relation, is_primary, is_legal_custody, can_pickup, created_at, updated_at`

func scan(r resource.Scanner) (Link, error) {
	var l Link
	err := r.Scan(&l.ID, &l.StudentID, &l.RegistrationCode, &l.StudentName, &l.GuardianID, &l.GuardianName, &l.GuardianPhone,
		&l.Relation, &l.IsPrimary, &l.IsLegalCustody, &l.CanPickup, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

// Normalise trims and validates one link on its own.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.StudentID, in.GuardianID = strings.TrimSpace(in.StudentID), strings.TrimSpace(in.GuardianID)
	in.Relation = strings.ToLower(strings.TrimSpace(in.Relation))
	refs.Require(&errs, "studentId", "a student", in.StudentID)
	refs.Require(&errs, "guardianId", "a guardian", in.GuardianID)
	errs.Choice("relation", in.Relation, Relations...)
	return in, errs.OrNil()
}

var spec = resource.Spec[Link, Input]{
	Table: t, Action: "student_guardians", ResourceType: "student_guardian",
	Columns: columns, Scan: scan, ID: func(l Link) string { return l.ID },
	WriteColumns: []string{"student_id", "guardian_id", "relation", "is_primary", "is_legal_custody", "can_pickup"},
	Args: func(in Input) []any {
		return []any{in.StudentID, in.GuardianID, in.Relation, in.IsPrimary, in.IsLegalCustody, in.CanPickup}
	},
	SearchColumns: []string{sub("students", "x.registration_code", "student_id"), sub("students", "x.first_name || ' ' || COALESCE(x.last_name, '')", "student_id"), sub("guardians", "x.name", "guardian_id"), sub("guardians", "x.phone", "guardian_id")},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		for _, r := range []struct{ field, table, id string }{{"studentId", "students", in.StudentID}, {"guardianId", "guardians", in.GuardianID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		return fe.OrNil()
	},
	Constraints: map[string]crud.FieldError{
		"student_guardians_pair_uq":                    {Field: "guardianId", Code: "duplicate", Message: "This guardian is already linked to this student."},
		"student_guardians_primary_uq":                 {Field: "isPrimary", Code: "duplicate", Message: "This student already has a primary guardian. Change that link first."},
		"student_guardians_tenant_id_student_id_fkey":  {Field: "studentId", Code: "not_found", Message: "This student does not exist."},
		"student_guardians_tenant_id_guardian_id_fkey": {Field: "guardianId", Code: "not_found", Message: "This guardian does not exist."},
	},
}

// Service reads and writes links.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Link, Input] { return s.store() }

func (s Service) store() resource.Store[Link, Input] {
	return resource.Store[Link, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"student_id::text", f.StudentID}, {"guardian_id::text", f.GuardianID}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// List returns one page of links.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Link], error) {
	return s.store().List(ctx, q, filterConds(f)...)
}

// ListAudited is List for the API: the read is audited with the paging and filters, never the search text.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Link], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "studentId": f.StudentID, "guardianId": f.GuardianID}, q, filterConds(f)...)
}

// Get returns one link.
func (s Service) Get(ctx context.Context, id string) (Link, error) { return s.store().Get(ctx, id) }

// View is Get for the API: the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Link, error) {
	return s.store().View(ctx, a, id)
}

// Create adds a link.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Link, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a link's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Link, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a link.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
