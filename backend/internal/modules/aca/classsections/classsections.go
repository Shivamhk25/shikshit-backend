// Package classsections implements ACA-30 Class Section Mappings: which sections a class has in an academic year.
package classsections

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pie360/backend/internal/modules/aca/refs"
	"github.com/pie360/backend/internal/modules/aca/yearcheck"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the mapping does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// ClassSection is a mapping as the API shows it.
type ClassSection struct {
	ID          string    `json:"id"`
	YearID      string    `json:"yearId"`
	YearName    string    `json:"yearName"`
	ClassID     string    `json:"classId"`
	ClassCode   string    `json:"classCode"`
	ClassName   string    `json:"className"`
	SectionID   string    `json:"sectionId"`
	SectionCode string    `json:"sectionCode"`
	SectionName string    `json:"sectionName"`
	Capacity    *int      `json:"capacity"`
	RoomID      *string   `json:"roomId"`
	RoomName    *string   `json:"roomName"`
	Remarks     *string   `json:"remarks"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	YearID    string
	ClassID   string
	SectionID string
	Capacity  *int
	RoomID    *string
	Remarks   *string
}

// Filter narrows a list.
type Filter struct{ YearID, ClassID, SectionID string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"className": "class_name", "sectionName": "section_name", "capacity": "capacity", "createdAt": "created_at"}

// DefaultSort lists classes in name order, then their sections.
const DefaultSort = "className,sectionName"

const columns = `id::text, year_id::text,
	(SELECT y.name FROM academic_years y WHERE y.tenant_id = class_section_mappings.tenant_id AND y.id = class_section_mappings.year_id) AS year_name,
	class_id::text,
	(SELECT c.code FROM classes c WHERE c.tenant_id = class_section_mappings.tenant_id AND c.id = class_section_mappings.class_id) AS class_code,
	(SELECT c.name FROM classes c WHERE c.tenant_id = class_section_mappings.tenant_id AND c.id = class_section_mappings.class_id) AS class_name,
	section_id::text,
	(SELECT s.code FROM sections s WHERE s.tenant_id = class_section_mappings.tenant_id AND s.id = class_section_mappings.section_id) AS section_code,
	(SELECT s.name FROM sections s WHERE s.tenant_id = class_section_mappings.tenant_id AND s.id = class_section_mappings.section_id) AS section_name,
	capacity, room_id::text,
	(SELECT r.name FROM rooms r WHERE r.tenant_id = class_section_mappings.tenant_id AND r.id = class_section_mappings.room_id) AS room_name,
	remarks, created_at, updated_at`

func scan(r resource.Scanner) (ClassSection, error) {
	var m ClassSection
	err := r.Scan(&m.ID, &m.YearID, &m.YearName, &m.ClassID, &m.ClassCode, &m.ClassName, &m.SectionID, &m.SectionCode, &m.SectionName, &m.Capacity, &m.RoomID, &m.RoomName, &m.Remarks, &m.CreatedAt, &m.UpdatedAt)
	return m, err
}

// Normalise trims, defaults and validates one mapping on its own; rules that need other records are in Check.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.YearID, in.ClassID, in.SectionID = strings.TrimSpace(in.YearID), strings.TrimSpace(in.ClassID), strings.TrimSpace(in.SectionID)
	in.RoomID, in.Remarks = crud.TrimPtr(in.RoomID), crud.TrimPtr(in.Remarks)
	refs.Require(&errs, "yearId", "an academic year", in.YearID)
	refs.Require(&errs, "classId", "a class", in.ClassID)
	refs.Require(&errs, "sectionId", "a section", in.SectionID)
	if in.RoomID != nil && !refs.IsUUID(*in.RoomID) {
		errs.Add("roomId", "invalid", "Choose a room from the list.")
	}
	if in.Capacity != nil && (*in.Capacity < 1 || *in.Capacity > 1000) {
		errs.Add("capacity", "out_of_range", "Capacity must be between 1 and 1000.")
	}
	errs.Optional("remarks", "remarks", in.Remarks, 500)
	return in, errs.OrNil()
}

var spec = resource.Spec[ClassSection, Input]{
	Table: "class_section_mappings", Action: "class_section_mappings", ResourceType: "class_section_mapping",
	Columns: columns, Scan: scan, ID: func(m ClassSection) string { return m.ID },
	WriteColumns: []string{"year_id", "class_id", "section_id", "capacity", "room_id", "remarks"},
	Args: func(in Input) []any {
		return []any{in.YearID, in.ClassID, in.SectionID, in.Capacity, in.RoomID, in.Remarks}
	},
	SearchColumns: []string{
		"remarks",
		"(SELECT c.name FROM classes c WHERE c.tenant_id = class_section_mappings.tenant_id AND c.id = class_section_mappings.class_id)",
		"(SELECT s.name FROM sections s WHERE s.tenant_id = class_section_mappings.tenant_id AND s.id = class_section_mappings.section_id)",
	},
	Sorts: SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, _ string, in Input) error {
		var fe crud.FieldErrors
		if _, _, err := yearcheck.Dates(ctx, tx, in.YearID); err != nil {
			return err
		}
		for _, r := range []struct{ field, table, id string }{{"classId", "classes", in.ClassID}, {"sectionId", "sections", in.SectionID}} {
			if err := refs.Live(ctx, tx, &fe, r.field, r.table, r.id); err != nil {
				return err
			}
		}
		if in.RoomID != nil {
			if err := refs.Live(ctx, tx, &fe, "roomId", "rooms", *in.RoomID); err != nil {
				return err
			}
		}
		return fe.OrNil()
	},
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM subject_class_mappings s
			JOIN class_section_mappings m ON m.tenant_id = s.tenant_id AND m.year_id = s.year_id AND m.class_id = s.class_id AND m.section_id = s.section_id
			WHERE m.id::text = $1 AND s.deleted_at IS NULL`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return resource.InUseError{Message: "Subjects are still mapped to this section of the class. Remove them first."}
		}
		return nil
	},
	Constraints: map[string]crud.FieldError{
		"class_section_mappings_uq":                        {Field: "sectionId", Code: "duplicate", Message: "This section is already mapped to this class for this year."},
		"class_section_mappings_tenant_id_year_id_fkey":    {Field: "yearId", Code: "not_found", Message: "This academic year does not exist."},
		"class_section_mappings_tenant_id_class_id_fkey":   {Field: "classId", Code: "not_found", Message: "This class does not exist."},
		"class_section_mappings_tenant_id_section_id_fkey": {Field: "sectionId", Code: "not_found", Message: "This section does not exist."},
		"class_section_mappings_tenant_id_room_id_fkey":    {Field: "roomId", Code: "not_found", Message: "This room does not exist."},
	},
}

// Service reads and writes class-section mappings.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[ClassSection, Input] { return s.store() }

func (s Service) store() resource.Store[ClassSection, Input] {
	return resource.Store[ClassSection, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of mappings.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[ClassSection], error) {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"year_id", f.YearID}, {"class_id", f.ClassID}, {"section_id", f.SectionID}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + "::text = ?", Args: []any{c.v}})
		}
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one mapping.
func (s Service) Get(ctx context.Context, id string) (ClassSection, error) {
	return s.store().Get(ctx, id)
}

// Create adds a mapping.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (ClassSection, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a mapping's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (ClassSection, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a mapping unless a subject is still mapped to that section.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
