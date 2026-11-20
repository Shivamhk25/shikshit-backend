package authz

import (
	"sort"
	"strings"
)

// Scope names stored on grants. "tenant" means the whole institute.
const (
	ScopeTenant = "tenant"
	ScopeSelf   = "self"
	ScopeOwned  = "owned"
)

// Grant is one permission pattern given to the user by a role, with the scope it applies to.
type Grant struct{ Pattern, Scope string }

// Override is an explicit per-user allow or deny. Deny always wins over every allow.
type Override struct{ Pattern, Effect, Scope string }

// Assignment links a user to the records a scope type refers to, e.g. {class_section, 12}.
type Assignment struct{ Type, Ref string }

// Inputs is everything needed to compute a user's effective access. All time filtering (role windows, override
// expiry, assignment windows) has already been applied by the loader.
type Inputs struct {
	Catalog        []string // every concrete permission code that exists
	EnabledModules map[string]bool
	Grants         []Grant
	Overrides      []Override
	Assignments    []Assignment
}

// Effective is the computed access: concrete permission codes and the scopes they apply to, per resource.
type Effective struct {
	Permissions []string
	Scopes      map[string][]string // "module.resource" -> scope strings
}

// Compute expands grants against the catalog, removes denied and module-disabled permissions and derives scopes.
//
// Rules: a permission is allowed when any role grant or allow override matches it; a matching deny override removes
// it whatever else grants it; permissions of a module the institute has not enabled are dropped. A permission's
// scopes are the union over everything that grants it, expanded to concrete records for assignment-based scopes.
func Compute(in Inputs) Effective {
	assigned := map[string][]string{}
	for _, a := range in.Assignments {
		assigned[a.Type] = append(assigned[a.Type], a.Ref)
	}

	out := Effective{Permissions: []string{}, Scopes: map[string][]string{}}
	resourceScopes := map[string]map[string]bool{}

	for _, code := range in.Catalog {
		if !in.EnabledModules[moduleOf(code)] || denied(in.Overrides, code) {
			continue
		}
		scopes := map[string]bool{}
		for _, g := range in.Grants {
			if MatchPermission(g.Pattern, code) {
				scopes[g.Scope] = true
			}
		}
		for _, o := range in.Overrides {
			if o.Effect == "allow" && MatchPermission(o.Pattern, code) {
				scopes[o.Scope] = true
			}
		}
		if len(scopes) == 0 {
			continue
		}
		out.Permissions = append(out.Permissions, code)

		res := resourceOf(code)
		if resourceScopes[res] == nil {
			resourceScopes[res] = map[string]bool{}
		}
		for s := range scopes {
			switch s {
			case ScopeTenant, ScopeSelf, ScopeOwned:
				resourceScopes[res][s] = true
			default:
				// A user with a class-section scope but no class assigned sees nothing: the resource gets an
				// empty list, which is how the client tells "no records" from "everything".
				for _, ref := range assigned[s] {
					resourceScopes[res][s+":"+ref] = true
				}
			}
		}
	}

	sort.Strings(out.Permissions)
	for res, set := range resourceScopes {
		list := make([]string, 0, len(set))
		for s := range set {
			list = append(list, s)
		}
		sort.Strings(list)
		out.Scopes[res] = list
	}
	return out
}

func denied(overrides []Override, code string) bool {
	for _, o := range overrides {
		if o.Effect == "deny" && MatchPermission(o.Pattern, code) {
			return true
		}
	}
	return false
}

func moduleOf(code string) string { return code[:strings.IndexByte(code, '.')] }

func resourceOf(code string) string {
	i := strings.LastIndexByte(code, '.')
	return code[:i]
}

// Can reports whether the permission is in the effective set.
func (e Effective) Can(code string) bool {
	i := sort.SearchStrings(e.Permissions, code)
	return i < len(e.Permissions) && e.Permissions[i] == code
}
