// Package timefmt centralizes the wall-clock formats fngr accepts on input
// and the canonical formats it uses for display and storage.
package timefmt

import (
	"fmt"
	"strconv"
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
	"3pm",
	"3 pm",
	"3PM",
	"3 PM",
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

// Year bounds for any timestamp fngr will accept. DateTimeFormat renders the
// year with %04d, so a year outside this range produces a string SQLite stores
// happily but the driver cannot scan back into a time.Time — which used to
// poison every read of the whole table. Reject at the door instead.
const (
	minYear = 1
	maxYear = 9999
)

// InRange reports whether t can be stored and read back. Exported so the
// storage layer can re-check timestamps that never went through ParsePartial
// (--format=json import, or a *time.Time set programmatically).
func InRange(t time.Time) bool {
	y := t.Year()
	return y >= minYear && y <= maxYear
}

// FormatStorage renders t in the canonical SQLite TEXT encoding. Every value
// compared against or written to events.created_at must go through here, so
// writes and date-range bounds cannot drift apart. Callers that store the
// result must check InRange first — see event.formatTimestamp.
func FormatStorage(t time.Time) string { return t.UTC().Format(DateTimeFormat) }

// ParseStorage is the inverse of FormatStorage. ok=false means s is not in the
// canonical encoding, which in practice means it was written by a version
// without the InRange guard.
func ParseStorage(s string) (t time.Time, ok bool) {
	t, err := time.ParseInLocation(DateTimeFormat, s, time.UTC)
	return t, err == nil
}

// ParsePartial parses s using the same layouts as Parse but reports which
// components were present in the input. Time-only inputs (e.g. "9:30",
// "3:04PM") return hasDate=false; date-only inputs ("2026-04-15") return
// hasTime=false; full timestamps return both true.
//
// When hasDate is false, the returned t carries today's local date so the
// caller can either use it as-is or splice into another date.
func ParsePartial(s string) (t time.Time, hasDate, hasTime bool, err error) {
	t, hasDate, hasTime, err = parsePartial(s)
	if err != nil {
		return time.Time{}, false, false, err
	}
	if !InRange(t) {
		return time.Time{}, false, false, fmt.Errorf(
			"time %q resolves to year %d, outside the supported range %d-%d",
			s, t.Year(), minYear, maxYear)
	}
	return t, hasDate, hasTime, nil
}

func parsePartial(s string) (t time.Time, hasDate, hasTime bool, err error) {
	if t, hasDate, hasTime, ok := parseRelative(s, time.Now()); ok {
		return t, hasDate, hasTime, nil
	}
	for _, layout := range fullFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true, layoutHasTime(layout), nil
		}
	}
	if clock, ok := parseClock(s); ok {
		now := time.Now()
		t = time.Date(
			now.Year(), now.Month(), now.Day(),
			clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(),
			time.Local,
		)
		return t, false, true, nil
	}
	return time.Time{}, false, false, fmt.Errorf(
		"unrecognized time %q (try YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC3339, HH:MM, 3:04PM, "+
			"or relative forms like \"today\", \"yesterday\", \"2 days ago\", \"yesterday at 9am\")", s)
}

// layoutHasTime reports whether layout (one of fullFormats) carries a time
// component. The only date-only layout in fullFormats is DateFormat.
func layoutHasTime(layout string) bool { return layout != DateFormat }

// parseClock parses s against the time-only layouts and returns the parsed
// wall-clock time (its date fields are unspecified and must be ignored).
func parseClock(s string) (time.Time, bool) {
	for _, layout := range timeOnlyFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// parseRelative recognizes human relative timestamps anchored at now:
//
//	now
//	today | yesterday                         (optionally "... at <time>")
//	N {day|week|month}s ago                    (optionally "... at <time>")
//	N {hour|minute}s ago                       (sub-day; no "at" suffix)
//
// "a"/"an" count as 1 (e.g. "an hour ago"). Bare day expressions with no
// explicit time carry now's time-of-day and report hasTime=false so callers
// can still splice. Sub-day offsets and "now" report hasTime=true.
// ok=false means s is not a relative form (the caller should try other
// layouts).
func parseRelative(s string, now time.Time) (t time.Time, hasDate, hasTime, ok bool) {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return time.Time{}, false, false, false
	}
	if s == "now" {
		return now, true, true, true
	}

	dayPart, timePart, hasAt := strings.Cut(s, " at ")

	base, hasClock, ok := relOffset(dayPart, now)
	if !ok {
		return time.Time{}, false, false, false
	}
	if hasClock {
		// Sub-day offsets ("3 hours ago") already fix the time of day and
		// cannot be combined with an explicit "at <time>".
		if hasAt {
			return time.Time{}, false, false, false
		}
		return base, true, true, true
	}
	if !hasAt {
		// No explicit time: keep now's time-of-day but report date-only.
		return base, true, false, true
	}
	clock, clockOK := parseClock(timePart)
	if !clockOK {
		return time.Time{}, false, false, false
	}
	return time.Date(base.Year(), base.Month(), base.Day(),
		clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(),
		now.Location()), true, true, true
}

