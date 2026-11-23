// Package enquirydiscussions implements ADM-04 Enquiry Discussions: the follow-up notes the admissions desk keeps on an
// enquiry. One note is one contact with a family (a call, a visit, a WhatsApp message): when, by which channel, what
// was said, how it ended and when to contact them next. A note belongs to exactly one enquiry. It can also move the
// enquiry's own follow-up date, in the same save.
package enquirydiscussions

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/aca/refs"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// ErrNotFound is returned when the discussion does not exist in this institute.
var ErrNotFound = resource.ErrNotFound

// Channels are how the contact happened, Outcomes how it ended. Languages are the languages a conversation can be
// recorded in: English and twelve Indian languages by their ISO 639-1 codes, then "other". A fixed list (not free text)
// keeps the same language from being typed in five spellings, so the desk can filter and count by it. "other" covers a
// language not listed; the notes can name it.
var (
	Channels  = []string{"call", "visit", "whatsapp", "email", "other"}
	Outcomes  = []string{"interested", "visit_scheduled", "call_back_later", "not_reachable", "not_interested", "other"}
	Languages = []string{"en", "hi", "bn", "mr", "te", "ta", "gu", "ur", "kn", "ml", "or", "pa", "as", "other"}
)

// Discussion is one follow-up note as the API shows it. LoggedBy is whoever saved it first.
type Discussion struct {
	ID               string    `json:"id"`
	EnquiryID        string    `json:"enquiryId"`
	EnquiryCode      string    `json:"enquiryCode"`
	EnquiryChildName string    `json:"enquiryChildName"`
	ContactedAt      time.Time `json:"contactedAt"`
	Channel          string    `json:"channel"`
	Summary          string    `json:"summary"`
	Notes            *string   `json:"notes"`
	Outcome          string    `json:"outcome"`
	NextFollowUpDate *string   `json:"nextFollowUpDate"`
	Language         string    `json:"language"`
	LoggedByID       *string   `json:"loggedById"`
	LoggedByName     *string   `json:"loggedByName"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Input is what create and replace accept. UpdateEnquiryFollowUp is an instruction, not stored on the note: when true
// the enquiry's follow-up date becomes NextFollowUpDate in the same transaction.
type Input struct {
	EnquiryID             string
	ContactedAt           time.Time
	Channel               string
	Summary               string
	Notes                 *string
	Outcome               string
	NextFollowUpDate      *string
	Language              string
	UpdateEnquiryFollowUp bool
}

// Filter narrows a list.
type Filter struct{ EnquiryID, Channel, Outcome, Language string }

// SortKeys are the sort keys clients may use.
var SortKeys = crud.Sorts{"contactedAt": "contacted_at", "enquiryCode": enquiryCodeSQL, "channel": "channel", "outcome": "outcome",
	"nextFollowUpDate": "next_follow_up_date", "createdAt": "created_at"}

// DefaultSort lists the latest contact first.
const DefaultSort = "-contactedAt"

// clockSkew is how far ahead of the server a contact time may be, so a phone whose clock runs a little fast can save.
const clockSkew = 5 * time.Minute

// ist is the calendar the school keeps: dates of contact and follow-up are India dates.
var ist = time.FixedZone("IST", 5*3600+1800)

const (
	enquiryCodeSQL = `(SELECT lower(e.code) FROM enquiries e WHERE e.tenant_id = enquiry_discussions.tenant_id AND e.id = enquiry_discussions.enquiry_id)`
	enquiryTextSQL = `(SELECT e.code || ' ' || e.child_name FROM enquiries e WHERE e.tenant_id = enquiry_discussions.tenant_id AND e.id = enquiry_discussions.enquiry_id)`
	// liveEnquirySQL hides the notes of a deleted enquiry: they stay stored, but nobody sees them.
	liveEnquirySQL = `EXISTS (SELECT 1 FROM enquiries e WHERE e.tenant_id = enquiry_discussions.tenant_id AND e.id = enquiry_discussions.enquiry_id AND e.deleted_at IS NULL)`
)

const columns = `id::text, enquiry_id::text,
	COALESCE((SELECT e.code FROM enquiries e WHERE e.tenant_id = enquiry_discussions.tenant_id AND e.id = enquiry_discussions.enquiry_id), '') AS enquiry_code,
	COALESCE((SELECT e.child_name FROM enquiries e WHERE e.tenant_id = enquiry_discussions.tenant_id AND e.id = enquiry_discussions.enquiry_id), '') AS enquiry_child_name,
	contacted_at, channel, summary, notes, outcome, next_follow_up_date::text, language,
	created_by::text,
	(SELECT u.display_name FROM users u WHERE u.tenant_id = enquiry_discussions.tenant_id AND u.id = enquiry_discussions.created_by) AS logged_by_name,
	created_at, updated_at`

func scan(r resource.Scanner) (Discussion, error) {
	var d Discussion
	err := r.Scan(&d.ID, &d.EnquiryID, &d.EnquiryCode, &d.EnquiryChildName, &d.ContactedAt, &d.Channel, &d.Summary, &d.Notes, &d.Outcome,
		&d.NextFollowUpDate, &d.Language, &d.LoggedByID, &d.LoggedByName, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

// Normalise trims, defaults and validates one note on its own, using the present time for the "not in the future"
// rule; rules that need other records are in Check.
func Normalise(in Input) (Input, error) { return normalise(in, time.Now()) }

// NormaliseAt is Normalise with the present time given, so the date rules can be tested at their edges.
func NormaliseAt(in Input, now time.Time) (Input, error) { return normalise(in, now) }

func normalise(in Input, now time.Time) (Input, error) {
	var errs crud.FieldErrors
	in.EnquiryID, in.Summary = strings.TrimSpace(in.EnquiryID), strings.TrimSpace(in.Summary)
	in.Channel, in.Outcome, in.Language = strings.ToLower(strings.TrimSpace(in.Channel)), strings.ToLower(strings.TrimSpace(in.Outcome)), strings.ToLower(strings.TrimSpace(in.Language))
	in.Notes, in.NextFollowUpDate = crud.TrimPtr(in.Notes), crud.TrimPtr(in.NextFollowUpDate)
	if in.EnquiryID == "" || !refs.IsUUID(in.EnquiryID) {
		errs.Add("enquiryId", "required", "Choose the enquiry this contact was about.")
	}
	errs.Required("summary", "a one-line summary of what was discussed", in.Summary, 200)
	errs.Optional("notes", "notes", in.Notes, 2000)
	errs.Choice("channel", in.Channel, Channels...)
	errs.Choice("outcome", in.Outcome, Outcomes...)
	errs.Choice("language", in.Language, Languages...)
	if in.Channel == "" {
		errs.Add("channel", "required", "Choose how the family was contacted.")
	}
	if in.Outcome == "" {
		errs.Add("outcome", "required", "Choose how the contact ended.")
	}
	if in.Language == "" {
		errs.Add("language", "required", "Choose the language of the conversation.")
	}
	switch {
	case in.ContactedAt.IsZero():
		errs.Add("contactedAt", "required", "Enter when the family was contacted.")
	case in.ContactedAt.After(now.Add(clockSkew)):
		errs.Add("contactedAt", "in_future", "The time of contact cannot be in the future.")
	}
	var next time.Time
	if in.NextFollowUpDate != nil {
		var err error
		if next, err = time.Parse("2006-01-02", *in.NextFollowUpDate); err != nil {
			errs.Add("nextFollowUpDate", "invalid_date", "Enter the next follow-up date as YYYY-MM-DD.")
		} else if !in.ContactedAt.IsZero() {
			y, m, d := in.ContactedAt.In(ist).Date()
			if next.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
				errs.Add("nextFollowUpDate", "before_contact", "The next follow-up cannot be before the day of this contact.")
			}
		}
	}
	if in.UpdateEnquiryFollowUp && in.NextFollowUpDate == nil {
		errs.Add("nextFollowUpDate", "required", "Enter the next follow-up date to update the enquiry's follow-up date.")
	}
	return in, errs.OrNil()
}

// updateFollowUp moves the enquiry's follow-up date, and writes the change to the audit trail as an enquiry update
// (the dates only, never the enquiry's personal details).
func updateFollowUp(ctx context.Context, tx pgx.Tx, actor audit.Actor, discussionID string, in Input) error {
	if !in.UpdateEnquiryFollowUp || in.NextFollowUpDate == nil {
		return nil
	}
	var before *string
	if err := tx.QueryRow(ctx, `SELECT follow_up_date::text FROM enquiries WHERE id::text = $1 AND deleted_at IS NULL FOR UPDATE`, in.EnquiryID).Scan(&before); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE enquiries SET follow_up_date = $2::date, updated_by = NULLIF($3, '')::uuid WHERE id::text = $1`, in.EnquiryID, *in.NextFollowUpDate, actor.UserID); err != nil {
		return err
	}
	return audit.Record(ctx, tx, actor, audit.Entry{Action: "enquiries.update", ResourceType: "enquiry", ResourceID: in.EnquiryID,
		Before: map[string]any{"followUpDate": before}, After: map[string]any{"followUpDate": *in.NextFollowUpDate, "discussionId": discussionID}})
}

