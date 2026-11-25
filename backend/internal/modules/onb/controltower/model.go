// Package controltower implements ONB-01, the onboarding readiness model: the domains an institute has to load before
// go-live, the order they depend on each other, and what state each is in.
//
// A domain's status is never stored. It is computed from the data (Measure) and from two marks people set (state:
// asked for review, confirmed ready). A domain cannot show Ready while a visible prerequisite is not Ready, whatever
// its stored mark says. The go-live gate (ONB-23) reads the same Statuses result.
package controltower

// Status of a domain as shown to users.
const (
	NotStarted  = "not_started"
	InProgress  = "in_progress"
	NeedsReview = "needs_review"
	Ready       = "ready"
)

// Stored states: what people decided.
const (
	StateOpen   = "open"
	StateReview = "review"
	StateReady  = "ready"
)

// Domain is one area of data an institute loads. Module is the module that must be enabled for the domain to appear.
type Domain struct {
	ID, Title, Module string
	// Needs are the domains that must be Ready first. The list below is in dependency order (a domain comes after
	// everything it needs), which Statuses relies on.
	Needs []string
}

// Domains is the dependency graph, in dependency order.
var Domains = []Domain{
	{ID: "academic", Title: "Academic structure", Module: "aca"},
	{ID: "students", Title: "Students and guardians", Module: "stu", Needs: []string{"academic"}},
	{ID: "staff", Title: "Staff and HR", Module: "hrp", Needs: []string{"academic"}},
	{ID: "fees", Title: "Fees", Module: "fee", Needs: []string{"academic", "students"}},
	{ID: "library", Title: "Library", Module: "lib", Needs: []string{"students", "staff"}},
	{ID: "transport", Title: "Transport", Module: "trn", Needs: []string{"students", "staff"}},
	{ID: "residential", Title: "Residential", Module: "res", Needs: []string{"students"}},
	{ID: "cafeteria", Title: "Cafeteria", Module: "caf", Needs: []string{"students"}},
	{ID: "inventory", Title: "Inventory", Module: "inv", Needs: []string{"staff"}},
	{ID: "health", Title: "Health", Module: "hlt", Needs: []string{"students"}},
	{ID: "devices", Title: "Devices and cards", Module: "dev", Needs: []string{"students", "staff"}},
	{ID: "communication", Title: "Communication", Module: "com", Needs: []string{"students"}},
	{ID: "certificates", Title: "Certificates", Module: "crt", Needs: []string{"students"}},
}

// Lookup returns a domain by id.
func Lookup(id string) (Domain, bool) {
	for _, d := range Domains {
		if d.ID == id {
			return d, true
		}
	}
	return Domain{}, false
}

// Check is one readiness rule with the counts behind it: Done of Total satisfy it.
type Check struct {
	Key, Label  string
	Done, Total int
}

// Measure is what the data says about a domain. HasSource is false for a domain whose data does not exist yet.
type Measure struct {
	HasSource bool
	Checks    []Check
}

// Started says whether any of the domain's data exists.
func (m Measure) Started() bool {
	if !m.HasSource {
		return false
	}
	for _, c := range m.Checks {
		if c.Done > 0 {
			return true
		}
	}
	return false
}

// RulesMet says whether every check is satisfied in full. A check with nothing to measure is not satisfied.
func (m Measure) RulesMet() bool {
	if !m.HasSource || len(m.Checks) == 0 {
		return false
	}
	for _, c := range m.Checks {
		if c.Total <= 0 || c.Done != c.Total {
			return false
		}
	}
	return true
}

// Percent is the share of required items present, rounded down so 100 means every check is met.
func (m Measure) Percent() int {
	done, total := 0, 0
	for _, c := range m.Checks {
		done += c.Done
		total += c.Total
	}
	if total == 0 {
		return 0
	}
	return done * 100 / total
}

// Blocker is a prerequisite that is not Ready.
type Blocker struct{ ID, Title string }

// Computed is a domain's status with its blockers.
type Computed struct {
	Domain   Domain
	Status   string
	Blockers []Blocker
}

// Statuses computes every visible domain's status in dependency order. enabled says which modules are on for the
// institute; a domain whose module is off is left out, and a prerequisite that is left out blocks nothing.
func Statuses(enabled map[string]bool, measures map[string]Measure, states map[string]string) []Computed {
	out := []Computed{}
	status := map[string]string{}
	for _, d := range Domains {
		if !enabled[d.Module] {
			continue
		}
		var blockers []Blocker
		for _, n := range d.Needs {
			if nd, ok := Lookup(n); ok && enabled[nd.Module] && status[n] != Ready {
				blockers = append(blockers, Blocker{ID: nd.ID, Title: nd.Title})
			}
		}
		s := effective(measures[d.ID], states[d.ID], len(blockers) == 0)
		status[d.ID] = s
		out = append(out, Computed{Domain: d, Status: s, Blockers: blockers})
	}
	return out
}

func effective(m Measure, state string, prereqsReady bool) string {
	if !m.Started() {
		return NotStarted
	}
	if !m.RulesMet() {
		return InProgress
	}
	switch state {
	case StateReview:
		return NeedsReview
	case StateReady:
		if prereqsReady {
			return Ready
		}
		return NeedsReview
	}
	return InProgress
}
