// Package timefmt centralizes the wall-clock formats fngr accepts on input
// and the canonical formats it uses for display and storage.
package timefmt

import (
	"fmt"
	"strings"
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
	"3.04pm",
	"3:04pm",
	"3:04 pm",
	"3.04PM",
	"3:04PM",
	"3:04 PM",
}

const (
	// LayoutToday is used when the event happened on `now`'s local date.
	LayoutToday = "3.04pm"
	// LayoutThisYear is used for events in the same calendar year as `now`
	// but on a different day.
	LayoutThisYear = "Jan 02 3.04pm"
	// LayoutOlder is used for events from a prior calendar year.
	LayoutOlder = "Jan 02 2006 3.04pm"
)

// FormatRelative formats t in the most compact human form that retains the
// information needed to disambiguate from `now`:
//   - same local date as now -> "9.32pm"
//   - same local year as now -> "Dec 09 9.32pm"
//   - older                  -> "Dec 09 2024 9.32pm"
//
// am/pm are emitted lowercase; the time uses '.' as the hour/minute
// separator (e.g. "9.32pm") to match fngr's display convention.
func FormatRelative(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	layout := LayoutOlder
	switch {
	case sameDay(t, now):
		layout = LayoutToday
	case t.Year() == now.Year():
		layout = LayoutThisYear
	}
	out := t.Format(layout)
	// time.Format emits "AM"/"PM"; lowercase only that suffix so month
	// abbreviations stay capitalized.
	return strings.Replace(strings.Replace(out, "AM", "am", 1), "PM", "pm", 1)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// ParsePartial parses s using the same layouts as Parse but reports which
// components were present in the input. Time-only inputs (e.g. "9:30",
// "3:04PM") return hasDate=false; date-only inputs ("2026-04-15") return
// hasTime=false; full timestamps return both true.
//
// When hasDate is false, the returned t carries today's local date so the
// caller can either use it as-is or splice into another date.
func ParsePartial(s string) (t time.Time, hasDate, hasTime bool, err error) {
	for _, layout := range fullFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true, layoutHasTime(layout), nil
		}
	}
	for _, layout := range timeOnlyFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			now := time.Now()
			t = time.Date(
				now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), t.Nanosecond(),
				t.Location(),
			)
			return t, false, true, nil
		}
	}
	return time.Time{}, false, false, fmt.Errorf("unrecognized time %q (try YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC3339, HH:MM, or 3:04PM)", s)
}

// layoutHasTime reports whether layout (one of fullFormats) carries a time
// component. The only date-only layout in fullFormats is DateFormat.
func layoutHasTime(layout string) bool { return layout != DateFormat }

// Parse accepts a timestamp in one of several layouts. Time-only inputs
// (e.g. "15:04", "3:04PM") are completed with today's local date.
func Parse(s string) (time.Time, error) {
	t, _, _, err := ParsePartial(s)
	return t, err
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
