package importers_test

import (
	"context"
	"testing"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/classes"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/sections"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/subjects"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/years"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// masters arranges one year, two classes, two sections and one subject in alpha, and a class of the same code in bravo.
func masters(t *testing.T, e *env) {
	t.Helper()
	a := modtest.In(e.Alpha)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := years.Service{DB: e.App}.Create(a, e.Actor, years.Input{Code: "Y1", Name: "2026-27", StartDate: "2026-04-01", EndDate: "2027-03-31"})
	must(err)
	for _, c := range []string{"V", "VI"} {
		_, err = classes.Service{DB: e.App}.Create(a, e.Actor, classes.Input{Code: c, Name: "Class " + c})
		must(err)
	}
	for _, s := range []string{"A", "B"} {
		_, err = sections.Service{DB: e.App}.Create(a, e.Actor, sections.Input{Code: s, Name: "Section " + s})
		must(err)
	}
	_, err = subjects.Service{DB: e.App}.Create(a, e.Actor, subjects.Input{Code: "MATH", Name: "Maths", Category: "core", Area: "mathematics", Scholastic: true})
	must(err)
	_, err = classes.Service{DB: e.App}.Create(modtest.In(e.Bravo), e.Actor, classes.Input{Code: "BRAVO", Name: "Only in bravo"})
	must(err)
}

func (e *env) problemsOf(t *testing.T, id string) map[int]string {
	t.Helper()
	page, err := e.svc.Rows(modtest.In(e.Alpha), id, "error", crud.ListQuery{Page: 1, Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, r := range page.Items {
		if len(r.Problems) > 0 {
			out[r.RowNo] = r.Problems[0].Column + ":" + r.Problems[0].Code
		}
	}
	return out
}

func TestClassSectionMappingsImportByCodeAndRejectBadReferences(t *testing.T) {
	e := setup(t)
	masters(t, e)
	e.commit(t, e.upload(t, "rooms", "", "", "code,name\nR1,Room 1\n").ID)
	b := e.upload(t, "class_section_mappings", "", "skip_invalid",
		"year,class,section,capacity,room\nY1,V,A,40,R1\nY1,V,B,35,\n2026-27,vi,a,30,\nY1,Nope,A,30,\nY1,BRAVO,A,30,\nY1,V,A,30,\n")
	got := e.problemsOf(t, b.ID)
	if b.ValidRows != 3 || got[5] != "class:not_found" || got[6] != "class:not_found" || got[7] != "section:duplicate_in_file" {
		t.Fatalf("%+v %v", b, got)
	}
	e.commit(t, b.ID)
	if n := e.count(t, "class_section_mappings"); n != 3 {
		t.Fatalf("mappings: %d", n)
	}
}

func TestUpdatingAMappingKeepsBlankCells(t *testing.T) {
	e := setup(t)
	masters(t, e)
	e.commit(t, e.upload(t, "class_section_mappings", "", "", "year,class,section,capacity,remarks\nY1,V,A,40,front row\n").ID)
	b := e.upload(t, "class_section_mappings", "update", "", "year,class,section,capacity,remarks\nY1,V,A,,\n")
	if b.ErrorRows != 0 {
		t.Fatalf("%+v", b)
	}
	e.commit(t, b.ID)
	var capacity int
	var remarks string
	if err := e.Admin.QueryRow(context.Background(), `SELECT capacity, remarks FROM class_section_mappings WHERE deleted_at IS NULL`).Scan(&capacity, &remarks); err != nil {
		t.Fatal(err)
	}
	if capacity != 40 || remarks != "front row" {
		t.Fatalf("blank cells cleared the values: %d %q", capacity, remarks)
	}
	e.commit(t, e.upload(t, "class_section_mappings", "update", "", "year,class,section,capacity\nY1,V,A,45\n").ID)
	if err := e.Admin.QueryRow(context.Background(), `SELECT capacity, remarks FROM class_section_mappings WHERE deleted_at IS NULL`).Scan(&capacity, &remarks); err != nil || capacity != 45 || remarks != "front row" {
		t.Fatalf("a column left out of the file must keep the value: %d %q %v", capacity, remarks, err)
	}
}

func TestSubjectMappingsTellWholeClassFromSectionAndNeedTheSectionMappedFirst(t *testing.T) {
	e := setup(t)
	masters(t, e)
	// a section row before the section is mapped to the class is refused
	b := e.upload(t, "subject_class_mappings", "", "skip_invalid", "year,class,subject,section,periodsPerWeek\nY1,V,MATH,A,5\nY1,V,MATH,,6\nY1,V,MATH,,6\n")
	got := e.problemsOf(t, b.ID)
	if b.ValidRows != 1 || got[2] != "sectionId:not_in_class" && got[2] != "section:not_in_class" {
		t.Fatalf("%+v %v", b, got)
	}
	e.commit(t, b.ID)
	e.commit(t, e.upload(t, "class_section_mappings", "", "", "year,class,section\nY1,V,A\n").ID)
	b = e.upload(t, "subject_class_mappings", "", "", "year,class,subject,section,isElective\nY1,V,MATH,A,yes\n")
	if b.ErrorRows != 0 {
		t.Fatalf("a section row and a whole-class row for the same subject are different records: %+v %v", b, e.problemsOf(t, b.ID))
	}
	e.commit(t, b.ID)
	if n := e.count(t, "subject_class_mappings"); n != 2 {
		t.Fatalf("mappings: %d", n)
	}
	// re-importing the whole-class row with a blank flag keeps what it had, and defaults only apply on create
	e.commit(t, e.upload(t, "subject_class_mappings", "update", "", "year,class,subject,section,periodsPerWeek,isExamined\nY1,V,MATH,,8,\n").ID)
	var periods int
	var examined bool
	if err := e.Admin.QueryRow(context.Background(), `SELECT periods_per_week, is_examined FROM subject_class_mappings WHERE section_id IS NULL AND deleted_at IS NULL`).Scan(&periods, &examined); err != nil || periods != 8 || !examined {
		t.Fatalf("%d %v %v", periods, examined, err)
	}
}

func TestTextbooksAndChaptersImportAndMatchAgain(t *testing.T) {
	e := setup(t)
	masters(t, e)
	b := e.upload(t, "textbooks", "", "skip_invalid",
		"code,title,class,subject,isbn,bookUrl\nT1,Maths 5,V,MATH,978-81-7450-000-0,https://example.org/b\nT2,Bad link,V,MATH,,javascript:alert(1)\nT3,Bad subject,V,Nope,,\n")
	got := e.problemsOf(t, b.ID)
	if b.ValidRows != 1 || got[3] != "bookUrl:invalid_url" || got[4] != "subject:not_found" {
		t.Fatalf("%+v %v", b, got)
	}
	e.commit(t, b.ID)

	c := e.upload(t, "textbook_chapters", "", "skip_invalid",
		"textbook,chapterNo,title,sortOrder,periods,fromPage,toPage,isOptional\nT1,1,Numbers,1,8,1,20,\nT1,2,Extra,2,5,21,30,yes\nT1,1,Again,3,,,,\nT1,3,Backwards,4,,30,10,\nT9,1,Orphan,1,,,,\n")
	got = e.problemsOf(t, c.ID)
	if c.ValidRows != 2 || got[4] == "" || got[5] == "" || got[6] != "textbook:not_found" {
		t.Fatalf("%+v %v", c, got)
	}
	e.commit(t, c.ID)
	var needed int
	if err := e.Admin.QueryRow(context.Background(), `SELECT COALESCE(sum(periods),0) FROM textbook_chapters WHERE NOT is_optional AND deleted_at IS NULL`).Scan(&needed); err != nil || needed != 8 {
		t.Fatalf("%d %v", needed, err)
	}
	// a second upload of the same chapter number is an update, found by textbook and number
	up := e.upload(t, "textbook_chapters", "upsert", "", "textbook,chapterNo,title\nt1,1,Numbers and place value\n")
	e.commit(t, up.ID)
	var title string
	var periods int
	if err := e.Admin.QueryRow(context.Background(), `SELECT title, periods FROM textbook_chapters WHERE chapter_no = '1' AND deleted_at IS NULL`).Scan(&title, &periods); err != nil || title != "Numbers and place value" || periods != 8 {
		t.Fatalf("%q %d %v", title, periods, err)
	}
	if n := e.count(t, "textbook_chapters"); n != 2 {
		t.Fatalf("chapters: %d", n)
	}
}
