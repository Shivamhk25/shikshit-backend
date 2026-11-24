package electivechanges_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/modtest"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electivechanges"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/electives"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/approvals"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/scope"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/shared/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }

var bg = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type person struct {
	actor  audit.Actor
	access authz.Access
}

// world is institute alpha: Class 9 sections A and B, the group "Skill" (Music, Dance, Art; Art for section B only) and
// "Second language" (Hindi, French), with one student in each section who has already chosen.
type world struct {
	f                    modtest.Fixture
	svc                  electivechanges.Service
	choices              electives.Service
	approvals            approvals.Service
	requester            person
	approver             person // the whole school
	sectionA, sectionB   person // a class-section grant for one section each
	year, c9, secA, secB string
	csmA, csmB           string
	skill, lang          string
	music, dance, art    string
	drama                string
	hindi, french        string
	sA, sB               string // students of section A and B
	choiceA, choiceB     string // sA: Music+Dance; sB: Music+Dance
	langA                string // sA's choice of Hindi in the second language group
}

func (w world) in() context.Context { return modtest.In(w.f.Alpha) }

func (w world) id(t *testing.T, sql string, args ...any) string {
	t.Helper()
	var id string
	must(t, w.f.Admin.QueryRow(bg, sql, args...).Scan(&id))
	return id
}

func (w world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := w.f.Admin.Exec(bg, sql, args...)
	must(t, err)
}

func (w world) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, w.f.Admin.QueryRow(bg, sql, args...).Scan(&n))
	return n
}

func (w world) user(t *testing.T, name string, grants map[string]string, assignments ...string) person {
	t.Helper()
	uid := w.id(t, `INSERT INTO users (tenant_id, username, display_name, status) VALUES ($1, $2::text, $2::text, 'active') RETURNING id::text`, w.f.Alpha.ID, name)
	rid := w.id(t, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, $2::text, $2::text) RETURNING id::text`, w.f.Alpha.ID, "r_"+name)
	for code, sc := range grants {
		w.exec(t, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, $4)`, w.f.Alpha.ID, rid, code, sc)
	}
	w.exec(t, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, w.f.Alpha.ID, uid, rid)
	for _, ref := range assignments {
		w.exec(t, `INSERT INTO scope_assignments (tenant_id, user_id, scope_type, scope_ref) VALUES ($1, $2::uuid, 'class_section', $3)`, w.f.Alpha.ID, uid, ref)
	}
	acc, err := authz.Service{DB: w.f.App}.Resolve(w.in(), uid)
	must(t, err)
	return person{actor: audit.Actor{UserID: uid, Source: "web", RequestID: "req-" + name}, access: acc}
}

const (
	view   = "stu.student_elective_approvals.view"
	create = "stu.student_elective_approvals.create"
	edit   = "stu.student_elective_approvals.edit"
)

