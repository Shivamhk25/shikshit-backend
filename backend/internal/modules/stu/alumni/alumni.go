// Package alumni implements STU-10 Alumni: a read-only list of former students, derived from records that already
// exist. It writes nothing and has no table of its own.
//
//   - A graduate is a student whose status is "alumni". The promotions module (STU-07) sets it when a committed batch
//     graduates the student. The last class, section and year are those of the enrolment the batch graduated from, and
//     the exit date is the day the batch was committed (India time). An undone batch puts the student back to the
//     earlier status, so the student leaves this list too.
//   - A transferred student is one whose status is "transferred". The last class is the enrolment the transfer was
//     requested from (STU-08), the exit date is the date on the transfer certificate (it defaults to the day the
//     transfer was completed), and the certificate number, destination school and last attendance date are shown.
//   - A student whose status was set to "alumni" without a committed graduation, or to "transferred" without a
//     completed transfer (an edit on the student form, an import), is still listed. The exit date cannot be known, so
//     it is empty; the last class is the student's latest enrolment, and a student set to "alumni" this way has the
//     reason "other".
//
// The list never shows why a transfer was made (health, money): that stays with the transfers screen.
package alumni

import (
	"context"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the person is not a former student of this institute.
var ErrNotFound = resource.ErrNotFound

// Reasons a student left.
const (
	ReasonGraduated   = "graduated"
	ReasonTransferred = "transferred"
	ReasonOther       = "other"
)

// ReasonLabel is the text shown for a reason code.
func ReasonLabel(code string) string {
	switch code {
	case ReasonGraduated:
		return "Graduated"
	case ReasonTransferred:
		return "Transferred"
	}
	return "Other"
}

// Alumnus is a former student as the API shows them. ID is the student's id.
type Alumnus struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	RegistrationCode   string    `json:"registrationCode"`
	AdmissionDate      *string   `json:"admissionDate"`
	LastClassName      *string   `json:"lastClassName"`
	LastSectionName    *string   `json:"lastSectionName"`
	YearName           *string   `json:"yearName"`
	Reason             string    `json:"reason"`
	ExitDate           *string   `json:"exitDate"`
	TCNumber           *string   `json:"tcNumber"`
	DestinationSchool  *string   `json:"destinationSchool"`
	LastAttendanceDate *string   `json:"lastAttendanceDate"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// Filter narrows a list. ClassID and YearID are those of the last enrolment.
type Filter struct{ Reason, ClassID, YearID string }

// SortKeys are the sort keys clients may use. A student with no known exit date sorts last in either direction: the
// first term of "exit" puts the unknown dates after the known ones, and the direction applies to the date.
var SortKeys = crud.Sorts{
	"student": "student_name", "class": "class_name", "year": "year_start",
	"reason": "reason", "exit": "(exit_date IS NULL), exit_date",
}

// DefaultSort lists the most recent leavers first.
const DefaultSort = "-exit"

// source is the table the list reads. It is a derived table so the shared list engine (paging, search, sorting, the
// audit row) works on it unchanged. Row-level security of the tables it reads keeps it inside the institute. It
// exposes the id and deleted_at columns the engine expects.
const source = `(
	SELECT s.id, NULL::timestamptz AS deleted_at, s.registration_code, s.admission_date, s.updated_at,
		btrim(s.first_name || ' ' || COALESCE(s.middle_name || ' ', '') || COALESCE(s.last_name, '')) AS student_name,
		CASE WHEN s.status = 'transferred' THEN 'transferred' WHEN g.batch_id IS NOT NULL THEN 'graduated' ELSE 'other' END AS reason,
		CASE WHEN s.status = 'transferred' THEN tr.tc_issued_on ELSE (g.committed_at AT TIME ZONE 'Asia/Kolkata')::date END AS exit_date,
		tr.tc_number, tr.destination_school, tr.last_attendance_date,
		c.id AS class_id, c.name AS class_name, sc.name AS section_name, y.id AS year_id, y.name AS year_name, y.start_date AS year_start
	FROM students s
	LEFT JOIN LATERAL (
		SELECT pb.id AS batch_id, pb.committed_at, pbs.source_enrollment_id
		FROM promotion_batch_students pbs JOIN promotion_batches pb ON pb.tenant_id = pbs.tenant_id AND pb.id = pbs.batch_id
		WHERE pbs.tenant_id = s.tenant_id AND pbs.student_id = s.id AND pbs.decision = 'graduate' AND pb.status = 'committed' AND pb.deleted_at IS NULL
		ORDER BY pb.committed_at DESC LIMIT 1) g ON s.status = 'alumni'
	LEFT JOIN LATERAL (
		SELECT st.tc_number, st.tc_issued_on, st.destination_school, st.last_attendance_date, st.enrolment_id
		FROM student_transfers st
		WHERE st.tenant_id = s.tenant_id AND st.student_id = s.id AND st.status = 'completed' AND st.deleted_at IS NULL
		ORDER BY st.completed_at DESC LIMIT 1) tr ON s.status = 'transferred'
	LEFT JOIN LATERAL (
		SELECT e2.id FROM student_enrollments e2 JOIN academic_years y2 ON y2.tenant_id = e2.tenant_id AND y2.id = e2.year_id
		WHERE e2.tenant_id = s.tenant_id AND e2.student_id = s.id AND e2.deleted_at IS NULL
		ORDER BY y2.start_date DESC, e2.created_at DESC LIMIT 1) le ON true
	LEFT JOIN student_enrollments e ON e.tenant_id = s.tenant_id AND e.id = COALESCE(g.source_enrollment_id, tr.enrolment_id, le.id)
	LEFT JOIN classes c ON c.tenant_id = e.tenant_id AND c.id = e.class_id
	LEFT JOIN sections sc ON sc.tenant_id = e.tenant_id AND sc.id = e.section_id
	LEFT JOIN academic_years y ON y.tenant_id = e.tenant_id AND y.id = e.year_id
	WHERE s.deleted_at IS NULL AND s.status IN ('alumni', 'transferred')
) alumni`

const columns = `id::text, student_name, registration_code, admission_date::text, class_name, section_name, year_name, reason, exit_date::text,
	tc_number, destination_school, last_attendance_date::text, updated_at`

func scan(r resource.Scanner) (Alumnus, error) {
	var x Alumnus
	err := r.Scan(&x.ID, &x.Name, &x.RegistrationCode, &x.AdmissionDate, &x.LastClassName, &x.LastSectionName, &x.YearName, &x.Reason, &x.ExitDate,
		&x.TCNumber, &x.DestinationSchool, &x.LastAttendanceDate, &x.UpdatedAt)
	return x, err
}

var spec = resource.Spec[Alumnus, struct{}]{
	Table: source, Action: "alumni", ResourceType: "alumnus",
	Columns: columns, Scan: scan, ID: func(x Alumnus) string { return x.ID },
	SearchColumns: []string{"student_name", "registration_code", "tc_number"},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
}

// Service reads the alumni list.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Alumnus, struct{}] {
	return resource.Store[Alumnus, struct{}]{DB: s.DB, Spec: spec}
}

// ListAudited returns one page. The read is audited with the paging and filters, never the search text (a name).
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Alumnus], error) {
	var conds []resource.Cond
	if f.Reason != "" {
		conds = append(conds, resource.Cond{SQL: "reason = ?", Args: []any{f.Reason}})
	}
	if f.ClassID != "" {
		conds = append(conds, resource.Cond{SQL: "class_id = ?::uuid", Args: []any{f.ClassID}})
	}
	if f.YearID != "" {
		conds = append(conds, resource.Cond{SQL: "year_id = ?::uuid", Args: []any{f.YearID}})
	}
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "", "reason": f.Reason, "classId": f.ClassID, "yearId": f.YearID}, q, conds...)
}

// View returns one former student; the read is audited.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Alumnus, error) {
	return s.store().View(ctx, a, id)
}
