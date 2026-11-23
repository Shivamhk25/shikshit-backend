package registrations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func ptr[T any](v T) *T { return &v }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fieldErr(t *testing.T, name string, err error, field string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Errorf("%s: want field errors, got %v", name, err)
		return
	}
	for _, e := range fe {
		if e.Field == field {
			return
		}
	}
	t.Errorf("%s: no error on %q: %v", name, field, fe)
}

type world struct {
	f                       modtest.Fixture
	class, year, section    string
	otherClass, otherYear   string
	otherSection, unmapped  string
	siblingCode, otherSibCd string
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	a, b := modtest.In(f.Alpha), modtest.In(f.Bravo)
	now := time.Now().UTC()
	start, end := now.AddDate(0, -2, 0).Format("2006-01-02"), now.AddDate(0, 10, 0).Format("2006-01-02")
	y, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "Y", Name: "2026-27", StartDate: start, EndDate: end})
	must(t, err)
	c, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	oy, err := years.Service{DB: f.App}.Create(b, f.Actor, years.Input{Code: "Y", Name: "Other", StartDate: start, EndDate: end})
	must(t, err)
	oc, err := classes.Service{DB: f.App}.Create(b, f.Actor, classes.Input{Code: "V", Name: "Class 5"})
	must(t, err)
	w := world{f: f, class: c.ID, year: y.ID, otherClass: oc.ID, otherYear: oy.ID, siblingCode: "S-100"}
	bg := context.Background()
	for _, s := range []struct {
		tenant, code string
		dst          *string
	}{{f.Alpha.ID, "A", &w.section}, {f.Alpha.ID, "B", &w.unmapped}, {f.Bravo.ID, "A", &w.otherSection}} {
		must(t, f.Admin.QueryRow(bg, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, $2, 'Section ' || $2) RETURNING id::text`, s.tenant, s.code).Scan(s.dst))
	}
	_, err = f.Admin.Exec(bg, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2, $3, $4)`, f.Alpha.ID, y.ID, c.ID, w.section)
	must(t, err)
	for _, s := range []struct{ tenant, code string }{{f.Alpha.ID, w.siblingCode}, {f.Bravo.ID, "S-OTHER"}} {
		_, err = f.Admin.Exec(bg, `INSERT INTO students (tenant_id, registration_code, first_name, gender, date_of_birth) VALUES ($1, $2, 'Sib', 'female', '2015-01-01')`, s.tenant, s.code)
		must(t, err)
	}
	w.otherSibCd = "S-OTHER"
	return w
}

func (w world) reg() registrations.Input {
	return registrations.Input{AppliedOn: registrations.Today(time.Now()), FirstName: "Asha", LastName: ptr("Rao"), DateOfBirth: "2018-06-01",
		GuardianPhone: "+91 98765 43210", ClassID: w.class, YearID: w.year}
}

