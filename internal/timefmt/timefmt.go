// Package timefmt centralizes the wall-clock formats fngr accepts on input
// and the canonical formats it uses for display and storage.
package timefmt

import (
	"fmt"
	"time"
)

const (
	// DateFormat is the canonical YYYY-MM-DD layout used for date-only input,
	// CLI display, and SQLite TEXT timestamps.
	DateFormat = "2006-01-02"
	// DateTimeFormat is the canonical layout used to store timestamps in
	// SQLite.
	DateTimeFormat = "2006-01-02 15:04:05"
)

var fullFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	DateFormat,
}

var timeOnlyFormats = []string{
	"15:04:05",
	"15:04",
	"3:04PM",
	"3:04 PM",
}

// Parse accepts a timestamp in one of several layouts. Time-only inputs
// (e.g. "15:04", "3:04PM") are completed with today's local date.
func Parse(s string) (time.Time, error) {
	for _, layout := range fullFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	for _, layout := range timeOnlyFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			now := time.Now()
			return time.Date(
				now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), t.Nanosecond(),
				t.Location(),
			), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q (try YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC3339, HH:MM, or 3:04PM)", s)
}

// ParseDate accepts a date-only input (YYYY-MM-DD) and returns the start of
// that day in the local timezone.
func ParseDate(s string) (time.Time, error) {
	t, err := time.ParseInLocation(DateFormat, s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("unrecognized date %q (expected YYYY-MM-DD)", s)
	}
	return t, nil
}
