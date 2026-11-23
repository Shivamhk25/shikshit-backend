// Package institutes implements the Super Admin institute directory (SAD-01), the new-institute wizard (SAD-02) and
// plans and module entitlements (SAD-03), decision 0017.
//
// Every query runs in a platform-tier transaction. The one place institute data is written is the wizard's first
// Admin: the transaction switches to the new institute (tenant tier) for those rows and back, so the whole institute
// is created or nothing is.
//
// Rules enforced here (each has a test):
//   - an institute code is lowercase letters, digits and hyphens, not a reserved name, and unique;
//   - a new institute starts "onboarding" with its plan's modules, the default roles and one invited Admin;
//   - status changes follow one table (transitions); archived is final;
//   - modules can only be ones some plan sells, and iam is always on; changing them bumps every user's access
//     version (database trigger);
//   - every change is in the audit trail as the platform user who made it.
package institutes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/users"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/messaging"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var (
	// ErrNotFound is returned for an unknown institute.
	ErrNotFound = errors.New("institutes: not found")
	// ErrInvalidState is returned for a status change the transitions table does not allow.
	ErrInvalidState = errors.New("This status change is not allowed.")
)

// Statuses an institute can have.
var Statuses = []string{"onboarding", "live", "suspended", "archived"}

// transitions lists the statuses each status may move to. Archived is final.
var transitions = map[string][]string{
	"onboarding": {"live", "archived"},
	"live":       {"suspended"},
	"suspended":  {"live", "archived"},
}

// Reserved codes are subdomains Shikshit uses itself (they match the tenancy middleware's reserved labels).
var Reserved = []string{"www", "api", "admin", "app", "static", "mail", "console", "platform", "status", "help", "docs"}

var codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// Plan is a commercial plan.
type Plan struct {
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Modules []string `json:"modules"`
}

// Institute is an institute as the Super Admin console shows it.
type Institute struct {
	ID         string     `json:"id"`
	Code       string     `json:"code"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	PlanCode   *string    `json:"planCode"`
	PlanName   *string    `json:"planName"`
	Modules    []string   `json:"modules"`
	CreatedAt  time.Time  `json:"createdAt"`
	WentLiveAt *time.Time `json:"wentLiveAt"`
}

// Created is what the wizard returns: the institute and its first Admin's invite link (shown once).
type Created struct {
	Institute
	AdminUsername   string `json:"adminUsername"`
	AdminInviteLink string `json:"adminInviteLink"`
	// AdminEmailQueued is true when the invite link was also queued for email to the Admin.
	AdminEmailQueued bool `json:"adminEmailQueued"`
}

// CreateInput is what the wizard sends.
type CreateInput struct {
	Code, Name, PlanCode string
	Admin                users.CreateInput
}

// Filter narrows the directory.
type Filter struct{ Status string }

// SortKeys are the directory's sort keys.
var SortKeys = crud.Sorts{"name": "lower(t.name)", "code": "t.code", "status": "t.status", "createdAt": "t.created_at"}

// DefaultSort lists institutes by name.
const DefaultSort = "name"

// Service reads and writes institutes.
type Service struct {
	DB tenancy.Beginner
	// Mail emails the first Admin's invite link (FND-19). nil: the link is only returned.
	Mail *messaging.Outbox
}

func (s Service) tx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return tenancy.WithTx(ctx, s.DB, "", tenancy.TierPlatform, fn)
}

const columns = `t.id::text, t.code::text, t.name, t.status, p.code, p.name,
	COALESCE((SELECT array_agg(m.module_code ORDER BY m.module_code) FROM tenant_modules m WHERE m.tenant_id = t.id AND m.enabled), '{}'),
	t.created_at, t.went_live_at`

const from = `FROM tenants t LEFT JOIN plans p ON p.id = t.plan_id WHERE t.deleted_at IS NULL`

func scan(r interface{ Scan(...any) error }, i *Institute, extra ...any) error {
	return r.Scan(append([]any{&i.ID, &i.Code, &i.Name, &i.Status, &i.PlanCode, &i.PlanName, &i.Modules, &i.CreatedAt, &i.WentLiveAt}, extra...)...)
}

// Plans returns the active plans.
func (s Service) Plans(ctx context.Context) ([]Plan, error) {
	out := []Plan{}
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code, name, modules FROM plans WHERE is_active ORDER BY cardinality(modules), code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Plan
			if err := rows.Scan(&p.Code, &p.Name, &p.Modules); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// List returns one page of institutes.
func (s Service) List(ctx context.Context, q crud.ListQuery, f Filter) (crud.Page[Institute], error) {
	page := crud.Page[Institute]{Items: []Institute{}, Page: q.Page, Size: q.Size}
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		where, args := from, []any{}
		arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
		if q.Search != "" {
			p := arg(crud.LikePattern(q.Search))
			where += fmt.Sprintf(` AND (t.name ILIKE %[1]s ESCAPE '\' OR t.code::text ILIKE %[1]s ESCAPE '\')`, p)
		}
		if f.Status != "" {
			where += " AND t.status = " + arg(f.Status)
		}
		limit, offset := arg(q.Size), arg(q.Offset())
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s, count(*) OVER() %s ORDER BY %s, t.id LIMIT %s OFFSET %s`, columns, where, q.OrderBy, limit, offset), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var i Institute
			if err := scan(rows, &i, &page.Total); err != nil {
				return err
			}
			page.Items = append(page.Items, i)
		}
		return rows.Err()
	})
	return page, err
}

func get(ctx context.Context, tx pgx.Tx, id string) (Institute, error) {
	var i Institute
	if _, err := uuid.Parse(id); err != nil {
		return i, ErrNotFound
	}
	err := scan(tx.QueryRow(ctx, `SELECT `+columns+` `+from+` AND t.id = $1::uuid`, id), &i)
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// Get returns one institute.
func (s Service) Get(ctx context.Context, id string) (Institute, error) {
	var out Institute
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = get(ctx, tx, id)
		return err
	})
	return out, err
}

// NormaliseCode trims and lowercases an institute code and checks it.
func NormaliseCode(errs *crud.FieldErrors, code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	switch {
	case !codePattern.MatchString(code):
		errs.Add("code", "invalid", "Use 2 to 63 lowercase letters, digits or hyphens, starting with a letter or digit.")
	case contains(Reserved, code):
		errs.Add("code", "reserved", "This name is reserved. Choose another.")
	}
	return code
}

func planID(ctx context.Context, tx pgx.Tx, code string) (string, []string, error) {
	var id string
	var modules []string
	err := tx.QueryRow(ctx, `SELECT id::text, modules FROM plans WHERE code = $1 AND is_active`, code).Scan(&id, &modules)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, crud.FieldErrors{{Field: "planCode", Code: "not_found", Message: "Choose a plan from the list."}}
	}
	return id, modules, err
}

// asTenant switches the open transaction to the given institute (tenant tier) or back to the platform tier ("").
func asTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	tier := tenancy.TierPlatform
	if tenantID != "" {
		tier = tenancy.TierTenant
	}
	_, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true), set_config('app.actor_tier', $2, true)`, tenantID, string(tier))
	return err
}

