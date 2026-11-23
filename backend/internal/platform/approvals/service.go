package approvals

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Statuses of a request.
const (
	StatusPending   = "pending"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
)

const (
	maxComment = 1000
	maxSummary = 300
	// inboxScan bounds how many open requests one inbox read examines.
	inboxScan = 1000
	// maxApprovers bounds how many people are looked at when choosing who to notify.
	maxApprovers = 500
)

// ErrNotFound means the request does not exist in this institute, or the caller has no part in it. The two look the
// same on purpose, so that ids cannot be probed.
var ErrNotFound = errors.New("approvals: not found")

// ConflictError means the request is not in a state that allows this. The API answers 409.
type ConflictError struct{ Message string }

func (e ConflictError) Error() string { return "approvals: " + e.Message }

// ForbiddenError means the caller can see the request but may not do this to it. The API answers 403.
type ForbiddenError struct{ Message string }

func (e ForbiddenError) Error() string { return "approvals: " + e.Message }

// Request is one thing waiting for, or given, a decision.
type Request struct {
	ID            string     `json:"id"`
	TypeKey       string     `json:"typeKey"`
	TypeTitle     string     `json:"typeTitle"`
	SubjectID     string     `json:"subjectId"`
	Summary       string     `json:"summary"`
	Amount        *float64   `json:"amount"`
	ScopeType     *string    `json:"-"`
	ScopeRef      *string    `json:"-"`
	RequesterID   string     `json:"requesterId"`
	RequesterName string     `json:"requesterName"`
	Status        string     `json:"status"`
	CurrentStep   int        `json:"currentStep"`
	StepCount     int        `json:"stepCount"`
	StepName      string     `json:"stepName"`
	CreatedAt     time.Time  `json:"createdAt"`
	DecidedAt     *time.Time `json:"decidedAt"`
}

// StepInfo is one stage of a request and how it went.
type StepInfo struct {
	No          int
	Name        string
	Permission  string
	Status      string
	DecidedBy   *string
	DeciderName *string
	DecidedAt   *time.Time
}

// Event is one line of a request's history.
type Event struct {
	StepNo    *int
	ActorID   string
	ActorName string
	Action    string
	Comment   string
	CreatedAt time.Time
}

// Detail is a request with its steps and history, as one viewer may act on it.
type Detail struct {
	Request
	Steps     []StepInfo
	Events    []Event
	CanDecide bool
	CanCancel bool
}

// Page is one page of requests.
type Page struct {
	Items      []Request
	Page, Size int
	Total      int
}

// AccessResolver builds a user's access (authz.Service satisfies it). It is used to find whom to notify.
type AccessResolver interface {
	Resolve(ctx context.Context, userID string) (authz.Access, error)
}

// Service runs approvals.
type Service struct {
	DB  tenancy.Beginner
	Reg *Registry
	// Access finds the people who may decide a step, to notify them. Without it nobody is notified.
	Access AccessResolver
}

// SubmitInput describes a record put up for approval.
type SubmitInput struct {
	TypeKey   string
	SubjectID string
	Summary   string
	Amount    *float64
}

// MayDecide reports whether access lets its owner decide a step that needs permission, for a request about the
// record (scopeType, scopeRef); both nil means the request is not scoped. A user whose scope for the permission's
// resource covers the whole institute may decide any; otherwise they need an assignment to that very record.
func MayDecide(a authz.Access, permission string, scopeType, scopeRef *string) bool {
	if !a.Effective.Can(permission) {
		return false
	}
	if scopeType == nil || scopeRef == nil {
		return true
	}
	resource := permission[:strings.LastIndexByte(permission, '.')+1]
	resource = strings.TrimSuffix(resource, ".")
	for _, sc := range a.Effective.Scopes[resource] {
		if sc == authz.ScopeTenant || sc == *scopeType+":"+*scopeRef {
			return true
		}
	}
	return false
}

func (s Service) title(key string) string {
	if t, ok := s.Reg.Get(key); ok {
		return t.Title
	}
	return key
}

func trunc(v string, n int) string {
	if utf8.RuneCountInString(v) <= n {
		return v
	}
	return string([]rune(v)[:n-1]) + "…"
}

