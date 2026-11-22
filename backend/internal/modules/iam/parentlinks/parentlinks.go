// Package parentlinks implements IAM-09 parent–student links (decision 0016).
//
// A parent's account is a user linked to a guardian record (users.person_type = 'guardian', users.person_id =
// guardians.id). The guardian's links to students (student_guardians, kept by the Students module) decide which
// children the account sees. The school can mark one link "no access" (for example a custody order): the guardian
// stays on the child's file but their account no longer sees that child.
//
// Rules enforced here (each has a test):
//   - only staff link an account to a guardian; a parent cannot claim a child;
//   - one account per guardian (users_tenant_person_uq), and an account linked to a student cannot become a parent;
//   - a parent sees only children linked to their guardian record, never through a "no access" link, never a
//     deleted student, guardian or link, and only inside their institute;
//   - every change bumps the account's access version, so the parent's apps refetch.
package parentlinks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/stuscope"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

var (
	// ErrNotFound is returned when the account (or link) does not exist in this institute, or the account is not a
	// parent account.
	ErrNotFound = errors.New("parentlinks: not found")
	// ErrStudentAccount is returned when the account is linked to a student and so cannot be a parent's account.
	ErrStudentAccount = errors.New("This account belongs to a student. Use a separate account for the parent.")
)

// Child is one student linked to a parent account's guardian.
type Child struct {
	LinkID           string `json:"linkId"`
	StudentID        string `json:"studentId"`
	StudentName      string `json:"studentName"`
	RegistrationCode string `json:"registrationCode"`
	Relation         string `json:"relation"`
	NoAccess         bool   `json:"noAccess"`
}

// Account is a parent account as the Parent links screen shows it.
type Account struct {
	UserID        string  `json:"userId"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"displayName"`
	Status        string  `json:"status"`
	GuardianID    string  `json:"guardianId"`
	GuardianName  *string `json:"guardianName"`  // nil when the guardian record was deleted
	GuardianPhone *string `json:"guardianPhone"` // nil when the guardian record was deleted
	Children      []Child `json:"children"`
}

// MyChild is a child as the parent sees it.
type MyChild struct {
	StudentID        string `json:"studentId"`
	Name             string `json:"name"`
	RegistrationCode string `json:"registrationCode"`
	Relation         string `json:"relation"`
}

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"displayName": "lower(u.display_name)", "username": "lower(u.username)", "guardianName": "lower(g.name)"}

// DefaultSort lists accounts by name.
const DefaultSort = "displayName"

// Service reads and writes parent links.
type Service struct{ DB tenancy.Beginner }

const accountColumns = `u.id::text, u.username::text, u.display_name, u.status, u.person_id::text, g.name, g.phone`

const accountFrom = `FROM users u
	LEFT JOIN guardians g ON g.tenant_id = u.tenant_id AND g.id = u.person_id AND g.deleted_at IS NULL
	WHERE u.deleted_at IS NULL AND u.person_type = 'guardian'`

func scanAccount(r interface{ Scan(...any) error }, a *Account, extra ...any) error {
	return r.Scan(append([]any{&a.UserID, &a.Username, &a.DisplayName, &a.Status, &a.GuardianID, &a.GuardianName, &a.GuardianPhone}, extra...)...)
}