// Create runs the new-institute wizard: the institute (onboarding), its plan's modules, the default roles and an
// invited first Admin, all in one transaction.
func (s Service) Create(ctx context.Context, a audit.Actor, in CreateInput) (Created, error) {
	var errs crud.FieldErrors
	in.Code = NormaliseCode(&errs, in.Code)
	in.Name = strings.TrimSpace(in.Name)
	errs.Required("name", "the institute's name", in.Name, 200)
	in.PlanCode = strings.TrimSpace(in.PlanCode)
	errs.Required("planCode", "a plan", in.PlanCode, 50)
	if _, err := users.NormaliseCreate(in.Admin); err != nil {
		var fe crud.FieldErrors
		if errors.As(err, &fe) {
			for _, e := range fe {
				errs.Add("admin."+e.Field, e.Code, e.Message)
			}
		}
	}
	if err := errs.OrNil(); err != nil {
		return Created{}, err
	}
	var out Created
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		pid, modules, err := planID(ctx, tx, in.PlanCode)
		if err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO tenants (code, name, plan_id, status) VALUES ($1::citext, $2, $3::uuid, 'onboarding') RETURNING id::text`,
			in.Code, in.Name, pid).Scan(&id); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return crud.FieldErrors{{Field: "code", Code: "duplicate", Message: "Another institute already uses this code."}}
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_modules (tenant_id, module_code, enabled_by) SELECT $1::uuid, m, NULL FROM unnest($2::text[]) AS m`, id, modules); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT provision_tenant_roles($1::uuid)`, id); err != nil {
			return err
		}
		if err := asTenant(ctx, tx, id); err != nil {
			return err
		}
		admin, err := users.CreateFirstAdmin(ctx, tx, id, in.Admin, a.UserID)
		if err != nil {
			var fe crud.FieldErrors
			if errors.As(err, &fe) {
				for i := range fe {
					fe[i].Field = "admin." + fe[i].Field
				}
				return fe
			}
			return err
		}
		if err := asTenant(ctx, tx, ""); err != nil {
			return err
		}
		inst, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		out = Created{Institute: inst, AdminUsername: admin.Username, AdminInviteLink: admin.InviteLink}
		if s.Mail != nil && admin.Email != nil {
			// Queued in the platform session (restored above): the job belongs to no institute, like the wizard.
			out.AdminEmailQueued, err = s.Mail.QueueAccountLink(ctx, tx, messaging.AccountLink{To: *admin.Email, Name: admin.DisplayName,
				Place: inst.Name, Username: admin.Username, Link: s.Mail.Links.School(inst.Code, admin.InviteLink),
				Hours: int(users.InviteTTL.Hours()), Invite: true})
			if err != nil {
				return err
			}
		}
		// the invite link is never written to the audit trail: it would let anyone reading the log take the account
		return audit.RecordPlatform(ctx, tx, a, id, audit.Entry{Action: "institutes.create", ResourceType: "institute", ResourceID: id,
			After: map[string]any{"code": inst.Code, "name": inst.Name, "plan": in.PlanCode, "modules": inst.Modules, "adminUserId": admin.ID}})
	})
	return out, err
}

// Update changes the name or the plan. Changing the plan does not change the modules: SetModules does that, so an
// institute never loses a module by surprise.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, name, planCode *string) (Institute, error) {
	var errs crud.FieldErrors
	if name != nil {
		*name = strings.TrimSpace(*name)
		errs.Required("name", "the institute's name", *name, 200)
	}
	if err := errs.OrNil(); err != nil {
		return Institute{}, err
	}
	var out Institute
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if name != nil {
			if _, err := tx.Exec(ctx, `UPDATE tenants SET name = $2 WHERE id = $1::uuid`, id, *name); err != nil {
				return err
			}
		}
		if planCode != nil {
			pid, _, err := planID(ctx, tx, strings.TrimSpace(*planCode))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tenants SET plan_id = $2::uuid WHERE id = $1::uuid`, id, pid); err != nil {
				return err
			}
		}
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, a, id, audit.Entry{Action: "institutes.update", ResourceType: "institute", ResourceID: id, Before: before, After: out})
	})
	return out, err
}

