package notify_test

import (
	"strings"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/notify"
)

func TestValidate(t *testing.T) {
	ok := notify.Notification{Kind: "k", Title: "Hello", Body: "b", Link: "/students/1"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid notification refused: %v", err)
	}
	if err := (notify.Notification{Kind: "k", Title: "t"}).Validate(); err != nil {
		t.Fatalf("link and body are optional: %v", err)
	}
	for name, n := range map[string]notify.Notification{
		"no kind":          {Title: "t"},
		"long kind":        {Kind: strings.Repeat("k", 61), Title: "t"},
		"no title":         {Kind: "k", Title: "  "},
		"long title":       {Kind: "k", Title: strings.Repeat("t", 121)},
		"long body":        {Kind: "k", Title: "t", Body: strings.Repeat("b", 501)},
		"full address":     {Kind: "k", Title: "t", Link: "https://evil.example/x"},
		"scheme-relative":  {Kind: "k", Title: "t", Link: "//evil.example/x"},
		"no leading slash": {Kind: "k", Title: "t", Link: "students/1"},
		"backslash":        {Kind: "k", Title: "t", Link: `/\evil.example`},
		"long link":        {Kind: "k", Title: "t", Link: "/" + strings.Repeat("a", 300)},
	} {
		if err := n.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
