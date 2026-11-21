// Package batch2 tests the class-section, subject-class, textbook and chapter masters together, because they
// point at each other and at the earlier masters.
package batch2_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pie360/backend/internal/modules/aca/chapters"
	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/aca/classsections"
	"github.com/pie360/backend/internal/modules/aca/rooms"
	"github.com/pie360/backend/internal/modules/aca/sections"
	"github.com/pie360/backend/internal/modules/aca/subjectclasses"
	"github.com/pie360/backend/internal/modules/aca/subjects"
	"github.com/pie360/backend/internal/modules/aca/textbooks"
	"github.com/pie360/backend/internal/modules/aca/years"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/resource"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

func ptr[T any](v T) *T { return &v }

type world struct {
	f                        modtest.Fixture
	year, class, class2, sec string
	sec2, subj, room         string
	other                    struct{ year, class, sec string }
}

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f}
	a, b := modtest.In(f.Alpha), modtest.In(f.Bravo)
	y, err := years.Service{DB: f.App}.Create(a, f.Actor, years.Input{Code: "2026", Name: "2026-27", StartDate: "2026-04-01", EndDate: "2027-03-31"})
	must(t, err)
	c1, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "C1", Name: "Class 1"})
	must(t, err)
	c2, err := classes.Service{DB: f.App}.Create(a, f.Actor, classes.Input{Code: "C2", Name: "Class 2"})
	must(t, err)
	s1, err := sections.Service{DB: f.App}.Create(a, f.Actor, sections.Input{Code: "A", Name: "A"})
	must(t, err)
	s2, err := sections.Service{DB: f.App}.Create(a, f.Actor, sections.Input{Code: "B", Name: "B"})
	must(t, err)
	sj, err := subjects.Service{DB: f.App}.Create(a, f.Actor, subjects.Input{Code: "MAT", Name: "Maths", Category: "core", Area: "mathematics", Scholastic: true, Credits: 4})
	must(t, err)
	r, err := rooms.Service{DB: f.App}.Create(a, f.Actor, rooms.Input{Name: "Room 1", Capacity: 40})
	must(t, err)
	oy, err := years.Service{DB: f.App}.Create(b, f.Actor, years.Input{Code: "2026", Name: "2026-27", StartDate: "2026-04-01", EndDate: "2027-03-31"})
	must(t, err)
	oc, err := classes.Service{DB: f.App}.Create(b, f.Actor, classes.Input{Code: "C1", Name: "Class 1"})
	must(t, err)
	os_, err := sections.Service{DB: f.App}.Create(b, f.Actor, sections.Input{Code: "A", Name: "A"})
	must(t, err)
	w.year, w.class, w.class2, w.sec, w.sec2, w.subj, w.room = y.ID, c1.ID, c2.ID, s1.ID, s2.ID, sj.ID, r.ID
	w.other.year, w.other.class, w.other.sec = oy.ID, oc.ID, os_.ID
	return w
}

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

