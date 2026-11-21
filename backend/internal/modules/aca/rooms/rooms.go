// Package rooms implements ACA-24 Rooms. It is the pilot for the CRUD kit: every master-data resource follows this
// shape (validate and normalise, run in an institute-bound transaction, write the audit row in the same transaction).
package rooms

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the room does not exist in this institute (or was deleted).
var ErrNotFound = resource.ErrNotFound

// Types a room can have.
var Types = []string{"classroom", "lab", "library", "hall", "staff", "other"}

// Room is a room as the API shows it.
type Room struct {
	ID                    string    `json:"id"`
	Code                  *string   `json:"code"`
	Name                  string    `json:"name"`
	Type                  *string   `json:"type"`
	Capacity              int       `json:"capacity"`
	Building              *string   `json:"building"`
	Floor                 *string   `json:"floor"`
	AvailableForTimetable bool      `json:"availableForTimetable"`
	DisplayOrder          int       `json:"displayOrder"`
	Description           *string   `json:"description"`
	Status                string    `json:"status"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// Input is what create and update accept. Update replaces every field.
type Input struct {
	Code                  *string
	Name                  string
	Type                  *string
	Capacity              int
	Building              *string
	Floor                 *string
	AvailableForTimetable *bool
	DisplayOrder          int
	Description           *string
	Status                string
}

// Filter narrows a list.
type Filter struct {
	Status                string // "", active or inactive
	AvailableForTimetable *bool
}

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{
	"name": "name", "code": "code", "capacity": "capacity", "displayOrder": "display_order",
	"building": "building", "createdAt": "created_at",
}

// DefaultSort orders by the institute's own ordering, then name.
const DefaultSort = "displayOrder,name"

const columns = `id::text, code, name, room_type, capacity, building, floor, available_for_timetable, display_order, description, status, created_at, updated_at`

func scan(row resource.Scanner) (Room, error) {
	var r Room
	err := row.Scan(&r.ID, &r.Code, &r.Name, &r.Type, &r.Capacity, &r.Building, &r.Floor, &r.AvailableForTimetable, &r.DisplayOrder, &r.Description, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

var spec = resource.Spec[Room, Input]{
	Table: "rooms", Action: "rooms", ResourceType: "room",
	Columns: columns, Scan: scan, ID: func(r Room) string { return r.ID },
	WriteColumns: []string{"code", "name", "room_type", "capacity", "building", "floor", "available_for_timetable", "display_order", "description", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, in.Type, in.Capacity, in.Building, in.Floor, *in.AvailableForTimetable, in.DisplayOrder, in.Description, in.Status}
	},
	SearchColumns: []string{"name", "code", "building"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	BeforeDelete: func(ctx context.Context, tx pgx.Tx, id string) error {
		return refs.Used(ctx, tx, id, `SELECT count(*) FROM class_section_mappings WHERE room_id::text = $1 AND deleted_at IS NULL`,
			"This room is still the room of a class section. Change those mappings first.")
	},
	Constraints: map[string]crud.FieldError{
		"rooms_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another room already uses this code."},
	},
}

func trimmed(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

// Normalise trims text, turns blank optional fields into nil, applies defaults and validates. The returned Input is
// what gets stored.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Name = strings.TrimSpace(in.Name)
	in.Code, in.Type, in.Building, in.Floor, in.Description = trimmed(in.Code), trimmed(in.Type), trimmed(in.Building), trimmed(in.Floor), trimmed(in.Description)
	if in.Status == "" {
		in.Status = "active"
	}
	if in.AvailableForTimetable == nil {
		yes := true
		in.AvailableForTimetable = &yes
	}

	if in.Name == "" {
		errs.Add("name", "required", "Enter a name.")
	} else if len([]rune(in.Name)) > 100 {
		errs.Add("name", "too_long", "Name can be at most 100 characters.")
	}
	if in.Code != nil && len([]rune(*in.Code)) > 20 {
		errs.Add("code", "too_long", "Code can be at most 20 characters.")
	}
	if in.Type != nil && !contains(Types, *in.Type) {
		errs.Add("type", "invalid_choice", "Choose one of: "+strings.Join(Types, ", ")+".")
	}
	if in.Capacity < 1 || in.Capacity > 10000 {
		errs.Add("capacity", "out_of_range", "Capacity must be between 1 and 10000.")
	}
	if in.Building != nil && len([]rune(*in.Building)) > 100 {
		errs.Add("building", "too_long", "Building can be at most 100 characters.")
	}
	if in.Floor != nil && len([]rune(*in.Floor)) > 20 {
		errs.Add("floor", "too_long", "Floor can be at most 20 characters.")
	}
	if in.DisplayOrder < 0 || in.DisplayOrder > 100000 {
		errs.Add("displayOrder", "out_of_range", "Display order must be between 0 and 100000.")
	}
	if in.Description != nil && len([]rune(*in.Description)) > 500 {
		errs.Add("description", "too_long", "Description can be at most 500 characters.")
	}
	if in.Status != "active" && in.Status != "inactive" {
		errs.Add("status", "invalid_choice", "Status must be active or inactive.")
	}
	return in, errs.OrNil()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Service reads and writes rooms.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Room, Input] { return s.store() }

func (s Service) store() resource.Store[Room, Input] {
	return resource.Store[Room, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of rooms.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Room], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	if f.AvailableForTimetable != nil {
		conds = append(conds, resource.Cond{SQL: "available_for_timetable = ?", Args: []any{*f.AvailableForTimetable}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one room.
func (s Service) Get(ctx context.Context, id string) (Room, error) { return s.store().Get(ctx, id) }

// Create adds a room and records the audit row in the same transaction.
func (s Service) Create(ctx context.Context, actor audit.Actor, in Input) (Room, error) {
	return s.store().Create(ctx, actor, in)
}

// Update replaces a room's fields and records before and after in the audit trail.
func (s Service) Update(ctx context.Context, actor audit.Actor, id string, in Input) (Room, error) {
	return s.store().Update(ctx, actor, id, in)
}

// Delete soft-deletes a room, which also frees its code for reuse.
func (s Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	return s.store().Delete(ctx, actor, id)
}
