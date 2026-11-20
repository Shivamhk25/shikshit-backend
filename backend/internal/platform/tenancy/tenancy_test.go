package tenancy_test

import (
	"os"
	"testing"

	"github.com/pie360/backend/internal/platform/tenancy"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func TestKeyFromHost(t *testing.T) {
	cfg := tenancy.Config{BaseDomains: []string{"shikshit.app", "localhost"}, ReservedLabels: []string{"www", "api", "app"}}
	cases := []struct {
		host string
		want string // "" means no key
	}{
		{"springfield.shikshit.app", "springfield"},
		{"Springfield.Shikshit.App:443", "springfield"},
		{"springfield.shikshit.app.", "springfield"},
		{"shikshit.app", ""},
		{"www.shikshit.app", ""},
		{"api.shikshit.app", ""},
		{"a.b.shikshit.app", ""},
		{"school.example.org", "school.example.org"},
		{"school.example.org:8443", "school.example.org"},
		{"acme.localhost:5173", "acme"},
		{"localhost:5173", ""},
		{"127.0.0.1:8080", ""},
		{"[::1]:8080", ""},
		{"::1", ""},
		{"", ""},
		{"nodots", ""},
		{"evil..example.org", ""},
		{"-bad.example.org", ""},
		{"bad_underscore.example.org", ""},
		{"bad host.example.org", ""},
		{"a.shikshit.app/../x", ""},
	}
	for _, c := range cases {
		got, ok := tenancy.KeyFromHost(c.host, cfg)
		if c.want == "" && ok {
			t.Errorf("KeyFromHost(%q) = %q, want no key", c.host, got)
		}
		if c.want != "" && (!ok || got != c.want) {
			t.Errorf("KeyFromHost(%q) = %q (ok=%v), want %q", c.host, got, ok, c.want)
		}
	}
}