func TestClassSectionMappings(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	svc := classsections.Service{DB: w.f.App}
	in := classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec, Capacity: ptr(40), RoomID: &w.room, Remarks: ptr("  front  ")}
	m, err := svc.Create(a, w.f.Actor, in)
	must(t, err)
	if m.ClassName != "Class 1" || m.SectionName != "A" || *m.RoomName != "Room 1" || *m.Remarks != "front" {
		t.Fatalf("%+v", m)
	}
	if _, err := svc.Create(a, w.f.Actor, in); err == nil {
		t.Error("duplicate accepted")
	} else {
		fieldErr(t, "duplicate", err, "sectionId")
	}
	for name, c := range map[string]struct {
		in    classsections.Input
		field string
	}{
		"no year":            {classsections.Input{ClassID: w.class, SectionID: w.sec2}, "yearId"},
		"no class":           {classsections.Input{YearID: w.year, SectionID: w.sec2}, "classId"},
		"capacity 0":         {classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec2, Capacity: ptr(0)}, "capacity"},
		"capacity 1001":      {classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec2, Capacity: ptr(1001)}, "capacity"},
		"other institute":    {classsections.Input{YearID: w.year, ClassID: w.other.class, SectionID: w.sec2}, "classId"},
		"other inst section": {classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.other.sec}, "sectionId"},
		"other inst year":    {classsections.Input{YearID: w.other.year, ClassID: w.class, SectionID: w.sec2}, "yearId"},
		"bad room":           {classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec2, RoomID: ptr("nope")}, "roomId"},
		"long remarks":       {classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec2, Remarks: ptr(strings.Repeat("x", 501))}, "remarks"},
	} {
		_, err := svc.Create(a, w.f.Actor, c.in)
		if name == "other inst year" && err != nil && !errors.As(err, new(crud.FieldErrors)) {
			continue // the year check may answer not-found instead of a field error
		}
		fieldErr(t, name, err, c.field)
	}
	// update, then another institute cannot see it
	in.Capacity = ptr(35)
	u, err := svc.Update(a, w.f.Actor, m.ID, in)
	must(t, err)
	if *u.Capacity != 35 {
		t.Errorf("update: %+v", u)
	}
	if _, err := svc.Get(modtest.In(w.f.Bravo), m.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
	q, _ := crud.ParseList(1, 50, "", "", classsections.SortKeys, classsections.DefaultSort)
	if p, err := svc.List(a, q, classsections.Filter{ClassID: w.class2}); err != nil || p.Total != 0 {
		t.Errorf("filter by other class: %+v %v", p, err)
	}
	if p, err := svc.List(a, q, classsections.Filter{ClassID: w.class}); err != nil || p.Total != 1 {
		t.Errorf("filter by class: %+v %v", p, err)
	}
	if w.f.AuditCount(t, "class_section_mappings.create") != 1 {
		t.Errorf("audit: %d", w.f.AuditCount(t, "class_section_mappings.create"))
	}
	must(t, svc.Delete(a, w.f.Actor, m.ID))
	if _, err := svc.Create(a, w.f.Actor, in); err != nil {
		t.Errorf("re-map after delete: %v", err)
	}
}

