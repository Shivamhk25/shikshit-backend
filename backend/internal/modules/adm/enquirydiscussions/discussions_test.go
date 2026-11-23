package enquirydiscussions_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquirydiscussions"
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
	enq, enq2, otherEnquiry string
	enqCode                 string
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	svc := enquiries.Service{DB: f.App}
	mk := func(ctxTenant int, child string) enquiries.Enquiry {
		tn := f.Alpha
		if ctxTenant == 1 {
			tn = f.Bravo
		}
		e, err := svc.Create(modtest.In(tn), f.Actor, enquiries.Input{EnquirerName: "Meera Rao", Phone: "9876543210", ChildName: child, Source: "walk_in", FollowUpDate: ptr("2026-12-01")})
		must(t, err)
		return e
	}
	e1, e2, o := mk(0, "Asha Rao"), mk(0, "Kabir Das"), mk(1, "Other Child")
	return world{f: f, enq: e1.ID, enq2: e2.ID, otherEnquiry: o.ID, enqCode: e1.Code}
}

func note(enquiry string) enquirydiscussions.Input {
	return enquirydiscussions.Input{EnquiryID: enquiry, ContactedAt: time.Now().Add(-time.Hour), Channel: "call", Summary: "Asked about fees",
		Outcome: "interested", Language: "hi"}
}

func TestDiscussionRules(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := enquirydiscussions.Service{DB: w.f.App}
	in := note(w.enq)
	in.Notes, in.NextFollowUpDate = ptr("  Mother will visit with father  "), ptr(time.Now().AddDate(0, 0, 3).Format("2006-01-02"))
	in.Summary = "  Asked about fees  "
	d, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if d.EnquiryCode != w.enqCode || d.EnquiryChildName != "Asha Rao" || d.Summary != "Asked about fees" || *d.Notes != "Mother will visit with father" ||
		d.LoggedByName == nil || *d.LoggedByName != "Asha" || d.LoggedByID == nil || *d.LoggedByID != w.f.Actor.UserID || d.Language != "hi" {
		t.Fatalf("%+v", d)
	}
	future := time.Now().Add(2 * time.Hour)
	for name, c := range map[string]struct {
		mut   func(*enquirydiscussions.Input)
		field string
	}{
		"no enquiry":          {func(i *enquirydiscussions.Input) { i.EnquiryID = "" }, "enquiryId"},
		"enquiry not a uuid":  {func(i *enquirydiscussions.Input) { i.EnquiryID = "nope" }, "enquiryId"},
		"unknown enquiry":     {func(i *enquirydiscussions.Input) { i.EnquiryID = "00000000-0000-0000-0000-000000000001" }, "enquiryId"},
		"enquiry of another":  {func(i *enquirydiscussions.Input) { i.EnquiryID = w.otherEnquiry }, "enquiryId"},
		"no time":             {func(i *enquirydiscussions.Input) { i.ContactedAt = time.Time{} }, "contactedAt"},
		"future time":         {func(i *enquirydiscussions.Input) { i.ContactedAt = future }, "contactedAt"},
		"no channel":          {func(i *enquirydiscussions.Input) { i.Channel = "" }, "channel"},
		"bad channel":         {func(i *enquirydiscussions.Input) { i.Channel = "telegram" }, "channel"},
		"no summary":          {func(i *enquirydiscussions.Input) { i.Summary = "  " }, "summary"},
		"long summary":        {func(i *enquirydiscussions.Input) { i.Summary = strings.Repeat("s", 201) }, "summary"},
		"long notes":          {func(i *enquirydiscussions.Input) { i.Notes = ptr(strings.Repeat("n", 2001)) }, "notes"},
		"no outcome":          {func(i *enquirydiscussions.Input) { i.Outcome = "" }, "outcome"},
		"bad outcome":         {func(i *enquirydiscussions.Input) { i.Outcome = "won" }, "outcome"},
		"no language":         {func(i *enquirydiscussions.Input) { i.Language = "" }, "language"},
		"bad language":        {func(i *enquirydiscussions.Input) { i.Language = "klingon" }, "language"},
		"bad follow-up":       {func(i *enquirydiscussions.Input) { i.NextFollowUpDate = ptr("tomorrow") }, "nextFollowUpDate"},
		"follow-up before":    {func(i *enquirydiscussions.Input) { i.NextFollowUpDate = ptr("2000-01-01") }, "nextFollowUpDate"},
		"update without date": {func(i *enquirydiscussions.Input) { i.UpdateEnquiryFollowUp = true }, "nextFollowUpDate"},
	} {
		x := note(w.enq)
		c.mut(&x)
		_, err := svc.Create(a, w.f.Actor, x)
		fieldErr(t, name, err, c.field)
	}
	// every language, channel and outcome on the lists is accepted, and the case is forgiven
	for _, l := range enquirydiscussions.Languages {
		x := note(w.enq)
		x.Language = strings.ToUpper(l)
		if _, err := svc.Create(a, w.f.Actor, x); err != nil {
			t.Errorf("language %s: %v", l, err)
		}
	}
	// isolation: another institute sees nothing and cannot change it
	b := modtest.In(w.f.Bravo)
	if _, err := svc.View(b, w.f.Actor, d.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation view: %v", err)
	}
	if _, err := svc.Update(b, w.f.Actor, d.ID, note(w.otherEnquiry)); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation update: %v", err)
	}
	if err := svc.Delete(b, w.f.Actor, d.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation delete: %v", err)
	}
	// replace changes every field, and a note can be moved to the right enquiry
	up := note(w.enq2)
	up.Summary, up.Outcome, up.Channel = "Visited the school", "visit_scheduled", "visit"
	if u, err := svc.Update(a, w.f.Actor, d.ID, up); err != nil || u.EnquiryID != w.enq2 || u.Channel != "visit" || u.Outcome != "visit_scheduled" || u.Notes != nil {
		t.Errorf("update: %+v %v", u, err)
	}
	must(t, svc.Delete(a, w.f.Actor, d.ID))
	if _, err := svc.View(a, w.f.Actor, d.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
	if w.f.AuditCount(t, "enquiry_discussions.create") != 1+len(enquirydiscussions.Languages) || w.f.AuditCount(t, "enquiry_discussions.update") != 1 || w.f.AuditCount(t, "enquiry_discussions.delete") != 1 {
		t.Errorf("audit: %d creates, %d updates, %d deletes", w.f.AuditCount(t, "enquiry_discussions.create"), w.f.AuditCount(t, "enquiry_discussions.update"), w.f.AuditCount(t, "enquiry_discussions.delete"))
	}
}

func TestDiscussionDateBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) // 15:30 in India
	at := func(contacted time.Time, next *string, update bool) error {
		in := note(strings.Repeat("a", 8) + "-0000-0000-0000-000000000000")
		in.ContactedAt, in.NextFollowUpDate, in.UpdateEnquiryFollowUp = contacted, next, update
		_, err := enquirydiscussions.NormaliseAt(in, now)
		return err
	}
	for name, c := range map[string]struct {
		contacted time.Time
		next      *string
		ok        bool
	}{
		"now":                       {now, nil, true},
		"a few minutes ahead":       {now.Add(4 * time.Minute), nil, true},
		"ten minutes ahead":         {now.Add(10 * time.Minute), nil, false},
		"follow-up the same day":    {now, ptr("2026-11-22"), true},
		"follow-up the day before":  {now, ptr("2026-11-21"), false},
		"follow-up uses India date": {time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC), ptr("2026-11-22"), true}, // 01:30 on the 22nd in India
		"india date is the floor":   {time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC), ptr("2026-11-21"), false},
	} {
		if err := at(c.contacted, c.next, false); (err == nil) != c.ok {
			t.Errorf("%s: ok=%v err=%v", name, c.ok, err)
		}
	}
}

func TestDiscussionCanMoveTheEnquiryFollowUpDate(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := enquirydiscussions.Service{DB: w.f.App}
	enq := enquiries.Service{DB: w.f.App}
	followUp := func(id string) string {
		e, err := enq.Get(a, id)
		must(t, err)
		return *e.FollowUpDate
	}
	next := time.Now().AddDate(0, 0, 5).Format("2006-01-02")
	in := note(w.enq)
	in.NextFollowUpDate = &next
	// not asked: the enquiry keeps its date
	if _, err := svc.Create(a, w.f.Actor, in); err != nil || followUp(w.enq) != "2026-12-01" {
		t.Fatalf("without the flag: %v %s", err, followUp(w.enq))
	}
	// asked: the enquiry's date moves in the same save, and the change is in the audit trail
	in.UpdateEnquiryFollowUp = true
	d, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if followUp(w.enq) != next || d.NextFollowUpDate == nil || *d.NextFollowUpDate != next {
		t.Errorf("flag: enquiry %s note %v", followUp(w.enq), d.NextFollowUpDate)
	}
	if w.f.AuditCount(t, "enquiries.update") != 1 {
		t.Errorf("the enquiry change must be audited: %d", w.f.AuditCount(t, "enquiries.update"))
	}
	// a replace can move it again
	later := time.Now().AddDate(0, 0, 9).Format("2006-01-02")
	in.NextFollowUpDate = &later
	if _, err := svc.Update(a, w.f.Actor, d.ID, in); err != nil || followUp(w.enq) != later {
		t.Errorf("update with the flag: %v %s", err, followUp(w.enq))
	}
	// a failed save leaves the enquiry alone: the note is rejected (unknown language) and the date does not move
	bad := in
	bad.Language, bad.NextFollowUpDate = "klingon", ptr(time.Now().AddDate(0, 0, 20).Format("2006-01-02"))
	if _, err := svc.Create(a, w.f.Actor, bad); err == nil || followUp(w.enq) != later {
		t.Errorf("a rejected note must not move the date: %v %s", err, followUp(w.enq))
	}
}