func TestRegistrationRules(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	in := w.reg()
	in.MiddleName, in.Gender, in.FatherName, in.MotherName, in.GuardianName = ptr("K"), ptr(" Female "), ptr("Ravi Rao"), ptr("Meera Rao"), ptr("Ravi Rao")
	in.SectionID, in.SiblingStudentCode = &w.section, ptr("s-100")
	g, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if g.ApplicationNumber != "REG-000001" || g.Status != "submitted" || g.FullName != "Asha K Rao" || g.ClassName != "Class 5" || g.YearName != "2026-27" ||
		g.SectionName == nil || *g.SectionName != "Section A" || g.Gender == nil || *g.Gender != "female" || g.EnquiryID != nil {
		t.Fatalf("%+v", g)
	}
	n := 0
	for name, c := range map[string]struct {
		mut   func(*registrations.Input)
		field string
	}{
		"no first name":        {func(i *registrations.Input) { i.FirstName = " " }, "firstName"},
		"long first name":      {func(i *registrations.Input) { i.FirstName = strings.Repeat("a", 101) }, "firstName"},
		"no dob":               {func(i *registrations.Input) { i.DateOfBirth = "" }, "dateOfBirth"},
		"future dob":           {func(i *registrations.Input) { i.DateOfBirth = time.Now().AddDate(0, 0, 2).Format("2006-01-02") }, "dateOfBirth"},
		"too old":              {func(i *registrations.Input) { i.DateOfBirth = "1980-01-01" }, "dateOfBirth"},
		"bad dob":              {func(i *registrations.Input) { i.DateOfBirth = "01/06/2015" }, "dateOfBirth"},
		"no phone":             {func(i *registrations.Input) { i.GuardianPhone = "" }, "guardianPhone"},
		"bad phone":            {func(i *registrations.Input) { i.GuardianPhone = "abc" }, "guardianPhone"},
		"short phone":          {func(i *registrations.Input) { i.GuardianPhone = "12345" }, "guardianPhone"},
		"bad gender":           {func(i *registrations.Input) { i.Gender = ptr("robot") }, "gender"},
		"bad status":           {func(i *registrations.Input) { i.Status = "enrolled" }, "status"},
		"applied in future":    {func(i *registrations.Input) { i.AppliedOn = time.Now().AddDate(0, 0, 3).Format("2006-01-02") }, "appliedOn"},
		"no applied date":      {func(i *registrations.Input) { i.AppliedOn = "" }, "appliedOn"},
		"no class":             {func(i *registrations.Input) { i.ClassID = "" }, "classId"},
		"no year":              {func(i *registrations.Input) { i.YearID = "" }, "yearId"},
		"class not a uuid":     {func(i *registrations.Input) { i.ClassID = "nope" }, "classId"},
		"class of another":     {func(i *registrations.Input) { i.ClassID = w.otherClass }, "classId"},
		"year of another":      {func(i *registrations.Input) { i.YearID = w.otherYear }, "yearId"},
		"section of another":   {func(i *registrations.Input) { i.SectionID = &w.otherSection }, "sectionId"},
		"section not in class": {func(i *registrations.Input) { i.SectionID = &w.unmapped }, "sectionId"},
		"unknown sibling":      {func(i *registrations.Input) { i.SiblingStudentCode = ptr("NOPE") }, "siblingStudentCode"},
		"sibling of another":   {func(i *registrations.Input) { i.SiblingStudentCode = &w.otherSibCd }, "siblingStudentCode"},
		"long father":          {func(i *registrations.Input) { i.FatherName = ptr(strings.Repeat("f", 151)) }, "fatherName"},
	} {
		n++
		x := w.reg()
		c.mut(&x)
		_, err := svc.Create(a, w.f.Actor, x)
		fieldErr(t, name, err, c.field)
	}
	// the section is optional
	if x, err := svc.Create(a, w.f.Actor, w.reg()); err != nil || x.SectionID != nil || x.ApplicationNumber != "REG-000002" {
		t.Errorf("no section: %+v %v", x, err)
	}
	// replace changes fields but never the number
	up := in
	up.Status, up.FirstName = "under_review", "Aisha"
	u, err := svc.Update(a, w.f.Actor, g.ID, up)
	if err != nil || u.Status != "under_review" || u.FirstName != "Aisha" || u.ApplicationNumber != g.ApplicationNumber {
		t.Errorf("update: %+v %v", u, err)
	}
	// isolation, audit, delete
	if _, err := svc.Get(modtest.In(w.f.Bravo), g.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	if w.f.AuditCount(t, "student_registrations.create") != 2 || w.f.AuditCount(t, "student_registrations.update") != 1 {
		t.Errorf("audit: %d creates, %d updates", w.f.AuditCount(t, "student_registrations.create"), w.f.AuditCount(t, "student_registrations.update"))
	}
	if err := (classes.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.class); err == nil {
		t.Error("a class with a registration must not be deleted")
	}
	must(t, svc.Delete(a, w.f.Actor, g.ID))
	// a deleted registration's number is not given out again
	if x, err := svc.Create(a, w.f.Actor, w.reg()); err != nil || x.ApplicationNumber != "REG-000003" {
		t.Errorf("number after delete: %+v %v", x, err)
	}
}

func TestRegistrationDateBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) // 15:30 on 22 November in India
	base := registrations.Input{AppliedOn: "2026-11-22", FirstName: "A", DateOfBirth: "2018-01-01", GuardianPhone: "9876543210",
		ClassID: "00000000-0000-0000-0000-000000000001", YearID: "00000000-0000-0000-0000-000000000002"}
	for _, c := range []struct {
		field, value string
		ok           bool
	}{
		{"dob", "2026-11-22", true}, {"dob", "2026-11-23", false}, {"dob", "1996-11-22", true}, {"dob", "1996-11-21", false},
		{"applied", "2026-11-22", true}, {"applied", "2026-11-23", false},
	} {
		in := base
		if c.field == "dob" {
			in.DateOfBirth = c.value
		} else {
			in.AppliedOn = c.value
		}
		if _, err := registrations.NormaliseAt(in, now); (err == nil) != c.ok {
			t.Errorf("%s %s: ok=%v err=%v", c.field, c.value, c.ok, err)
		}
	}
	// near midnight UTC it is already the next day in India
	late := time.Date(2026, 11, 22, 19, 0, 0, 0, time.UTC) // 00:30 on 23 November in India
	in := base
	in.AppliedOn = "2026-11-23"
	if _, err := registrations.NormaliseAt(in, late); err != nil {
		t.Errorf("applied on today in India: %v", err)
	}
}

