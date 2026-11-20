package importers_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pie360/backend/internal/modules/aca/classes"
	"github.com/pie360/backend/internal/modules/aca/divisions"
	"github.com/pie360/backend/internal/modules/aca/importers"
	"github.com/pie360/backend/internal/modules/modtest"
	"github.com/pie360/backend/internal/platform/crud"
	"github.com/pie360/backend/internal/platform/importer"
	"github.com/pie360/backend/internal/platform/jobs"
	"github.com/pie360/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

type env struct {
	modtest.Fixture
	svc    importer.Service
	worker *jobs.Worker
	now    time.Time
}

func setup(t *testing.T) *env {
	t.Helper()
	e := &env{Fixture: modtest.New(t), now: time.Now()}
	e.svc = importer.Service{DB: e.App, Reg: importers.Registry(e.App), Now: func() time.Time { return e.now }}
	e.worker = &jobs.Worker{DB: e.App, Handlers: e.svc.Handlers()}
	return e
}

// drain runs every queued job.
func (e *env) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 50; i++ {
		ran, err := e.worker.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("jobs never settled")
}

func (e *env) upload(t *testing.T, entity, strategy, mode, csv string) importer.Batch {
	t.Helper()
	b, err := e.svc.Upload(modtest.In(e.Alpha), e.Actor, importer.UploadInput{Entity: entity, Strategy: strategy, CommitMode: mode, FileName: "x.csv", Data: []byte(csv)})
	if err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	return e.get(t, b.ID)
}

