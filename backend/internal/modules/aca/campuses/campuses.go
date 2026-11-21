// Package campuses implements ACA-11 Campuses.
package campuses

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the campus does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Campus is a campus as the API shows it.
type Campus struct {
	ID             string    `json:"id"`
	Code           *string   `json:"code"`
	Name           string    `json:"name"`
	CheckInRadiusM int       `json:"checkInRadiusM"`
	Latitude       *float64  `json:"latitude"`
	Longitude      *float64  `json:"longitude"`
	StaffDueIn     *string   `json:"staffDueIn"`
	Address        *string   `json:"address"`
	Remarks        *string   `json:"remarks"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Input is what create and replace accept.
type Input struct {
	Code           *string
	Name           string
	CheckInRadiusM int
	Latitude       *float64
	Longitude      *float64
	StaffDueIn     *string
	Address        *string
	Remarks        *string
	Status         string
}

// Filter narrows a list.
type Filter struct{ Status string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"code": "lower(code)", "name": "name", "createdAt": "created_at"}

// DefaultSort orders by name.
const DefaultSort = "name"

const columns = `id::text, code, name, check_in_radius_m, latitude::float8, longitude::float8, staff_due_in, address, remarks, status, created_at, updated_at`

func scan(r resource.Scanner) (Campus, error) {
	var c Campus
	err := r.Scan(&c.ID, &c.Code, &c.Name, &c.CheckInRadiusM, &c.Latitude, &c.Longitude, &c.StaffDueIn, &c.Address, &c.Remarks, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

var timeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// Normalise trims, defaults and validates. Latitude and longitude are set together or not at all.
func Normalise(in Input) (Input, error) {
	var errs crud.FieldErrors
	in.Name = strings.TrimSpace(in.Name)
	in.Code, in.StaffDueIn, in.Address, in.Remarks = crud.TrimPtr(in.Code), crud.TrimPtr(in.StaffDueIn), crud.TrimPtr(in.Address), crud.TrimPtr(in.Remarks)
	if in.Status == "" {
		in.Status = "active"
	}
	errs.Optional("code", "code", in.Code, 20)
	errs.Required("name", "a name", in.Name, 100)
	errs.Range("checkInRadiusM", "check-in radius", in.CheckInRadiusM, 10, 5000)
	if (in.Latitude == nil) != (in.Longitude == nil) {
		errs.Add("longitude", "incomplete_position", "Enter both latitude and longitude, or neither.")
	}
	if in.Latitude != nil && (*in.Latitude < -90 || *in.Latitude > 90) {
		errs.Add("latitude", "out_of_range", "Latitude must be between -90 and 90.")
	}
	if in.Longitude != nil && (*in.Longitude < -180 || *in.Longitude > 180) {
		errs.Add("longitude", "out_of_range", "Longitude must be between -180 and 180.")
	}
	if in.StaffDueIn != nil && !timeRE.MatchString(*in.StaffDueIn) {
		errs.Add("staffDueIn", "invalid_time", "Use a 24-hour time like 08:30.")
	}
	errs.Optional("address", "address", in.Address, 300)
	errs.Optional("remarks", "remarks", in.Remarks, 500)
	errs.Choice("status", in.Status, "active", "inactive")
	return in, errs.OrNil()
}

var spec = resource.Spec[Campus, Input]{
	Table: "campuses", Action: "campuses", ResourceType: "campus",
	Columns: columns, Scan: scan, ID: func(c Campus) string { return c.ID },
	WriteColumns: []string{"code", "name", "check_in_radius_m", "latitude", "longitude", "staff_due_in", "address", "remarks", "status"},
	Args: func(in Input) []any {
		return []any{in.Code, in.Name, in.CheckInRadiusM, in.Latitude, in.Longitude, in.StaffDueIn, in.Address, in.Remarks, in.Status}
	},
	SearchColumns: []string{"code", "name", "address"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Constraints: map[string]crud.FieldError{
		"campuses_tenant_code_uq": {Field: "code", Code: "duplicate", Message: "Another campus already uses this code."},
	},
}

// Service reads and writes campuses.
type Service struct{ DB tenancy.Beginner }

// Store exposes the resource store, for the import engine, which writes through the same rules as the screens.
func (s Service) Store() resource.Store[Campus, Input] { return s.store() }

func (s Service) store() resource.Store[Campus, Input] {
	return resource.Store[Campus, Input]{DB: s.DB, Spec: spec}
}

// List returns one page of campuses.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Campus], error) {
	var conds []resource.Cond
	if f.Status != "" {
		conds = append(conds, resource.Cond{SQL: "status = ?", Args: []any{f.Status}})
	}
	return s.store().List(ctx, q, conds...)
}

// Get returns one campus.
func (s Service) Get(ctx context.Context, id string) (Campus, error) { return s.store().Get(ctx, id) }

// Create adds a campus.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Campus, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a campus's fields.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Campus, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a campus.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