// SetStatus moves an institute through its lifecycle. Going live the first time records when.
func (s Service) SetStatus(ctx context.Context, a audit.Actor, id, status string) (Institute, error) {
	var out Institute
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if !contains(transitions[before.Status], status) {
			return ErrInvalidState
		}
		if _, err := tx.Exec(ctx, `UPDATE tenants SET status = $2, went_live_at = CASE WHEN $2 = 'live' THEN COALESCE(went_live_at, now()) ELSE went_live_at END
			WHERE id = $1::uuid`, id, status); err != nil {
			return err
		}
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, a, id, audit.Entry{Action: "institutes.status", ResourceType: "institute", ResourceID: id,
			Before: map[string]string{"status": before.Status}, After: map[string]string{"status": status}})
	})
	return out, err
}

// SetModules replaces the institute's enabled modules. Turning a module off keeps its data and its role grants; the
// module simply disappears for the institute until it is turned on again. Every user's access version changes (a
// database trigger on tenant_modules does it).
func (s Service) SetModules(ctx context.Context, a audit.Actor, id string, modules []string) (Institute, error) {
	var out Institute
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		before, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		var sold []string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(DISTINCT m), '{}') FROM plans, unnest(modules) AS m`).Scan(&sold); err != nil {
			return err
		}
		want := []string{}
		var errs crud.FieldErrors
		for _, m := range modules {
			m = strings.ToLower(strings.TrimSpace(m))
			if !contains(sold, m) {
				errs.Add("modules", "invalid_choice", fmt.Sprintf("%q is not a module any plan includes.", m))
				continue
			}
			if !contains(want, m) {
				want = append(want, m)
			}
		}
		if !contains(want, "iam") {
			errs.Add("modules", "required", "Users and roles (iam) cannot be turned off: nobody could sign in to manage the school.")
		}
		if err := errs.OrNil(); err != nil {
			return err
		}
		sort.Strings(want)
		if _, err := tx.Exec(ctx, `UPDATE tenant_modules SET enabled = (module_code = ANY($2::text[])) WHERE tenant_id = $1::uuid`, id, want); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO tenant_modules (tenant_id, module_code) SELECT $1::uuid, m FROM unnest($2::text[]) AS m
			ON CONFLICT (tenant_id, module_code) DO UPDATE SET enabled = true`, id, want); err != nil {
			return err
		}
		// the tenant_modules trigger (000004) bumps every user's access version, so cached access is dropped
		if out, err = get(ctx, tx, id); err != nil {
			return err
		}
		return audit.RecordPlatform(ctx, tx, a, id, audit.Entry{Action: "institutes.modules", ResourceType: "institute", ResourceID: id,
			Before: map[string]any{"modules": before.Modules}, After: map[string]any{"modules": out.Modules}})
	})
	return out, err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
