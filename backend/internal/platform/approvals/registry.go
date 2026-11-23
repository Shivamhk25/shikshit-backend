// Package approvals is the generic approval engine (FND-20). A module registers a workflow type in code, then calls
// Service.Submit inside its own transaction to put one of its records up for approval. Approvers decide through the
// API (or Service.Decide); the type's hooks run inside the deciding transaction, so the module's change and the
// decision commit or roll back together.
//
// Who may decide is never stored per person: a step names a permission, and the approver's effective access
// (permission, plus a record scope such as a class section when the type gives one) is checked at the moment of
// reading and deciding. The requester can never decide their own request.
package approvals

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
)

// Step is one stage of approval. Permission is the catalog code a person needs to decide this step.
type Step struct {
	Name       string
	Permission string
}

// Hook runs inside the transaction that decides (or cancels) a request, with the request in its new state. An error
// rolls the whole decision back. Return a ConflictError to refuse with a message the approver can read.
type Hook func(ctx context.Context, tx pgx.Tx, actor audit.Actor, r Request) error

// ScopeResolver names the record a subject belongs to, for example ("class_section", "<mapping id>"), so that only
// approvers whose access covers that record may decide. It runs once, when the request is made, inside the
// submitting transaction. Returning "" for both means the request is not scoped.
type ScopeResolver func(ctx context.Context, tx pgx.Tx, subjectID string) (scopeType, scopeRef string, err error)

// Type is a workflow a module registers.
type Type struct {
	Key   string // lower-case, dot or underscore separated, for example "fee.concession"
	Title string // shown in the inbox and in notifications
	Steps []Step // in order; every step must be approved
	Scope ScopeResolver
	// OnApproved runs when the last step is approved, OnRejected when any step is rejected, OnCancelled when the
	// requester withdraws. All are optional.
	OnApproved, OnRejected, OnCancelled Hook
}

var typeKey = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,79}$`)

// Registry holds the workflow types. Register everything at start-up; it is read-only afterwards.
type Registry struct{ types map[string]Type }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{types: map[string]Type{}} }

// Register adds a type. It refuses a duplicate key, a malformed key, a missing title and a type without steps.
func (r *Registry) Register(t Type) error {
	switch {
	case !typeKey.MatchString(t.Key):
		return fmt.Errorf("approvals: invalid type key %q", t.Key)
	case t.Title == "" || len(t.Title) > 80:
		return fmt.Errorf("approvals: type %q needs a title of at most 80 characters", t.Key)
	case len(t.Steps) == 0:
		return fmt.Errorf("approvals: type %q needs at least one step", t.Key)
	}
	for i, s := range t.Steps {
		if s.Name == "" || len(s.Name) > 80 || s.Permission == "" || len(s.Permission) > 120 {
			return fmt.Errorf("approvals: type %q step %d needs a name and a permission", t.Key, i+1)
		}
	}
	if _, dup := r.types[t.Key]; dup {
		return fmt.Errorf("approvals: type %q is already registered", t.Key)
	}
	t.Steps = append([]Step(nil), t.Steps...)
	r.types[t.Key] = t
	return nil
}

// Get returns the type with the key.
func (r *Registry) Get(key string) (Type, bool) {
	t, ok := r.types[key]
	return t, ok
}
