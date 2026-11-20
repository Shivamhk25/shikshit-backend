package authz

import (
	"encoding/json"
	"os"
	"testing"
)

// The same cases run in the TypeScript client; if the two ever disagree, the UI would show or hide things the
// server treats differently. testdata/permission-match.json is a copy of shikshit-shared/fixtures/permission-match.json.
func TestMatchPermissionConformance(t *testing.T) {
	raw, err := os.ReadFile("testdata/permission-match.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Pattern string `json:"pattern"`
			Code    string `json:"code"`
			Want    bool   `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 20 {
		t.Fatalf("fixture looks truncated: %d cases", len(f.Cases))
	}
	for _, c := range f.Cases {
		if got := MatchPermission(c.Pattern, c.Code); got != c.Want {
			t.Errorf("MatchPermission(%q, %q) = %v, want %v", c.Pattern, c.Code, got, c.Want)
		}
	}
}
