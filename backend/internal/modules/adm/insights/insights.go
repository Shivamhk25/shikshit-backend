// Package insights implements ADM-02 Admission Insights: a read-only dashboard over enquiries (ADM-03) and student
// registrations (ADM-06). Every number is a count of rows the list endpoints would return for the same filters; the
// same filter rules are applied here (soft-deleted rows excluded, class and year as exact ids, the follow-up rule
// taken from enquiries.DueSQL), and the database's row-level security limits everything to the caller's institute.
//
// Date basis. A "from"/"to" range (both inclusive) is applied to the day an enquiry was created (its created_at as a
// calendar date in India) and to the day a registration was applied (applied_on). The monthly trend uses the same two
// bases.
//
// Admitted (ADM-05). Admitting a registration does not change its status: it stays "accepted" and gets an
// admitted_student_id. So the "accepted" count means "accepted ever" and includes the admitted ones, and
// RegistrationsAdmitted is the part of it that has been admitted. Admitted is every live registration with an
// admitted_student_id, which is what the admissions list returns for state=admitted (that list does not look at the
// status), under the same year, class and date filters (the date is still applied_on). Registrations cannot be
// un-admitted, but the registrations API still lets someone change the status of an admitted one (for example to
// withdrawn); such a row is admitted but no longer "accepted". To keep the funnel step a true share (never above 100%)
// its denominator is "accepted or admitted": accepted registrations plus admitted ones that are no longer accepted.
// Normally that equals the accepted count. "Accepted, not yet admitted" is the admissions queue (state=ready).
package insights

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/enquiries"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// MaxTrendMonths is the longest range the trend covers.
const MaxTrendMonths = 60

// DefaultTrendMonths is the trend length when no range is given: this month and the 11 before it.
const DefaultTrendMonths = 12

// Filter narrows the dashboard. Empty strings mean "not filtered". From and To are YYYY-MM-DD, both inclusive.
type Filter struct{ YearID, ClassID, From, To string }

// Ratio is a conversion step: Rate is Converted/Of rounded to 4 decimals, nil when Of is 0 (never a division by zero).
type Ratio struct {
	Of        int      `json:"of"`
	Converted int      `json:"converted"`
	Rate      *float64 `json:"rate"`
}

// FollowUps counts open enquiries (new, contacted, visit_scheduled) with a follow-up date today or earlier.
// Due = DueToday + Overdue and equals the enquiries list with followUpDue=true.
type FollowUps struct{ Due, DueToday, Overdue int }

// Month is one month of the trend, "YYYY-MM".
type Month struct {
	Month         string `json:"month"`
	Enquiries     int    `json:"enquiries"`
	Registrations int    `json:"registrations"`
}

// Conversion holds the funnel steps. AcceptedToAdmitted is admitted out of accepted-or-admitted; Get always sets it.
type Conversion struct {
	EnquiryToRegistration  Ratio
	RegistrationToAccepted Ratio
	AcceptedToAdmitted     *Ratio
}

// Insights is the dashboard.
type Insights struct {
	From, To           string // the trend range actually used
	AsOf               string // today in India
	EnquiriesTotal     int
	EnquiriesByStatus  map[string]int
	EnquiriesBySource  map[string]int
	FollowUps          FollowUps
	RegistrationsTotal int
	RegistrationsBy    map[string]int
	// RegistrationsAdmitted counts registrations that have been admitted; RegistrationsAcceptedOrAdmitted counts those
	// that are accepted or admitted (the denominator of the admitted step).
	RegistrationsAdmitted, RegistrationsAcceptedOrAdmitted int
	Conversion                                             Conversion
	Trend                                                  []Month

	enquiriesRegistered int // enquiries (in the filter) that have a live registration
}

var india = func() *time.Location {
	if l, err := time.LoadLocation("Asia/Kolkata"); err == nil {
		return l
	}
	return time.FixedZone("IST", 5*3600+1800)
}()

// Service reads the dashboard.
type Service struct{ DB tenancy.Beginner }

