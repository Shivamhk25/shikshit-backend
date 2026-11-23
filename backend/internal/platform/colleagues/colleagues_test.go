package colleagues

import (
	"strings"
	"testing"
)

func TestParseQuery(t *testing.T) {
	good := []struct {
		page, size int
		q          string
		want       Query
	}{
		{0, 0, "", Query{Page: 1, Size: DefaultPageSize}},
		{3, MaxPageSize, "  Priya ", Query{Search: "Priya", Page: 3, Size: MaxPageSize}},
		{1, 1, "ab", Query{Search: "ab", Page: 1, Size: 1}},
		{1, 1, "   ", Query{Page: 1, Size: 1}},
		{MaxPage, 10, "नमस", Query{Search: "नमस", Page: MaxPage, Size: 10}}, // counted in characters, not bytes
	}
	for _, c := range good {
		got, err := ParseQuery(c.page, c.size, c.q)
		if err != nil || got != c.want {
			t.Errorf("ParseQuery(%d, %d, %q) = %+v, %v; want %+v", c.page, c.size, c.q, got, err, c.want)
		}
	}
	bad := []struct {
		page, size int
		q          string
	}{
		{-1, 0, ""}, {MaxPage + 1, 0, ""}, {0, -1, ""}, {0, MaxPageSize + 1, ""}, {0, 0, "a"}, {0, 0, " a "}, {0, 0, strings.Repeat("x", MaxSearch+1)},
	}
	for _, c := range bad {
		if _, err := ParseQuery(c.page, c.size, c.q); err == nil {
			t.Errorf("ParseQuery(%d, %d, %q) accepted", c.page, c.size, c.q)
		}
	}
}

func TestLabel(t *testing.T) {
	for _, c := range []struct {
		roles []string
		want  string
	}{
		{nil, "Staff"},
		{[]string{"Principal"}, "Principal"},
		{[]string{"A", "B", "C"}, "A, B, C"},
		{[]string{"A", "B", "C", "D", "E"}, "A, B, C, +2 more"},
	} {
		if got := Label(c.roles); got != c.want {
			t.Errorf("Label(%v) = %q, want %q", c.roles, got, c.want)
		}
	}
}
