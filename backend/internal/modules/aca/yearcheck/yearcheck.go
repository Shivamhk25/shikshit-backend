// Package yearcheck holds the rules shared by everything that hangs off an academic year (terms, holidays, events).
package yearcheck

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// Dates returns the first and last day of the live academic year, or a yearId field error when it does not exist in
// this institute. It must run inside the write transaction.
func Dates(ctx context.Context, tx pgx.Tx, yearID string) (start, end string, err error) {
	err = tx.QueryRow(ctx, `SELECT start_date::text, end_date::text FROM academic_years WHERE id::text = $1 AND deleted_at IS NULL`, yearID).Scan(&start, &end)
	if err == pgx.ErrNoRows {
		var fe crud.FieldErrors
		fe.Add("yearId", "not_found", "This academic year does not exist.")
		return "", "", fe
	}
	return start, end, err
}

// Inside adds an outside_year error on startField when [start, end] is not within [yStart, yEnd]. ISO dates compare
// correctly as text.
func Inside(fe *crud.FieldErrors, startField, start, end, yStart, yEnd string) {
	if start < yStart || end > yEnd {
		fe.Add(startField, "outside_year", "Dates must lie inside the academic year ("+yStart+" to "+yEnd+").")
	}
}