// List returns one page of parent accounts with their children, and records the read in the audit trail.
func (s Service) List(ctx context.Context, a audit.Actor, q crud.ListQuery) (crud.Page[Account], error) {
	page := crud.Page[Account]{Items: []Account{}, Page: q.Page, Size: q.Size}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		where, args := accountFrom, []any{}
		arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
		if q.Search != "" {
			p := arg(crud.LikePattern(q.Search))
			where += fmt.Sprintf(` AND (u.username::text ILIKE %[1]s ESCAPE '\' OR u.display_name ILIKE %[1]s ESCAPE '\'
				OR g.name ILIKE %[1]s ESCAPE '\' OR g.phone ILIKE %[1]s ESCAPE '\'
				OR EXISTS (SELECT 1 FROM student_guardians sg JOIN students s ON s.tenant_id = sg.tenant_id AND s.id = sg.student_id AND s.deleted_at IS NULL
					WHERE sg.tenant_id = u.tenant_id AND sg.guardian_id = u.person_id AND sg.deleted_at IS NULL
					AND (s.registration_code ILIKE %[1]s ESCAPE '\' OR (s.first_name || ' ' || COALESCE(s.last_name, '')) ILIKE %[1]s ESCAPE '\')))`, p)
		}
		limit, offset := arg(q.Size), arg(q.Offset())
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s, count(*) OVER() %s ORDER BY %s, u.id LIMIT %s OFFSET %s`,
			accountColumns, where, q.OrderBy, limit, offset), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var acc Account
			if err := scanAccount(rows, &acc, &page.Total); err != nil {
				rows.Close()
				return err
			}
			page.Items = append(page.Items, acc)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := attachChildren(ctx, tx, page.Items); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "parent_links.list", ResourceType: "parent_link",
			After: map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != ""}})
	})
	return page, err
}

// attachChildren loads the children of every account's guardian in one query.
func attachChildren(ctx context.Context, tx pgx.Tx, accounts []Account) error {
	if len(accounts) == 0 {
		return nil
	}
	ids := make([]string, len(accounts))
	byGuardian := map[string][]int{}
	for i := range accounts {
		accounts[i].Children = []Child{}
		ids[i] = accounts[i].GuardianID
		byGuardian[accounts[i].GuardianID] = append(byGuardian[accounts[i].GuardianID], i)
	}
	rows, err := tx.Query(ctx, `
		SELECT sg.guardian_id::text, sg.id::text, s.id::text, btrim(s.first_name || ' ' || COALESCE(s.last_name, '')),
			s.registration_code, sg.relation, sg.no_access
		FROM student_guardians sg
		JOIN students s ON s.tenant_id = sg.tenant_id AND s.id = sg.student_id AND s.deleted_at IS NULL
		JOIN guardians g ON g.tenant_id = sg.tenant_id AND g.id = sg.guardian_id AND g.deleted_at IS NULL
		WHERE sg.deleted_at IS NULL AND sg.guardian_id::text = ANY($1::text[])
		ORDER BY lower(s.first_name), lower(COALESCE(s.last_name, '')), s.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var gid string
		var c Child
		if err := rows.Scan(&gid, &c.LinkID, &c.StudentID, &c.StudentName, &c.RegistrationCode, &c.Relation, &c.NoAccess); err != nil {
			return err
		}
		for _, i := range byGuardian[gid] {
			accounts[i].Children = append(accounts[i].Children, c)
		}
	}
	return rows.Err()
}

func getAccount(ctx context.Context, tx pgx.Tx, userID string) (Account, error) {
	var acc Account
	if _, err := uuid.Parse(userID); err != nil {
		return acc, ErrNotFound
	}
	err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` `+accountFrom+` AND u.id = $1::uuid`, userID), &acc)
	if errors.Is(err, pgx.ErrNoRows) {
		return acc, ErrNotFound
	}
	if err != nil {
		return acc, err
	}
	list := []Account{acc}
	if err := attachChildren(ctx, tx, list); err != nil {
		return acc, err
	}
	return list[0], nil
}

// Link makes the account a parent account of the guardian. An account with no person link, or already linked to a
// guardian, can be (re)linked; an account linked to a student cannot. A guardian already linked to another account
// is refused (field error on guardianId).
func (s Service) Link(ctx context.Context, a audit.Actor, userID, guardianID string) (Account, error) {
	var errs crud.FieldErrors
	userID, guardianID = strings.TrimSpace(userID), strings.TrimSpace(guardianID)
	if _, err := uuid.Parse(userID); err != nil {
		errs.Add("userId", "invalid", "Choose an account from the list.")
	}
	if _, err := uuid.Parse(guardianID); err != nil {
		errs.Add("guardianId", "invalid", "Choose a guardian from the list.")
	}
	if err := errs.OrNil(); err != nil {
		return Account{}, err
	}
	var out Account
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var personType, personID *string
		err := tx.QueryRow(ctx, `SELECT person_type, person_id::text FROM users WHERE id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, userID).
			Scan(&personType, &personID)
		if errors.Is(err, pgx.ErrNoRows) {
			return crud.FieldErrors{{Field: "userId", Code: "not_found", Message: "This account is not in this institute."}}
		}
		if err != nil {
			return err
		}
		if personType != nil && *personType != "guardian" {
			return ErrStudentAccount
		}
		var guardianExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM guardians WHERE id = $1::uuid AND deleted_at IS NULL)`, guardianID).Scan(&guardianExists); err != nil {
			return err
		}
		if !guardianExists {
			return crud.FieldErrors{{Field: "guardianId", Code: "not_found", Message: "This guardian is not in this institute."}}
		}
		var before *string
		if personType != nil {
			before = personID
		}
		if before != nil && *before == guardianID {
			out, err = getAccount(ctx, tx, userID)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET person_type = 'guardian', person_id = $2::uuid, access_version = access_version + 1
			WHERE id = $1::uuid AND deleted_at IS NULL`, userID, guardianID); err != nil {
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "users_tenant_person_uq" {
				return crud.FieldErrors{{Field: "guardianId", Code: "duplicate", Message: "Another account is already linked to this guardian."}}
			}
			return err
		}
		if out, err = getAccount(ctx, tx, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "parent_links.link", ResourceType: "user", ResourceID: userID,
			Before: map[string]any{"guardianId": before}, After: map[string]any{"guardianId": guardianID}})
	})
	return out, err
}

// Unlink removes the guardian link from a parent account: the account stays, but sees no children.
func (s Service) Unlink(ctx context.Context, a audit.Actor, userID string) error {
	if _, err := uuid.Parse(userID); err != nil {
		return ErrNotFound
	}
	return tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var guardianID string
		err := tx.QueryRow(ctx, `SELECT person_id::text FROM users WHERE id = $1::uuid AND person_type = 'guardian' AND deleted_at IS NULL FOR UPDATE`, userID).
			Scan(&guardianID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET person_type = NULL, person_id = NULL, access_version = access_version + 1 WHERE id = $1::uuid`, userID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "parent_links.unlink", ResourceType: "user", ResourceID: userID,
			Before: map[string]any{"guardianId": guardianID}, After: map[string]any{"guardianId": nil}})
	})
}

