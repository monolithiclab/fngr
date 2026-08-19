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
func ParsePartial(s string) (t time.Time, hasDate, hasTime, exists bool, err error) {
	t, hasDate, hasTime, exists, err = parsePartial(s, time.Now())
	if err != nil {
		return time.Time{}, false, false, false, err
	}
	if !InRange(t) {
		return time.Time{}, false, false, false, fmt.Errorf(
			"time %q resolves to year %d, outside the supported range %d-%d",
			s, t.Year(), minYear, maxYear)
	}
	return t, hasDate, hasTime, exists, nil
}

// parsePartial is ParsePartial with the anchor for relative and time-only
// input injected, for testability.
func parsePartial(s string, now time.Time) (t time.Time, hasDate, hasTime, exists bool, err error) {
	if t, hasDate, hasTime, exists, ok := parseRelative(s, now); ok {
		return t, hasDate, hasTime, exists, nil
	}
	for _, layout := range fullFormats {
		if layoutHasOffset(layout) {
			// The input carries its own UTC offset, so it names an instant
			// rather than a local clock and nothing can contradict it.
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return t, true, true, true, nil
			}
			continue
		}
		// Parsed in UTC — which has no transitions — the fields come back
		// exactly as written, before the local zone has had any say over
		// them. Rebuilding them with localClock is what notices a clock the
		// zone skips; parsing straight into time.Local would shift it first
		// and leave nothing to compare against.
		asked, err := time.ParseInLocation(layout, s, time.UTC)
		if err != nil {
			continue
		}
		t, exists := localClock(asked.Year(), asked.Month(), asked.Day(),
			asked.Hour(), asked.Minute(), asked.Second(), asked.Nanosecond(), time.Local)
		if !layoutHasTime(layout) {
			// A bare date names no clock, so the zone has nothing to skip —
			// and some zones do shift at midnight.
			exists = true
		}
		return t, true, layoutHasTime(layout), exists, nil
	}
	if clock, ok := parseClock(s); ok {
		t, exists := localClock(
			now.Year(), now.Month(), now.Day(),
			clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(),
			time.Local,
		)
		return t, false, true, exists, nil
	}
	return time.Time{}, false, false, false, fmt.Errorf(
		"unrecognized time %q (try %s, or relative forms like %s)",
		s, AbsoluteForms, RelativeForms)
}

// AbsoluteForms and RelativeForms name every shape ParsePartial accepts, and
// are the one place that list is written down. They are exported because the
// same vocabulary has to appear in the `--time`, `event time` and `event date`
// help text, and spelling it out at each site is what let the three disagree:
// the 12-hour form was `3:04PM` in all of them — Go's reference clock, a
// literal sitting in a list of placeholders, and a Go layout shown to someone
// typing a time. Kong interpolates these through kongVars, the same way
// render.ListFormats reaches the `--format` help.
//
// The halves are exported too, and the two full lists are built from them, so
// that a verb accepting a *subset* still spells its tokens once. `event time`
// refuses a value with no clock in it and `event date` one with no date, and a
// help string listing forms the verb rejects is worse than none — it is what
// makes `fngr event time 1 yesterday` read as supported. Splitting on that
// same line keeps the answer beside layoutHasTime, which is what decides it.
//
// Placeholders, not layouts: a caller wanting a layout wants DateFormat or
// DateTimeFormat. Sites that gesture at the grammar without enumerating it
// (list's --from/--to) are deliberately not built from these — there is
// nothing there to drift.
const (
	// DateForms names no clock; ClockForms no date; DateTimeForms both.
	DateForms     = "YYYY-MM-DD"
	DateTimeForms = "YYYY-MM-DDTHH:MM, RFC3339"
	ClockForms    = "HH:MM, HH:MMpm"
	AbsoluteForms = DateForms + ", " + DateTimeForms + ", " + ClockForms

	// A relative form always resolves against `now`, so every one of them
	// carries a date; the split is over whether it also carries a clock of its
	// own. RelativeDateForms take now's time-of-day and report date-only,
	// which is what lets `event time` refuse them.
	RelativeDateForms = `"today", "yesterday", "2 days ago"`
	RelativeTimeForms = `"yesterday at 9am", "now", "3 hours ago"`
	RelativeForms     = RelativeDateForms + ", " + RelativeTimeForms
)

// layoutHasTime reports whether layout (one of fullFormats) carries a time
// component. The only date-only layout in fullFormats is DateFormat.
func layoutHasTime(layout string) bool { return layout != DateFormat }

