package mail

import "time"

// NormalizeDate returns t in loc, or in UTC when loc is nil. A zero time
// stays zero.
func NormalizeDate(t time.Time, loc *time.Location) time.Time {
	if t.IsZero() {
		return t
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc)
}