// requireLive is ErrNotFound when the note's enquiry has been deleted: such a note is hidden, so it cannot be changed.
func requireLive(ctx context.Context, tx pgx.Tx, id string) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM enquiry_discussions WHERE id::text = $1 AND `+liveEnquirySQL, id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

var spec = resource.Spec[Discussion, Input]{
	Table: "enquiry_discussions", Action: "enquiry_discussions", ResourceType: "enquiry_discussion",
	Columns: columns, Scan: scan, ID: func(d Discussion) string { return d.ID },
	WriteColumns: []string{"enquiry_id", "contacted_at", "channel", "summary", "notes", "outcome", "next_follow_up_date", "language"},
	Args: func(in Input) []any {
		return []any{in.EnquiryID, in.ContactedAt, in.Channel, in.Summary, in.Notes, in.Outcome, in.NextFollowUpDate, in.Language}
	},
	SearchColumns: []string{"summary", "notes", enquiryTextSQL},
	Sorts:         SortKeys, DefaultSort: DefaultSort,
	Normalise: Normalise,
	Check: func(ctx context.Context, tx pgx.Tx, id string, in Input) error {
		if id != "" {
			if err := requireLive(ctx, tx, id); err != nil {
				return err
			}
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM enquiries WHERE id::text = $1 AND deleted_at IS NULL`, in.EnquiryID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return crud.FieldErrors{{Field: "enquiryId", Code: "not_found", Message: "This enquiry does not exist."}}
		}
		return nil
	},
	AfterWrite:   updateFollowUp,
	BeforeDelete: requireLive,
	Constraints: map[string]crud.FieldError{
		"enquiry_discussions_tenant_id_enquiry_id_fkey": {Field: "enquiryId", Code: "not_found", Message: "This enquiry does not exist."},
	},
	Restrict: func(context.Context) (resource.Cond, bool) { return resource.Cond{SQL: liveEnquirySQL}, true },
}

