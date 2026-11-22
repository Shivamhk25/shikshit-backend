package identity

import (
	"strings"
	"testing"
)

func TestCheckPassword(t *testing.T) {
	for _, tc := range []struct {
		password, username string
		ok                 bool
	}{
		{"correct horse", "asha", true},
		{"ten chars!", "asha", true},
		{"nine char", "asha", false},
		{strings.Repeat("a", 128), "asha", true},
		{strings.Repeat("a", 129), "asha", false},
		{"Principal01", "principal01", false},
		{"  principal01 ", "Principal01", false},
		{"पासवर्ड१२३४", "asha", true}, // 11 runes, more than 10 bytes too: length counts characters
	} {
		if err := CheckPassword(tc.password, tc.username); (err == nil) != tc.ok {
			t.Errorf("CheckPassword(%q, %q) = %v, want ok=%v", tc.password, tc.username, err, tc.ok)
		}
	}
}
