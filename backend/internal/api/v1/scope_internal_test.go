package apiv1

import (
	"context"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/scope"
)

// The roles shipped today never give a narrow scope a write permission on students, so these rules are checked here
// directly: a custom role with stu.*.* on class sections must still not write, and a scope the resource cannot apply
// must refuse the request instead of being skipped.
func TestNarrowToScope(t *testing.T) {
	students, addresses := scopedResources["stu.students"], scopedResources["stu.student_addresses"]
	cases := []struct {
		name       string
		policy     *scopePolicy
		scopes     []string
		permission string
		ok         bool
		limit      []string // nil = no limit expected
	}{
		{"institute-wide read", students, []string{"tenant", "class_section:1"}, "stu.students.view", true, nil},
		{"institute-wide write", students, []string{"tenant"}, "stu.students.create", true, nil},
		{"class-section read", students, []string{"class_section:1", "class_section:2"}, "stu.students.view", true, []string{"1", "2"}},
		{"no assignment read", students, []string{}, "stu.students.view", true, []string{}},
		{"class-section write", students, []string{"class_section:1"}, "stu.students.edit", false, nil},
		{"class-section delete", students, []string{"class_section:1"}, "stu.students.delete", false, nil},
		{"mixed with a foreign scope", students, []string{"class_section:1", "self"}, "stu.students.view", false, nil},
		{"hostel scope", students, []string{"assigned_hostel:3"}, "stu.students.view", false, nil},
		{"addresses by class section", addresses, []string{"class_section:1"}, "stu.student_addresses.view", false, nil},
		{"addresses institute-wide", addresses, []string{"tenant"}, "stu.student_addresses.view", true, nil},
	}
	for _, c := range cases {
		ctx, ok := narrowToScope(context.Background(), c.policy, c.scopes, c.permission)
		if ok != c.ok {
			t.Errorf("%s: ok = %v", c.name, ok)
			continue
		}
		got, limited := scope.ClassSections(ctx)
		if !ok {
			continue
		}
		if (c.limit != nil) != limited || len(got) != len(c.limit) {
			t.Errorf("%s: limit = %v (%v), want %v", c.name, got, limited, c.limit)
		}
	}
}
