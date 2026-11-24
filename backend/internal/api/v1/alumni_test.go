package apiv1_test

import (
	"net/http"
	"testing"
)

const aluView = "stu.alumni.view"

type alumnusOut struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	RegistrationCode string  `json:"registrationCode"`
	Reason           string  `json:"reason"`
	ReasonLabel      string  `json:"reasonLabel"`
	ExitDate         *string `json:"exitDate"`
	TCNumber         *string `json:"tcNumber"`
}

type alumniPageOut struct {
	Items []alumnusOut `json:"items"`
	Page  struct{ Total int }
}

func TestTheAlumniListIsReadOnlyScopedAndAudited(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	betaAdmin := s.signIn(t, betaHost, "betaadmin", password)
	viewer := s.trUser(t, "alumniviewer", map[string]string{aluView: "tenant"})
	narrow := s.trUser(t, "narrowed", map[string]string{aluView: "class_section"})
	plain := s.signIn(t, alphaHost, "plain", password)

	gone := s.student(t, "alpha", "G-1")
	stay := s.student(t, "alpha", "A-1")
	if _, err := s.adm.Exec(bg, `UPDATE students SET status = 'alumni' WHERE id = $1::uuid`, gone); err != nil {
		t.Fatal(err)
	}
	betaGone := s.student(t, "beta", "B-1")
	if _, err := s.adm.Exec(bg, `UPDATE students SET status = 'alumni' WHERE id = $1::uuid`, betaGone); err != nil {
		t.Fatal(err)
	}

	for name, tok := range map[string]string{"admin": admin, "viewer": viewer} {
		list := decode[alumniPageOut](t, s.do(http.MethodGet, alphaHost, "/alumni?reason=other&q=g-1", tok, ""))
		if list.Page.Total != 1 || list.Items[0].ID != gone || list.Items[0].Reason != "other" || list.Items[0].ReasonLabel != "Other" || list.Items[0].ExitDate != nil {
			t.Errorf("%s list = %+v", name, list)
		}
		rec := s.do(http.MethodGet, alphaHost, "/alumni/"+gone, tok, "")
		want(t, rec, http.StatusOK, "")
		if got := decode[alumnusOut](t, rec); got.RegistrationCode != "G-1" {
			t.Errorf("%s detail = %+v", name, got)
		}
	}
	// a student who has not left, an unknown id and another institute's alumnus are all "not found"
	want(t, s.do(http.MethodGet, alphaHost, "/alumni/"+stay, viewer, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodGet, alphaHost, "/alumni/00000000-0000-0000-0000-000000000000", viewer, ""), http.StatusNotFound, "not_found")
	want(t, s.do(http.MethodGet, alphaHost, "/alumni/"+betaGone, viewer, ""), http.StatusNotFound, "not_found")
	if got := decode[alumniPageOut](t, s.do(http.MethodGet, betaHost, "/alumni", betaAdmin, "")); got.Page.Total != 1 || got.Items[0].ID != betaGone {
		t.Errorf("beta list = %+v", got)
	}
	want(t, s.do(http.MethodGet, betaHost, "/alumni/"+gone, betaAdmin, ""), http.StatusNotFound, "not_found")

	// permission and scope
	for name, tok := range map[string]string{"plain": plain, "narrowed": narrow} {
		want(t, s.do(http.MethodGet, alphaHost, "/alumni", tok, ""), http.StatusForbidden, "permission_denied")
		want(t, s.do(http.MethodGet, alphaHost, "/alumni/"+gone, tok, ""), http.StatusForbidden, "permission_denied")
		_ = name
	}
	if rec := s.do(http.MethodGet, alphaHost, "/alumni", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token = %d", rec.Code)
	}
	// nothing can be written
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := s.do(m, alphaHost, "/alumni/"+gone, admin, `{}`); rec.Code < 400 {
			t.Errorf("%s /alumni/{id} = %d", m, rec.Code)
		}
	}
	want(t, s.do(http.MethodGet, alphaHost, "/alumni?reason=lost", viewer, ""), http.StatusBadRequest, "")
	want(t, s.do(http.MethodGet, alphaHost, "/alumni?sort=reasonLabel", viewer, ""), http.StatusBadRequest, "validation_failed")
	for _, action := range []string{"alumni.list", "alumni.view"} {
		var got int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&got); err != nil || got < 1 {
			t.Errorf("audit %s = %d (%v)", action, got, err)
		}
	}
}