func setup(t *testing.T) world {
	t.Helper()
	f := modtest.New(t)
	w := world{f: f, choices: electives.Service{DB: f.App}}
	a := f.Alpha.ID
	w.exec(t, `INSERT INTO tenant_modules (tenant_id, module_code) VALUES ($1, 'stu'), ($1, 'wfl')`, a)
	reg := approvals.NewRegistry()
	must(t, electivechanges.Register(reg))
	w.approvals = approvals.Service{DB: f.App, Reg: reg, Access: authz.Service{DB: f.App}}
	w.svc = electivechanges.Service{DB: f.App, Approvals: w.approvals}

	w.year = w.id(t, `INSERT INTO academic_years (tenant_id, code, name, start_date, end_date, is_current) VALUES ($1, 'Y', '2026-27', DATE '2026-04-01', DATE '2027-03-31', true) RETURNING id::text`, a)
	w.c9 = w.id(t, `INSERT INTO classes (tenant_id, code, name) VALUES ($1, 'IX', 'Class 9') RETURNING id::text`, a)
	w.secA = w.id(t, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'A', 'A') RETURNING id::text`, a)
	w.secB = w.id(t, `INSERT INTO sections (tenant_id, code, name) VALUES ($1, 'B', 'B') RETURNING id::text`, a)
	csm := func(sec string) string {
		return w.id(t, `INSERT INTO class_section_mappings (tenant_id, year_id, class_id, section_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid) RETURNING id::text`, a, w.year, w.c9, sec)
	}
	w.csmA, w.csmB = csm(w.secA), csm(w.secB)
	w.skill = w.id(t, `INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects, enforce_limits) VALUES ($1, 'SKILL', 'Skill', 2, 2, true) RETURNING id::text`, a)
	w.lang = w.id(t, `INSERT INTO elective_groups (tenant_id, code, name, min_subjects, max_subjects, enforce_limits) VALUES ($1, 'LANG', 'Second language', 1, 1, true) RETURNING id::text`, a)
	subject := func(code, name string) string {
		return w.id(t, `INSERT INTO subjects (tenant_id, code, name, category, area, scholastic, credits) VALUES ($1, $2::text, $3, 'elective', 'other', true, 1) RETURNING id::text`, a, code, name)
	}
	w.music, w.dance, w.art, w.hindi, w.french = subject("MUS", "Music"), subject("DAN", "Dance"), subject("ART", "Art"), subject("HIN", "Hindi"), subject("FRE", "French")
	w.drama = subject("DRA", "Drama")
	offer := func(s string, sec *string, group string) {
		w.exec(t, `INSERT INTO subject_class_mappings (tenant_id, year_id, class_id, subject_id, section_id, is_elective, elective_group_id) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, true, $6::uuid)`,
			a, w.year, w.c9, s, sec, group)
	}
	offer(w.music, nil, w.skill)
	offer(w.dance, nil, w.skill)
	offer(w.art, &w.secB, w.skill)
	offer(w.drama, nil, w.skill)
	offer(w.hindi, nil, w.lang)
	offer(w.french, nil, w.lang)

	student := func(code, sec string) string {
		id := w.id(t, `INSERT INTO students (tenant_id, registration_code, first_name, last_name, gender, date_of_birth, admission_date)
			VALUES ($1, $2::text, 'Asha', 'Rao ' || $2::text, 'female', DATE '2012-04-01', DATE '2020-04-01') RETURNING id::text`, a, code)
		w.exec(t, `INSERT INTO student_enrollments (tenant_id, student_id, year_id, class_id, section_id, roll_number) VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::text)`, a, id, w.year, w.c9, sec, code)
		return id
	}
	w.sA, w.sB = student("S-A", w.secA), student("S-B", w.secB)

	// the requester is the fixture's actor
	rid := w.id(t, `INSERT INTO roles (tenant_id, code, name) VALUES ($1, 'r_asha', 'r_asha') RETURNING id::text`, a)
	for _, code := range []string{view, create, "stu.student_electives.view", "stu.student_electives.create", "stu.student_electives.edit"} {
		w.exec(t, `INSERT INTO role_permissions (tenant_id, role_id, permission_code, scope) VALUES ($1, $2::uuid, $3, 'tenant')`, a, rid, code)
	}
	w.exec(t, `INSERT INTO user_roles (tenant_id, user_id, role_id) VALUES ($1, $2::uuid, $3::uuid)`, a, f.Actor.UserID, rid)
	acc, err := authz.Service{DB: f.App}.Resolve(w.in(), f.Actor.UserID)
	must(t, err)
	w.requester = person{actor: f.Actor, access: acc}
	w.approver = w.user(t, "bala", map[string]string{edit: "tenant", view: "tenant"})
	w.sectionA = w.user(t, "chitra", map[string]string{edit: "class_section", view: "class_section"}, w.csmA)
	w.sectionB = w.user(t, "dev", map[string]string{edit: "class_section", view: "class_section"}, w.csmB)

	mk := func(student, group string, subjects ...string) string {
		c, err := w.choices.Create(w.in(), f.Actor, electives.Input{StudentID: student, YearID: w.year, GroupID: group, SubjectIDs: subjects})
		must(t, err)
		return c.ID
	}
	w.choiceA, w.choiceB = mk(w.sA, w.skill, w.music, w.dance), mk(w.sB, w.skill, w.music, w.dance)
	w.langA = mk(w.sA, w.lang, w.hindi)
	return w
}

func (w world) request(t *testing.T, choice, from, to string) electivechanges.Change {
	t.Helper()
	c, err := w.svc.Create(w.in(), w.requester.actor, electivechanges.Input{ChoiceID: choice, FromSubjectID: from, ToSubjectID: to, Reason: "wants to learn art"})
	must(t, err)
	return c
}

func (w world) decide(who person, c electivechanges.Change, approve bool) (approvals.Detail, error) {
	return w.approvals.Decide(w.in(), who.actor, who.access, *c.ApprovalRequestID, approvals.DecideInput{Approve: approve, Comment: "noted"})
}

func (w world) choice(t *testing.T, id string) electives.Choice {
	t.Helper()
	c, err := w.choices.Get(w.in(), id)
	must(t, err)
	return c
}

func subjectNames(c electives.Choice) string {
	out := make([]string, len(c.Subjects))
	for i, s := range c.Subjects {
		out[i] = s.Name
	}
	return strings.Join(out, ",")
}

func fieldErr(t *testing.T, name string, err error, field, code string) {
	t.Helper()
	var fe crud.FieldErrors
	if !errors.As(err, &fe) {
		t.Errorf("%s: want field errors, got %v", name, err)
		return
	}
	for _, f := range fe {
		if f.Field == field && (code == "" || f.Code == code) {
			return
		}
	}
	t.Errorf("%s: no %q error on %q: %v", name, code, field, fe)
}

func TestAChangeRequestIsFiledWithItsApprovalRequestAndShowsTheStudentsClass(t *testing.T) {
	w := setup(t)
	c := w.request(t, w.choiceB, w.music, w.art)
	if c.Status != "requested" || c.ApprovalRequestID == nil || c.Student.RegistrationCode != "S-B" || c.ClassName == nil || *c.ClassName != "Class 9" ||
		c.SectionName == nil || *c.SectionName != "B" || c.FromSubject.Name != "Music" || c.ToSubject.Name != "Art" || c.Group.Name != "Skill" ||
		c.Decision != nil || c.RequestedBy == nil || c.RequestedBy.Name != "Asha" || c.Reason != "wants to learn art" || c.YearName != "2026-27" || c.ChoiceID != w.choiceB {
		t.Fatalf("change = %+v", c)
	}
	today := electives.Today(time.Now())
	if c.WantedFrom < "2026-04-01" || c.WantedFrom > "2027-03-31" || (today >= "2026-04-01" && today <= "2027-03-31" && c.WantedFrom != today) {
		t.Errorf("wantedFrom = %s (today %s)", c.WantedFrom, today)
	}
	req, err := w.approvals.Get(w.in(), w.requester.access, *c.ApprovalRequestID)
	must(t, err)
	if req.TypeKey != "student_elective_change" || req.SubjectID != c.ID || !strings.Contains(req.Summary, "Music to Art") || req.CanDecide {
		t.Fatalf("approval request = %+v", req.Request)
	}
	if w.f.AuditCount(t, "student_elective_changes.create") != 1 {
		t.Error("the request was not audited")
	}
	// the choice is not touched until the request is approved
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Dance,Music" {
		t.Errorf("choice = %s", got)
	}
}

func TestARequestIsCheckedAgainstTheChoiceAndTheGroup(t *testing.T) {
	w := setup(t)
	try := func(choice, from, to, reason string, wanted *string) error {
		_, err := w.svc.Create(w.in(), w.requester.actor, electivechanges.Input{ChoiceID: choice, FromSubjectID: from, ToSubjectID: to, Reason: reason, WantedFrom: wanted})
		return err
	}
	fieldErr(t, "from not chosen", try(w.choiceA, w.hindi, w.dance, "r", nil), "fromSubjectId", "not_chosen")
	fieldErr(t, "to already chosen", try(w.choiceA, w.music, w.dance, "r", nil), "toSubjectId", "already_chosen")
	fieldErr(t, "same subject", try(w.choiceA, w.music, w.music, "r", nil), "toSubjectId", "same_subject")
	fieldErr(t, "to not in the group", try(w.choiceA, w.music, w.french, "r", nil), "toSubjectId", "not_in_group")
	fieldErr(t, "art is for section B only", try(w.choiceA, w.music, w.art, "r", nil), "toSubjectId", "not_in_group")
	fieldErr(t, "no reason", try(w.choiceA, w.music, w.drama, "  ", nil), "reason", "required")
	fieldErr(t, "reason too long", try(w.choiceA, w.music, w.drama, strings.Repeat("x", 501), nil), "reason", "too_long")
	fieldErr(t, "bad ids", try("x", "y", "z", "r", nil), "choiceId", "invalid")
	fieldErr(t, "no such choice", try("00000000-0000-0000-0000-000000000001", w.music, w.art, "r", nil), "choiceId", "not_found")
	fieldErr(t, "bad date", try(w.choiceB, w.music, w.art, "r", ptr("soon")), "wantedFrom", "invalid_date")
	fieldErr(t, "before the year", try(w.choiceB, w.music, w.art, "r", ptr("2026-03-31")), "wantedFrom", "before_year")
	fieldErr(t, "after the year", try(w.choiceB, w.music, w.art, "r", ptr("2027-04-01")), "wantedFrom", "after_year")
	if n := w.count(t, `SELECT count(*) FROM student_elective_changes`); n != 0 {
		t.Fatalf("refused requests left %d rows", n)
	}
	c, err := w.svc.Create(w.in(), w.requester.actor, electivechanges.Input{ChoiceID: w.choiceB, FromSubjectID: w.music, ToSubjectID: w.art, Reason: "r", WantedFrom: ptr("2026-12-01")})
	must(t, err)
	if c.WantedFrom != "2026-12-01" {
		t.Errorf("wantedFrom = %s", c.WantedFrom)
	}
}

func ptr[T any](v T) *T { return &v }

func TestOnlyOneRequestMayWaitToReplaceTheSameSubject(t *testing.T) {
	w := setup(t)
	first := w.request(t, w.choiceB, w.music, w.art)
	_, err := w.svc.Create(w.in(), w.requester.actor, electivechanges.Input{ChoiceID: w.choiceB, FromSubjectID: w.music, ToSubjectID: w.art, Reason: "again"})
	var ce electivechanges.ConflictError
	if !errors.As(err, &ce) || ce.Code != "open_change" {
		t.Fatalf("second request for the same subject: %v", err)
	}
	// replacing the other subject is a different request
	w.request(t, w.choiceB, w.dance, w.art)
	// after the first is cancelled the subject can be asked for again
	if _, err := w.svc.Cancel(w.in(), w.requester.actor, w.requester.access, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	w.request(t, w.choiceB, w.music, w.art)
}

func TestApprovingSwapsTheSubjectAndWritesTheHistoryInOneTransaction(t *testing.T) {
	w := setup(t)
	c := w.request(t, w.choiceB, w.music, w.art)
	if _, err := w.decide(w.requester, c, true); err == nil {
		t.Fatal("the requester approved their own request")
	} else {
		var fo approvals.ForbiddenError
		if !errors.As(err, &fo) {
			t.Fatalf("requester: %v", err)
		}
	}
	if got := w.choice(t, w.choiceB); subjectNames(got) != "Dance,Music" {
		t.Fatalf("a refused approval changed the choice: %s", subjectNames(got))
	}
	_, err := w.decide(w.approver, c, true)
	must(t, err)

	got, err := w.svc.Get(w.in(), c.ID)
	must(t, err)
	if got.Status != "approved" || got.Decision == nil || got.Decision.Outcome != "approved" || got.Decision.DecidedBy == nil || got.Decision.DecidedBy.Name != "bala" ||
		got.Decision.Note == nil || *got.Decision.Note != "noted" || got.Decision.DecidedAt.IsZero() {
		t.Fatalf("change after approval = %+v / %+v", got, got.Decision)
	}
	ch := w.choice(t, w.choiceB)
	if subjectNames(ch) != "Art,Dance" || !ch.WithinLimits {
		t.Fatalf("choice after approval = %s", subjectNames(ch))
	}
	h, err := w.choices.History(w.in(), w.choiceB)
	must(t, err)
	if len(h) != 2 || h[0].Action != "swap" || h[0].Source != "change_request" || h[0].ChangeID == nil || *h[0].ChangeID != c.ID ||
		h[0].ApprovalRequestID == nil || *h[0].ApprovalRequestID != *c.ApprovalRequestID || h[0].ChangedBy == nil || h[0].ChangedBy.Name != "bala" {
		t.Fatalf("history = %+v", h)
	}
	if len(h[0].SubjectsBefore) != 2 || len(h[0].SubjectsAfter) != 2 || h[0].SubjectsAfter[0].Name != "Art" {
		t.Errorf("swap row = %+v", h[0])
	}
	if w.f.AuditCount(t, "student_elective_changes.approve") != 1 || w.f.AuditCount(t, "student_electives.swap") != 1 {
		t.Error("the approval and the swap were not audited")
	}
	// the other student is untouched, and a second decision is refused by the engine
	if subjectNames(w.choice(t, w.choiceA)) != "Dance,Music" {
		t.Error("another student's choice changed")
	}
	if _, err := w.decide(w.approver, c, true); err == nil {
		t.Error("a request was approved twice")
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE choice_id = $1::uuid AND action = 'swap'`, w.choiceB); n != 1 {
		t.Errorf("swap rows = %d", n)
	}
}

