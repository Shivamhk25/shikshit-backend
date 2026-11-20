// Package authz decides what a signed-in user may see and do: it matches permission patterns, combines role
// grants with per-user overrides and scope assignments, and builds the /me/access payload.
// The matching rules are shared with the web and mobile clients (shikshit-shared, fixtures/permission-match.json).
package authz

import (
	"regexp"
	"strings"
)

var segment = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// MatchPermission reports whether pattern grants the concrete permission code.
//
// A code has exactly three segments (module.resource.action). A pattern has one to three segments, each a literal or
// "*"; missing trailing segments mean "*". Matching is case-sensitive and segment-wise.
func MatchPermission(pattern, code string) bool {
	c := strings.Split(code, ".")
	if len(c) != 3 || !allMatch(c, segment) {
		return false
	}
	p := strings.Split(pattern, ".")
	if len(p) < 1 || len(p) > 3 {
		return false
	}
	for _, s := range p {
		if s != "*" && !segment.MatchString(s) {
			return false
		}
	}
	for len(p) < 3 {
		p = append(p, "*")
	}
	for i := range p {
		if p[i] != "*" && p[i] != c[i] {
			return false
		}
	}
	return true
}

func allMatch(parts []string, re *regexp.Regexp) bool {
	for _, s := range parts {
		if !re.MatchString(s) {
			return false
		}
	}
	return true
}
