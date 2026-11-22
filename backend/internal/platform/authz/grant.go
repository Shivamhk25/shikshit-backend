package authz

import "strings"

// CanGrant reports whether a user with this effective access may give someone else the permission code at the
// given scope, through a role or a role assignment. The rule (IAM-02): nobody can grant what they do not hold, and
// nobody can grant it more widely than they hold it.
//
//   - The permission must be in the user's effective set.
//   - Holding it for the whole institute (tenant) covers every scope.
//   - Otherwise the scope must be one the user holds for that resource: class_section is covered by any
//     class_section scope, self and owned only by themselves.
//
// A user granted a narrowly scoped permission with no record assigned yet has an empty scope list, which does not
// say which scope kind they hold, so they cannot grant it. That errs on the side of refusing.
func (e Effective) CanGrant(code, scope string) bool {
	if !e.Can(code) {
		return false
	}
	for _, s := range e.Scopes[resourceOf(code)] {
		kind, _, _ := strings.Cut(s, ":")
		if kind == ScopeTenant || kind == scope {
			return true
		}
	}
	return false
}
