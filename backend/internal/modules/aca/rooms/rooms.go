// Package rooms implements ACA-24 Rooms. It is the pilot for the CRUD kit: every master-data resource follows this
// shape (validate and normalise, run in an institute-bound transaction, write the audit row in the same transaction).
package rooms

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the room does not exist in this institute (or was deleted).
var ErrNotFound = errors.New("rooms: not found")

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

// Service reads and writes rooms.
type Service struct{ DB tenancy.Beginner }

func scan(row pgx.Row) (Room, error) {
	var r Room
	err := row.Scan(&r.ID, &r.Code, &r.Name, &r.Type, &r.Capacity, &r.Building, &r.Floor, &r.AvailableForTimetable, &r.DisplayOrder, &r.Description, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	return r, err
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

// duplicateCode turns the unique-code violation into a field error the form can show next to the field.
func duplicateCode(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "rooms_tenant_code_uq" {
		var e crud.FieldErrors
		e.Add("code", "duplicate", "Another room already uses this code.")
		return e
	}
	return err
}

// List returns one page of rooms.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Room], error) {
	page := crud.Page[Room]{Items: []Room{}, Page: q.Page, Size: q.Size}
	args := []any{}
	where := "deleted_at IS NULL"
	add := func(cond string, v any) {
		args = append(args, v)
		where += " AND " + strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args)))
	}
	if q.Search != "" {
		add(`(name ILIKE ? ESCAPE '\' OR code ILIKE ? ESCAPE '\' OR building ILIKE ? ESCAPE '\')`, crud.LikePattern(q.Search))
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.AvailableForTimetable != nil {
		add("available_for_timetable = ?", *f.AvailableForTimetable)
	}
	args = append(args, q.Size, q.Offset())

	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		// OrderBy comes only from the SortKeys allow-list (crud.ParseList), never from request text.
		sql := `SELECT ` + columns + `, count(*) OVER() FROM rooms WHERE ` + where +
			` ORDER BY ` + q.OrderBy + `, id LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Room
			if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Type, &r.Capacity, &r.Building, &r.Floor, &r.AvailableForTimetable, &r.DisplayOrder, &r.Description, &r.Status, &r.CreatedAt, &r.UpdatedAt, &page.Total); err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	return page, err
}

// Get returns one room.
func (s Service) Get(ctx context.Context, id string) (Room, error) {
	var r Room
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		r, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM rooms WHERE id::text = $1 AND deleted_at IS NULL`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	return r, err
}

// Create adds a room and records the audit row in the same transaction.
func (s Service) Create(ctx context.Context, actor audit.Actor, in Input) (Room, error) {
	in, err := Normalise(in)
	if err != nil {
		return Room{}, err
	}
	t, ok := tenancy.FromContext(ctx)
	if !ok {
		return Room{}, tenancy.ErrNoTenant
	}
	var r Room
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) (err error) {
		r, err = scan(tx.QueryRow(ctx, `
			INSERT INTO rooms (tenant_id, code, name, room_type, capacity, building, floor, available_for_timetable, display_order, description, status, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid, NULLIF($12, '')::uuid)
			RETURNING `+columns, t.ID, in.Code, in.Name, in.Type, in.Capacity, in.Building, in.Floor, *in.AvailableForTimetable, in.DisplayOrder, in.Description, in.Status, actor.UserID))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "rooms.create", ResourceType: "room", ResourceID: r.ID, After: r})
	})
	if err != nil {
		return Room{}, duplicateCode(err)
	}
	return r, nil
}

// Update replaces a room's fields and records before and after in the audit trail.
func (s Service) Update(ctx context.Context, actor audit.Actor, id string, in Input) (Room, error) {
	in, err := Normalise(in)
	if err != nil {
		return Room{}, err
	}
	var after Room
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM rooms WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, id))
		if err != nil {
			return err
		}
		after, err = scan(tx.QueryRow(ctx, `
			UPDATE rooms SET code = $2, name = $3, room_type = $4, capacity = $5, building = $6, floor = $7,
			       available_for_timetable = $8, display_order = $9, description = $10, status = $11, updated_by = NULLIF($12, '')::uuid
			WHERE id = $1::uuid RETURNING `+columns, id, in.Code, in.Name, in.Type, in.Capacity, in.Building, in.Floor, *in.AvailableForTimetable, in.DisplayOrder, in.Description, in.Status, actor.UserID))
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "rooms.update", ResourceType: "room", ResourceID: id, Before: before, After: after})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Room{}, ErrNotFound
	}
	if err != nil {
		return Room{}, duplicateCode(err)
	}
	return after, nil
}

// Delete soft-deletes a room, which also frees its code for reuse.
func (s Service) Delete(ctx context.Context, actor audit.Actor, id string) error {
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		before, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM rooms WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE rooms SET deleted_at = now(), updated_by = NULLIF($2, '')::uuid WHERE id = $1::uuid`, id, actor.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "rooms.delete", ResourceType: "room", ResourceID: id, Before: before})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
