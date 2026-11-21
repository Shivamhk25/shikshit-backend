package apiv1_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type scopeBody struct {
	Items []struct {
		ID, UserID, ScopeType, ScopeRef, Label string
	} `json:"items"`
}

// TestAnAdminAssignsATeacherToAClassSection: the assignment is the only thing between a teacher and their class's
// students, so creating it opens the students list, removing it closes it again, and only an IAM manager may do either.
func TestAnAdminAssignsATeacherToAClassSection(t *testing.T) {
	s := newAccessStack(t)
	boss, registrar, teacher := s.signIn(t, "boss").AccessToken, s.signIn(t, "clerk").AccessToken, s.signIn(t, "tina").AccessToken
	mk := func(path, body string) string {
		rec := s.call("POST", path, boss, body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("setup %s = %d %s", path, rec.Code, rec.Body)
		}
		return decode[idBody](t, rec).ID
	}
	year := mk("/academic-years", `{"code":"Y","name":"2026-27","startDate":"2026-04-01","endDate":"2027-03-31","isCurrent":true}`)
	class := mk("/classes", `{"code":"V","name":"Class 5","displayOrder":5}`)
	sec := mk("/sections", `{"code":"A","name":"A"}`)
	mapping := mk("/class-section-mappings", fmt.Sprintf(`{"yearId":%q,"classId":%q,"sectionId":%q}`, year, class, sec))
	kid := mk("/students", `{"registrationCode":"R-1","firstName":"Asha","gender":"female","dateOfBirth":"2015-06-01"}`)
	mk("/student-enrollments", fmt.Sprintf(`{"studentId":%q,"yearId":%q,"classId":%q,"sectionId":%q}`, kid, year, class, sec))
	var tina string
	if err := s.adm.QueryRow(bg, `SELECT id::text FROM users WHERE username = 'tina'`).Scan(&tina); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"userId":%q,"scopeType":"class_section","scopeRef":%q}`, tina, mapping)

	if rec := s.call("GET", "/students", teacher, ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("before: %s", rec.Body)
	}
	for who, tok := range map[string]string{"registrar": registrar, "teacher": teacher} {
		if rec := s.call("POST", "/scope-assignments", tok, body); rec.Code != http.StatusForbidden {
			t.Errorf("%s create = %d, want 403", who, rec.Code)
		}
	}
	rec := s.call("POST", "/scope-assignments", boss, body)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"label":"Class 5 A (2026-27)"`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id := decode[idBody](t, rec).ID
	if rec := s.call("GET", "/students", teacher, ""); !strings.Contains(rec.Body.String(), `"total":1`) || !strings.Contains(rec.Body.String(), kid) {
		t.Errorf("after assigning, the teacher sees the class: %s", rec.Body)
	}
	for name, c := range map[string]string{
		"duplicate":        body,
		"unknown target":   fmt.Sprintf(`{"userId":%q,"scopeType":"class_section","scopeRef":"00000000-0000-4000-8000-000000000000"}`, tina),
		"unknown user":     fmt.Sprintf(`{"userId":"00000000-0000-4000-8000-000000000000","scopeType":"class_section","scopeRef":%q}`, mapping),
		"unsupported type": fmt.Sprintf(`{"userId":%q,"scopeType":"assigned_hostel","scopeRef":%q}`, tina, mapping),
		"end before start": fmt.Sprintf(`{"userId":%q,"scopeType":"class_section","scopeRef":%q,"startsAt":"2026-06-01T00:00:00Z","endsAt":"2026-05-01T00:00:00Z"}`, tina, mapping),
		"empty":            `{}`,
	} {
		if rec := s.call("POST", "/scope-assignments", boss, c); rec.Code != 400 {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
	list := decode[scopeBody](t, s.call("GET", "/scope-assignments?userId="+tina, boss, ""))
	if len(list.Items) != 1 || list.Items[0].ScopeRef != mapping {
		t.Errorf("list: %+v", list)
	}
	if rec := s.call("GET", "/scope-assignments", registrar, ""); rec.Code != http.StatusForbidden {
		t.Errorf("registrar list = %d", rec.Code)
	}
	var audited int
	if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'scope_assignments.create' AND resource_id = $1`, id).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("create audited %d %v", audited, err)
	}
	if rec := s.call("DELETE", "/scope-assignments/"+id, boss, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := s.call("GET", "/students", teacher, ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("after removing, the teacher sees nothing again: %s", rec.Body)
	}
	if rec := s.call("DELETE", "/scope-assignments/"+id, boss, ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete twice = %d", rec.Code)
	}
	// an assignment that has not started yet gives nothing
	future := fmt.Sprintf(`{"userId":%q,"scopeType":"class_section","scopeRef":%q,"startsAt":"2099-01-01T00:00:00Z"}`, tina, mapping)
	if rec := s.call("POST", "/scope-assignments", boss, future); rec.Code != http.StatusCreated {
		t.Fatalf("future = %d %s", rec.Code, rec.Body)
	}
	if rec := s.call("GET", "/students", teacher, ""); !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Errorf("a future assignment must not apply yet: %s", rec.Body)
	}
}