func (e *env) get(t *testing.T, id string) importer.Batch {
	t.Helper()
	b, err := e.svc.Get(modtest.In(e.Alpha), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *env) commit(t *testing.T, id string) importer.Batch {
	t.Helper()
	if _, err := e.svc.Commit(modtest.In(e.Alpha), e.Actor, id); err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	return e.get(t, id)
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func rowStatus(t *testing.T, e *env, id string) map[int]string {
	t.Helper()
	page, err := e.svc.Rows(modtest.In(e.Alpha), id, "", crud.ListQuery{Page: 1, Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	for _, r := range page.Items {
		out[r.RowNo] = r.Status
	}
	return out
}

const divs = "code,name,displayOrder\nPRI,Primary,1\nSEN,Senior,2\n"

func TestValidationIsADryRunThatWritesNothing(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "", divs)
	if b.Status != "validated" || b.ValidRows != 2 || b.ErrorRows != 0 || b.TotalRows != 2 {
		t.Fatalf("%+v", b)
	}
	if n := e.count(t, "school_divisions"); n != 0 {
		t.Fatalf("validation wrote %d divisions", n)
	}
	if n := e.AuditCount(t, "school_divisions.create"); n != 0 {
		t.Fatalf("validation left %d audit rows", n)
	}
}

func TestCommitCreatesRowsAndAuditsThemWithTheBatch(t *testing.T) {
	e := setup(t)
	b := e.commit(t, e.upload(t, "school_divisions", "", "", divs).ID)
	if b.Status != "committed" || b.CommittedRows != 2 || b.RollbackDeadline == nil {
		t.Fatalf("%+v", b)
	}
	if e.count(t, "school_divisions") != 2 {
		t.Fatal("rows not created")
	}
	var tagged int
	if err := e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE meta->>'importBatch' = $1`, b.ID).Scan(&tagged); err != nil || tagged != 2 {
		t.Fatalf("audit rows carrying the batch = %d (%v)", tagged, err)
	}
	// a second commit of the same batch is refused, so rows cannot be written twice
	if _, err := e.svc.Commit(modtest.In(e.Alpha), e.Actor, b.ID); err == nil {
		t.Fatal("committed twice")
	} else if !errors.As(err, new(importer.ConflictError)) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if e.count(t, "school_divisions") != 2 {
		t.Fatal("second commit wrote rows")
	}
}

func TestStrategies(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "school_divisions", "", "", "code,name\nPRI,Primary\n").ID)

	ins := e.upload(t, "school_divisions", "insert", "skip_invalid", "code,name\npri,Again\nNEW,New\n")
	st := rowStatus(t, e, ins.ID)
	if ins.ValidRows != 1 || ins.ErrorRows != 1 || st[2] != "error" || st[3] != "valid" {
		t.Fatalf("insert: %+v %v", ins, st)
	}
	upd := e.upload(t, "school_divisions", "update", "skip_invalid", "code,name\nPRI,Primary School\nMISSING,x\n")
	st = rowStatus(t, e, upd.ID)
	if upd.ValidRows != 1 || st[2] != "valid" || st[3] != "error" {
		t.Fatalf("update: %+v %v", upd, st)
	}
	ups := e.upload(t, "school_divisions", "upsert", "", "code,name\nPRI,Primary School\nSEN,Senior\n")
	if ups.ValidRows != 2 || ups.ErrorRows != 0 {
		t.Fatalf("upsert: %+v", ups)
	}
	done := e.commit(t, ups.ID)
	if done.CommittedRows != 2 || e.count(t, "school_divisions") != 2 {
		t.Fatalf("%+v", done)
	}
}

func TestRowsRepeatingAKeyInTheFileAreCaught(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "skip_invalid", "code,name\nPRI,Primary\nSEN,Senior\npri,Primary again\n")
	st := rowStatus(t, e, b.ID)
	if st[4] != "error" || b.ErrorRows != 1 || b.ValidRows != 2 {
		t.Fatalf("%+v %v", b, st)
	}
}

func TestAllOrNothingRefusesAFileWithErrors(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "all_or_nothing", "code,name\nPRI,Primary\n,No code\n")
	if b.ErrorRows != 1 {
		t.Fatalf("%+v", b)
	}
	if _, err := e.svc.Commit(modtest.In(e.Alpha), e.Actor, b.ID); !errors.As(err, new(importer.ConflictError)) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if e.count(t, "school_divisions") != 0 {
		t.Fatal("rows written")
	}
}

func TestSkipInvalidImportsTheGoodRowsOnly(t *testing.T) {
	e := setup(t)
	b := e.commit(t, e.upload(t, "school_divisions", "", "skip_invalid", "code,name\nPRI,Primary\n,No code\nSEN,Senior\n").ID)
	if b.CommittedRows != 2 || b.ErrorRows != 1 || e.count(t, "school_divisions") != 2 {
		t.Fatalf("%+v", b)
	}
}

// A row can pass validation and still fail at commit if the data changed in between. All-or-nothing must then leave
// the database untouched.
func TestAllOrNothingIsAtomicWhenARowFailsAtCommit(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "insert", "all_or_nothing", "code,name\nAAA,A\nBBB,B\nCCC,C\n")
	if b.ValidRows != 3 {
		t.Fatalf("%+v", b)
	}
	if _, err := (divisions.Service{DB: e.App}).Create(modtest.In(e.Alpha), e.Actor, divisions.Input{Code: "BBB", Name: "Taken meanwhile"}); err != nil {
		t.Fatal(err)
	}
	after := e.commit(t, b.ID)
	if after.Status != "validated" || after.Failure == nil || after.CommittedRows != 0 {
		t.Fatalf("%+v", after)
	}
	if e.count(t, "school_divisions") != 1 {
		t.Fatalf("a partial import was left behind: %d divisions", e.count(t, "school_divisions"))
	}
	if st := rowStatus(t, e, b.ID); st[3] != "error" || st[2] != "valid" {
		t.Fatalf("%v", st)
	}
}

func TestRollbackUndoesCreatedAndUpdatedRows(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "school_divisions", "", "", "code,name,displayOrder\nPRI,Primary,1\n").ID)

	b := e.commit(t, e.upload(t, "school_divisions", "upsert", "", "code,name,displayOrder\nPRI,Changed,9\nSEN,Senior,2\n").ID)
	if b.CommittedRows != 2 {
		t.Fatalf("%+v", b)
	}
	if _, err := e.svc.Rollback(modtest.In(e.Alpha), e.Actor, b.ID); err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	rb := e.get(t, b.ID)
	if rb.Status != "rolled_back" || rb.RevertedRows != 2 {
		t.Fatalf("%+v", rb)
	}
	if e.count(t, "school_divisions") != 1 {
		t.Fatal("the created division survived the rollback")
	}
	var name string
	var order int
	if err := e.Admin.QueryRow(context.Background(), `SELECT name, display_order FROM school_divisions WHERE code = 'PRI' AND deleted_at IS NULL`).Scan(&name, &order); err != nil || name != "Primary" || order != 1 {
		t.Fatalf("the updated division was not restored: %s %d %v", name, order, err)
	}
}

func TestRollbackLeavesRecordsChangedAfterTheImport(t *testing.T) {
	e := setup(t)
	b := e.commit(t, e.upload(t, "school_divisions", "", "", divs).ID)
	rows := rowStatus(t, e, b.ID)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	// someone edits Primary after the import
	list, err := divisions.Service{DB: e.App}.List(modtest.In(e.Alpha), crud.ListQuery{Page: 1, Size: 10, OrderBy: "display_order"}, divisions.Filter{})
	if err != nil || len(list.Items) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	pri := list.Items[0]
	if _, err := (divisions.Service{DB: e.App}).Update(modtest.In(e.Alpha), e.Actor, pri.ID, divisions.Input{Code: pri.Code, Name: "Edited by hand", DisplayOrder: pri.DisplayOrder}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Rollback(modtest.In(e.Alpha), e.Actor, b.ID); err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	rb := e.get(t, b.ID)
	if rb.RevertedRows != 1 {
		t.Fatalf("%+v", rb)
	}
	if e.count(t, "school_divisions") != 1 {
		t.Fatalf("want only the edited division left, have %d", e.count(t, "school_divisions"))
	}
	var name string
	if err := e.Admin.QueryRow(context.Background(), `SELECT name FROM school_divisions WHERE deleted_at IS NULL`).Scan(&name); err != nil || name != "Edited by hand" {
		t.Fatalf("the later edit was lost: %q %v", name, err)
	}
	page, _ := e.svc.Rows(modtest.In(e.Alpha), b.ID, "revert_failed", crud.ListQuery{Page: 1, Size: 10})
	if len(page.Items) != 1 || len(page.Items[0].Problems) == 0 {
		t.Fatalf("the skipped row is not explained: %+v", page.Items)
	}
}

func TestRollbackWindowEnds(t *testing.T) {
	e := setup(t)
	b := e.commit(t, e.upload(t, "school_divisions", "", "", divs).ID)
	e.now = e.now.Add(importer.RollbackWindow + time.Hour)
	if _, err := e.svc.Rollback(modtest.In(e.Alpha), e.Actor, b.ID); !errors.As(err, new(importer.ConflictError)) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if e.count(t, "school_divisions") != 2 {
		t.Fatal("rows removed after the window")
	}
}

func TestOnlyACommittedBatchCanBeRolledBack(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "", divs)
	if _, err := e.svc.Rollback(modtest.In(e.Alpha), e.Actor, b.ID); !errors.As(err, new(importer.ConflictError)) {
		t.Fatalf("want ConflictError, got %v", err)
	}
}

func TestAnotherInstituteCannotSeeOrUseABatch(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "", divs)
	ctx := modtest.In(e.Bravo)
	if _, err := e.svc.Get(ctx, b.ID); !errors.Is(err, importer.ErrNotFound) {
		t.Fatalf("Get: %v", err)
	}
	if _, err := e.svc.Commit(ctx, e.Actor, b.ID); !errors.Is(err, importer.ErrNotFound) {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := e.svc.ErrorsCSV(ctx, b.ID); !errors.Is(err, importer.ErrNotFound) {
		t.Fatalf("ErrorsCSV: %v", err)
	}
	if _, err := e.svc.Rows(ctx, b.ID, "", crud.ListQuery{Page: 1, Size: 10}); !errors.Is(err, importer.ErrNotFound) {
		t.Fatalf("Rows: %v", err)
	}
	// bravo importing the same codes is independent of alpha's
	e.commit(t, b.ID)
	if _, err := e.svc.Upload(ctx, e.Actor, importer.UploadInput{Entity: "school_divisions", Strategy: "insert", FileName: "x.csv", Data: []byte(divs)}); err != nil {
		t.Fatal(err)
	}
	e.drain(t)
	var n int
	_ = e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM school_divisions WHERE deleted_at IS NULL`).Scan(&n)
	if n != 2 {
		t.Fatalf("bravo's validation wrote rows: %d", n)
	}
}

func TestClassesMatchTheirDivisionByCodeOrName(t *testing.T) {
	e := setup(t)
	e.commit(t, e.upload(t, "school_divisions", "", "", divs).ID)
	b := e.upload(t, "classes", "", "skip_invalid", "code,name,division,displayOrder\nV,Class 5,PRI,5\nIX,Class 9,senior,9\nX,Class 10,Nowhere,10\nXI,Class 11,,11\n")
	st := rowStatus(t, e, b.ID)
	if b.ValidRows != 3 || st[4] != "error" || st[2] != "valid" || st[3] != "valid" || st[5] != "valid" {
		t.Fatalf("%+v %v", b, st)
	}
	page, _ := e.svc.Rows(modtest.In(e.Alpha), b.ID, "error", crud.ListQuery{Page: 1, Size: 10})
	if len(page.Items) != 1 || page.Items[0].Problems[0].Column != "division" || page.Items[0].Problems[0].Code != "not_found" {
		t.Fatalf("%+v", page.Items)
	}
	e.commit(t, b.ID)
	cl, err := classes.Service{DB: e.App}.List(modtest.In(e.Alpha), crud.ListQuery{Page: 1, Size: 10, OrderBy: "display_order"}, classes.Filter{})
	if err != nil || len(cl.Items) != 3 || cl.Items[0].DivisionName == nil || *cl.Items[0].DivisionName != "Primary" {
		t.Fatalf("%v %+v", err, cl.Items)
	}
}

func TestAClassCannotPointAtAnotherInstitutesDivision(t *testing.T) {
	e := setup(t)
	if _, err := (divisions.Service{DB: e.App}).Create(modtest.In(e.Bravo), e.Actor, divisions.Input{Code: "PRI", Name: "Primary"}); err != nil {
		t.Fatal(err)
	}
	b := e.upload(t, "classes", "", "skip_invalid", "code,name,division\nV,Class 5,PRI\n")
	if b.ErrorRows != 1 {
		t.Fatalf("a division of another institute was matched: %+v", b)
	}
}

func TestSubjectsAndSections(t *testing.T) {
	e := setup(t)
	s := e.upload(t, "subjects", "", "skip_invalid",
		"code,name,category,area,scholastic,credits,color\nMATH,Mathematics,core,mathematics,yes,4,#2F5BFF\nBAD,Bad,nonsense,mathematics,yes,1,\nCOL,Colour,core,science,maybe,1,red\n")
	st := rowStatus(t, e, s.ID)
	if st[2] != "valid" || st[3] != "error" || st[4] != "error" {
		t.Fatalf("%v", st)
	}
	e.commit(t, s.ID)
	sec := e.commit(t, e.upload(t, "sections", "", "", "code,name,shortName\nA,Section A,A\nB,Section B,B\n").ID)
	if sec.CommittedRows != 2 {
		t.Fatalf("%+v", sec)
	}
}

func TestFileProblemsStopAnUploadBeforeAnythingIsStored(t *testing.T) {
	e := setup(t)
	up := func(entity, name, data string) error {
		_, err := e.svc.Upload(modtest.In(e.Alpha), e.Actor, importer.UploadInput{Entity: entity, FileName: name, Data: []byte(data)})
		return err
	}
	cases := map[string]error{
		"missing column":     up("school_divisions", "x.csv", "code\nPRI\n"),
		"duplicate column":   up("school_divisions", "x.csv", "code,name,Code\nPRI,Primary,PRI\n"),
		"unknown entity":     up("nope", "x.csv", divs),
		"unsupported format": up("school_divisions", "x.pdf", divs),
		"no rows":            up("school_divisions", "x.csv", "code,name\n"),
	}
	for name, err := range cases {
		var fe crud.FieldErrors
		if !errors.As(err, &fe) {
			t.Errorf("%s: want field errors, got %v", name, err)
		}
	}
	var n int
	_ = e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM import_batches`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d batches stored for bad files", n)
	}
}

func TestUnknownColumnsAreIgnoredWithAWarning(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "", "code,name,Notes\nPRI,Primary,hello\n")
	if b.ValidRows != 1 {
		t.Fatalf("%+v", b)
	}
	ps, err := e.svc.FileProblems(modtest.In(e.Alpha), b.ID)
	if err != nil || len(ps) != 1 || ps[0].Code != "unknown_column" || ps[0].Severity != "warning" {
		t.Fatalf("%v %+v", err, ps)
	}
}

func TestErrorsFileIsSafeToOpenInASpreadsheet(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "skip_invalid", "code,name\n\"=HYPERLINK(\"\"http://x\"\")\",Evil\nPRI,Primary\n")
	out, err := e.svc.ErrorsCSV(modtest.In(e.Alpha), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "\xef\xbb\xbf") || strings.Contains(s, "\n=") || strings.Contains(s, ",=") || !strings.Contains(s, "'=HYPERLINK") {
		t.Fatalf("formula not neutralised:\n%s", s)
	}
	if strings.Contains(s, "Primary") {
		t.Fatal("a valid row is in the errors file")
	}
}

func TestTemplateListsTheColumns(t *testing.T) {
	e := setup(t)
	out, err := e.svc.Template("classes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Division") || !strings.Contains(string(out), "Code") {
		t.Fatalf("%s", out)
	}
	if _, err := e.svc.Template("nope"); err == nil {
		t.Fatal("template for an unknown entity")
	}
}

func TestPurgeExpiredRemovesOldBatchesAndTheirRows(t *testing.T) {
	e := setup(t)
	b := e.upload(t, "school_divisions", "", "", divs)
	n, err := importer.PurgeExpired(context.Background(), e.App, e.now)
	if err != nil || n != 0 {
		t.Fatalf("purged a live batch: %d %v", n, err)
	}
	n, err = importer.PurgeExpired(context.Background(), e.App, e.now.Add(importer.Retention+time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	var rows int
	_ = e.Admin.QueryRow(context.Background(), `SELECT count(*) FROM import_rows WHERE batch_id = $1::uuid`, b.ID).Scan(&rows)
	if rows != 0 {
		t.Fatalf("%d staged rows survived", rows)
	}
}