// SetAccess sets or clears "no access" on one guardian–student link and returns the link as the screen shows it.
// Every account of that guardian gets a new access version.
func (s Service) SetAccess(ctx context.Context, a audit.Actor, linkID string, noAccess bool) (Child, error) {
	var c Child
	if _, err := uuid.Parse(linkID); err != nil {
		return c, ErrNotFound
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var before bool
		var guardianID string
		err := tx.QueryRow(ctx, `SELECT no_access, guardian_id::text FROM student_guardians WHERE id = $1::uuid AND deleted_at IS NULL FOR UPDATE`, linkID).
			Scan(&before, &guardianID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if before != noAccess {
			if _, err := tx.Exec(ctx, `UPDATE student_guardians SET no_access = $2::boolean, updated_by = $3::uuid WHERE id = $1::uuid`,
				linkID, noAccess, nullableUUID(a.UserID)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET access_version = access_version + 1
				WHERE person_type = 'guardian' AND person_id = $1::uuid AND deleted_at IS NULL`, guardianID); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `
			SELECT sg.id::text, s.id::text, btrim(s.first_name || ' ' || COALESCE(s.last_name, '')), s.registration_code, sg.relation, sg.no_access
			FROM student_guardians sg JOIN students s ON s.tenant_id = sg.tenant_id AND s.id = sg.student_id
			WHERE sg.id = $1::uuid`, linkID).Scan(&c.LinkID, &c.StudentID, &c.StudentName, &c.RegistrationCode, &c.Relation, &c.NoAccess)
		if err != nil {
			return err
		}
		if before == noAccess {
			return nil
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "parent_links.set_access", ResourceType: "student_guardian", ResourceID: linkID,
			Before: map[string]any{"noAccess": before}, After: map[string]any{"noAccess": noAccess}})
	})
	return c, err
}

// MyChildren returns the children the signed-in account may see (empty for an account that is not a parent's), and
// records the read: guardian and student data are personal data. Nothing is returned while the institute has the
// Students module switched off.
func (s Service) MyChildren(ctx context.Context, a audit.Actor, userID string) ([]MyChild, error) {
	out := []MyChild{}
	if _, err := uuid.Parse(userID); err != nil {
		return out, nil
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, btrim(s.first_name || ' ' || COALESCE(s.last_name, '')), s.registration_code,
				(SELECT sg.relation FROM student_guardians sg JOIN users u ON u.tenant_id = sg.tenant_id AND u.person_type = 'guardian' AND u.person_id = sg.guardian_id
					WHERE sg.student_id = s.id AND sg.deleted_at IS NULL AND NOT sg.no_access AND u.id = $1::uuid LIMIT 1)
			FROM students s
			WHERE s.deleted_at IS NULL
				AND EXISTS (SELECT 1 FROM tenant_modules tm WHERE tm.module_code = 'stu' AND tm.enabled)
				AND `+stuscope.ChildOfAccount("s.id", "$1")+`
			ORDER BY lower(s.first_name), lower(COALESCE(s.last_name, '')), s.id`, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c MyChild
			if err := rows.Scan(&c.StudentID, &c.Name, &c.RegistrationCode, &c.Relation); err != nil {
				rows.Close()
				return err
			}
			out = append(out, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out) == 0 {
			return nil
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "parent_links.my_children", ResourceType: "student",
			After: map[string]any{"count": len(out)}})
	})
	return out, err
}

func nullableUUID(s string) any {
	if _, err := uuid.Parse(s); err != nil {
		return nil
	}
	return s
}
