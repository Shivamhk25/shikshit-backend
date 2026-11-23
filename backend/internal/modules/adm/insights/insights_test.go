package insights_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/admissions"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/insights"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

func ptr[T any](v T) *T { return &v }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

var india = time.FixedZone("IST", 5*3600+1800)

// world is one institute (alpha) with two years and two classes, an unrelated institute (bravo), and the rows made in them.
type world struct {
	f             modtest.Fixture
	year, year2   string
	class, class2 string
	bYear, bClass string
	today         time.Time // the date in India, at 00:00 UTC
	enq           []enquiries.Enquiry
	reg           []registrations.Registration
	section       string                   // one section mapped to every (year, class) pair, so any accepted registration can be admitted
	month         func(back int) time.Time // first day of the month `back` months ago
}

func day(t time.Time) string { return t.Format("2006-01-02") }

// at returns a timestamp that falls on `d` in India (noon there).
func at(d time.Time) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, india)
}

type enqSpec struct {
	source, status string
	class, year    int  // 0 none, 1 first, 2 second
	followUp       *int // days from today
	created        time.Time
}

type regSpec struct {
	status      string
	class, year int
	applied     time.Time
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	a, b := modtest.In(f.Alpha), modtest.In(f.Bravo)
	now := time.Now().In(india)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	w := world{f: f, today: today}
	w.month = func(back int) time.Time {
		return time.Date(today.Year(), today.Month()-time.Month(back), 1, 0, 0, 0, 0, time.UTC)
	}
	y1, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "Y1", Name: "Year one", StartDate: day(today.AddDate(-1, 0, 0)), EndDate: day(today.AddDate(0, 6, 0))})
	must(t, err)
	y2, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "Y2", Name: "Year two", StartDate: day(today.AddDate(0, 7, 0)), EndDate: day(today.AddDate(1, 6, 0))})
	must(t, err)
	c1, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	c2, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "VI", Name: "Class 6"})
	must(t, err)
	by, err := years.Service{DB: f.App}.Create(b, f.Actor, years.Input{Code: "Y1", Name: "B year", StartDate: day(today.AddDate(-1, 0, 0)), EndDate: day(today.AddDate(0, 6, 0))})
	must(t, err)
	bc, err := classes.Service{DB: f.App}.Create(b, f.Actor, classes.Input{Code: "V", Name: "B class"})
	must(t, err)
	w.year, w.year2, w.class, w.class2, w.bYear, w.bClass = y1.ID, y2.ID, c1.ID, c2.ID, by.ID, bc.ID
	must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'Section A') RETURNING id::text`, f.Alpha.ID).Scan(&w.section))
	for _, y := range []string{y1.ID, y2.ID} {
		for _, c := range []string{c1.ID, c2.ID} {
			_, err = f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, y, c, w.section)
			must(t, err)
		}
	}
	return w
}

func (w world) pick(n int, a, b string) *string {
	switch n {
	case 1:
		return &a
	case 2:
		return &b
	}
	return nil
}

// addEnquiry creates an enquiry and moves its creation time (the API always stamps "now").
func (w *world) addEnquiry(t *testing.T, tenant string, ctx context.Context, s enqSpec, class, year *string) enquiries.Enquiry {
	t.Helper()
	in := enquiries.Input{EnquirerName: "Meera Rao", Phone: "9876543210", ChildName: "Asha Rao", Source: s.source, Status: s.status, ClassID: class, YearID: year}
	if s.followUp != nil {
		in.FollowUpDate = ptr(day(w.today.AddDate(0, 0, *s.followUp)))
	}
	e, err := enquiries.Service{DB: w.f.App}.Create(ctx, w.f.Actor, in)
	must(t, err)
	_, err = w.f.Admin.Exec(bg, `UPDATE enquiries SET created_at = $1 WHERE id = $2`, s.created, e.ID)
	must(t, err)
	return e
}

// build makes a spread of enquiries and registrations in alpha, a few in bravo, one deleted of each kind, and three
// registrations created from enquiries.
func (w *world) build(t *testing.T) {
	t.Helper()
	a, b := modtest.In(w.f.Alpha), modtest.In(w.f.Bravo)
	n := func(v int) *int { return &v }
	m := func(back, d int) time.Time { return at(w.month(back).AddDate(0, 0, d-1)) }
	specs := []enqSpec{
		{"walk_in", "new", 1, 1, n(0), at(w.today)},
		{"phone", "new", 1, 1, n(-3), m(1, 10)},
		{"website", "contacted", 2, 1, n(-1), m(1, 20)},
		{"referral", "visit_scheduled", 2, 2, n(2), m(3, 5)},
		{"social", "converted", 1, 0, nil, m(3, 25)},
		{"other", "closed", 0, 1, n(-5), m(4, 15)},
		{"walk_in", "contacted", 1, 2, n(0), m(1, 1)},
		{"phone", "new", 2, 2, nil, m(5, 28)},
		// 00:30 on the 1st in India is still the previous day in UTC: it belongs to this month, not the one before
		{"walk_in", "new", 1, 1, nil, time.Date(w.month(2).Year(), w.month(2).Month(), 1, 0, 30, 0, 0, india)},
	}
	for _, s := range specs {
		e := w.addEnquiry(t, "", a, s, w.pick(s.class, w.class, w.class2), w.pick(s.year, w.year, w.year2))
		w.enq = append(w.enq, e)
	}
	// a deleted enquiry and a deleted registration are never counted
	gone := w.addEnquiry(t, "", a, enqSpec{"walk_in", "new", 1, 1, n(-9), at(w.today)}, &w.class, &w.year)
	must(t, enquiries.Service{DB: w.f.App}.Delete(a, w.f.Actor, gone.ID))
	// bravo has its own data, including follow-ups due
	for i := 0; i < 3; i++ {
		w.addEnquiry(t, "", b, enqSpec{"walk_in", "new", 1, 1, n(-2), at(w.today)}, &w.bClass, &w.bYear)
	}
	rs := registrations.Service{DB: w.f.App}
	mk := func(s regSpec) registrations.Registration {
		g, err := rs.Create(a, w.f.Actor, registrations.Input{AppliedOn: day(s.applied), FirstName: "Kid", DateOfBirth: "2018-06-01", GuardianPhone: "9876543210",
			ClassID: *w.pick(s.class, w.class, w.class2), YearID: *w.pick(s.year, w.year, w.year2), Status: s.status})
		must(t, err)
		return g
	}
	regs := []regSpec{
		{"submitted", 1, 1, w.today}, {"under_review", 1, 1, w.month(1).AddDate(0, 0, 9)}, {"accepted", 1, 1, w.month(1).AddDate(0, 0, 14)},
		{"accepted", 2, 1, w.month(2).AddDate(0, 0, 3)}, {"rejected", 2, 2, w.month(3).AddDate(0, 0, 3)}, {"withdrawn", 1, 2, w.month(3).AddDate(0, 0, 12)},
		{"submitted", 2, 2, w.month(5).AddDate(0, 0, 27)},
	}
	for _, s := range regs {
		w.reg = append(w.reg, mk(s))
	}
	del := mk(regSpec{"accepted", 1, 1, w.today})
	must(t, rs.Delete(a, w.f.Actor, del.ID))
	// registrations made from enquiries: the link is what the enquiry-to-registration step counts
	for i, st := range []string{"submitted", "accepted", "accepted"} {
		eq := w.enq[[]int{3, 7, 8}[i]]
		g, err := rs.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{DateOfBirth: ptr("2018-06-01")})
		must(t, err)
		_, err = w.f.Admin.Exec(bg, `UPDATE student_registrations SET status = $1, applied_on = $2 WHERE id = $3`, st, day(w.month(i)), g.ID)
		must(t, err)
		w.reg = append(w.reg, g)
	}
	// admit some of the accepted registrations (they keep status "accepted"); leave others waiting, and admit one that is
	// then deleted: a deleted registration is never counted
	adm := admissions.Service{DB: w.f.App}
	in := admissions.Input{SectionID: w.section, Gender: ptr("female"), ConfirmDuplicate: true} // the fixture's children share a name
	admitted, firstAdmitted := 0, ""
	for _, g := range w.reg {
		var st string
		must(t, w.f.Admin.QueryRow(bg, `SELECT status FROM student_registrations WHERE id = $1`, g.ID).Scan(&st))
		if st == "accepted" && admitted%2 == 0 {
			_, err := adm.Admit(a, w.f.Actor, g.ID, in)
			must(t, err)
			if firstAdmitted == "" {
				firstAdmitted = g.ID
			}
		}
		if st == "accepted" {
			admitted++
		}
	}
	ghost := mk(regSpec{"accepted", 1, 1, w.month(1)})
	_, err := adm.Admit(a, w.f.Actor, ghost.ID, in)
	must(t, err)
	must(t, rs.Delete(a, w.f.Actor, ghost.ID))
	// an admitted registration whose status was later changed is still admitted (the admissions list shows it) but no longer accepted
	_, err = w.f.Admin.Exec(bg, `UPDATE student_registrations SET status = 'withdrawn' WHERE id = $1`, firstAdmitted)
	must(t, err)
	// a registration made from an enquiry and then deleted does not count as a conversion
	dead, err := rs.CreateFromEnquiry(a, w.f.Actor, w.enq[1].ID, registrations.Fill{DateOfBirth: ptr("2018-06-01")})
	must(t, err)
	must(t, rs.Delete(a, w.f.Actor, dead.ID))
	// and one for a bravo enquiry, which must never show in alpha
	be := w.addEnquiry(t, "", b, enqSpec{"phone", "new", 1, 1, nil, at(w.today)}, &w.bClass, &w.bYear)
	_, err = rs.CreateFromEnquiry(b, w.f.Actor, be.ID, registrations.Fill{DateOfBirth: ptr("2018-06-01")})
	must(t, err)
}

// list reads everything the list endpoints would return (page by page) for alpha.
func (w world) allEnquiries(t *testing.T, ctx context.Context) []enquiries.Enquiry {
	t.Helper()
	q, _ := crud.ParseList(1, 200, "", "", enquiries.SortKeys, enquiries.DefaultSort)
	p, err := enquiries.Service{DB: w.f.App}.List(ctx, q, enquiries.Filter{})
	must(t, err)
	return p.Items
}

func (w world) allRegistrations(t *testing.T, ctx context.Context) []registrations.Registration {
	t.Helper()
	q, _ := crud.ParseList(1, 200, "", "", registrations.SortKeys, registrations.DefaultSort)
	p, err := registrations.Service{DB: w.f.App}.List(ctx, q, registrations.Filter{})
	must(t, err)
	return p.Items
}

func between(d, from, to string) bool { return (from == "" || d >= from) && (to == "" || d <= to) }

// expected computes the dashboard from the lists, the way a client reading the list endpoints would.
type expected struct {
	enqTotal, regTotal, registered  int
	admitted, acceptedOrAdmitted    int
	byStatus, bySource, regByStatus map[string]int
	due, dueToday, overdue          int
	enqMonth, regMonth              map[string]int
}

func (w world) expect(t *testing.T, f insights.Filter) expected {
	t.Helper()
	a := modtest.In(w.f.Alpha)
	x := expected{byStatus: map[string]int{}, bySource: map[string]int{}, regByStatus: map[string]int{}, enqMonth: map[string]int{}, regMonth: map[string]int{}}
	linked := map[string]bool{}
	for _, g := range w.allRegistrations(t, a) {
		if g.EnquiryID != nil {
			linked[*g.EnquiryID] = true
		}
		if (f.ClassID != "" && g.ClassID != f.ClassID) || (f.YearID != "" && g.YearID != f.YearID) || !between(g.AppliedOn, f.From, f.To) {
			continue
		}
		x.regTotal++
		x.regByStatus[g.Status]++
		x.regMonth[g.AppliedOn[:7]]++
	}
	// the admitted registrations as the admissions list returns them
	aq, _ := crud.ParseList(1, 200, "", "", admissions.SortKeys, admissions.DefaultSort)
	ap, err := admissions.Service{DB: w.f.App}.ListAudited(a, w.f.Actor, aq, admissions.Filter{State: admissions.StateAdmitted})
	must(t, err)
	admittedIDs := map[string]bool{}
	for _, g := range ap.Items {
		admittedIDs[g.ID] = true
		if (f.ClassID == "" || g.ClassID == f.ClassID) && (f.YearID == "" || g.YearID == f.YearID) && between(g.AppliedOn, f.From, f.To) {
			x.admitted++
		}
	}
	for _, g := range w.allRegistrations(t, a) {
		if (f.ClassID == "" || g.ClassID == f.ClassID) && (f.YearID == "" || g.YearID == f.YearID) && between(g.AppliedOn, f.From, f.To) && (g.Status == "accepted" || admittedIDs[g.ID]) {
			x.acceptedOrAdmitted++
		}
	}
	today := day(w.today)
	for _, e := range w.allEnquiries(t, a) {
		created := e.CreatedAt.In(india).Format("2006-01-02")
		if (f.ClassID != "" && (e.ClassID == nil || *e.ClassID != f.ClassID)) || (f.YearID != "" && (e.YearID == nil || *e.YearID != f.YearID)) || !between(created, f.From, f.To) {
			continue
		}
		x.enqTotal++
		x.byStatus[e.Status]++
		x.bySource[e.Source]++
		x.enqMonth[created[:7]]++
		if linked[e.ID] {
			x.registered++
		}
		if e.FollowUpDue {
			x.due++
			if *e.FollowUpDate == today {
				x.dueToday++
			} else {
				x.overdue++
			}
		}
	}
	return x
}

func (w world) check(t *testing.T, name string, f insights.Filter) insights.Insights {
	t.Helper()
	got, err := insights.Service{DB: w.f.App}.Get(modtest.In(w.f.Alpha), f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	x := w.expect(t, f)
	eq := func(what string, got, want any) {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %s = %v, want %v", name, what, got, want)
		}
	}
	for _, s := range enquiries.Statuses {
		eq("enquiries "+s, got.EnquiriesByStatus[s], x.byStatus[s])
	}
	for _, s := range enquiries.Sources {
		eq("source "+s, got.EnquiriesBySource[s], x.bySource[s])
	}
	for _, s := range registrations.Statuses {
		eq("registrations "+s, got.RegistrationsBy[s], x.regByStatus[s])
	}
	eq("enquiries total", got.EnquiriesTotal, x.enqTotal)
	eq("registrations total", got.RegistrationsTotal, x.regTotal)
	eq("followUps", got.FollowUps, insights.FollowUps{Due: x.due, DueToday: x.dueToday, Overdue: x.overdue})
	eq("enquiry->registration", [2]int{got.Conversion.EnquiryToRegistration.Of, got.Conversion.EnquiryToRegistration.Converted}, [2]int{x.enqTotal, x.registered})
	eq("registration->accepted", [2]int{got.Conversion.RegistrationToAccepted.Of, got.Conversion.RegistrationToAccepted.Converted}, [2]int{x.regTotal, x.regByStatus["accepted"]})
	eq("admitted", got.RegistrationsAdmitted, x.admitted)
	if r := got.Conversion.AcceptedToAdmitted; r == nil {
		t.Errorf("%s: acceptedToAdmitted is missing", name)
	} else {
		eq("accepted->admitted", [2]int{r.Of, r.Converted}, [2]int{x.acceptedOrAdmitted, x.admitted})
	}
	ratios := []insights.Ratio{got.Conversion.EnquiryToRegistration, got.Conversion.RegistrationToAccepted}
	if got.Conversion.AcceptedToAdmitted != nil {
		ratios = append(ratios, *got.Conversion.AcceptedToAdmitted)
	}
	for _, r := range ratios {
		if (r.Of == 0) != (r.Rate == nil) {
			t.Errorf("%s: rate %v with %d in the denominator", name, r.Rate, r.Of)
		}
		if r.Rate != nil && (*r.Rate < 0 || *r.Rate > 1) {
			t.Errorf("%s: rate %v out of range", name, *r.Rate)
		}
	}
	for _, m := range got.Trend {
		eq("trend enquiries "+m.Month, m.Enquiries, x.enqMonth[m.Month])
		eq("trend registrations "+m.Month, m.Registrations, x.regMonth[m.Month])
	}
	// the dashboard's own statuses add up to its totals
	sum := 0
	for _, v := range got.EnquiriesByStatus {
		sum += v
	}
	eq("status sum", sum, got.EnquiriesTotal)
	return got
}

func TestDashboardEqualsTheLists(t *testing.T) {
	w := setup(t)
	w.build(t)
	from, to := day(w.month(3).AddDate(0, 0, 4)), day(w.month(1).AddDate(0, 0, 14))
	cases := map[string]insights.Filter{
		"no filter":      {},
		"class":          {ClassID: w.class},
		"other class":    {ClassID: w.class2},
		"year":           {YearID: w.year},
		"year and class": {YearID: w.year, ClassID: w.class},
		"second year":    {YearID: w.year2},
		"date range":     {From: from, To: to},
		"from only":      {From: day(w.month(2))},
		"to only":        {To: day(w.month(2).AddDate(0, 0, 27))},
		"all filters":    {YearID: w.year, ClassID: w.class2, From: day(w.month(5)), To: day(w.today)},
		"empty result":   {From: day(w.today.AddDate(0, 0, 1)), To: day(w.today.AddDate(0, 0, 2))},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) { w.check(t, name, f) })
	}
	// sanity: the data is rich enough that the comparison means something
	all := w.check(t, "all", insights.Filter{})
	if all.EnquiriesTotal != 9 || all.RegistrationsTotal != 10 || all.FollowUps.DueToday < 2 || all.FollowUps.Overdue < 1 || all.Conversion.EnquiryToRegistration.Converted != 3 || all.RegistrationsAdmitted < 2 || all.RegistrationsAdmitted >= all.RegistrationsAcceptedOrAdmitted {
		t.Errorf("fixture too thin: %+v", all)
	}
}

// The counts the list endpoint can filter on itself (status, source, class, followUpDue) are compared with its own total.
func TestDashboardEqualsTheListTotals(t *testing.T) {
	w := setup(t)
	w.build(t)
	a := modtest.In(w.f.Alpha)
	es := enquiries.Service{DB: w.f.App}
	q, _ := crud.ParseList(1, 1, "", "", enquiries.SortKeys, enquiries.DefaultSort)
	for _, class := range []string{"", w.class, w.class2} {
		got, err := insights.Service{DB: w.f.App}.Get(a, insights.Filter{ClassID: class})
		must(t, err)
		for _, s := range enquiries.Statuses {
			p, err := es.List(a, q, enquiries.Filter{Status: s, ClassID: class})
			must(t, err)
			if got.EnquiriesByStatus[s] != p.Total {
				t.Errorf("class %q status %s: dashboard %d, list %d", class, s, got.EnquiriesByStatus[s], p.Total)
			}
		}
		for _, s := range enquiries.Sources {
			p, err := es.List(a, q, enquiries.Filter{Source: s, ClassID: class})
			must(t, err)
			if got.EnquiriesBySource[s] != p.Total {
				t.Errorf("class %q source %s: dashboard %d, list %d", class, s, got.EnquiriesBySource[s], p.Total)
			}
		}
		yes := true
		p, err := es.List(a, q, enquiries.Filter{FollowUpDue: &yes, ClassID: class})
		must(t, err)
		if got.FollowUps.Due != p.Total {
			t.Errorf("class %q follow-ups: dashboard %d, list %d", class, got.FollowUps.Due, p.Total)
		}
		// admitted equals the admissions list (state=admitted)
		aq, _ := crud.ParseList(1, 1, "", "", admissions.SortKeys, admissions.DefaultSort)
		for state, want := range map[string]int{admissions.StateAdmitted: got.RegistrationsAdmitted} {
			p, err := admissions.Service{DB: w.f.App}.ListAudited(a, w.f.Actor, aq, admissions.Filter{State: state, ClassID: class})
			must(t, err)
			if p.Total != want {
				t.Errorf("class %q admissions %s: list %d, dashboard %d", class, state, p.Total, want)
			}
		}
		rq, _ := crud.ParseList(1, 1, "", "", registrations.SortKeys, registrations.DefaultSort)
		for _, s := range registrations.Statuses {
			p, err := registrations.Service{DB: w.f.App}.List(a, rq, registrations.Filter{Status: s, ClassID: class})
			must(t, err)
			if got.RegistrationsBy[s] != p.Total {
				t.Errorf("class %q registrations %s: dashboard %d, list %d", class, s, got.RegistrationsBy[s], p.Total)
			}
		}
	}
}

func TestInstitutesAreIsolated(t *testing.T) {
	w := setup(t)
	w.build(t)
	svc := insights.Service{DB: w.f.App}
	alpha, err := svc.Get(modtest.In(w.f.Alpha), insights.Filter{})
	must(t, err)
	bravo, err := svc.Get(modtest.In(w.f.Bravo), insights.Filter{})
	must(t, err)
	if alpha.EnquiriesTotal != 9 || bravo.EnquiriesTotal != 4 || bravo.RegistrationsTotal != 1 || bravo.Conversion.EnquiryToRegistration.Converted != 1 {
		t.Errorf("alpha %d enquiries; bravo %d enquiries, %d registrations, %d registered", alpha.EnquiriesTotal, bravo.EnquiriesTotal, bravo.RegistrationsTotal, bravo.Conversion.EnquiryToRegistration.Converted)
	}
	// bravo's class and year ids find nothing in alpha, and the reverse
	if got, err := svc.Get(modtest.In(w.f.Alpha), insights.Filter{ClassID: w.bClass}); err != nil || got.EnquiriesTotal != 0 || got.RegistrationsTotal != 0 {
		t.Errorf("alpha with bravo's class: %+v %v", got, err)
	}
	if got, err := svc.Get(modtest.In(w.f.Bravo), insights.Filter{YearID: w.year}); err != nil || got.EnquiriesTotal != 0 {
		t.Errorf("bravo with alpha's year: %+v %v", got, err)
	}
	// without an institute there is nothing to read
	if _, err := svc.Get(context.Background(), insights.Filter{}); err == nil {
		t.Error("no institute in the context must fail")
	}
}

func TestTrendIsZeroFilledAndUsesTheIndianCalendar(t *testing.T) {
	w := setup(t)
	w.build(t)
	from, to := w.month(6), w.month(0).AddDate(0, 1, -1)
	got := w.check(t, "trend", insights.Filter{From: day(from), To: day(to)})
	if len(got.Trend) != 7 || got.Trend[0].Month != from.Format("2006-01") || got.Trend[6].Month != w.month(0).Format("2006-01") || got.From != day(from) {
		t.Fatalf("trend months: %+v", got.Trend)
	}
	// month(4) has one enquiry and nothing else; a month with nothing at all must still be present with zeros
	empty := 0
	for _, m := range got.Trend {
		if m.Enquiries == 0 && m.Registrations == 0 {
			empty++
		}
	}
	if empty == 0 {
		t.Errorf("fixture has no empty month: %+v", got.Trend)
	}
	// the enquiry created 30 minutes into the 1st (India) of month(2) is counted in month(2), not month(3)
	for _, m := range got.Trend {
		if m.Month == w.month(2).Format("2006-01") && m.Enquiries < 1 {
			t.Errorf("the 1st-in-India enquiry is missing from %s: %+v", m.Month, got.Trend)
		}
	}
	// a one-month range
	one := w.check(t, "one month", insights.Filter{From: day(w.month(1)), To: day(w.month(1).AddDate(0, 1, -1))})
	if len(one.Trend) != 1 || one.Trend[0].Enquiries != 3 || one.Trend[0].Registrations != 2+1 {
		t.Errorf("one month: %+v", one.Trend)
	}
}

func TestDefaultsAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC) // 08:30 on 23 Nov in India
	first, last, err := insights.Validate(insights.Filter{}, now)
	must(t, err)
	if day(first) != "2025-12-01" || day(last) != "2026-11-01" {
		t.Errorf("default trend = %s..%s", day(first), day(last))
	}
	// 19:00 UTC on 30 Nov is already 1 Dec in India
	_, last, err = insights.Validate(insights.Filter{}, time.Date(2026, 11, 30, 19, 0, 0, 0, time.UTC))
	must(t, err)
	if day(last) != "2026-12-01" {
		t.Errorf("trend end across the month boundary = %s", day(last))
	}
	first, last, err = insights.Validate(insights.Filter{From: "2026-02-10"}, now)
	if err != nil || day(first) != "2026-02-01" || day(last) != "2026-11-01" {
		t.Errorf("from only: %s..%s %v", day(first), day(last), err)
	}
	first, _, err = insights.Validate(insights.Filter{To: "2026-03-05"}, now)
	if err != nil || day(first) != "2025-04-01" {
		t.Errorf("to only: %s %v", day(first), err)
	}
	for name, c := range map[string]struct {
		f     insights.Filter
		field string
	}{
		"bad from":  {insights.Filter{From: "2026-13-01"}, "from"},
		"bad to":    {insights.Filter{To: "yesterday"}, "to"},
		"reversed":  {insights.Filter{From: "2026-05-02", To: "2026-05-01"}, "to"},
		"too long":  {insights.Filter{From: "2020-01-01", To: "2026-01-01"}, "to"},
		"bad class": {insights.Filter{ClassID: "nope"}, "classId"},
		"bad year":  {insights.Filter{YearID: "123"}, "yearId"},
	} {
		_, _, err := insights.Validate(c.f, now)
		var fe crud.FieldErrors
		if !errors.As(err, &fe) || fe[0].Field != c.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	// exactly 60 months is fine
	if _, _, err := insights.Validate(insights.Filter{From: "2022-01-01", To: "2026-12-31"}, now); err != nil {
		t.Errorf("60 months: %v", err)
	}
}

func TestAnEmptyInstituteGivesZerosNotErrors(t *testing.T) {
	w := setup(t)
	got, err := insights.Service{DB: w.f.App}.Get(modtest.In(w.f.Alpha), insights.Filter{})
	must(t, err)
	if got.EnquiriesTotal != 0 || got.RegistrationsTotal != 0 || got.FollowUps != (insights.FollowUps{}) || got.Conversion.EnquiryToRegistration.Rate != nil ||
		got.Conversion.RegistrationToAccepted.Rate != nil || len(got.Trend) != 12 || len(got.EnquiriesByStatus) != 5 || len(got.EnquiriesBySource) != 6 || len(got.RegistrationsBy) != 5 {
		t.Errorf("%+v", got)
	}
}