func TestApprovalIsAllOrNothingWhenAStepFailsHalfWay(t *testing.T) {
	w := setup(t)
	c := w.request(t, w.choiceB, w.music, w.art)
	// the last write of the approval (marking the request approved) fails, after the subject was swapped and the history
	// row written: everything, and the decision itself, must roll back
	w.exec(t, `CREATE FUNCTION boom() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced failure'; END $$`)
	w.exec(t, `CREATE TRIGGER boom BEFORE UPDATE ON student_elective_changes FOR EACH ROW WHEN (NEW.status = 'approved') EXECUTE FUNCTION boom()`)
	if _, err := w.decide(w.approver, c, true); err == nil || !strings.Contains(err.Error(), "forced failure") {
		t.Fatalf("approval: %v", err)
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Dance,Music" {
		t.Errorf("the choice changed: %s", got)
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE action = 'swap'`); n != 0 {
		t.Errorf("a swap row was left: %d", n)
	}
	if st := w.count(t, `SELECT count(*) FROM student_elective_changes WHERE id = $1::uuid AND status = 'requested'`, c.ID); st != 1 {
		t.Error("the change is no longer requested")
	}
	if st := w.count(t, `SELECT count(*) FROM approval_requests WHERE id = $1::uuid AND status = 'pending'`, *c.ApprovalRequestID); st != 1 {
		t.Error("the approval request is no longer pending")
	}
	if n := w.count(t, `SELECT count(*) FROM approval_events WHERE request_id = $1::uuid AND action = 'approved'`, *c.ApprovalRequestID); n != 0 {
		t.Error("the approval was recorded")
	}
	// with the fault gone the same request can be approved
	w.exec(t, `DROP TRIGGER boom ON student_elective_changes`)
	if _, err := w.decide(w.approver, c, true); err != nil {
		t.Fatal(err)
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Art,Dance" {
		t.Errorf("after the retry: %s", got)
	}
}

func TestApprovalIsRefusedWhenTheSwapNoLongerFits(t *testing.T) {
	cases := map[string]func(w world){
		"the subject was changed by hand": func(w world) {
			_, err := w.choices.Replace(w.in(), w.requester.actor, w.choiceB, electives.Update{SubjectIDs: []string{w.dance, w.art}})
			must(t, err)
		},
		"the subject is no longer offered": func(w world) {
			w.exec(t, `UPDATE subject_class_mappings SET deleted_at = now() WHERE subject_id = $1::uuid`, w.art)
		},
		"the group's limits changed": func(w world) {
			w.exec(t, `UPDATE elective_groups SET min_subjects = 3, max_subjects = 3 WHERE id = $1::uuid`, w.skill)
		},
		"the student left the class": func(w world) {
			w.exec(t, `UPDATE student_enrollments SET status = 'withdrawn' WHERE student_id = $1::uuid`, w.sB)
		},
		"the choice was removed": func(w world) {
			w.exec(t, `UPDATE student_elective_choices SET deleted_at = now() WHERE id = $1::uuid`, w.choiceB)
		},
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			w := setup(t)
			c := w.request(t, w.choiceB, w.music, w.art)
			spoil(w)
			before := subjectNames(w.choice2(t, w.choiceB))
			_, err := w.decide(w.approver, c, true)
			var ce approvals.ConflictError
			if !errors.As(err, &ce) || !strings.Contains(ce.Message, "Reject the request") {
				t.Fatalf("approval: %v", err)
			}
			if after := subjectNames(w.choice2(t, w.choiceB)); after != before {
				t.Errorf("the choice changed: %s -> %s", before, after)
			}
			if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE action = 'swap'`); n != 0 {
				t.Errorf("swap rows = %d", n)
			}
			if n := w.count(t, `SELECT count(*) FROM approval_requests WHERE id = $1::uuid AND status = 'pending'`, *c.ApprovalRequestID); n != 1 {
				t.Error("the request is no longer pending")
			}
			// and it can still be rejected
			if _, err := w.decide(w.approver, c, false); err != nil {
				t.Errorf("rejecting: %v", err)
			}
		})
	}
}

