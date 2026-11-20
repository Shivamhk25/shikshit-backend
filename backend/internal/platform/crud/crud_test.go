package crud

import (
	"errors"
	"strings"
	"testing"
)

var sorts = Sorts{"name": "name", "capacity": "capacity", "displayOrder": "display_order"}

func TestParseListDefaults(t *testing.T) {
	q, err := ParseList(0, 0, "", "  hall ", sorts, "displayOrder,name")
	if err != nil {
		t.Fatal(err)
	}
	if q.Page != 1 || q.Size != 50 || q.Offset() != 0 || q.Search != "hall" || q.OrderBy != "display_order ASC, name ASC" {
		t.Errorf("%+v", q)
	}
}

func TestParseListDescendingAndOffset(t *testing.T) {
	q, err := ParseList(3, 20, "-capacity,name", "", sorts, "name")
	if err != nil {
		t.Fatal(err)
	}
	if q.OrderBy != "capacity DESC, name ASC" || q.Offset() != 40 {
		t.Errorf("%+v", q)
	}
}

func TestParseListRejectsAbuseWithFieldErrors(t *testing.T) {
	for name, c := range map[string]struct {
		page, size int
		sort, q    string
		field      string
	}{
		"negative page":       {-1, 10, "name", "", "page"},
		"huge page size":      {1, 201, "name", "", "size"},
		"unknown sort key":    {1, 10, "password", "", "sort"},
		"sql in sort":         {1, 10, "name; DROP TABLE rooms", "", "sort"},
		"sort by column name": {1, 10, "display_order", "", "sort"},
		"long search":         {1, 10, "name", strings.Repeat("a", 101), "q"},
	} {
		_, err := ParseList(c.page, c.size, c.sort, c.q, sorts, "name")
		var fe FieldErrors
		if !errors.As(err, &fe) || fe[0].Field != c.field {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOrderByNeverContainsUserText(t *testing.T) {
	q, err := ParseList(1, 10, "-name", "", sorts, "name")
	if err != nil || q.OrderBy != "name DESC" {
		t.Fatalf("%+v %v", q, err)
	}
}

func TestLikePatternEscapesWildcards(t *testing.T) {
	if got := LikePattern(`50%_off\`); got != `%50\%\_off\\%` {
		t.Errorf("got %q", got)
	}
}

func TestFieldErrorsOrNil(t *testing.T) {
	var e FieldErrors
	if e.OrNil() != nil {
		t.Error("empty must be nil")
	}
	e.Add("name", "required", "x")
	if e.OrNil() == nil || !strings.Contains(e.Error(), "name: required") {
		t.Error("non-empty must be an error naming the field")
	}
}