// Validate checks the filter and returns the trend range [first month, last month] it implies, as the first day of
// each month. now is the clock (tests pass their own). Dates must be real dates, From not after To, and the trend at
// most MaxTrendMonths long. With no dates the trend is the last DefaultTrendMonths months.
func Validate(f Filter, now time.Time) (first, last time.Time, err error) {
	var fe crud.FieldErrors
	parse := func(field, v string) (time.Time, bool) {
		if v == "" {
			return time.Time{}, false
		}
		d, perr := time.Parse(crud.DateLayout, v)
		if perr != nil {
			fe.Add(field, "invalid_date", "Enter the date as YYYY-MM-DD.")
			return time.Time{}, false
		}
		return d, true
	}
	from, hasFrom := parse("from", f.From)
	to, hasTo := parse("to", f.To)
	for _, r := range []struct{ field, v string }{{"yearId", f.YearID}, {"classId", f.ClassID}} {
		if r.v != "" && !isUUID(r.v) {
			fe.Add(r.field, "invalid", "This is not a valid id.")
		}
	}
	if hasFrom && hasTo && from.After(to) {
		fe.Add("to", "before_from", "The end date cannot be before the start date.")
	}
	if len(fe) > 0 {
		return first, last, fe
	}
	today := now.In(india)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case hasFrom && hasTo:
	case hasFrom:
		to = today
		if to.Before(from) {
			to = from
		}
	case hasTo:
		from = to.AddDate(0, -(DefaultTrendMonths - 1), 0)
	default:
		from, to = today.AddDate(0, -(DefaultTrendMonths-1), 0), today
	}
	first = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC)
	last = time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, time.UTC)
	if months := (last.Year()-first.Year())*12 + int(last.Month()-first.Month()) + 1; months > MaxTrendMonths {
		fe.Add("to", "range_too_long", fmt.Sprintf("The range can cover at most %d months.", MaxTrendMonths))
		return first, last, fe
	}
	return first, last, nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
			return false
		}
	}
	return true
}

type where struct {
	sql  string
	args []any
}

func (w *where) add(sql string, a ...any) {
	for _, v := range a {
		w.args = append(w.args, v)
		sql = strings.Replace(sql, "?", fmt.Sprintf("$%d", len(w.args)), 1)
	}
	w.sql += " AND " + sql
}

// enquiryDay is the calendar day (India) an enquiry was created.
const enquiryDay = `((created_at AT TIME ZONE 'Asia/Kolkata')::date)`

// build applies the filters shared by both tables: not deleted, class, year, and the date range on dateExpr.
func build(f Filter, dateExpr string) where {
	w := where{sql: "deleted_at IS NULL"}
	if f.ClassID != "" {
		w.add("class_id::text = ?", strings.ToLower(f.ClassID))
	}
	if f.YearID != "" {
		w.add("year_id::text = ?", strings.ToLower(f.YearID))
	}
	if f.From != "" {
		w.add(dateExpr+" >= ?::date", f.From)
	}
	if f.To != "" {
		w.add(dateExpr+" <= ?::date", f.To)
	}
	return w
}

// Get returns the dashboard for the caller's institute. The caller has already been checked for
// adm.admission_insights.view with a tenant-wide scope.
func (s Service) Get(ctx context.Context, f Filter) (Insights, error) {
	return s.get(ctx, f, time.Now())
}

func (s Service) get(ctx context.Context, f Filter, now time.Time) (Insights, error) {
	first, last, err := Validate(f, now)
	if err != nil {
		return Insights{}, err
	}
	out := Insights{
		From: first.Format(crud.DateLayout), To: last.Format(crud.DateLayout), AsOf: now.In(india).Format(crud.DateLayout),
		EnquiriesByStatus: zero(enquiries.Statuses), EnquiriesBySource: zero(enquiries.Sources), RegistrationsBy: zero(registrations.Statuses),
	}
	months := map[string]int{}
	for m := first; !m.After(last); m = m.AddDate(0, 1, 0) {
		months[m.Format("2006-01")] = len(out.Trend)
		out.Trend = append(out.Trend, Month{Month: m.Format("2006-01")})
	}
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		if err := loadEnquiries(ctx, tx, f, &out, months); err != nil {
			return err
		}
		return loadRegistrations(ctx, tx, f, &out, months)
	})
	if err != nil {
		return Insights{}, err
	}
	admitted := ratio(out.RegistrationsAcceptedOrAdmitted, out.RegistrationsAdmitted)
	out.Conversion = Conversion{
		EnquiryToRegistration:  ratio(out.EnquiriesTotal, out.enquiriesRegistered),
		RegistrationToAccepted: ratio(out.RegistrationsTotal, out.RegistrationsBy["accepted"]),
		AcceptedToAdmitted:     &admitted,
	}
	return out, nil
}