// relOffset resolves a relative day expression to a time anchored at now.
// hasClock reports whether the expression already fixes the time of day
// ("N {hour|minute}s ago"); when false the result is date-granular and
// carries now's time-of-day for the caller to keep or replace.
func relOffset(s string, now time.Time) (t time.Time, hasClock, ok bool) {
	switch s {
	case "today":
		return now, false, true
	case "yesterday":
		return now.AddDate(0, 0, -1), false, true
	}
	n, unit, ok := relCountUnit(s)
	if !ok {
		return time.Time{}, false, false
	}
	switch unit {
	case "minute":
		return now.Add(-time.Duration(n) * time.Minute), true, true
	case "hour":
		return now.Add(-time.Duration(n) * time.Hour), true, true
	case "day":
		return now.AddDate(0, 0, -n), false, true
	case "week":
		return now.AddDate(0, 0, -7*n), false, true
	case "month":
		return addMonths(now, -n), false, true
	}
	return time.Time{}, false, false
}

// addMonths shifts t by months, clamping the day to the last valid day of the
// target month instead of letting it spill into the next one.
//
// time.AddDate normalizes overflow, so on 2026-03-31 "1 month ago" went to
// Feb 31 and rolled forward to 2026-03-03 — a backdate of 28 days that stayed
// in the *current* month. Clamping gives 2026-02-28, which is what "a month
// ago" means to a person.
func addMonths(t time.Time, months int) time.Time {
	// Day 0 of the month after the target is the target's last day, and it
	// normalizes any year rollover for us.
	y, m, d := t.Date()
	last := time.Date(y, m+time.Month(months)+1, 0, 0, 0, 0, 0, t.Location())
	hh, mm, ss := t.Clock()
	return time.Date(last.Year(), last.Month(), min(d, last.Day()),
		hh, mm, ss, t.Nanosecond(), t.Location())
}

// maxRelCount bounds the count in "N <unit> ago". Two things need the bound.
// `time.Duration(n) * time.Hour` overflows int64 above ~2.56e6 hours and wraps
// to a *positive* offset, so "9223372036854775807 hours ago" used to resolve
// one hour into the future. And a large enough n pushes the year out of
// [minYear, maxYear] (see ParsePartial).
//
// 1e6 is past any real-world entry — a million hours is 114 years — while
// 1e6 * time.Hour stays under a third of math.MaxInt64.
const maxRelCount = 1_000_000

// relCountUnit parses "<count> <unit> ago" into a non-negative count and the
// singularized unit (e.g. "days" -> "day"). "a"/"an" count as 1. Counts above
// maxRelCount are rejected.
func relCountUnit(s string) (n int, unit string, ok bool) {
	fields := strings.Fields(s)
	if len(fields) != 3 || fields[2] != "ago" {
		return 0, "", false
	}
	switch fields[0] {
	case "a", "an":
		n = 1
	default:
		parsed, err := strconv.Atoi(fields[0])
		if err != nil || parsed < 0 || parsed > maxRelCount {
			return 0, "", false
		}
		n = parsed
	}
	return n, strings.TrimSuffix(fields[1], "s"), true
}

// Parse accepts a timestamp in one of several layouts. Time-only inputs
// (e.g. "15:04", "3:04PM") are completed with today's local date.
func Parse(s string) (time.Time, error) {
	t, _, _, err := ParsePartial(s)
	return t, err
}

// timePrefixDelim separates an optional leading timestamp from the rest of a
// title. A bare ":" would clash with the colons inside times like "9:30", so
// the delimiter is ": " (colon followed by a space).
const timePrefixDelim = ": "

// SplitTimePrefix detects a leading time/date token in s. If the text before
// the first ": " parses as a timestamp via Parse, it returns the parsed time,
// the remaining text (whitespace-trimmed), and ok=true. Otherwise it returns
// the zero time, s unchanged, and ok=false.
func SplitTimePrefix(s string) (t time.Time, rest string, ok bool) {
	before, after, found := strings.Cut(s, timePrefixDelim)
	if !found {
		return time.Time{}, s, false
	}
	t, err := Parse(strings.TrimSpace(before))
	if err != nil {
		return time.Time{}, s, false
	}
	return t, strings.TrimSpace(after), true
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

// SpliceTime returns orig with its wall-clock time replaced by the
// hour/minute/second/nanosecond of newTime. Date and timezone come from
// orig.
func SpliceTime(orig, newTime time.Time) time.Time {
	return time.Date(
		orig.Year(), orig.Month(), orig.Day(),
		newTime.Hour(), newTime.Minute(), newTime.Second(), newTime.Nanosecond(),
		orig.Location(),
	)
}

// SpliceDate returns orig with its date replaced by the year/month/day of
// newDate. Wall-clock time and timezone come from orig.
func SpliceDate(orig, newDate time.Time) time.Time {
	return time.Date(
		newDate.Year(), newDate.Month(), newDate.Day(),
		orig.Hour(), orig.Minute(), orig.Second(), orig.Nanosecond(),
		orig.Location(),
	)
}
