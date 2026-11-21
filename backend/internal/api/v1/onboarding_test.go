package apiv1_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/Shivamhk25/shikshit-backend/backend/internal/api/v1"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/importer"
)

type batchBody struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ValidRows     int    `json:"validRows"`
	ErrorRows     int    `json:"errorRows"`
	CommittedRows int    `json:"committedRows"`
	FileProblems  []struct {
		Code string `json:"code"`
	} `json:"fileProblems"`
}

type rowsBody struct {
	Items []struct {
		RowNo    int    `json:"rowNo"`
		Status   string `json:"status"`
		Problems []struct {
			Column, Code string
		} `json:"problems"`
	} `json:"items"`
}

// upload posts a multipart form; fields are name/value pairs and the file is sent as "file".
func (s accessStack) upload(token, fileName, content string, fields ...string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i := 0; i+1 < len(fields); i += 2 {
		_ = w.WriteField(fields[i], fields[i+1])
	}
	if fileName != "" {
		fw, _ := w.CreateFormFile("file", fileName)
		_, _ = fw.Write([]byte(content))
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, apiv1.BasePath+"/onboarding/imports", &buf)
	req.Host = "demo.shikshit.test"
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

func (s accessStack) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 50; i++ {
		ran, err := s.worker.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
	t.Fatal("jobs never settled")
}

const importDivs = "code,name\nPRI,Primary\nSEN,Senior\n"

func TestImportFlowOverHTTP(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken

	rec := s.get("/onboarding/entities", boss)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"school_divisions"`) || !strings.Contains(rec.Body.String(), `"displayOrder"`) {
		t.Fatalf("entities = %d %s", rec.Code, rec.Body)
	}
	rec = s.get("/onboarding/templates/classes", boss)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Body.String(), "Division") {
		t.Fatalf("template = %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}

	rec = s.upload(boss, "divisions.csv", importDivs+",,\nBAD\n", "entity", "school_divisions", "strategy", "insert", "commitMode", "skip_invalid")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	b := decode[batchBody](t, rec)
	s.drain(t)

	rec = s.get("/onboarding/imports/"+b.ID, boss)
	got := decode[batchBody](t, rec)
	if rec.Code != 200 || got.Status != "validated" || got.ValidRows != 2 || got.ErrorRows != 1 {
		t.Fatalf("batch = %d %s", rec.Code, rec.Body)
	}
	rec = s.get("/onboarding/imports/"+b.ID+"/rows?status=error", boss)
	rows := decode[rowsBody](t, rec)
	if rec.Code != 200 || len(rows.Items) != 1 || rows.Items[0].RowNo != 5 || len(rows.Items[0].Problems) == 0 {
		t.Fatalf("rows = %d %s", rec.Code, rec.Body)
	}
	rec = s.get("/onboarding/imports/"+b.ID+"/errors.csv", boss)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "BAD") || strings.Contains(rec.Body.String(), "Primary") {
		t.Fatalf("errors.csv = %d %s", rec.Code, rec.Body)
	}

	if rec = s.call("POST", "/onboarding/imports/"+b.ID+"/commit", boss, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body)
	}
	s.drain(t)
	if got = decode[batchBody](t, s.get("/onboarding/imports/"+b.ID, boss)); got.Status != "committed" || got.CommittedRows != 2 {
		t.Fatalf("after commit: %+v", got)
	}
	if rec = s.get("/school-divisions", boss); !strings.Contains(rec.Body.String(), `"total":2`) {
		t.Fatalf("divisions = %s", rec.Body)
	}
	if rec = s.call("POST", "/onboarding/imports/"+b.ID+"/commit", boss, ""); rec.Code != http.StatusConflict {
		t.Fatalf("second commit = %d, want 409", rec.Code)
	}
	if rec = s.call("POST", "/onboarding/imports/"+b.ID+"/rollback", boss, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("rollback = %d %s", rec.Code, rec.Body)
	}
	s.drain(t)
	if rec = s.get("/school-divisions", boss); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("divisions after rollback = %s", rec.Body)
	}
	if rec = s.get("/onboarding/imports", boss); rec.Code != 200 || !strings.Contains(rec.Body.String(), b.ID) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
}

func TestBadUploadsAreFieldErrors(t *testing.T) {
	s := newAccessStack(t)
	boss := s.signIn(t, "boss").AccessToken
	cases := map[string]*httptest.ResponseRecorder{
		"no file":        s.upload(boss, "", "", "entity", "school_divisions"),
		"unknown entity": s.upload(boss, "x.csv", importDivs, "entity", "payroll"),
		"bad strategy":   s.upload(boss, "x.csv", importDivs, "entity", "school_divisions", "strategy", "delete"),
		"wrong format":   s.upload(boss, "x.pdf", importDivs, "entity", "school_divisions"),
		"missing column": s.upload(boss, "x.csv", "code\nPRI\n", "entity", "school_divisions"),
	}
	for name, rec := range cases {
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), `"errors"`) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	// a file over the limit is refused without being read into memory
	big := "code,name\n" + strings.Repeat("A,B\n", importer.MaxFileBytes/4+10)
	if rec := s.upload(boss, "x.csv", big, "entity", "school_divisions"); rec.Code != 400 || !strings.Contains(rec.Body.String(), "file_too_large") {
		t.Errorf("oversize = %d %.200s", rec.Code, rec.Body)
	}
	// an unknown file reads as not found; so does a batch id that is not a UUID path
	if rec := s.get("/onboarding/imports/00000000-0000-0000-0000-000000000000", boss); rec.Code != 404 {
		t.Errorf("unknown batch = %d", rec.Code)
	}
	if rec := s.get("/onboarding/templates/payroll", boss); rec.Code != 404 {
		t.Errorf("unknown template = %d", rec.Code)
	}
}

func TestImportPermissionsAreCheckedPerOperationAndPerDomain(t *testing.T) {
	s := newAccessStack(t)
	boss, dean, intake, viewer := s.signIn(t, "boss").AccessToken, s.signIn(t, "dean").AccessToken, s.signIn(t, "intake").AccessToken, s.signIn(t, "viewer").AccessToken
	nobody, clerk := s.signIn(t, "nobody").AccessToken, s.signIn(t, "clerk").AccessToken

	// no import permission at all
	if rec := s.get("/onboarding/entities", nobody); rec.Code != 403 {
		t.Errorf("entities without onb.imports.view = %d", rec.Code)
	}
	if rec := s.upload(nobody, "x.csv", importDivs, "entity", "school_divisions"); rec.Code != 403 {
		t.Errorf("upload without onb.imports.import = %d", rec.Code)
	}
	// the registrar template carries onb.imports.* (for the students pack) but nothing for the academic domain
	if rec := s.get("/onboarding/entities", clerk); rec.Code != 200 || strings.Contains(rec.Body.String(), "school_divisions") {
		t.Errorf("registrar entities = %d %s", rec.Code, rec.Body)
	}
	if rec := s.upload(clerk, "x.csv", importDivs, "entity", "school_divisions"); rec.Code != 403 {
		t.Errorf("registrar academic upload = %d", rec.Code)
	}
	if rec := s.get("/onboarding/entities", ""); rec.Code != 401 {
		t.Errorf("anonymous = %d", rec.Code)
	}

	// the operation permission without the domain permission: nothing importable, nothing visible
	if rec := s.get("/onboarding/entities", intake); rec.Code != 200 || strings.Contains(rec.Body.String(), "school_divisions") {
		t.Errorf("intake entities = %d %s", rec.Code, rec.Body)
	}
	if rec := s.upload(intake, "x.csv", importDivs, "entity", "school_divisions"); rec.Code != 403 {
		t.Errorf("upload without onb.academic.import = %d %s", rec.Code, rec.Body)
	}
	if rec := s.get("/onboarding/templates/school_divisions", intake); rec.Code != 403 {
		t.Errorf("template without onb.academic.import = %d", rec.Code)
	}

	// a batch made by the boss is invisible to the one without the domain's view permission, and visible to the dean
	rec := s.upload(boss, "x.csv", importDivs, "entity", "school_divisions")
	if rec.Code != 202 {
		t.Fatalf("boss upload = %d %s", rec.Code, rec.Body)
	}
	id := decode[batchBody](t, rec).ID
	s.drain(t)
	for _, path := range []string{"", "/rows", "/errors.csv"} {
		if rec := s.get("/onboarding/imports/"+id+path, intake); rec.Code != 404 {
			t.Errorf("intake GET %s = %d, want 404", path, rec.Code)
		}
		if rec := s.get("/onboarding/imports/"+id+path, dean); rec.Code != 200 {
			t.Errorf("dean GET %s = %d", path, rec.Code)
		}
	}
	if rec := s.get("/onboarding/imports", intake); rec.Code != 200 || strings.Contains(rec.Body.String(), id) {
		t.Errorf("intake list shows another domain's batch: %s", rec.Body)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", intake, ""); rec.Code != 404 {
		t.Errorf("intake commit = %d, want 404", rec.Code)
	}

	// can see the batch (domain view) and holds onb.imports.commit, but not onb.academic.commit: 403, nothing queued
	if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", viewer, ""); rec.Code != 403 {
		t.Errorf("viewer commit = %d %s", rec.Code, rec.Body)
	}
	if got := decode[batchBody](t, s.get("/onboarding/imports/"+id, boss)); got.Status != "validated" {
		t.Errorf("a refused commit changed the batch: %s", got.Status)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/rollback", viewer, ""); rec.Code != 403 {
		t.Errorf("viewer rollback = %d", rec.Code)
	}

	// the dean has the full set for the domain
	if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", dean, ""); rec.Code != 202 {
		t.Errorf("dean commit = %d %s", rec.Code, rec.Body)
	}
	s.drain(t)
	if rec := s.call("POST", "/onboarding/imports/"+id+"/rollback", dean, ""); rec.Code != 202 {
		t.Errorf("dean rollback = %d %s", rec.Code, rec.Body)
	}
}

type packBody struct {
	Key   string `json:"key"`
	Ready bool   `json:"ready"`
	Steps []struct {
		Entity string `json:"entity"`
		State  string `json:"state"`
		Count  int    `json:"count"`
		Next   bool   `json:"next"`
	} `json:"steps"`
	Rules []struct {
		Key     string `json:"key"`
		Checked bool   `json:"checked"`
		Met     bool   `json:"met"`
	} `json:"rules"`
}

func TestAcademicPackStatusFollowsTheDomainPermission(t *testing.T) {
	s := newAccessStack(t)
	boss, dean, intake := s.signIn(t, "boss").AccessToken, s.signIn(t, "dean").AccessToken, s.signIn(t, "intake").AccessToken
	nobody, clerk := s.signIn(t, "nobody").AccessToken, s.signIn(t, "clerk").AccessToken

	rec := s.get("/onboarding/packs/academic", boss)
	p := decode[packBody](t, rec)
	if rec.Code != 200 || p.Key != "academic" || p.Ready || len(p.Steps) != 13 || p.Steps[0].Entity != "academic_years" || !p.Steps[0].Next || p.Steps[1].State != "waiting" {
		t.Fatalf("pack = %d %s", rec.Code, rec.Body)
	}
	checked := false
	for _, r := range p.Rules {
		if r.Key == "class_mappings" && r.Checked && !r.Met {
			checked = true
		}
	}
	if !checked {
		t.Fatalf("the mappings rule should be checked and not met: %s", rec.Body)
	}

	// importing a year changes the picture
	up := s.upload(boss, "y.csv", "code,name,startDate,endDate,isCurrent\nY1,Y1,2026-04-01,2027-03-31,yes\n", "entity", "academic_years")
	if up.Code != 202 {
		t.Fatalf("upload = %d %s", up.Code, up.Body)
	}
	s.drain(t)
	s.call("POST", "/onboarding/imports/"+decode[batchBody](t, up).ID+"/commit", boss, "")
	s.drain(t)
	p = decode[packBody](t, s.get("/onboarding/packs/academic", dean))
	if p.Steps[0].State != "done" || p.Steps[0].Count != 1 || p.Steps[1].State != "todo" {
		t.Fatalf("after import: %+v", p.Steps[:2])
	}

	for name, tok := range map[string]string{"intake": intake, "registrar": clerk} {
		if rec := s.get("/onboarding/packs/academic", tok); rec.Code != 404 {
			t.Errorf("%s without onb.academic.view = %d, want 404", name, rec.Code)
		}
	}
	if rec := s.get("/onboarding/packs/academic", nobody); rec.Code != 403 {
		t.Errorf("no role = %d, want 403", rec.Code)
	}
	if rec := s.get("/onboarding/packs/academic", ""); rec.Code != 401 {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if rec := s.get("/onboarding/packs/payroll", boss); rec.Code != 404 {
		t.Errorf("unknown pack = %d", rec.Code)
	}
	if rec := s.get("/onboarding/packs/students", boss); rec.Code != 200 {
		t.Errorf("students pack for the admin = %d", rec.Code)
	}
	if rec := s.get("/onboarding/packs/students", clerk); rec.Code != 200 {
		t.Errorf("students pack for the registrar = %d", rec.Code)
	}
	if rec := s.get("/onboarding/packs/students", intake); rec.Code != 404 {
		t.Errorf("a role without the students domain must not see the pack: %d", rec.Code)
	}
}

func TestAPrincipalCanRunTheAcademicSetup(t *testing.T) {
	s := newAccessStack(t)
	pria := s.signIn(t, "pria").AccessToken

	// the onboarding screen is offered, so the wizard is reachable
	a, _ := s.access(t, "pria")
	found := false
	for _, n := range a.Workspace.Navigation {
		if n.ScreenID == "onboarding" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a principal is not offered the onboarding screen: %+v", a.Workspace.Navigation)
	}
	if rec := s.get("/onboarding/entities", pria); rec.Code != 200 || !strings.Contains(rec.Body.String(), "school_divisions") {
		t.Fatalf("entities = %d %s", rec.Code, rec.Body)
	}
	if rec := s.get("/onboarding/packs/academic", pria); rec.Code != 200 {
		t.Fatalf("pack = %d %s", rec.Code, rec.Body)
	}
	up := s.upload(pria, "d.csv", importDivs, "entity", "school_divisions")
	if up.Code != 202 {
		t.Fatalf("upload = %d %s", up.Code, up.Body)
	}
	id := decode[batchBody](t, up).ID
	s.drain(t)
	if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", pria, ""); rec.Code != 202 {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body)
	}
	s.drain(t)
	if rec := s.call("POST", "/onboarding/imports/"+id+"/rollback", pria, ""); rec.Code != 202 {
		t.Fatalf("rollback = %d %s", rec.Code, rec.Body)
	}
}

func (s accessStack) requireApproval(t *testing.T, on bool) {
	t.Helper()
	if _, err := s.adm.Exec(bg, `UPDATE tenants SET settings = jsonb_set(settings, '{importApprovalRequired}', to_jsonb($2::boolean)) WHERE id = $1`, s.tenantID, on); err != nil {
		t.Fatal(err)
	}
}

type approvalBatch struct {
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	ApprovalRequired bool    `json:"approvalRequired"`
	ApprovedAt       *string `json:"approvedAt"`
	RejectReason     *string `json:"rejectReason"`
	CommittedRows    int     `json:"committedRows"`
}

func TestAdminOrPrincipalApprovesAFileSomeoneElseUploaded(t *testing.T) {
	s := newAccessStack(t)
	s.requireApproval(t, true)
	boss, pria := s.signIn(t, "boss").AccessToken, s.signIn(t, "pria").AccessToken

	// the principal uploads; the admin approves
	up := s.upload(pria, "d.csv", importDivs, "entity", "school_divisions")
	if up.Code != 202 || !decode[approvalBatch](t, up).ApprovalRequired {
		t.Fatalf("upload = %d %s", up.Code, up.Body)
	}
	id := decode[approvalBatch](t, up).ID
	s.drain(t)

	// nobody can commit it directly, whatever their rank, and the uploader cannot approve it
	for name, tok := range map[string]string{"admin": boss, "principal": pria} {
		if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", tok, ""); rec.Code != 409 {
			t.Errorf("%s commit = %d, want 409", name, rec.Code)
		}
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", pria, ""); rec.Code != 409 || !strings.Contains(rec.Body.String(), "someone else") {
		t.Errorf("self-approval = %d %s", rec.Code, rec.Body)
	}
	if rec := s.get("/school-divisions", boss); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("rows written before approval: %s", rec.Body)
	}

	rec := s.call("POST", "/onboarding/imports/"+id+"/approve", boss, "")
	if rec.Code != 202 {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body)
	}
	s.drain(t)
	got := decode[approvalBatch](t, s.get("/onboarding/imports/"+id, boss))
	if got.Status != "committed" || got.CommittedRows != 2 || got.ApprovedAt == nil {
		t.Fatalf("%+v", got)
	}
	if rec := s.get("/school-divisions", boss); !strings.Contains(rec.Body.String(), `"total":2`) {
		t.Fatalf("divisions = %s", rec.Body)
	}

	// and the other way round: the admin uploads, the principal approves
	up = s.upload(boss, "s.csv", "code,name\nA,Section A\n", "entity", "sections")
	id2 := decode[approvalBatch](t, up).ID
	s.drain(t)
	if rec := s.call("POST", "/onboarding/imports/"+id2+"/approve", pria, ""); rec.Code != 202 {
		t.Fatalf("principal approve = %d %s", rec.Code, rec.Body)
	}
}

func TestOnlyReviewersMayApproveOrReject(t *testing.T) {
	s := newAccessStack(t)
	s.requireApproval(t, true)
	boss, dean, intake, viewer := s.signIn(t, "boss").AccessToken, s.signIn(t, "dean").AccessToken, s.signIn(t, "intake").AccessToken, s.signIn(t, "viewer").AccessToken
	nobody, clerk := s.signIn(t, "nobody").AccessToken, s.signIn(t, "clerk").AccessToken
	up := s.upload(dean, "d.csv", importDivs, "entity", "school_divisions")
	if up.Code != 202 {
		t.Fatalf("upload = %d %s", up.Code, up.Body)
	}
	id := decode[approvalBatch](t, up).ID
	s.drain(t)

	// no onb.imports.review at all: refused by the operation
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", nobody, ""); rec.Code != 403 {
		t.Errorf("no role = %d", rec.Code)
	}
	// the operation permission without the domain's review: dean and viewer can see the file, so they are told 403
	for name, tok := range map[string]string{"dean": dean, "viewer": viewer} {
		for _, action := range []string{"approve", "reject"} {
			if rec := s.call("POST", "/onboarding/imports/"+id+"/"+action, tok, `{"reason":"no"}`); rec.Code != 403 {
				t.Errorf("%s %s without onb.academic.review = %d, want 403", name, action, rec.Code)
			}
		}
	}
	// onb.imports.review without the domain's review: the operation passes, the domain check refuses
	chet := s.signIn(t, "chet").AccessToken
	for _, action := range []string{"approve", "reject"} {
		if rec := s.call("POST", "/onboarding/imports/"+id+"/"+action, chet, `{"reason":"no"}`); rec.Code != 403 {
			t.Errorf("checker %s without onb.academic.review = %d, want 403", action, rec.Code)
		}
	}
	// intake lacks onb.imports.review itself (403). The registrar template holds onb.imports.* but nothing for the
	// academic domain, so it cannot see the file at all (404).
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", intake, ""); rec.Code != 403 {
		t.Errorf("intake approve = %d, want 403", rec.Code)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", clerk, ""); rec.Code != 404 {
		t.Errorf("registrar approve = %d, want 404", rec.Code)
	}
	if got := decode[approvalBatch](t, s.get("/onboarding/imports/"+id, boss)); got.Status != "validated" || got.ApprovedAt != nil {
		t.Fatalf("a refused request changed the file: %+v", got)
	}
	// the domain's review together with the operation permission is enough, for a person who uploaded nothing
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", s.signIn(t, "rina").AccessToken, ""); rec.Code != 202 {
		t.Errorf("reviewer approve = %d %s", rec.Code, rec.Body)
	}
}

func TestRejectingOverHTTPKeepsTheReason(t *testing.T) {
	s := newAccessStack(t)
	s.requireApproval(t, true)
	boss, pria := s.signIn(t, "boss").AccessToken, s.signIn(t, "pria").AccessToken
	id := decode[approvalBatch](t, s.upload(pria, "d.csv", importDivs, "entity", "school_divisions")).ID
	s.drain(t)

	if rec := s.call("POST", "/onboarding/imports/"+id+"/reject", boss, `{"reason":""}`); rec.Code != 400 {
		t.Errorf("empty reason = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/reject", boss, `{}`); rec.Code != 400 {
		t.Errorf("missing reason = %d", rec.Code)
	}
	rec := s.call("POST", "/onboarding/imports/"+id+"/reject", boss, `{"reason":"Wrong academic year."}`)
	got := decode[approvalBatch](t, rec)
	if rec.Code != 200 || got.Status != "rejected" || got.RejectReason == nil || *got.RejectReason != "Wrong academic year." {
		t.Fatalf("reject = %d %s", rec.Code, rec.Body)
	}
	// the uploader sees why
	if seen := decode[approvalBatch](t, s.get("/onboarding/imports/"+id, pria)); seen.Status != "rejected" || seen.RejectReason == nil {
		t.Fatalf("%+v", seen)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/approve", boss, ""); rec.Code != 409 {
		t.Errorf("approve after reject = %d", rec.Code)
	}
	if rec := s.get("/school-divisions", boss); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("rows written: %s", rec.Body)
	}
}

func TestWithApprovalOffTheUploaderCommitsDirectly(t *testing.T) {
	s := newAccessStack(t) // approval is off in this stack unless a test turns it on
	pria := s.signIn(t, "pria").AccessToken
	id := decode[approvalBatch](t, s.upload(pria, "d.csv", importDivs, "entity", "school_divisions")).ID
	s.drain(t)
	if rec := s.call("POST", "/onboarding/imports/"+id+"/commit", pria, ""); rec.Code != 202 {
		t.Fatalf("commit = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("POST", "/onboarding/imports/"+id+"/reject", pria, `{"reason":"x"}`); rec.Code != 409 {
		t.Errorf("reject when no approval was needed = %d, want 409", rec.Code)
	}
}

func TestOnlyManagersOfApprovalCanChangeTheSetting(t *testing.T) {
	s := newAccessStack(t)
	boss, pria, nobody := s.signIn(t, "boss").AccessToken, s.signIn(t, "pria").AccessToken, s.signIn(t, "nobody").AccessToken

	rec := s.get("/onboarding/settings", boss)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"importApprovalRequired":false`) { // this stack starts with approval off
		t.Fatalf("get = %d %s", rec.Code, rec.Body)
	}
	// someone without the permission can neither read nor change it
	for name, tok := range map[string]string{"no role": nobody} {
		if rec := s.get("/onboarding/settings", tok); rec.Code != 403 {
			t.Errorf("%s read = %d, want 403", name, rec.Code)
		}
		if rec := s.call("PUT", "/onboarding/settings", tok, `{"importApprovalRequired":true}`); rec.Code != 403 {
			t.Errorf("%s write = %d, want 403", name, rec.Code)
		}
	}
	ada := s.signIn(t, "ada").AccessToken
	if rec := s.get("/onboarding/settings", ada); rec.Code != 200 {
		t.Errorf("a viewer cannot read the setting: %d", rec.Code)
	}
	if rec := s.call("PUT", "/onboarding/settings", ada, `{"importApprovalRequired":false}`); rec.Code != 403 {
		t.Errorf("a viewer changed the setting: %d", rec.Code)
	}
	if rec := s.get("/onboarding/settings", ""); rec.Code != 401 {
		t.Errorf("anonymous = %d", rec.Code)
	}
	for _, body := range []string{`{}`, `{"importApprovalRequired":"yes"}`, `not json`} {
		if rec := s.call("PUT", "/onboarding/settings", boss, body); rec.Code != 400 {
			t.Errorf("body %q = %d, want 400", body, rec.Code)
		}
	}
	rec = s.call("PUT", "/onboarding/settings", boss, `{"importApprovalRequired":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"importApprovalRequired":true`) {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}

	// it takes effect: the principal's next file needs approval, and then it appears in the list for approvers
	id := decode[approvalBatch](t, s.upload(pria, "d.csv", importDivs, "entity", "school_divisions")).ID
	s.drain(t)
	if !decode[approvalBatch](t, s.get("/onboarding/imports/"+id, boss)).ApprovalRequired {
		t.Fatal("the new setting did not apply to a new file")
	}
	list := s.get("/onboarding/imports?awaitingApproval=true", boss)
	if list.Code != 200 || !strings.Contains(list.Body.String(), id) {
		t.Fatalf("awaiting list = %d %s", list.Code, list.Body)
	}
	s.call("POST", "/onboarding/imports/"+id+"/approve", boss, "")
	s.drain(t)
	if list = s.get("/onboarding/imports?awaitingApproval=true", boss); strings.Contains(list.Body.String(), id) {
		t.Fatalf("an approved file is still listed as waiting: %s", list.Body)
	}
	if rec := s.get("/onboarding/imports?awaitingApproval=maybe", boss); rec.Code != 400 {
		t.Errorf("bad flag = %d", rec.Code)
	}
}

func TestThePrincipalCanReadAndChangeTheApprovalSetting(t *testing.T) {
	s := newAccessStack(t)
	pria := s.signIn(t, "pria").AccessToken
	if rec := s.get("/onboarding/settings", pria); rec.Code != 200 {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	rec := s.call("PUT", "/onboarding/settings", pria, `{"importApprovalRequired":true}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"importApprovalRequired":true`) {
		t.Fatalf("write = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("PUT", "/onboarding/settings", pria, `{"importApprovalRequired":false}`); rec.Code != 200 {
		t.Fatalf("turn off = %d %s", rec.Code, rec.Body)
	}
}
