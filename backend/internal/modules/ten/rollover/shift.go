package rollover

import "time"

// Shift says how dates of the year being closed move into the new year.
//
// When both years start on the same day of the same month (the usual case: 1 April to 31 March) every date moves by
// whole calendar years, so 26 January stays 26 January. 29 February moves to 28 February when the new year has none.
// When the years start on different days, every date moves by the same number of days instead.
type Shift struct {
	Mode  string `json:"mode"` // "years" or "days"
	Years int    `json:"years"`
	Days  int    `json:"days"`
}

// Shift modes.
const (
	ShiftYears = "years"
	ShiftDays  = "days"
)

// ComputeShift works out the shift from the start dates of the two years (dates only; the time of day is ignored).
func ComputeShift(fromStart, toStart time.Time) Shift {
	if fromStart.Month() == toStart.Month() && fromStart.Day() == toStart.Day() {
		return Shift{Mode: ShiftYears, Years: toStart.Year() - fromStart.Year()}
	}
	a := time.Date(fromStart.Year(), fromStart.Month(), fromStart.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(toStart.Year(), toStart.Month(), toStart.Day(), 0, 0, 0, 0, time.UTC)
	return Shift{Mode: ShiftDays, Days: int(b.Sub(a).Hours() / 24)}
}

// Apply moves one date.
func (s Shift) Apply(d time.Time) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	if s.Mode == ShiftDays {
		return d.AddDate(0, 0, s.Days)
	}
	y := d.Year() + s.Years
	if d.Month() == time.February && d.Day() == 29 && !isLeap(y) {
		return time.Date(y, time.February, 28, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(y, d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

func isLeap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// iso is the API form of a date.
func iso(t time.Time) string { return t.Format("2006-01-02") }
