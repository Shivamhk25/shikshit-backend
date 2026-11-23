package enquiries_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
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

func enq(code string) enquiries.Input {
	return enquiries.Input{Code: code, EnquirerName: "Meera Rao", Phone: "+91 98765 43210", ChildName: "Asha Rao", Source: "walk_in"}
}

type world struct {
	f                   modtest.Fixture
	class, year, staff  string
	otherClass, otherYr string
	guardianUser        string
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
	var gu string
	must(t, f.Admin.QueryRow(context.Background(), `INSERT INTO users (tenant_id, username, display_name, status, person_type, person_id)
		VALUES ($1, 'parent1', 'Parent One', 'active', 'guardian', gen_random_uuid()) RETURNING id::text`, f.Alpha.ID).Scan(&gu))
	return world{f: f, class: c.ID, year: y.ID, staff: f.Actor.UserID, otherClass: oc.ID, otherYr: oy.ID, guardianUser: gu}
}

func TestEnquiryRules(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := enquiries.Service{DB: w.f.App}
	in := enq("  E-001 ")
	in.Email, in.DateOfBirth, in.ClassID, in.YearID = ptr("meera@example.org"), ptr("2021-06-01"), &w.class, &w.year
	in.AssignedToID, in.FollowUpDate, in.Notes = &w.staff, ptr("2999-01-01"), ptr(" wants a visit ")
	e, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if e.Code != "E-001" || e.Status != "new" || e.ClassName == nil || *e.ClassName != "Class 5" || e.YearName == nil || *e.YearName != "2026-27" ||
		e.AssignedToName == nil || *e.AssignedToName != "Asha" || e.FollowUpDue || *e.Notes != "wants a visit" {
		t.Fatalf("%+v", e)
	}
	n := 0
	for name, c := range map[string]struct {
		mut   func(*enquiries.Input)
		field string
	}{
		"duplicate code":       {func(i *enquiries.Input) { i.Code = "e-001" }, "code"},
		"no code":              {func(i *enquiries.Input) { i.Code = " " }, "code"},
		"no enquirer":          {func(i *enquiries.Input) { i.EnquirerName = " " }, "enquirerName"},
		"no child":             {func(i *enquiries.Input) { i.ChildName = "" }, "childName"},
		"no phone":             {func(i *enquiries.Input) { i.Phone = "" }, "phone"},
		"bad phone":            {func(i *enquiries.Input) { i.Phone = "abc" }, "phone"},
		"short phone":          {func(i *enquiries.Input) { i.Phone = "12345" }, "phone"},
		"bad email":            {func(i *enquiries.Input) { i.Email = ptr("not an email") }, "email"},
		"display email":        {func(i *enquiries.Input) { i.Email = ptr("Meera <m@example.org>") }, "email"},
		"future dob":           {func(i *enquiries.Input) { i.DateOfBirth = ptr(time.Now().AddDate(0, 0, 2).Format("2006-01-02")) }, "dateOfBirth"},
		"too old":              {func(i *enquiries.Input) { i.DateOfBirth = ptr("1980-01-01") }, "dateOfBirth"},
		"bad dob":              {func(i *enquiries.Input) { i.DateOfBirth = ptr("01/06/2015") }, "dateOfBirth"},
		"bad source":           {func(i *enquiries.Input) { i.Source = "carrier_pigeon" }, "source"},
		"no source":            {func(i *enquiries.Input) { i.Source = "" }, "source"},
		"bad status":           {func(i *enquiries.Input) { i.Status = "gone" }, "status"},
		"bad follow-up":        {func(i *enquiries.Input) { i.FollowUpDate = ptr("tomorrow") }, "followUpDate"},
		"long notes":           {func(i *enquiries.Input) { i.Notes = ptr(strings.Repeat("n", 1001)) }, "notes"},
		"class not a uuid":     {func(i *enquiries.Input) { i.ClassID = ptr("nope") }, "classId"},
		"class of another":     {func(i *enquiries.Input) { i.ClassID = &w.otherClass }, "classId"},
		"year of another":      {func(i *enquiries.Input) { i.YearID = &w.otherYr }, "yearId"},
		"unknown staff":        {func(i *enquiries.Input) { i.AssignedToID = ptr("00000000-0000-0000-0000-000000000001") }, "assignedToId"},
		"assigned to a parent": {func(i *enquiries.Input) { i.AssignedToID = &w.guardianUser }, "assignedToId"},
	} {
		n++
		x := enq(fmt.Sprintf("Z-%d", n))
		c.mut(&x)
		_, err := svc.Create(a, w.f.Actor, x)
		fieldErr(t, name, err, c.field)
	}
	// a deleted class is not a valid choice, even though the foreign key would still accept it
	gone, err := classes.Service{DB: w.f.App}.Create(a, w.f.Actor, classes.Input{Code: "OLD", Name: "Old class"})
	must(t, err)
	must(t, classes.Service{DB: w.f.App}.Delete(a, w.f.Actor, gone.ID))
	x := enq("GONE")
	x.ClassID = &gone.ID
	_, err = svc.Create(a, w.f.Actor, x)
	fieldErr(t, "deleted class", err, "classId")
	// the same code is allowed in another institute, and the record is invisible there
	if _, err := svc.Create(modtest.In(w.f.Bravo), w.f.Actor, enq("E-001")); err != nil {
		t.Errorf("codes are per institute: %v", err)
	}
	if _, err := svc.Get(modtest.In(w.f.Bravo), e.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	// replace updates every field; a deleted code can be reused; the class cannot be deleted while an enquiry uses it
	up := in
	up.Code, up.Status, up.Source = "E-001", "contacted", "referral"
	if u, err := svc.Update(a, w.f.Actor, e.ID, up); err != nil || u.Status != "contacted" || u.Source != "referral" {
		t.Errorf("update: %+v %v", u, err)
	}
	if err := (classes.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.class); err == nil {
		t.Error("a class with an enquiry must not be deleted")
	}
	if w.f.AuditCount(t, "enquiries.create") != 2 || w.f.AuditCount(t, "enquiries.update") != 1 {
		t.Errorf("audit: %d creates, %d updates", w.f.AuditCount(t, "enquiries.create"), w.f.AuditCount(t, "enquiries.update"))
	}
	must(t, svc.Delete(a, w.f.Actor, e.ID))
	if _, err := svc.Create(a, w.f.Actor, enq("E-001")); err != nil {
		t.Errorf("code reuse after delete: %v", err)
	}
}

func TestEnquiryDateOfBirthBoundary(t *testing.T) {
	today := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	cases := map[string]bool{"2026-11-22": true, "2026-11-23": false, "1996-11-22": true, "1996-11-21": false}
	for dob, ok := range cases {
		in := enq("E")
		in.DateOfBirth = ptr(dob)
		_, err := enquiries.NormaliseAt(in, today)
		if (err == nil) != ok {
			t.Errorf("dob %s: ok=%v err=%v", dob, ok, err)
		}
	}
}

func TestEnquiryListFiltersAndSearch(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := enquiries.Service{DB: w.f.App}
	mk := func(code, child, phone, source, status string, cls *string, follow *string) enquiries.Enquiry {
		i := enq(code)
		i.ChildName, i.Phone, i.Source, i.Status, i.ClassID, i.FollowUpDate = child, phone, source, status, cls, follow
		e, err := svc.Create(a, w.f.Actor, i)
		must(t, err)
		return e
	}
	past, future := ptr("2020-01-01"), ptr("2999-01-01")
	one := mk("E1", "Anaya Shah", "9000000001", "walk_in", "new", &w.class, past)
	mk("E2", "Kabir Das", "9000000002", "website", "contacted", nil, future)
	three := mk("E3", "Zoya Khan", "9000000003", "website", "closed", &w.class, past) // closed: never due
	four := mk("E4", "Ravi Iyer", "9000000004", "phone", "visit_scheduled", nil, past)
	mk("E5", "Tara Nair", "9000000005", "other", "new", nil, nil)
	list := func(search string, f enquiries.Filter) []string {
		t.Helper()
		q, err := crud.ParseList(1, 50, "code", search, enquiries.SortKeys, enquiries.DefaultSort)
		must(t, err)
		p, err := svc.List(a, q, f)
		must(t, err)
		var codes []string
		for _, e := range p.Items {
			codes = append(codes, e.Code)
		}
		return codes
	}
	yes, no := ptr(true), ptr(false)
	for name, c := range map[string]struct {
		search string
		f      enquiries.Filter
		want   string
	}{
		"all":           {"", enquiries.Filter{}, "E1,E2,E3,E4,E5"},
		"by child name": {"kabir", enquiries.Filter{}, "E2"},
		"by phone":      {"9000000004", enquiries.Filter{}, "E4"},
		"by code":       {"e3", enquiries.Filter{}, "E3"},
		"status":        {"", enquiries.Filter{Status: "closed"}, "E3"},
		"class":         {"", enquiries.Filter{ClassID: w.class}, "E1,E3"},
		"source":        {"", enquiries.Filter{Source: "website"}, "E2,E3"},
		"due":           {"", enquiries.Filter{FollowUpDue: yes}, "E1,E4"},
		"not due":       {"", enquiries.Filter{FollowUpDue: no}, "E2,E3,E5"},
		"filters and":   {"", enquiries.Filter{FollowUpDue: yes, ClassID: w.class}, "E1"},
		"search+filter": {"zoya", enquiries.Filter{Source: "website"}, "E3"},
		"nothing":       {"nobody", enquiries.Filter{}, ""},
	} {
		if got := strings.Join(list(c.search, c.f), ","); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	if !one.FollowUpDue || three.FollowUpDue || !four.FollowUpDue {
		t.Errorf("followUpDue: %v %v %v", one.FollowUpDue, three.FollowUpDue, four.FollowUpDue)
	}
	// another institute sees none of them
	q, _ := crud.ParseList(1, 50, "", "", enquiries.SortKeys, enquiries.DefaultSort)
	if p, err := svc.List(modtest.In(w.f.Bravo), q, enquiries.Filter{}); err != nil || p.Total != 0 {
		t.Errorf("isolation: %+v %v", p, err)
	}
	// reads through the API methods are audited, without the search text
	if _, err := svc.ListAudited(a, w.f.Actor, q, enquiries.Filter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.View(a, w.f.Actor, one.ID); err != nil {
		t.Fatal(err)
	}
	if w.f.AuditCount(t, "enquiries.list") != 1 || w.f.AuditCount(t, "enquiries.view") != 1 {
		t.Errorf("read audit: %d list, %d view", w.f.AuditCount(t, "enquiries.list"), w.f.AuditCount(t, "enquiries.view"))
	}
}