// choice2 reads a choice even when it was removed or the student left (for what a refused approval must leave alone).
func (w world) choice2(t *testing.T, id string) electives.Choice {
	t.Helper()
	var names []string
	rows, err := w.f.Admin.Query(bg, `SELECT s.name FROM student_elective_choice_subjects cs JOIN subjects s ON s.id = cs.subject_id WHERE cs.choice_id = $1::uuid ORDER BY s.name`, id)
	must(t, err)
	defer rows.Close()
	for rows.Next() {
		var n string
		must(t, rows.Scan(&n))
		names = append(names, n)
	}
	c := electives.Choice{}
	for _, n := range names {
		c.Subjects = append(c.Subjects, electives.SubjectRef{Name: n})
	}
	return c
}

func TestRejectingAndCancellingLeaveTheChoiceAlone(t *testing.T) {
	w := setup(t)
	rejected := w.request(t, w.choiceB, w.music, w.art)
	cancelled := w.request(t, w.choiceB, w.dance, w.art)

	// a rejection needs a reason
	if _, err := w.approvals.Decide(w.in(), w.approver.actor, w.approver.access, *rejected.ApprovalRequestID, approvals.DecideInput{Approve: false}); err == nil {
		t.Fatal("rejected without a reason")
	}
	if _, err := w.approvals.Decide(w.in(), w.approver.actor, w.approver.access, *rejected.ApprovalRequestID, approvals.DecideInput{Approve: false, Comment: "not this term"}); err != nil {
		t.Fatal(err)
	}
	got, err := w.svc.Get(w.in(), rejected.ID)
	must(t, err)
	if got.Status != "rejected" || got.Decision == nil || got.Decision.Outcome != "rejected" || got.Decision.Note == nil || *got.Decision.Note != "not this term" || got.Decision.DecidedBy == nil || got.Decision.DecidedBy.Name != "bala" {
		t.Fatalf("rejected = %+v / %+v", got, got.Decision)
	}

	// only the person who asked may cancel
	if _, err := w.svc.Cancel(w.in(), w.approver.actor, w.approver.access, cancelled.ID, ""); err == nil {
		t.Fatal("someone else cancelled the request")
	} else {
		var fo approvals.ForbiddenError
		if !errors.As(err, &fo) {
			t.Fatalf("cancel by another: %v", err)
		}
	}
	got, err = w.svc.Cancel(w.in(), w.requester.actor, w.requester.access, cancelled.ID, "changed my mind")
	must(t, err)
	if got.Status != "cancelled" || got.CancelledAt == nil || got.Decision != nil {
		t.Fatalf("cancelled = %+v", got)
	}
	if _, err := w.svc.Cancel(w.in(), w.requester.actor, w.requester.access, cancelled.ID, ""); err == nil {
		t.Error("a cancelled request was cancelled again")
	} else {
		var ce electivechanges.ConflictError
		if !errors.As(err, &ce) || ce.Code != "not_cancellable" {
			t.Errorf("second cancel: %v", err)
		}
	}
	if _, err := w.svc.Cancel(w.in(), w.requester.actor, w.requester.access, rejected.ID, ""); err == nil {
		t.Error("a rejected request was cancelled")
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Dance,Music" {
		t.Errorf("the choice changed: %s", got)
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE action = 'swap'`); n != 0 {
		t.Errorf("swap rows = %d", n)
	}
	if w.f.AuditCount(t, "student_elective_changes.reject") != 1 || w.f.AuditCount(t, "student_elective_changes.cancel") != 1 {
		t.Error("the rejection and the cancellation were not audited")
	}
}

func TestAHookThatRunsOnAChangeThatIsNoLongerRequestedWritesNothing(t *testing.T) {
	w := setup(t)
	c := w.request(t, w.choiceB, w.music, w.art)
	// the change was closed behind the approval's back
	w.exec(t, `UPDATE student_elective_changes SET status = 'cancelled', cancelled_at = now() WHERE id = $1::uuid`, c.ID)
	if _, err := w.decide(w.approver, c, true); err != nil {
		t.Fatal(err)
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Dance,Music" {
		t.Errorf("a closed change was applied: %s", got)
	}
	got, err := w.svc.Get(w.in(), c.ID)
	must(t, err)
	if got.Status != "cancelled" {
		t.Errorf("status = %s", got.Status)
	}
	if n := w.count(t, `SELECT count(*) FROM student_elective_choice_history WHERE action = 'swap'`); n != 0 {
		t.Errorf("swap rows = %d", n)
	}
}

func TestASectionApproverDecidesOnlyTheirOwnSectionsAndAWholeSchoolApproverAny(t *testing.T) {
	w := setup(t)
	inA := w.request(t, w.choiceA, w.music, w.drama)
	inB := w.request(t, w.choiceB, w.music, w.art)

	// section A's approver sees and decides A's request; B's request does not exist for them
	if _, err := w.approvals.Get(w.in(), w.sectionA.access, *inB.ApprovalRequestID); !errors.Is(err, approvals.ErrNotFound) {
		t.Errorf("section A's approver reads B's request: %v", err)
	}
	if _, err := w.decide(w.sectionA, inB, true); !errors.Is(err, approvals.ErrNotFound) {
		t.Errorf("section A's approver decided B's request: %v", err)
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Dance,Music" {
		t.Fatalf("B's choice changed: %s", got)
	}
	if _, err := w.decide(w.sectionA, inA, true); err != nil {
		t.Fatalf("section A's approver on A's request: %v", err)
	}
	if got := subjectNames(w.choice(t, w.choiceA)); got != "Dance,Drama" {
		t.Errorf("A's choice = %s", got)
	}
	if _, err := w.decide(w.sectionB, inB, true); err != nil {
		t.Fatalf("section B's approver on B's request: %v", err)
	}
	if got := subjectNames(w.choice(t, w.choiceB)); got != "Art,Dance" {
		t.Errorf("B's choice = %s", got)
	}
	// the whole-school approver can decide anything
	again := w.request(t, w.choiceA, w.dance, w.music)
	if _, err := w.decide(w.approver, again, false); err != nil {
		t.Errorf("whole-school approver: %v", err)
	}
}

func TestARequestForAStudentWithNoClassSectionCanBeDecidedByTheWholeSchoolOnly(t *testing.T) {
	w := setup(t)
	// section A is no longer mapped to the class: the student's request has no section to be scoped to
	w.exec(t, `UPDATE class_section_mappings SET deleted_at = now() WHERE id = $1::uuid`, w.csmA)
	c := w.request(t, w.choiceA, w.music, w.drama)
	if _, err := w.decide(w.sectionA, c, true); !errors.Is(err, approvals.ErrNotFound) {
		t.Errorf("a section approver decided a request with no section: %v", err)
	}
	if _, err := w.decide(w.approver, c, true); err != nil {
		t.Errorf("whole-school approver: %v", err)
	}
}

func TestAClassSectionScopeNarrowsRequestsAndRefusesFilingForOtherSections(t *testing.T) {
	w := setup(t)
	inA := w.request(t, w.choiceA, w.music, w.drama)
	inB := w.request(t, w.choiceB, w.music, w.art)
	teacher := scope.WithClassSections(w.in(), []string{w.csmA})

	q, _ := crud.ParseList(1, 50, "", "", electivechanges.SortKeys, electivechanges.DefaultSort)
	p, err := w.svc.List(teacher, q, electivechanges.Filter{})
	must(t, err)
	if p.Total != 1 || p.Items[0].ID != inA.ID {
		t.Fatalf("a section A teacher sees %d requests", p.Total)
	}
	if p, err = w.svc.List(w.in(), q, electivechanges.Filter{}); err != nil || p.Total != 2 {
		t.Fatalf("the whole school sees %d (%v)", p.Total, err)
	}
	if _, err := w.svc.Get(teacher, inB.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get of B's request: %v", err)
	}
	if _, err := w.svc.Get(teacher, inA.ID); err != nil {
		t.Errorf("get of A's request: %v", err)
	}
	_, err = w.svc.Create(teacher, w.requester.actor, electivechanges.Input{ChoiceID: w.choiceB, FromSubjectID: w.dance, ToSubjectID: w.art, Reason: "r"})
	fieldErr(t, "filing for section B", err, "choiceId", "not_found")
	if _, err := w.svc.Cancel(teacher, w.requester.actor, w.requester.access, inB.ID, ""); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("cancel of B's request: %v", err)
	}
	if _, err := w.svc.Create(teacher, w.requester.actor, electivechanges.Input{ChoiceID: w.choiceA, FromSubjectID: w.dance, ToSubjectID: w.drama, Reason: "r"}); err != nil {
		t.Errorf("filing for own section: %v", err)
	}
}

func TestOneInstituteCannotSeeOrFileForAnothers(t *testing.T) {
	w := setup(t)
	c := w.request(t, w.choiceB, w.music, w.art)
	bravo := modtest.In(w.f.Bravo)
	q, _ := crud.ParseList(1, 50, "", "", electivechanges.SortKeys, electivechanges.DefaultSort)
	if p, err := w.svc.List(bravo, q, electivechanges.Filter{}); err != nil || p.Total != 0 {
		t.Fatalf("bravo sees %d (%v)", p.Total, err)
	}
	if _, err := w.svc.Get(bravo, c.ID); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := w.svc.Cancel(bravo, w.requester.actor, w.requester.access, c.ID, ""); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("cancel: %v", err)
	}
	_, err := w.svc.Create(bravo, w.requester.actor, electivechanges.Input{ChoiceID: w.choiceA, FromSubjectID: w.music, ToSubjectID: w.drama, Reason: "r"})
	fieldErr(t, "alpha's choice", err, "choiceId", "not_found")
	// the database refuses a request that names another institute's choice
	_, err = w.f.Admin.Exec(bg, `INSERT INTO student_elective_changes (tenant_id, student_id, choice_id, year_id, group_id, from_subject_id, to_subject_id, wanted_from, reason)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, DATE '2026-09-01', 'x')`, w.f.Bravo.ID, w.sB, w.choiceB, w.year, w.skill, w.music, w.art)
	if err == nil {
		t.Error("a request mixed two institutes")
	}
}

func TestTheListFiltersByStatusClassAndStudent(t *testing.T) {
	w := setup(t)
	a := w.request(t, w.choiceA, w.music, w.drama)
	b := w.request(t, w.choiceB, w.music, w.art)
	if _, err := w.svc.Cancel(w.in(), w.requester.actor, w.requester.access, a.ID, ""); err != nil {
		t.Fatal(err)
	}
	list := func(f electivechanges.Filter, sort, search string) []string {
		t.Helper()
		q, err := crud.ParseList(1, 50, sort, search, electivechanges.SortKeys, electivechanges.DefaultSort)
		must(t, err)
		p, err := w.svc.List(w.in(), q, f)
		must(t, err)
		out := []string{}
		for _, c := range p.Items {
			out = append(out, c.Student.RegistrationCode)
		}
		return out
	}
	eq := func(name string, got []string, want string) {
		t.Helper()
		if strings.Join(got, ",") != want {
			t.Errorf("%s = %v, want %s", name, got, want)
		}
	}
	eq("requested", list(electivechanges.Filter{Status: "requested"}, "", ""), "S-B")
	eq("cancelled", list(electivechanges.Filter{Status: "cancelled"}, "", ""), "S-A")
	eq("section", list(electivechanges.Filter{SectionID: w.secB}, "", ""), "S-B")
	eq("student", list(electivechanges.Filter{StudentID: w.sA}, "", ""), "S-A")
	eq("choice", list(electivechanges.Filter{ChoiceID: b.ChoiceID}, "", ""), "S-B")
	eq("class sorted by student", list(electivechanges.Filter{ClassID: w.c9}, "student", ""), "S-A,S-B")
	eq("search", list(electivechanges.Filter{}, "", "S-A"), "S-A")
}