func TestSubjectClassMappingsAndGuards(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	cs := classsections.Service{DB: w.f.App}
	svc := subjectclasses.Service{DB: w.f.App}
	whole, err := svc.Create(a, w.f.Actor, subjectclasses.Input{YearID: w.year, ClassID: w.class, SubjectID: w.subj, PeriodsPerWeek: ptr(6), IsExamined: true})
	must(t, err)
	if whole.SectionID != nil || whole.SubjectName != "Maths" || whole.SubjectArea != "mathematics" {
		t.Fatalf("%+v", whole)
	}
	if _, err := svc.Create(a, w.f.Actor, subjectclasses.Input{YearID: w.year, ClassID: w.class, SubjectID: w.subj}); err == nil {
		t.Error("duplicate whole-class mapping accepted (NULL section must still collide)")
	}
	// a section must be mapped to the class first
	_, err = svc.Create(a, w.f.Actor, subjectclasses.Input{YearID: w.year, ClassID: w.class, SubjectID: w.subj, SectionID: &w.sec})
	fieldErr(t, "section not in class", err, "sectionId")
	cm, err := cs.Create(a, w.f.Actor, classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec})
	must(t, err)
	sm, err := svc.Create(a, w.f.Actor, subjectclasses.Input{YearID: w.year, ClassID: w.class, SubjectID: w.subj, SectionID: &w.sec})
	must(t, err)
	if _, err := svc.Create(a, w.f.Actor, subjectclasses.Input{YearID: w.year, ClassID: w.class, SubjectID: w.subj, SectionID: &w.sec}); err == nil {
		t.Error("duplicate section mapping accepted")
	}
	for name, c := range map[string]struct {
		in    subjectclasses.Input
		field string
	}{
		"periods -1":  {subjectclasses.Input{YearID: w.year, ClassID: w.class2, SubjectID: w.subj, PeriodsPerWeek: ptr(-1)}, "periodsPerWeek"},
		"periods 61":  {subjectclasses.Input{YearID: w.year, ClassID: w.class2, SubjectID: w.subj, PeriodsPerWeek: ptr(61)}, "periodsPerWeek"},
		"no subject":  {subjectclasses.Input{YearID: w.year, ClassID: w.class2}, "subjectId"},
		"bad section": {subjectclasses.Input{YearID: w.year, ClassID: w.class2, SubjectID: w.subj, SectionID: ptr("x")}, "sectionId"},
	} {
		_, err := svc.Create(a, w.f.Actor, c.in)
		fieldErr(t, name, err, c.field)
	}
	// guards: cannot unmap a section that still has subjects; cannot delete masters that are in use
	var inUse resource.InUseError
	if err := cs.Delete(a, w.f.Actor, cm.ID); !errors.As(err, &inUse) {
		t.Errorf("section mapping delete: %v", err)
	}
	if err := (classes.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.class); !errors.As(err, &inUse) {
		t.Errorf("class delete: %v", err)
	}
	if err := (sections.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.sec); !errors.As(err, &inUse) {
		t.Errorf("section delete: %v", err)
	}
	if err := (subjects.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.subj); !errors.As(err, &inUse) {
		t.Errorf("subject delete: %v", err)
	}
	if err := (years.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.year); !errors.As(err, &inUse) {
		t.Errorf("year delete: %v", err)
	}
	must(t, svc.Delete(a, w.f.Actor, sm.ID))
	must(t, cs.Delete(a, w.f.Actor, cm.ID))
	if err := (sections.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.sec); err != nil {
		t.Errorf("section free to delete now: %v", err)
	}
	if w.f.AuditCount(t, "subject_class_mappings.create") != 2 {
		t.Errorf("audit: %d", w.f.AuditCount(t, "subject_class_mappings.create"))
	}
}

func TestRoomInUseWhenMapped(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	m, err := classsections.Service{DB: w.f.App}.Create(a, w.f.Actor, classsections.Input{YearID: w.year, ClassID: w.class, SectionID: w.sec, RoomID: &w.room})
	must(t, err)
	var inUse resource.InUseError
	if err := (rooms.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.room); !errors.As(err, &inUse) {
		t.Fatalf("room delete: %v", err)
	}
	must(t, classsections.Service{DB: w.f.App}.Delete(a, w.f.Actor, m.ID))
	must(t, rooms.Service{DB: w.f.App}.Delete(a, w.f.Actor, w.room))
}

