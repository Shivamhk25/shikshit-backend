package apiv1_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type identityGuardianOut struct {
	Name  string  `json:"name"`
	Phone *string `json:"phone"`
	Email *string `json:"email"`
}

type identityCardOut struct {
	ID              string                `json:"id"`
	ContactsVisible bool                  `json:"contactsVisible"`
	Guardians       []identityGuardianOut `json:"guardians"`
	IDCard          struct {
		EmergencyPhone *string `json:"emergencyPhone"`
	} `json:"idCard"`
}

type identityFindOut struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

func TestStudentIdentityMasksContactsAndKeepsTenants(t *testing.T) {
	s := newUsersStack(t)
	admin := s.signIn(t, alphaHost, "admin", password)
	betaAdmin := s.signIn(t, betaHost, "betaadmin", password)
	viewer := s.trUser(t, "idviewer", map[string]string{"stu.student_identity.view": "tenant"})
	withContacts := s.trUser(t, "idcontacts", map[string]string{"stu.student_identity.view": "tenant", "stu.student_contacts.view": "tenant"})
	contactsOnly := s.trUser(t, "idonlycontacts", map[string]string{"stu.student_contacts.view": "tenant"})
	plain := s.signIn(t, alphaHost, "plain", password)

	id := s.student(t, "alpha", "ID-1")
	betaID := s.student(t, "beta", "ID-9")
	var gid string
	if err := s.adm.QueryRow(bg, `INSERT INTO guardians (tenant_id, name, phone, email) VALUES ($1, 'Meera Rao', '9876543210', 'meera@example.org') RETURNING id::text`, s.tenant["alpha"]).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.adm.Exec(bg, `INSERT INTO student_guardians (tenant_id, student_id, guardian_id, relation, is_primary) VALUES ($1, $2::uuid, $3::uuid, 'mother', true)`, s.tenant["alpha"], id, gid); err != nil {
		t.Fatal(err)
	}

	q := "/student-identity?q=" + url.QueryEscape("id-1")
	for name, tok := range map[string]string{"admin": admin, "viewer": viewer, "contacts": withContacts} {
		if got := decode[identityFindOut](t, s.do(http.MethodGet, alphaHost, q, tok, "")); len(got.Items) != 1 || got.Items[0].ID != id {
			t.Errorf("%s search = %+v", name, got)
		}
	}
	for name, tok := range map[string]string{"admin": admin, "contacts": withContacts} {
		c := decode[identityCardOut](t, s.do(http.MethodGet, alphaHost, "/student-identity/"+id, tok, ""))
		if !c.ContactsVisible || len(c.Guardians) != 1 || c.Guardians[0].Phone == nil || *c.Guardians[0].Phone != "9876543210" ||
			c.Guardians[0].Email == nil || c.IDCard.EmergencyPhone == nil {
			t.Errorf("%s card = %+v", name, c)
		}
	}
	c := decode[identityCardOut](t, s.do(http.MethodGet, alphaHost, "/student-identity/"+id, viewer, ""))
	if c.ContactsVisible || len(c.Guardians) != 1 || c.Guardians[0].Name != "Meera Rao" || c.Guardians[0].Phone != nil || c.Guardians[0].Email != nil || c.IDCard.EmergencyPhone != nil {
		t.Errorf("masked card = %+v", c)
	}
	if body := s.do(http.MethodGet, alphaHost, "/student-identity/"+id, viewer, "").Body.String(); strings.Contains(body, "9876543210") || strings.Contains(body, "meera@example.org") {
		t.Errorf("masked card leaks contact: %s", body)
	}

	// permission: the contacts permission alone does not open the card; no permission is refused
	for _, tok := range []string{plain, contactsOnly} {
		want(t, s.do(http.MethodGet, alphaHost, q, tok, ""), http.StatusForbidden, "permission_denied")
		want(t, s.do(http.MethodGet, alphaHost, "/student-identity/"+id, tok, ""), http.StatusForbidden, "permission_denied")
	}
	if rec := s.do(http.MethodGet, alphaHost, q, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token = %d", rec.Code)
	}
	want(t, s.do(http.MethodGet, alphaHost, "/student-identity?q=a", viewer, ""), http.StatusBadRequest, "")

	// tenant isolation
	want(t, s.do(http.MethodGet, alphaHost, "/student-identity/"+betaID, admin, ""), http.StatusNotFound, "not_found")
	if got := decode[identityFindOut](t, s.do(http.MethodGet, alphaHost, "/student-identity?q=id-9", admin, "")); len(got.Items) != 0 {
		t.Errorf("alpha found beta's student: %+v", got)
	}
	want(t, s.do(http.MethodGet, betaHost, "/student-identity/"+id, betaAdmin, ""), http.StatusNotFound, "not_found")

	// both reads are audited
	for _, action := range []string{"student_identity.search", "student_identity.view"} {
		var n int
		if err := s.adm.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil || n < 1 {
			t.Errorf("audit %s = %d (%v)", action, n, err)
		}
	}
}
