package authz

// Screen is a screen the server may offer. The server sends only ids and labels; each client resolves the id in its
// own registry, so a screen an old app does not know is skipped instead of breaking it.
type Screen struct {
	ID         string
	Label      string
	Icon       string
	Order      int
	Permission string // required to see it; empty = any signed-in user
	Module     string // institute module it belongs to; empty = always available
	MobileTab  bool   // candidate for the mobile bottom tab bar
}

// Screens is the catalog, in display order. Grow it as modules ship; ids must match the client registries.
var Screens = []Screen{
	{ID: "home", Label: "Home", Icon: "home", Order: 1, MobileTab: true},
	{ID: "onboarding", Label: "Onboarding", Icon: "upload", Order: 2, Permission: "onb.workspace.view", Module: "onb"},
	{ID: "profile", Label: "Profile", Icon: "user", Order: 90, MobileTab: true},
}

// MaxMobileTabs is the most tabs a bottom bar can hold.
const MaxMobileTabs = 5

// Visible returns the screens this user may open: permission held and module enabled.
func Visible(e Effective, enabled map[string]bool) []Screen {
	var out []Screen
	for _, s := range Screens {
		if s.Module != "" && !enabled[s.Module] {
			continue
		}
		if s.Permission != "" && !e.Can(s.Permission) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// MobileTabs returns the ids of the visible screens that are tab candidates, at most MaxMobileTabs.
func MobileTabs(visible []Screen) []string {
	tabs := []string{}
	for _, s := range visible {
		if s.MobileTab && len(tabs) < MaxMobileTabs {
			tabs = append(tabs, s.ID)
		}
	}
	return tabs
}