func TestDiscussionListFiltersSearchAndDeletedEnquiries(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := enquirydiscussions.Service{DB: w.f.App}
	base := time.Now().Add(-48 * time.Hour)
	mk := func(enq, summary, channel, outcome, lang string, ago time.Duration) enquirydiscussions.Discussion {
		i := note(enq)
		i.Summary, i.Channel, i.Outcome, i.Language, i.ContactedAt = summary, channel, outcome, lang, base.Add(ago)
		d, err := svc.Create(a, w.f.Actor, i)
		must(t, err)
		return d
	}
	mk(w.enq, "fees question", "call", "interested", "hi", 1*time.Hour)
	mk(w.enq, "school visit", "visit", "visit_scheduled", "en", 2*time.Hour)
	mk(w.enq2, "whatsapp reply", "whatsapp", "not_reachable", "mr", 3*time.Hour)
	mk(w.enq2, "second call", "call", "call_back_later", "hi", 4*time.Hour)
	list := func(search, sort string, f enquirydiscussions.Filter) string {
		t.Helper()
		q, err := crud.ParseList(1, 50, sort, search, enquirydiscussions.SortKeys, enquirydiscussions.DefaultSort)
		must(t, err)
		p, err := svc.ListAudited(a, w.f.Actor, q, f)
		must(t, err)
		var out []string
		for _, d := range p.Items {
			out = append(out, d.Summary)
		}
		return strings.Join(out, ",")
	}
	for name, c := range map[string]struct {
		search, sort string
		f            enquirydiscussions.Filter
		want         string
	}{
		"newest first":       {"", "", enquirydiscussions.Filter{}, "second call,whatsapp reply,school visit,fees question"},
		"oldest first":       {"", "contactedAt", enquirydiscussions.Filter{}, "fees question,school visit,whatsapp reply,second call"},
		"one enquiry":        {"", "contactedAt", enquirydiscussions.Filter{EnquiryID: w.enq}, "fees question,school visit"},
		"channel":            {"", "contactedAt", enquirydiscussions.Filter{Channel: "call"}, "fees question,second call"},
		"outcome":            {"", "contactedAt", enquirydiscussions.Filter{Outcome: "not_reachable"}, "whatsapp reply"},
		"language":           {"", "contactedAt", enquirydiscussions.Filter{Language: "hi"}, "fees question,second call"},
		"filters and":        {"", "contactedAt", enquirydiscussions.Filter{Channel: "call", EnquiryID: w.enq2}, "second call"},
		"search the note":    {"visit", "contactedAt", enquirydiscussions.Filter{}, "school visit"},
		"search child name":  {"kabir", "contactedAt", enquirydiscussions.Filter{}, "whatsapp reply,second call"},
		"search enquiry no.": {w.enqCode, "contactedAt", enquirydiscussions.Filter{}, "fees question,school visit"},
		"sort by enquiry":    {"", "enquiryCode,contactedAt", enquirydiscussions.Filter{}, "fees question,school visit,whatsapp reply,second call"},
		"nothing":            {"nobody", "", enquirydiscussions.Filter{}, ""},
	} {
		if got := list(c.search, c.sort, c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	// another institute sees none of them
	q, _ := crud.ParseList(1, 50, "", "", enquirydiscussions.SortKeys, enquirydiscussions.DefaultSort)
	if p, err := svc.ListAudited(modtest.In(w.f.Bravo), w.f.Actor, q, enquirydiscussions.Filter{}); err != nil || p.Total != 0 {
		t.Errorf("isolation: %+v %v", p, err)
	}
	if w.f.AuditCount(t, "enquiry_discussions.list") != 13 {
		t.Errorf("each list read is audited: %d", w.f.AuditCount(t, "enquiry_discussions.list"))
	}
	// deleting an enquiry hides its notes from lists and from a direct read, and they can no longer be changed
	first, err := svc.View(a, w.f.Actor, func() string {
		q, _ := crud.ParseList(1, 50, "contactedAt", "", enquirydiscussions.SortKeys, enquirydiscussions.DefaultSort)
		p, _ := svc.ListAudited(a, w.f.Actor, q, enquirydiscussions.Filter{EnquiryID: w.enq})
		return p.Items[0].ID
	}())
	must(t, err)
	must(t, enquiries.Service{DB: w.f.App}.Delete(a, w.f.Actor, w.enq))
	if got := list("", "contactedAt", enquirydiscussions.Filter{}); got != "whatsapp reply,second call" {
		t.Errorf("notes of a deleted enquiry still listed: %q", got)
	}
	if _, err := svc.View(a, w.f.Actor, first.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("view after the enquiry is deleted: %v", err)
	}
	if _, err := svc.Update(a, w.f.Actor, first.ID, note(w.enq2)); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("update after the enquiry is deleted: %v", err)
	}
	if err := svc.Delete(a, w.f.Actor, first.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("delete after the enquiry is deleted: %v", err)
	}
	if _, err := svc.Create(a, w.f.Actor, note(w.enq)); err == nil {
		t.Error("a note cannot be added to a deleted enquiry")
	}
}