// layoutHasOffset reports whether layout (one of fullFormats) carries a UTC
// offset. Lives beside layoutHasTime so fullFormats has one place that answers
// questions about its entries — a new offset-bearing layout has to be handled
// here or parsePartial will treat it as a bare local clock.
func layoutHasOffset(layout string) bool { return layout == time.RFC3339 }

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
//
// Only the "<day> at <time>" form can report exists=false: every other
// relative form is an offset from a real instant, and its clock comes from
// now rather than from the user.
func parseRelative(s string, now time.Time) (t time.Time, hasDate, hasTime, exists, ok bool) {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	if s == "" {
		return time.Time{}, false, false, false, false
	}
	if s == "now" {
		return now, true, true, true, true
	}

	dayPart, timePart, hasAt := strings.Cut(s, " at ")

	base, hasClock, ok := relOffset(dayPart, now)
	if !ok {
		return time.Time{}, false, false, false, false
	}
	if hasClock {
		// Sub-day offsets ("3 hours ago") already fix the time of day and
		// cannot be combined with an explicit "at <time>".
		if hasAt {
			return time.Time{}, false, false, false, false
		}
		return base, true, true, true, true
	}
	if !hasAt {
		// No explicit time: keep now's time-of-day but report date-only.
		return base, true, false, true, true
	}
	clock, clockOK := parseClock(timePart)
	if !clockOK {
		return time.Time{}, false, false, false, false
	}
	// "yesterday at 2:30" is a wall clock the user typed, so it can name an
	// hour the zone skips just as "2026-03-08 02:30" can.
	t, exists = localClock(base.Year(), base.Month(), base.Day(),
		clock.Hour(), clock.Minute(), clock.Second(), clock.Nanosecond(),
		now.Location())
	return t, true, true, exists, true
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
	t, _, _, _, err := ParsePartial(s)
	return t, err
}

// timePrefixDelim separates an optional leading timestamp from the rest of a
// title. A bare ":" would clash with the colons inside times like "9:30", so
// the delimiter is ": " (colon followed by a space).
const timePrefixDelim = ": "

// SplitTimePrefix detects a leading time/date token in s. If the text before
// the first ": " parses as a timestamp, it returns the parsed time, that token
// (whitespace-trimmed), the remaining text (likewise), whether the clock the
// token names exists locally, and ok=true. Otherwise it returns the zero time,
// an empty prefix, s unchanged, and ok=false.
//
// The prefix is returned rather than consumed silently so a caller warning
// about a skipped clock can name the very text the user typed.
func SplitTimePrefix(s string) (t time.Time, prefix, rest string, exists, ok bool) {
	before, after, found := strings.Cut(s, timePrefixDelim)
	if !found {
		return time.Time{}, "", s, false, false
	}
	before = strings.TrimSpace(before)
	t, _, _, exists, err := ParsePartial(before)
	if err != nil {
		return time.Time{}, "", s, false, false
	}
	return t, before, strings.TrimSpace(after), exists, true
}

// SpliceTime returns orig with its wall-clock time replaced by the
// hour/minute/second/nanosecond of newTime. Date and timezone come from
// orig. exists is false when the result is not the clock that was asked for
// — see localClock.
func SpliceTime(orig, newTime time.Time) (t time.Time, exists bool) {
	return localClock(
		orig.Year(), orig.Month(), orig.Day(),
		newTime.Hour(), newTime.Minute(), newTime.Second(), newTime.Nanosecond(),
		orig.Location(),
	)
}

// SpliceDate returns orig with its date replaced by the year/month/day of
// newDate. Wall-clock time and timezone come from orig. exists is false when
// the result is not the clock that was asked for — see localClock.
func SpliceDate(orig, newDate time.Time) (t time.Time, exists bool) {
	return localClock(
		newDate.Year(), newDate.Month(), newDate.Day(),
		orig.Hour(), orig.Minute(), orig.Second(), orig.Nanosecond(),
		orig.Location(),
	)
}

// localClock builds a wall clock in loc and reports whether that clock exists
// there — the single gate every wall clock in this package is assembled
// through. time.Date does not fail on a timestamp that a DST spring-forward
// skipped: 02:30 on 2026-03-08 in America/New_York comes back as 01:30 EST, an
// hour earlier than asked for, and every caller stored it and reported success.
// Neither does time.ParseInLocation, which is why parsePartial reads the clock
// in UTC first and rebuilds it here.
//
// The fall-back case counts as existing. A clock that occurs twice resolves to
// the first (still-DST) offset, so the user gets the stamp they typed; which of
// the two they meant is unknowable and the displayed time is right either way.
func localClock(year int, month time.Month, day, hour, minute, sec, nsec int, loc *time.Location) (time.Time, bool) {
	t := time.Date(year, month, day, hour, minute, sec, nsec, loc)
	exists := t.Year() == year && t.Month() == month && t.Day() == day &&
		t.Hour() == hour && t.Minute() == minute && t.Second() == sec
	return t, exists
}
