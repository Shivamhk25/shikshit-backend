package authz

import "strings"

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
	// TenantWide hides the screen unless the user's scope for the permission's resource covers the whole institute.
	// It mirrors an API guard for personal data that does not yet narrow records by a smaller scope, so a user who
	// would only get an error is not offered the screen.
	TenantWide bool
	// NarrowScope, with TenantWide, also offers the screen to a user whose every scope for the resource is of this
	// type ("class_section"), because the API narrows the records to those assignments.
	NarrowScope string
}

// Screens is the catalog, in display order. Grow it as modules ship; ids must match the client registries.
var Screens = []Screen{
	{ID: "home", Label: "Home", Icon: "home", Order: 1, MobileTab: true},
	{ID: "onboarding", Label: "Onboarding", Icon: "upload", Order: 2, Permission: "onb.workspace.view", Module: "onb"},
	{ID: "rooms", Label: "Rooms", Icon: "door-open", Order: 20, Permission: "aca.rooms.view", Module: "aca"},
	{ID: "academic_years", Label: "Academic years", Icon: "calendar", Order: 21, Permission: "aca.academic_years.view", Module: "aca"},
	{ID: "academic_terms", Label: "Terms", Icon: "calendar-range", Order: 22, Permission: "aca.academic_terms.view", Module: "aca"},
	// Year rollover sits with the academic years it works on. It is for the person who sets up the next year, so it is
	// tenant-wide only: the API refuses any narrower grant.
	{ID: "year_rollover", Label: "Year rollover", Icon: "calendar-sync", Order: 22, Permission: "ten.year_rollover.view", Module: "ten", TenantWide: true},
	{ID: "classes", Label: "Classes", Icon: "layers", Order: 23, Permission: "aca.classes.view", Module: "aca"},
	{ID: "sections", Label: "Sections", Icon: "columns", Order: 24, Permission: "aca.sections.view", Module: "aca"},
	{ID: "subjects", Label: "Subjects", Icon: "book-open", Order: 25, Permission: "aca.subjects.view", Module: "aca"},
	{ID: "school_divisions", Label: "School divisions", Icon: "building", Order: 26, Permission: "aca.school_divisions.view", Module: "aca"},
	{ID: "campuses", Label: "Campuses", Icon: "map-pin", Order: 27, Permission: "aca.campuses.view", Module: "aca"},
	{ID: "holidays", Label: "Holidays", Icon: "sun", Order: 28, Permission: "aca.holiday_calendars.view", Module: "aca"},
	{ID: "calendar_events", Label: "Academic calendar", Icon: "calendar-days", Order: 29, Permission: "aca.academic_calendars.view", Module: "aca"},
	{ID: "elective_groups", Label: "Elective groups", Icon: "git-branch", Order: 30, Permission: "aca.elective_groups.view", Module: "aca"},
	{ID: "class_section_mappings", Label: "Class sections", Icon: "grid", Order: 31, Permission: "aca.class_section_mappings.view", Module: "aca"},
	{ID: "subject_class_mappings", Label: "Class subjects", Icon: "list-checks", Order: 32, Permission: "aca.subject_class_mappings.view", Module: "aca"},
	{ID: "textbooks", Label: "Textbooks", Icon: "book", Order: 33, Permission: "aca.textbooks.view", Module: "aca"},
	{ID: "textbook_chapters", Label: "Textbook chapters", Icon: "list", Order: 34, Permission: "aca.textbook_chapters.view", Module: "aca"},
	{ID: "enquiries", Label: "Enquiries", Icon: "inbox", Order: 35, Permission: "adm.enquiry.view", Module: "adm", TenantWide: true},
	{ID: "enquiry_discussions", Label: "Enquiry discussions", Icon: "message-square", Order: 36, Permission: "adm.enquiry_discussions.view", Module: "adm", TenantWide: true},
	{ID: "registrations", Label: "Registrations", Icon: "file-user", Order: 37, Permission: "adm.student_registrations.view", Module: "adm", TenantWide: true},
	{ID: "admissions", Label: "Admissions", Icon: "user-check", Order: 38, Permission: "adm.admissions.view", Module: "adm", TenantWide: true},
	{ID: "admission_insights", Label: "Admission insights", Icon: "bar-chart-3", Order: 39, Permission: "adm.admission_insights.view", Module: "adm", TenantWide: true},
	{ID: "students", Label: "Students", Icon: "graduation-cap", Order: 40, Permission: "stu.students.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "student_enrollments", Label: "Enrollments", Icon: "clipboard-list", Order: 41, Permission: "stu.students.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "guardians", Label: "Guardians", Icon: "users", Order: 42, Permission: "stu.students.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "student_guardians", Label: "Student guardians", Icon: "link", Order: 43, Permission: "stu.students.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "student_addresses", Label: "Student addresses", Icon: "map-pin", Order: 44, Permission: "stu.student_addresses.view", Module: "stu", TenantWide: true},
	{ID: "student_electives", Label: "Student electives", Icon: "list-checks", Order: 45, Permission: "stu.student_electives.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "elective_changes", Label: "Elective changes", Icon: "repeat", Order: 46, Permission: "stu.student_elective_approvals.view", Module: "stu", TenantWide: true, NarrowScope: "class_section"},
	{ID: "promotions", Label: "Promotions", Icon: "trending-up", Order: 45, Permission: "stu.student_promotions.view", Module: "stu", TenantWide: true},
	{ID: "transfers", Label: "Transfers", Icon: "arrow-right-left", Order: 46, Permission: "stu.student_transfers.view", Module: "stu", TenantWide: true},
	{ID: "student_pauses", Label: "Pauses", Icon: "pause-circle", Order: 47, Permission: "stu.student_pauses.view", Module: "stu", TenantWide: true},
	{ID: "alumni", Label: "Alumni", Icon: "graduation-cap", Order: 48, Permission: "stu.alumni.view", Module: "stu", TenantWide: true},
	{ID: "scope_assignments", Label: "Class assignments", Icon: "user-check", Order: 60, Permission: "iam.scope_assignments.view", Module: "iam"},
	{ID: "users", Label: "Users", Icon: "user-cog", Order: 61, Permission: "iam.users.view", Module: "iam"},
	{ID: "roles", Label: "Roles", Icon: "shield", Order: 62, Permission: "iam.roles.view", Module: "iam"},
	{ID: "parent_links", Label: "Parent accounts", Icon: "users-round", Order: 63, Permission: "iam.parent_links.view", Module: "iam"},
	{ID: "appearance", Label: "Appearance", Icon: "palette", Order: 64, Permission: "iam.appearance.view", Module: "iam"},
	// Approvals: anyone may open it; the inbox shows only what the user may decide, and their own requests.
	{ID: "approvals", Label: "Approvals", Icon: "clipboard-check", Order: 70, Module: "wfl"},
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
		if s.TenantWide && !hasTenantScope(e, s.Permission) && !onlyScope(e, s.Permission, s.NarrowScope) {
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

func hasTenantScope(e Effective, permission string) bool {
	for _, sc := range e.Scopes[resourceOf(permission)] {
		if sc == ScopeTenant {
			return true
		}
	}
	return false
}

func onlyScope(e Effective, permission, kind string) bool {
	if kind == "" {
		return false
	}
	list := e.Scopes[resourceOf(permission)]
	for _, sc := range list {
		if !strings.HasPrefix(sc, kind+":") {
			return false
		}
	}
	return len(list) > 0
}