func zero(keys []string) map[string]int {
	m := make(map[string]int, len(keys))
	for _, k := range keys {
		m[k] = 0
	}
	return m
}

func ratio(of, converted int) Ratio {
	r := Ratio{Of: of, Converted: converted}
	if of > 0 {
		v := math.Round(float64(converted)/float64(of)*10000) / 10000
		r.Rate = &v
	}
	return r
}

// loadEnquiries runs one grouped query (GROUPING SETS) for status, source, month and the grand total, so every
// number comes from the same snapshot. The rows counted are exactly those the enquiries list returns for the filter.
// Months outside the trend range are counted in the other groups but not shown in the trend.
func loadEnquiries(ctx context.Context, tx pgx.Tx, f Filter, out *Insights, months map[string]int) error {
	w := build(f, enquiryDay)
	today := `(now() AT TIME ZONE 'Asia/Kolkata')::date`
	sql := fmt.Sprintf(`SELECT CASE WHEN GROUPING(status) = 0 THEN 'status' WHEN GROUPING(source) = 0 THEN 'source'
			WHEN GROUPING(m) = 0 THEN 'month' ELSE 'total' END AS grp,
		status, source, to_char(m, 'YYYY-MM'), count(*),
		count(*) FILTER (WHERE %[1]s AND follow_up_date = %[2]s),
		count(*) FILTER (WHERE %[1]s AND follow_up_date < %[2]s),
		count(*) FILTER (WHERE EXISTS (SELECT 1 FROM student_registrations r
			WHERE r.tenant_id = e.tenant_id AND r.enquiry_id = e.id AND r.deleted_at IS NULL))
		FROM (SELECT enquiries.*, date_trunc('month', %[3]s)::date AS m FROM enquiries WHERE %[4]s) e
		GROUP BY GROUPING SETS ((status), (source), (m), ())`, enquiries.DueSQL, today, enquiryDay, w.sql)
	rows, err := tx.Query(ctx, sql, w.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grp string
		var status, source, month *string
		var count, dueToday, overdue, registered int
		if err := rows.Scan(&grp, &status, &source, &month, &count, &dueToday, &overdue, &registered); err != nil {
			return err
		}
		switch grp {
		case "status":
			out.EnquiriesByStatus[*status] = count
		case "source":
			out.EnquiriesBySource[*source] = count
		case "month":
			if i, ok := months[*month]; ok {
				out.Trend[i].Enquiries = count
			}
		case "total":
			out.EnquiriesTotal, out.enquiriesRegistered = count, registered
			out.FollowUps = FollowUps{Due: dueToday + overdue, DueToday: dueToday, Overdue: overdue}
		}
	}
	return rows.Err()
}

// loadRegistrations is loadEnquiries for registrations (applied_on is the date basis). The admitted count uses the
// admissions list's rule (admitted_student_id set) and is read in the same pass, in the "total" group.
func loadRegistrations(ctx context.Context, tx pgx.Tx, f Filter, out *Insights, months map[string]int) error {
	w := build(f, "applied_on")
	sql := fmt.Sprintf(`SELECT CASE WHEN GROUPING(status) = 0 THEN 'status' WHEN GROUPING(m) = 0 THEN 'month' ELSE 'total' END,
		status, to_char(m, 'YYYY-MM'), count(*), count(*) FILTER (WHERE admitted_student_id IS NOT NULL),
		count(*) FILTER (WHERE status = 'accepted' OR admitted_student_id IS NOT NULL)
		FROM (SELECT student_registrations.*, date_trunc('month', applied_on)::date AS m FROM student_registrations WHERE %s) r
		GROUP BY GROUPING SETS ((status), (m), ())`, w.sql)
	rows, err := tx.Query(ctx, sql, w.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var grp string
		var status, month *string
		var count, admitted, acceptedOrAdmitted int
		if err := rows.Scan(&grp, &status, &month, &count, &admitted, &acceptedOrAdmitted); err != nil {
			return err
		}
		switch grp {
		case "status":
			out.RegistrationsBy[*status] = count
		case "month":
			if i, ok := months[*month]; ok {
				out.Trend[i].Registrations = count
			}
		case "total":
			out.RegistrationsTotal, out.RegistrationsAdmitted, out.RegistrationsAcceptedOrAdmitted = count, admitted, acceptedOrAdmitted
		}
	}
	return rows.Err()
}
