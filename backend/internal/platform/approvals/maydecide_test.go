package approvals

import (
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
)

func ptr(s string) *string { return &s }

func TestMayDecide(t *testing.T) {
	access := func(perms []string, scopes map[string][]string) authz.Access {
		return authz.Access{Effective: authz.Effective{Permissions: perms, Scopes: scopes}}
	}
	tenantWide := access([]string{"fee.concessions.approve"}, map[string][]string{"fee.concessions": {"tenant"}})
	oneClass := access([]string{"fee.concessions.approve"}, map[string][]string{"fee.concessions": {"class_section:cs-1"}})
	selfOnly := access([]string{"fee.concessions.approve"}, map[string][]string{"fee.concessions": {"self"}})
	none := access(nil, nil)
	cases := []struct {
		name   string
		a      authz.Access
		st, sr *string
		want   bool
	}{
		{"unscoped request, holder", oneClass, nil, nil, true},
		{"unscoped request, no permission", none, nil, nil, false},
		{"scoped request, whole institute", tenantWide, ptr("class_section"), ptr("cs-2"), true},
		{"scoped request, assigned record", oneClass, ptr("class_section"), ptr("cs-1"), true},
		{"scoped request, other record", oneClass, ptr("class_section"), ptr("cs-2"), false},
		{"scoped request, other scope type", oneClass, ptr("hostel"), ptr("cs-1"), false},
		{"scoped request, self scope only", selfOnly, ptr("class_section"), ptr("cs-1"), false},
		{"scoped request, no permission", none, ptr("class_section"), ptr("cs-1"), false},
	}
	for _, c := range cases {
		if got := MayDecide(c.a, "fee.concessions.approve", c.st, c.sr); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