// Two institutes number independently, and many registrations made at once never share a number.
func TestApplicationNumbersAreUniqueUnderRace(t *testing.T) {
	w := setup(t)
	svc := registrations.Service{DB: w.f.App}
	const n = 25
	nums := make([]string, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g, err := svc.Create(modtest.In(w.f.Alpha), w.f.Actor, w.reg())
			if err != nil {
				errs <- err
				return
			}
			nums[i] = g.ApplicationNumber
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	sort.Strings(nums)
	for i, got := range nums {
		if want := fmt.Sprintf("REG-%06d", i+1); got != want {
			t.Fatalf("numbers %v: position %d is %s, want %s (gaps or repeats)", nums, i, got, want)
		}
	}
	// another institute starts at one
	in := registrations.Input{AppliedOn: registrations.Today(time.Now()), FirstName: "B", DateOfBirth: "2018-06-01", GuardianPhone: "9876543210", ClassID: w.otherClass, YearID: w.otherYear}
	if g, err := svc.Create(modtest.In(w.f.Bravo), w.f.Actor, in); err != nil || g.ApplicationNumber != "REG-000001" {
		t.Errorf("other institute: %+v %v", g, err)
	}
	// a failed create gives its number back: the next one is still in sequence
	bad := w.reg()
	bad.FirstName = ""
	_, _ = svc.Create(modtest.In(w.f.Alpha), w.f.Actor, bad)
	bad = w.reg()
	bad.SiblingStudentCode = ptr("NOPE")
	_, _ = svc.Create(modtest.In(w.f.Alpha), w.f.Actor, bad)
	if g, err := svc.Create(modtest.In(w.f.Alpha), w.f.Actor, w.reg()); err != nil || g.ApplicationNumber != fmt.Sprintf("REG-%06d", n+1) {
		t.Errorf("after failures: %+v %v", g, err)
	}
}

func TestApplicationNumberAndEnquiryLinkCannotBeChanged(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	g, err := svc.Create(a, w.f.Actor, w.reg())
	must(t, err)
	// even straight SQL cannot renumber a registration or attach it to an enquiry later
	var enqID string
	must(t, w.f.Admin.QueryRow(context.Background(), `INSERT INTO enquiries (tenant_id, code, enquirer_name, phone, child_name, source) VALUES ($1, 'E', 'P', '9000000000', 'C', 'phone') RETURNING id::text`, w.f.Alpha.ID).Scan(&enqID))
	var num string
	var link *string
	must(t, w.f.Admin.QueryRow(context.Background(), `UPDATE student_registrations SET application_number = 'REG-999999', enquiry_id = $2 WHERE id::text = $1 RETURNING application_number, enquiry_id::text`, g.ID, enqID).Scan(&num, &link))
	if num != g.ApplicationNumber || link != nil {
		t.Errorf("number %s link %v changed", num, link)
	}
}

func enq(w world, _ string) enquiries.Input { // enquiry numbers are automatic (A-20)
	return enquiries.Input{EnquirerName: "Meera Rao", Phone: "+91 98765 43210", ChildName: "Asha Kumari Rao", Source: "walk_in",
		DateOfBirth: ptr("2018-06-01"), ClassID: &w.class, YearID: &w.year}
}

func status(t *testing.T, w world, id string) string {
	t.Helper()
	var s string
	must(t, w.f.Admin.QueryRow(context.Background(), `SELECT status FROM enquiries WHERE id::text = $1`, id).Scan(&s))
	return s
}

func TestRegisterAnEnquiry(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	eq, err := enquiries.Service{DB: w.f.App}.Create(a, w.f.Actor, enq(w, "E-1"))
	must(t, err)
	g, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{})
	must(t, err)
	if g.FirstName != "Asha" || g.LastName == nil || *g.LastName != "Kumari Rao" || g.DateOfBirth != "2018-06-01" || g.ClassID != w.class || g.YearID != w.year ||
		g.GuardianName == nil || *g.GuardianName != "Meera Rao" || g.GuardianPhone != "+91 98765 43210" || g.Status != "submitted" ||
		g.EnquiryID == nil || *g.EnquiryID != eq.ID || g.EnquiryCode == nil || *g.EnquiryCode != "ENQ-000001" || g.ApplicationNumber != "REG-000001" ||
		g.AppliedOn != registrations.Today(time.Now()) {
		t.Fatalf("%+v", g)
	}
	if s := status(t, w, eq.ID); s != "converted" {
		t.Errorf("enquiry status = %s", s)
	}
	if w.f.AuditCount(t, "student_registrations.create") != 1 || w.f.AuditCount(t, "enquiries.convert") != 1 {
		t.Errorf("audit: %d creates, %d conversions", w.f.AuditCount(t, "student_registrations.create"), w.f.AuditCount(t, "enquiries.convert"))
	}
	// only once
	if _, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{}); !errors.Is(err, registrations.ErrAlreadyConverted) {
		t.Errorf("second conversion: %v", err)
	}
	// an enquiry set to converted by hand, with no registration, is also refused
	by, err := enquiries.Service{DB: w.f.App}.Create(a, w.f.Actor, enq(w, "E-2"))
	must(t, err)
	_, err = w.f.Admin.Exec(context.Background(), `UPDATE enquiries SET status = 'converted' WHERE id::text = $1`, by.ID)
	must(t, err)
	if _, err := svc.CreateFromEnquiry(a, w.f.Actor, by.ID, registrations.Fill{}); !errors.Is(err, registrations.ErrAlreadyConverted) {
		t.Errorf("hand-converted: %v", err)
	}
	// reopening a converted enquiry by hand does not allow a second registration
	_, err = w.f.Admin.Exec(context.Background(), `UPDATE enquiries SET status = 'contacted' WHERE id::text = $1`, eq.ID)
	must(t, err)
	if _, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{}); !errors.Is(err, registrations.ErrAlreadyConverted) {
		t.Errorf("reopened enquiry: %v", err)
	}
	_, err = w.f.Admin.Exec(context.Background(), `UPDATE enquiries SET status = 'converted' WHERE id::text = $1`, eq.ID)
	must(t, err)
	// a closed enquiry is refused and stays closed
	cl := enq(w, "E-3")
	cl.Status = "closed"
	closed, err := enquiries.Service{DB: w.f.App}.Create(a, w.f.Actor, cl)
	must(t, err)
	if _, err := svc.CreateFromEnquiry(a, w.f.Actor, closed.ID, registrations.Fill{}); !errors.Is(err, registrations.ErrEnquiryClosed) || status(t, w, closed.ID) != "closed" {
		t.Errorf("closed: %v / %s", err, status(t, w, closed.ID))
	}
	// unknown, deleted and another institute's enquiries are not found
	other, err := enquiries.Service{DB: w.f.App}.Create(modtest.In(w.f.Bravo), w.f.Actor, enquiries.Input{EnquirerName: "P", Phone: "9000000000", ChildName: "C", Source: "phone"})
	must(t, err)
	for name, id := range map[string]string{"unknown": "00000000-0000-0000-0000-000000000001", "another institute": other.ID} {
		if _, err := svc.CreateFromEnquiry(a, w.f.Actor, id, registrations.Fill{}); !errors.Is(err, registrations.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if status(t, w, other.ID) != "new" {
		t.Error("another institute's enquiry was touched")
	}
	// the registration's own list shows the link; deleting the registration does not undo the enquiry's status
	must(t, svc.Delete(a, w.f.Actor, g.ID))
	if status(t, w, eq.ID) != "converted" {
		t.Error("deleting a registration changes the enquiry")
	}
}

func TestRegisterAnEnquiryNeedsTheMissingDetailsAndRollsBack(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	count := func() int {
		var n int
		must(t, w.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM student_registrations`).Scan(&n))
		return n
	}
	bare := enquiries.Input{EnquirerName: "Meera", Phone: "9876543210", ChildName: "Asha", Source: "phone"}
	eq, err := enquiries.Service{DB: w.f.App}.Create(a, w.f.Actor, bare)
	must(t, err)
	_, err = svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{})
	for _, f := range []string{"dateOfBirth", "classId", "yearId"} {
		fieldErr(t, "bare enquiry", err, f)
	}
	if count() != 0 || status(t, w, eq.ID) != "new" {
		t.Error("a refused conversion left something behind")
	}
	// a rule that fails after the checks (a bad gender, a section-less class from another institute) rolls everything back
	for name, fill := range map[string]registrations.Fill{
		"bad gender":    {DateOfBirth: ptr("2018-06-01"), ClassID: &w.class, YearID: &w.year, Gender: ptr("robot")},
		"foreign class": {DateOfBirth: ptr("2018-06-01"), ClassID: &w.otherClass, YearID: &w.year},
		"future dob":    {DateOfBirth: ptr("2999-01-01"), ClassID: &w.class, YearID: &w.year},
	} {
		if _, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, fill); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if count() != 0 || status(t, w, eq.ID) != "new" || w.f.AuditCount(t, "enquiries.convert") != 0 {
			t.Errorf("%s: rollback left a registration, a status change or an audit row", name)
		}
	}
	// the missing details can be given with the request
	g, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{DateOfBirth: ptr("2018-06-01"), ClassID: &w.class, YearID: &w.year, Gender: ptr("female")})
	if err != nil || g.Gender == nil || *g.Gender != "female" || g.FirstName != "Asha" || g.LastName != nil || status(t, w, eq.ID) != "converted" {
		t.Errorf("with fill: %+v %v", g, err)
	}
}

// Two people pressing "Register" at once: one wins, the other is told it is already converted.
func TestRegisterAnEnquiryRaces(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	eq, err := enquiries.Service{DB: w.f.App}.Create(a, w.f.Actor, enq(w, "E-1"))
	must(t, err)
	const n = 8
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateFromEnquiry(a, w.f.Actor, eq.ID, registrations.Fill{})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	ok, already := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, registrations.ErrAlreadyConverted):
			already++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || already != n-1 {
		t.Errorf("won %d, refused %d", ok, already)
	}
}

func TestRegistrationListFiltersAndSearch(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := registrations.Service{DB: w.f.App}
	c2, err := classes.Service{DB: w.f.App}.Create(a, w.f.Actor, classes.Input{Code: "VI", Name: "Class 6"})
	must(t, err)
	mk := func(first, last, phone, status string, class string) registrations.Registration {
		i := w.reg()
		i.FirstName, i.LastName, i.GuardianPhone, i.Status, i.ClassID = first, ptr(last), phone, status, class
		g, err := svc.Create(a, w.f.Actor, i)
		must(t, err)
		return g
	}
	one := mk("Anaya", "Shah", "9000000001", "submitted", w.class)
	mk("Kabir", "Das", "9000000002", "accepted", w.class)
	mk("Zoya", "Khan", "9000000003", "rejected", c2.ID)
	list := func(search string, f registrations.Filter) string {
		t.Helper()
		q, err := crud.ParseList(1, 50, "applicationNumber", search, registrations.SortKeys, registrations.DefaultSort)
		must(t, err)
		p, err := svc.List(a, q, f)
		must(t, err)
		var out []string
		for _, g := range p.Items {
			out = append(out, g.ApplicationNumber)
		}
		return strings.Join(out, ",")
	}
	for name, c := range map[string]struct {
		search string
		f      registrations.Filter
		want   string
	}{
		"all":            {"", registrations.Filter{}, "REG-000001,REG-000002,REG-000003"},
		"by first name":  {"kabir", registrations.Filter{}, "REG-000002"},
		"by full name":   {"anaya shah", registrations.Filter{}, "REG-000001"},
		"by number":      {"reg-000003", registrations.Filter{}, "REG-000003"},
		"by phone":       {"9000000002", registrations.Filter{}, "REG-000002"},
		"status":         {"", registrations.Filter{Status: "accepted"}, "REG-000002"},
		"class":          {"", registrations.Filter{ClassID: c2.ID}, "REG-000003"},
		"year":           {"", registrations.Filter{YearID: w.year}, "REG-000001,REG-000002,REG-000003"},
		"year of nobody": {"", registrations.Filter{YearID: w.otherYear}, ""},
		"filters and":    {"", registrations.Filter{ClassID: w.class, Status: "accepted"}, "REG-000002"},
		"nothing":        {"nobody", registrations.Filter{}, ""},
	} {
		if got := list(c.search, c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	q, _ := crud.ParseList(1, 50, "", "", registrations.SortKeys, registrations.DefaultSort)
	if p, err := svc.List(modtest.In(w.f.Bravo), q, registrations.Filter{}); err != nil || p.Total != 0 {
		t.Errorf("isolation: %+v %v", p, err)
	}
	if _, err := svc.ListAudited(a, w.f.Actor, q, registrations.Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.View(a, w.f.Actor, one.ID); err != nil {
		t.Fatal(err)
	}
	if w.f.AuditCount(t, "student_registrations.list") != 1 || w.f.AuditCount(t, "student_registrations.view") != 1 {
		t.Errorf("read audit: %d list, %d view", w.f.AuditCount(t, "student_registrations.list"), w.f.AuditCount(t, "student_registrations.view"))
	}
}