// Submit puts a record up for approval inside tx, which must be the caller's institute transaction, so the request
// exists only if the caller's own change commits. The requester is actor.UserID. It answers a ConflictError when the
// subject already has an open request of this type.
func (s Service) Submit(ctx context.Context, tx pgx.Tx, actor audit.Actor, in SubmitInput) (Request, error) {
	t, ok := s.Reg.Get(in.TypeKey)
	if !ok {
		return Request{}, fmt.Errorf("approvals: unknown workflow type %q", in.TypeKey)
	}
	in.SubjectID, in.Summary = strings.TrimSpace(in.SubjectID), strings.TrimSpace(in.Summary)
	var fe crud.FieldErrors
	fe.Required("subjectId", "a subject", in.SubjectID, 100)
	if utf8.RuneCountInString(in.Summary) > maxSummary {
		fe.Add("summary", "too_long", "The summary is at most 300 characters.")
	}
	if in.Amount != nil && *in.Amount < 0 {
		fe.Add("amount", "invalid", "The amount cannot be negative.")
	}
	if err := fe.OrNil(); err != nil {
		return Request{}, err
	}
	var scopeType, scopeRef *string
	if t.Scope != nil {
		st, sr, err := t.Scope(ctx, tx, in.SubjectID)
		if err != nil {
			return Request{}, err
		}
		if (st == "") != (sr == "") {
			return Request{}, fmt.Errorf("approvals: type %q scope resolver returned half a scope", t.Key)
		}
		if st != "" {
			scopeType, scopeRef = &st, &sr
		}
	}
	tenant, ok := tenancy.FromContext(ctx)
	if !ok {
		return Request{}, tenancy.ErrNoTenant
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO approval_requests (tenant_id, type_key, subject_id, summary, amount, scope_type, scope_ref, requester_id, current_step, step_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uuid, 1, $9)
		ON CONFLICT (tenant_id, type_key, subject_id) WHERE status = 'pending' DO NOTHING RETURNING id::text`,
		tenant.ID, t.Key, in.SubjectID, in.Summary, in.Amount, scopeType, scopeRef, actor.UserID, len(t.Steps)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, ConflictError{"This already has an open approval request."}
	}
	if err != nil {
		return Request{}, err
	}
	for i, st := range t.Steps {
		if _, err := tx.Exec(ctx, `INSERT INTO approval_steps (tenant_id, request_id, step_no, name, permission) VALUES ($1, $2::uuid, $3, $4, $5)`,
			tenant.ID, id, i+1, st.Name, st.Permission); err != nil {
			return Request{}, err
		}
	}
	if err := s.event(ctx, tx, id, nil, actor.UserID, "submitted", ""); err != nil {
		return Request{}, err
	}
	if err := audit.Record(ctx, tx, actor, audit.Entry{Action: "approval.submit", ResourceType: "approval_request", ResourceID: id,
		After: map[string]any{"type": t.Key, "subject": in.SubjectID, "summary": in.Summary}}); err != nil {
		return Request{}, err
	}
	r, _, _, err := s.load(ctx, tx, id, false)
	if err != nil {
		return Request{}, err
	}
	return r, s.notifyApprovers(ctx, tx, r, t.Steps[0].Permission)
}

func (s Service) event(ctx context.Context, tx pgx.Tx, id string, step *int, actorID, action, comment string) error {
	_, err := tx.Exec(ctx, `INSERT INTO approval_events (tenant_id, request_id, step_no, actor_id, action, comment)
		VALUES (app_current_tenant(), $1::uuid, $2, $3::uuid, $4, $5)`, id, step, actorID, action, comment)
	return err
}

const requestColumns = `r.id::text, r.type_key, r.subject_id, r.summary, r.amount::float8, r.scope_type, r.scope_ref, r.requester_id::text, u.display_name,
	r.status, r.current_step, r.step_count, COALESCE(cs.name, ''), r.created_at, r.decided_at`

const requestFrom = `FROM approval_requests r JOIN users u ON u.tenant_id = r.tenant_id AND u.id = r.requester_id
	LEFT JOIN approval_steps cs ON cs.request_id = r.id AND cs.step_no = r.current_step`

// scanRequest reads requestColumns; extra are scanned after them for queries that select more.
func scanRequest(r pgx.Row, extra ...any) (Request, error) {
	var q Request
	err := r.Scan(append([]any{&q.ID, &q.TypeKey, &q.SubjectID, &q.Summary, &q.Amount, &q.ScopeType, &q.ScopeRef, &q.RequesterID, &q.RequesterName,
		&q.Status, &q.CurrentStep, &q.StepCount, &q.StepName, &q.CreatedAt, &q.DecidedAt}, extra...)...)
	return q, err
}

// load reads a request with its steps and events; lock takes a row lock so that decisions on it run one at a time.
func (s Service) load(ctx context.Context, tx pgx.Tx, id string, lock bool) (Request, []StepInfo, []Event, error) {
	q := `SELECT ` + requestColumns + ` ` + requestFrom + ` WHERE r.id = $1::uuid`
	if lock {
		q += ` FOR UPDATE OF r`
	}
	r, err := scanRequest(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, nil, nil, ErrNotFound
	}
	if err != nil {
		return Request{}, nil, nil, err
	}
	r.TypeTitle = s.title(r.TypeKey)
	rows, err := tx.Query(ctx, `SELECT s.step_no, s.name, s.permission, s.status, s.decided_by::text, d.display_name, s.decided_at
		FROM approval_steps s LEFT JOIN users d ON d.tenant_id = s.tenant_id AND d.id = s.decided_by WHERE s.request_id = $1::uuid ORDER BY s.step_no`, id)
	if err != nil {
		return Request{}, nil, nil, err
	}
	var steps []StepInfo
	for rows.Next() {
		var st StepInfo
		if err := rows.Scan(&st.No, &st.Name, &st.Permission, &st.Status, &st.DecidedBy, &st.DeciderName, &st.DecidedAt); err != nil {
			rows.Close()
			return Request{}, nil, nil, err
		}
		steps = append(steps, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Request{}, nil, nil, err
	}
	rows, err = tx.Query(ctx, `SELECT e.step_no, e.actor_id::text, a.display_name, e.action, e.comment, e.created_at
		FROM approval_events e JOIN users a ON a.tenant_id = e.tenant_id AND a.id = e.actor_id WHERE e.request_id = $1::uuid ORDER BY e.created_at, e.id`, id)
	if err != nil {
		return Request{}, nil, nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.StepNo, &e.ActorID, &e.ActorName, &e.Action, &e.Comment, &e.CreatedAt); err != nil {
			return Request{}, nil, nil, err
		}
		events = append(events, e)
	}
	return r, steps, events, rows.Err()
}

// involved reports whether the viewer has a part in the request: they made it, decided a step of it, or may decide
// one of its steps. Anyone else does not see it.
func involved(a authz.Access, r Request, steps []StepInfo) bool {
	if r.RequesterID == a.UserID {
		return true
	}
	for _, st := range steps {
		if (st.DecidedBy != nil && *st.DecidedBy == a.UserID) || MayDecide(a, st.Permission, r.ScopeType, r.ScopeRef) {
			return true
		}
	}
	return false
}

func canDecide(a authz.Access, r Request, steps []StepInfo) bool {
	return r.Status == StatusPending && r.RequesterID != a.UserID && r.CurrentStep >= 1 && r.CurrentStep <= len(steps) &&
		MayDecide(a, steps[r.CurrentStep-1].Permission, r.ScopeType, r.ScopeRef)
}

// Get returns a request with its history, or ErrNotFound when the viewer has no part in it.
func (s Service) Get(ctx context.Context, a authz.Access, id string) (Detail, error) {
	var out Detail
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		r, steps, events, err := s.load(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if !involved(a, r, steps) {
			return ErrNotFound
		}
		out = Detail{Request: r, Steps: steps, Events: events, CanDecide: canDecide(a, r, steps), CanCancel: r.Status == StatusPending && r.RequesterID == a.UserID}
		return nil
	})
	return out, err
}

// Inbox lists the open requests the viewer may decide right now, oldest first. The viewer's own requests never appear.
func (s Service) Inbox(ctx context.Context, a authz.Access, page, size int) (Page, error) {
	out := Page{Items: []Request{}, Page: page, Size: size}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+requestColumns+`, cs.permission `+requestFrom+`
			WHERE r.status = 'pending' AND r.requester_id <> $1::uuid AND cs.permission = ANY($2)
			ORDER BY r.created_at, r.id LIMIT $3`, a.UserID, a.Effective.Permissions, inboxScan)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var perm string
			r, err := scanRequest(rows, &perm)
			if err != nil {
				return err
			}
			// the permission matched in SQL; the scope is checked here, by the same rule a decision uses
			if MayDecide(a, perm, r.ScopeType, r.ScopeRef) {
				r.TypeTitle = s.title(r.TypeKey)
				out.Items = append(out.Items, r)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	out.Total = len(out.Items)
	lo, hi := min((page-1)*size, out.Total), min(page*size, out.Total)
	out.Items = out.Items[lo:hi]
	return out, nil
}

// Mine lists the viewer's own requests, newest first; status filters when it is not empty.
func (s Service) Mine(ctx context.Context, userID, status string, page, size int) (Page, error) {
	out := Page{Items: []Request{}, Page: page, Size: size}
	var fe crud.FieldErrors
	if status != "" {
		fe.Choice("status", status, StatusPending, StatusApproved, StatusRejected, StatusCancelled)
	}
	if err := fe.OrNil(); err != nil {
		return out, err
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM approval_requests WHERE requester_id = $1::uuid AND ($2 = '' OR status = $2)`, userID, status).Scan(&out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+requestColumns+` `+requestFrom+` WHERE r.requester_id = $1::uuid AND ($2 = '' OR r.status = $2)
			ORDER BY r.created_at DESC, r.id DESC LIMIT $3 OFFSET $4`, userID, status, size, (page-1)*size)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRequest(rows)
			if err != nil {
				return err
			}
			r.TypeTitle = s.title(r.TypeKey)
			out.Items = append(out.Items, r)
		}
		return rows.Err()
	})
	return out, err
}
