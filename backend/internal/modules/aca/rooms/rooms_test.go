package rooms_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pie360/backend/internal/modules/aca/rooms"
	"github.com/pie360/backend/internal/platform/audit"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

type fixture struct {
	svc          rooms.Service
	admin        *pgxpool.Pool
	alpha, bravo tenancy.Tenant
	actor        audit.Actor
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.New(t)
	adm := d.Admin(t)
	mk := func(code string) tenancy.Tenant {
		var id string
		if err := adm.QueryRow(bg, "INSERT INTO tenants (code, name) VALUES ($1::text, $1::text) RETURNING id::text", code).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return tenancy.Tenant{ID: id, Code: code, Status: "live"}
	}
	f := fixture{alpha: mk("alpha"), bravo: mk("bravo")}
	var uid string
	if err := adm.QueryRow(bg, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, 'asha', 'Asha', 'active') RETURNING id::text`, f.alpha.ID).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	f.actor = audit.Actor{UserID: uid, Source: "web", RequestID: "req-1", IP: "203.0.113.5", UserAgent: "test"}

	cfg, _ := pgxpool.ParseConfig(d.AppDSN)
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	acfg, _ := pgxpool.ParseConfig(d.AdminDSN)
	f.admin, err = pgxpool.NewWithConfig(bg, acfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.admin.Close)
	f.svc = rooms.Service{DB: pool}
	return f
}

func in(t tenancy.Tenant) context.Context { return tenancy.WithTenant(bg, t) }

func ptr(s string) *string { return &s }

func valid(name string) rooms.Input { return rooms.Input{Name: name, Capacity: 30} }

func list(t *testing.T, f fixture, ten tenancy.Tenant, page, size int, sort, q string, flt rooms.Filter) crud.Page[rooms.Room] {
	t.Helper()
	lq, err := crud.ParseList(page, size, sort, q, rooms.SortKeys, rooms.DefaultSort)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.List(in(ten), lq, flt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateGetUpdateDelete(t *testing.T) {
	f := setup(t)
	r, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr(" R-101 "), Name: "  Room 101 ", Type: ptr("classroom"), Capacity: 40, Building: ptr("Main"), Floor: ptr("1"), DisplayOrder: 5})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Room 101" || *r.Code != "R-101" || r.Status != "active" || !r.AvailableForTimetable {
		t.Errorf("normalisation/defaults: %+v", r)
	}
	got, err := f.svc.Get(in(f.alpha), r.ID)
	if err != nil || got.ID != r.ID {
		t.Fatalf("get: %v %+v", err, got)
	}

	no := false
	upd, err := f.svc.Update(in(f.alpha), f.actor, r.ID, rooms.Input{Name: "Lab 1", Type: ptr("lab"), Capacity: 25, AvailableForTimetable: &no, Status: "inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "Lab 1" || upd.Code != nil || upd.AvailableForTimetable || upd.Status != "inactive" || upd.Building != nil {
		t.Errorf("update must replace every field: %+v", upd)
	}

	if err := f.svc.Delete(in(f.alpha), f.actor, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(in(f.alpha), r.ID); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := f.svc.Delete(in(f.alpha), f.actor, r.ID); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
	if _, err := f.svc.Update(in(f.alpha), f.actor, r.ID, valid("x")); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("update after delete: %v", err)
	}
}

func TestValidationReportsEveryFieldAtOnce(t *testing.T) {
	f := setup(t)
	_, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Name: "  ", Capacity: 0, Type: ptr("garage"), DisplayOrder: -1, Status: "weird"})
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v", err)
	}
	got := map[string]string{}
	for _, e := range fe {
		got[e.Field] = e.Code
	}
	for field, code := range map[string]string{"name": "required", "capacity": "out_of_range", "type": "invalid_choice", "displayOrder": "out_of_range", "status": "invalid_choice"} {
		if got[field] != code {
			t.Errorf("%s: %q, want %q (all: %v)", field, got[field], code, got)
		}
	}
	if p := list(t, f, f.alpha, 1, 50, "", "", rooms.Filter{}); p.Total != 0 {
		t.Error("an invalid create must store nothing")
	}
}

func TestDuplicateCodeIsAFieldErrorAndSoftDeleteFreesIt(t *testing.T) {
	f := setup(t)
	a, _ := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr("A1"), Name: "A", Capacity: 10})
	_, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr("a1"), Name: "B", Capacity: 10}) // case-insensitive
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || fe[0].Field != "code" || fe[0].Code != "duplicate" {
		t.Fatalf("err = %v", err)
	}
	// blank codes never collide
	for _, n := range []string{"x", "y"} {
		if _, err := f.svc.Create(in(f.alpha), f.actor, valid(n)); err != nil {
			t.Fatalf("two rooms without a code must be allowed: %v", err)
		}
	}
	if err := f.svc.Delete(in(f.alpha), f.actor, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr("A1"), Name: "again", Capacity: 10}); err != nil {
		t.Fatalf("code of a deleted room must be reusable: %v", err)
	}
	// the same code in another institute is fine
	if _, err := f.svc.Create(in(f.bravo), f.actor, rooms.Input{Code: ptr("A1"), Name: "other school", Capacity: 10}); err != nil {
		t.Fatalf("codes are unique per institute only: %v", err)
	}
}

func TestInstitutesCannotSeeOrTouchEachOthersRooms(t *testing.T) {
	f := setup(t)
	mine, _ := f.svc.Create(in(f.alpha), f.actor, valid("alpha room"))
	if p := list(t, f, f.bravo, 1, 50, "", "", rooms.Filter{}); p.Total != 0 {
		t.Fatalf("bravo sees %d rooms", p.Total)
	}
	if _, err := f.svc.Get(in(f.bravo), mine.ID); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("get across institutes: %v", err)
	}
	if _, err := f.svc.Update(in(f.bravo), f.actor, mine.ID, valid("hijack")); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("update across institutes: %v", err)
	}
	if err := f.svc.Delete(in(f.bravo), f.actor, mine.ID); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("delete across institutes: %v", err)
	}
	if got, _ := f.svc.Get(in(f.alpha), mine.ID); got.Name != "alpha room" {
		t.Errorf("owner's room was changed: %+v", got)
	}
}

func TestListPagingSortingAndFilters(t *testing.T) {
	f := setup(t)
	for i, n := range []string{"Gamma", "Alpha", "Delta", "Beta", "Epsilon"} {
		cap := 10 * (i + 1)
		if _, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Name: n, Capacity: cap, DisplayOrder: 10 - i}); err != nil {
			t.Fatal(err)
		}
	}
	p := list(t, f, f.alpha, 1, 2, "name", "", rooms.Filter{})
	if p.Total != 5 || len(p.Items) != 2 || p.Items[0].Name != "Alpha" || p.Items[1].Name != "Beta" {
		t.Errorf("page 1: %+v", p)
	}
	p = list(t, f, f.alpha, 3, 2, "name", "", rooms.Filter{})
	if len(p.Items) != 1 || p.Items[0].Name != "Gamma" {
		t.Errorf("page 3: %+v", p.Items)
	}
	if p = list(t, f, f.alpha, 1, 50, "-capacity", "", rooms.Filter{}); p.Items[0].Name != "Epsilon" {
		t.Errorf("sort -capacity: %v", p.Items[0].Name)
	}
	if p = list(t, f, f.alpha, 1, 50, "", "", rooms.Filter{}); p.Items[0].Name != "Epsilon" {
		t.Errorf("default sort is display order: first = %v", p.Items[0].Name)
	}
	if p = list(t, f, f.alpha, 1, 50, "", "ALP", rooms.Filter{}); p.Total != 1 || p.Items[0].Name != "Alpha" {
		t.Errorf("search is case-insensitive: %+v", p)
	}
	// total reflects the filter, not the page
	if p = list(t, f, f.alpha, 1, 1, "", "a", rooms.Filter{}); p.Total < 4 || len(p.Items) != 1 {
		t.Errorf("total with filter: %d items %d", p.Total, len(p.Items))
	}
	if p = list(t, f, f.alpha, 1, 50, "", "", rooms.Filter{Status: "inactive"}); p.Total != 0 {
		t.Errorf("status filter: %d", p.Total)
	}
	no := false
	if p = list(t, f, f.alpha, 1, 50, "", "", rooms.Filter{AvailableForTimetable: &no}); p.Total != 0 {
		t.Errorf("availability filter: %d", p.Total)
	}
}

func TestSearchTreatsWildcardsAsPlainText(t *testing.T) {
	f := setup(t)
	f.svc.Create(in(f.alpha), f.actor, valid("100% Hall"))
	f.svc.Create(in(f.alpha), f.actor, valid("Room A"))
	f.svc.Create(in(f.alpha), f.actor, valid("Room_B"))
	if p := list(t, f, f.alpha, 1, 50, "", "%", rooms.Filter{}); p.Total != 1 {
		t.Errorf("'%%' matched %d rooms, want only the one that contains it", p.Total)
	}
	if p := list(t, f, f.alpha, 1, 50, "", "_", rooms.Filter{}); p.Total != 1 {
		t.Errorf("'_' matched %d rooms, want 1", p.Total)
	}
}

func TestEveryChangeIsAuditedInTheSameTransaction(t *testing.T) {
	f := setup(t)
	r, _ := f.svc.Create(in(f.alpha), f.actor, valid("Hall"))
	f.svc.Update(in(f.alpha), f.actor, r.ID, rooms.Input{Name: "Great Hall", Capacity: 300})
	f.svc.Delete(in(f.alpha), f.actor, r.ID)

	rows, err := f.admin.Query(bg, `SELECT action, actor_id::text, source, request_id, before, after FROM audit_log WHERE resource_type = 'room' AND resource_id = $1 ORDER BY id`, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var action, actor, source, req string
		var before, after []byte
		if err := rows.Scan(&action, &actor, &source, &req, &before, &after); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		if actor != f.actor.UserID || source != "web" || req != "req-1" {
			t.Errorf("%s: actor/source/request = %s/%s/%s", action, actor, source, req)
		}
		switch action {
		case "rooms.create":
			if before != nil || !strings.Contains(string(after), "Hall") {
				t.Errorf("create snapshots: %s | %s", before, after)
			}
		case "rooms.update":
			var b, a map[string]any
			_ = json.Unmarshal(before, &b)
			_ = json.Unmarshal(after, &a)
			if b["name"] != "Hall" || a["name"] != "Great Hall" {
				t.Errorf("update must hold before and after: %v -> %v", b["name"], a["name"])
			}
		case "rooms.delete":
			if before == nil || after != nil {
				t.Errorf("delete snapshots: %s | %s", before, after)
			}
		}
	}
	if strings.Join(actions, ",") != "rooms.create,rooms.update,rooms.delete" {
		t.Errorf("audit trail = %v", actions)
	}
}

func TestFailedChangeLeavesNoAuditRow(t *testing.T) {
	f := setup(t)
	f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr("X"), Name: "one", Capacity: 1})
	if _, err := f.svc.Create(in(f.alpha), f.actor, rooms.Input{Code: ptr("X"), Name: "two", Capacity: 1}); err == nil {
		t.Fatal("duplicate accepted")
	}
	var n int
	_ = f.admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'rooms.create'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d create audit rows for 1 stored room", n)
	}
}

func TestInvalidIDIsNotFoundNotAnError(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.Get(in(f.alpha), "not-a-uuid"); !errors.Is(err, rooms.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
}