func TestTextbooksAndChapters(t *testing.T) {
	w := setup(t)
	a := modtest.In(w.f.Alpha)
	tb := textbooks.Service{DB: w.f.App}
	ch := chapters.Service{DB: w.f.App}
	in := textbooks.Input{Code: " NCERT-1 ", Title: " Maths 1 ", ClassID: w.class, SubjectID: w.subj, Isbn: ptr("978-81-7450-000-0"), BookUrl: ptr("https://example.org/b.pdf")}
	b, err := tb.Create(a, w.f.Actor, in)
	must(t, err)
	if b.Code != "NCERT-1" || b.Title != "Maths 1" || b.Status != "active" || b.ChapterCount != 0 {
		t.Fatalf("%+v", b)
	}
	in.Code = "ncert-1"
	_, err = tb.Create(a, w.f.Actor, in)
	fieldErr(t, "duplicate code", err, "code")
	if _, err := tb.Create(modtest.In(w.f.Bravo), w.f.Actor, textbooks.Input{Code: "NCERT-1", Title: "x", ClassID: w.other.class, SubjectID: w.subj}); err == nil {
		t.Error("textbook accepted a subject of another institute")
	}
	for name, c := range map[string]struct {
		mut   func(*textbooks.Input)
		field string
	}{
		"javascript url": {func(i *textbooks.Input) { i.BookUrl = ptr("javascript:alert(1)") }, "bookUrl"},
		"data url":       {func(i *textbooks.Input) { i.BookUrl = ptr("data:text/html,x") }, "bookUrl"},
		"ftp url":        {func(i *textbooks.Input) { i.BookUrl = ptr("ftp://example.org/b.pdf") }, "bookUrl"},
		"file url":       {func(i *textbooks.Input) { i.BookUrl = ptr("file://host/etc/passwd") }, "bookUrl"},
		"no host":        {func(i *textbooks.Input) { i.BookUrl = ptr("https://") }, "bookUrl"},
		"long url":       {func(i *textbooks.Input) { i.BookUrl = ptr("https://e.org/" + strings.Repeat("a", 500)) }, "bookUrl"},
		"bad isbn":       {func(i *textbooks.Input) { i.Isbn = ptr("12") }, "isbn"},
		"bad status":     {func(i *textbooks.Input) { i.Status = "gone" }, "status"},
		"no title":       {func(i *textbooks.Input) { i.Title = " " }, "title"},
		"other class":    {func(i *textbooks.Input) { i.ClassID = w.other.class }, "classId"},
	} {
		x := textbooks.Input{Code: "Z" + name[:2], Title: "T", ClassID: w.class, SubjectID: w.subj}
		c.mut(&x)
		_, err := tb.Create(a, w.f.Actor, x)
		fieldErr(t, name, err, c.field)
	}
	// chapters
	c1, err := ch.Create(a, w.f.Actor, chapters.Input{TextbookID: b.ID, SortOrder: 1, ChapterNo: ptr("1"), Title: "Numbers", Periods: ptr(8), FromPage: ptr(1), ToPage: ptr(20)})
	must(t, err)
	_, err = ch.Create(a, w.f.Actor, chapters.Input{TextbookID: b.ID, SortOrder: 2, ChapterNo: ptr("2"), Title: "Optional", Periods: ptr(5), IsOptional: true})
	must(t, err)
	_, err = ch.Create(a, w.f.Actor, chapters.Input{TextbookID: b.ID, SortOrder: 3, ChapterNo: ptr("1"), Title: "dup number"})
	fieldErr(t, "duplicate chapter no", err, "chapterNo")
	_, err = ch.Create(a, w.f.Actor, chapters.Input{TextbookID: b.ID, Title: "back", FromPage: ptr(30), ToPage: ptr(10)})
	fieldErr(t, "page range", err, "toPage")
	_, err = ch.Create(a, w.f.Actor, chapters.Input{TextbookID: b.ID, Title: "p", Periods: ptr(201)})
	fieldErr(t, "periods", err, "periods")
	_, err = ch.Create(a, w.f.Actor, chapters.Input{TextbookID: w.other.class, Title: "x"})
	if err == nil {
		t.Error("chapter accepted a foreign textbook id")
	}
	got, err := tb.Get(a, b.ID)
	must(t, err)
	if got.ChapterCount != 2 || got.PeriodsNeeded != 8 {
		t.Errorf("optional chapters must not count toward periods needed: %+v", got)
	}
	var inUse resource.InUseError
	if err := tb.Delete(a, w.f.Actor, b.ID); !errors.As(err, &inUse) {
		t.Errorf("textbook with chapters deleted: %v", err)
	}
	if err := (subjects.Service{DB: w.f.App}).Delete(a, w.f.Actor, w.subj); !errors.As(err, &inUse) {
		t.Errorf("subject with textbook deleted: %v", err)
	}
	must(t, ch.Delete(a, w.f.Actor, c1.ID))
	if _, err := ch.Get(modtest.In(w.f.Bravo), c1.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("isolation: %v", err)
	}
}
