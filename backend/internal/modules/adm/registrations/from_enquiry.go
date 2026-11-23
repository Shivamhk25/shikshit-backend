package registrations

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrAlreadyConverted means the enquiry already became a registration. ErrEnquiryClosed means it was closed, so it
// cannot be turned into one. The API answers 409 to both.
var (
	ErrAlreadyConverted = errors.New("registrations: enquiry already converted")
	ErrEnquiryClosed    = errors.New("registrations: enquiry is closed")
)

// Fill gives the details an enquiry may lack. A blank field keeps what the enquiry has.
type Fill struct {
	DateOfBirth, ClassID, YearID, Gender *string
}

// enquiryRow is what a registration is prefilled from.
type enquiryRow struct {
	code, enquirer, phone, child, status string
	dob, classID, yearID                 *string
}

// splitName gives the first word as the first name and the rest as the last name; the registrar corrects it after.
func splitName(full string) (first string, last *string) {
	parts := strings.Fields(full)
	if len(parts) == 0 {
		return "", nil
	}
	if len(parts) > 1 {
		l := strings.Join(parts[1:], " ")
		return parts[0], &l
	}
	return parts[0], nil
}

// CreateFromEnquiry makes a registration from an enquiry and marks the enquiry converted, in one transaction: the
// child's name, date of birth, class and year, and the parent's name and phone come from the enquiry, anything the
// enquiry lacks comes from fill, and the application date is today. The enquiry row is locked first, so two clicks
// at once cannot both convert it. A converted enquiry (ErrAlreadyConverted) and a closed one (ErrEnquiryClosed) are
// refused; anything invalid is a crud.FieldErrors and nothing is written.
func (s Service) CreateFromEnquiry(ctx context.Context, actor audit.Actor, enquiryID string, fill Fill) (Registration, error) {
	var out Registration
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var e enquiryRow
		err := tx.QueryRow(ctx, `SELECT code, enquirer_name, phone, child_name, status, date_of_birth::text, class_id::text, year_id::text
			FROM enquiries WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, enquiryID).
			Scan(&e.code, &e.enquirer, &e.phone, &e.child, &e.status, &e.dob, &e.classID, &e.yearID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch e.status {
		case "converted":
			return ErrAlreadyConverted
		case "closed":
			return ErrEnquiryClosed
		}
		var linked int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM student_registrations WHERE enquiry_id::text = $1 AND deleted_at IS NULL`, enquiryID).Scan(&linked); err != nil {
			return err
		}
		if linked > 0 {
			return ErrAlreadyConverted
		}

		pick := func(over, own *string) *string {
			if over != nil && strings.TrimSpace(*over) != "" {
				return over
			}
			return own
		}
		dob, classID, yearID := pick(fill.DateOfBirth, e.dob), pick(fill.ClassID, e.classID), pick(fill.YearID, e.yearID)
		var missing crud.FieldErrors
		for _, m := range []struct {
			field, what string
			v           *string
		}{{"dateOfBirth", "a date of birth", dob}, {"classId", "a class", classID}, {"yearId", "an academic year", yearID}} {
			if m.v == nil {
				missing.Add(m.field, "required", "This enquiry has no "+strings.TrimPrefix(m.what, "a ")+". Add it to the enquiry or enter it here.")
			}
		}
		if len(missing) > 0 {
			return missing
		}
		first, last := splitName(e.child)
		in := Input{AppliedOn: Today(time.Now()), FirstName: first, LastName: last, DateOfBirth: *dob, Gender: fill.Gender,
			GuardianName: &e.enquirer, GuardianPhone: e.phone, ClassID: *classID, YearID: *yearID, Status: "submitted", EnquiryID: &enquiryID}
		if out, err = s.store().CreateIn(ctx, tx, actor, in); err != nil {
			// the unique link is the last guard: a registration that slipped past the checks above is the same refusal
			var fe crud.FieldErrors
			if errors.As(err, &fe) && len(fe) == 1 && fe[0].Code == "already_converted" {
				return ErrAlreadyConverted
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE enquiries SET status = 'converted', updated_by = NULLIF($2, '')::uuid WHERE id::text = $1`, enquiryID, actor.UserID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, audit.Entry{Action: "enquiries.convert", ResourceType: "enquiry", ResourceID: enquiryID,
			Before: map[string]any{"status": e.status}, After: map[string]any{"status": "converted", "registrationId": out.ID, "applicationNumber": out.ApplicationNumber}})
	})
	if err != nil {
		var zero Registration
		return zero, err
	}
	return out, nil
}