// Service reads and writes enquiry discussions.
type Service struct{ DB tenancy.Beginner }

func (s Service) store() resource.Store[Discussion, Input] {
	return resource.Store[Discussion, Input]{DB: s.DB, Spec: spec}
}

func filterConds(f Filter) []resource.Cond {
	var conds []resource.Cond
	for _, c := range []struct{ col, v string }{{"enquiry_id::text", f.EnquiryID}, {"channel", f.Channel}, {"outcome", f.Outcome}, {"language", f.Language}} {
		if c.v != "" {
			conds = append(conds, resource.Cond{SQL: c.col + " = ?", Args: []any{c.v}})
		}
	}
	return conds
}

// ListAudited returns one page of discussions for the API: the read is audited with the paging and filters, never the
// search text. Notes of a deleted enquiry are left out.
func (s Service) ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f Filter) (crud.Page[Discussion], error) {
	return s.store().ListAudited(ctx, a, map[string]any{"page": q.Page, "size": q.Size, "searched": q.Search != "",
		"enquiryId": f.EnquiryID, "channel": f.Channel, "outcome": f.Outcome, "language": f.Language}, q, filterConds(f)...)
}

// View returns one discussion for the API: the read is audited. A note of a deleted enquiry is not found.
func (s Service) View(ctx context.Context, a audit.Actor, id string) (Discussion, error) {
	return s.store().View(ctx, a, id)
}

// Create adds a discussion, and moves the enquiry's follow-up date when asked.
func (s Service) Create(ctx context.Context, a audit.Actor, in Input) (Discussion, error) {
	return s.store().Create(ctx, a, in)
}

// Update replaces a discussion's fields. A note of a deleted enquiry is not found, so it cannot be changed.
func (s Service) Update(ctx context.Context, a audit.Actor, id string, in Input) (Discussion, error) {
	return s.store().Update(ctx, a, id, in)
}

// Delete soft-deletes a discussion.
func (s Service) Delete(ctx context.Context, a audit.Actor, id string) error {
	return s.store().Delete(ctx, a, id)
}
