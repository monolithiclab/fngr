package timefmt

import (
	"testing"
	"time"
	_ "time/tzdata" // embedded, so LoadLocation works with no system zoneinfo
)

// useNewYork points time.Local at America/New_York for the duration of the
// test. A DST spring-forward is the only way to produce a wall clock that does
// not exist, and time.FixedZone has no transitions — so unlike the rest of this
// package these tests need a real zone.
//
// It mutates package-global time.Local, so every caller must NOT use
// t.Parallel: the race detector flags concurrent swaps. Go resumes parallel
// tests only once the sequential ones have finished, so the swap is invisible
// to the parallel tests in this package.
func useNewYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v", err)
	}
	prev := time.Local
	t.Cleanup(func() { time.Local = prev })
	time.Local = loc
	return loc
}

// 2026-03-08 is the US spring-forward date: 02:00 EST jumps to 03:00 EDT, so no
// clock in [02:00, 03:00) exists that day. 2026-11-01 is the fall-back, where
// 01:30 happens twice.
const (
	springForward = "2026-03-08"
	fallBack      = "2026-11-01"
)

func TestSpliceTime_ReportsASkippedClock(t *testing.T) {
	loc := useNewYork(t)
	orig := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)

	got, exists := SpliceTime(orig, time.Date(2000, 1, 1, 2, 30, 0, 0, time.UTC))

	if exists {
		t.Error("exists = true, want false: 02:30 does not exist on " + springForward)
	}
	// The shifted value is still returned and still stored — the warning is
	// the whole remedy, since refusing would leave nothing to type instead.
	if got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("got %v, want the 01:30 EST that time.Date resolves to", got)
	}
}

func TestSpliceDate_ReportsASkippedClock(t *testing.T) {
	loc := useNewYork(t)
	orig := time.Date(2026, 6, 1, 2, 30, 0, 0, loc)

	got, exists := SpliceDate(orig, time.Date(2026, 3, 8, 0, 0, 0, 0, loc))

	if exists {
		t.Error("exists = true, want false: moving 02:30 onto " + springForward + " skips it")
	}
	if got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("got %v, want the 01:30 EST that time.Date resolves to", got)
	}
}

// TestSplice_FallBackAmbiguityCountsAsExisting pins the deliberate asymmetry: a
// clock that happens twice resolves to the first of the two offsets and the
// user gets the stamp they typed, so there is nothing to warn about.
func TestSplice_FallBackAmbiguityCountsAsExisting(t *testing.T) {
	loc := useNewYork(t)
	orig := time.Date(2026, 11, 1, 12, 0, 0, 0, loc)

	got, exists := SpliceTime(orig, time.Date(2000, 1, 1, 1, 30, 0, 0, time.UTC))

	if !exists {
		t.Error("exists = false, want true: 01:30 exists twice on " + fallBack + ", not zero times")
	}
	if got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("got %v, want 01:30", got)
	}
}

// TestParsePartial_ReportsASkippedClock is the DST half of ParsePartial's
// contract. Every wall clock a caller can type — absolute, bare clock, or the
// "<day> at <time>" relative form — is assembled through localClock, so a clock
// the zone skips is reported instead of being silently shifted an hour. now is
// the anchor for the relative and time-only forms; the absolute layouts ignore
// it.
func TestParsePartial_ReportsASkippedClock(t *testing.T) {
	loc := useNewYork(t)
	noon := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 12, 0, 0, 0, loc)
	}
	gapDay, dayAfter := noon(2026, 3, 8), noon(2026, 3, 9)

	tests := []struct {
		name string
		in   string
		now  time.Time
		want bool
	}{
		{"clock inside the gap", springForward + " 02:30", gapDay, false},
		{"gap, T separator", springForward + "T02:30", gapDay, false},
		{"gap, with seconds", springForward + " 02:30:00", gapDay, false},
		{"gap, top of the hour", springForward + " 02:00", gapDay, false},
		{"just before the gap", springForward + " 01:59", gapDay, true},
		{"just after the gap", springForward + " 03:00", gapDay, true},
		{"midnight on the same day", springForward, gapDay, true},
		{"ambiguous fall-back clock", fallBack + " 01:30", gapDay, true},
		// An RFC 3339 stamp carries its own offset, so it names an instant.
		// 02:30-05:00 is 03:30 EDT — a real time, spelled the other way.
		{"offset names an instant", springForward + "T02:30:00-05:00", gapDay, true},
		{"UTC instant", springForward + "T02:30:00Z", gapDay, true},
		// A bare clock lands on the anchor's date, so whether it exists
		// depends on which day the anchor is.
		{"bare clock in the gap", "2:30", gapDay, false},
		{"12h form in the gap", "2:30am", gapDay, false},
		{"same clock the day after", "2:30", dayAfter, true},
		{"bare clock outside the gap", "9:30", gapDay, true},
		// "<day> at <time>" is a clock the user typed too, on a day the
		// offset picked — the one relative form that can name the gap.
		{"relative day at a skipped clock", "yesterday at 2:30", dayAfter, false},
		{"relative day at a real clock", "yesterday at 9:30", dayAfter, true},
		{"today at a skipped clock", "today at 2:30", gapDay, false},
		// Every other relative form takes its clock from the anchor, which is
		// a real instant, so none of them can land in the gap.
		{"relative day", "yesterday", gapDay, true},
		{"relative offset", "3 hours ago", gapDay, true},
		{"now", "now", gapDay, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, exists, err := parsePartial(tt.in, tt.now)
			if err != nil {
				t.Fatalf("parsePartial(%q): %v", tt.in, err)
			}
			if exists != tt.want {
				t.Errorf("parsePartial(%q) exists = %v, want %v", tt.in, exists, tt.want)
			}
		})
	}
}

// TestSplitTimePrefix_ReportsASkippedClock pins that the prefix path carries
// existence out too: `fngr add "2026-03-08 02:30: standup"` has to warn for the
// same reason `--time` does, and the token comes back verbatim so the warning
// can quote what was typed.
func TestSplitTimePrefix_ReportsASkippedClock(t *testing.T) {
	useNewYork(t)

	got, prefix, rest, exists, ok := SplitTimePrefix(springForward + " 02:30: standup")
	if !ok {
		t.Fatal("ok = false, want the prefix parsed")
	}
	if prefix != springForward+" 02:30" {
		t.Errorf("prefix = %q, want the timestamp token", prefix)
	}
	if rest != "standup" {
		t.Errorf("rest = %q, want %q", rest, "standup")
	}
	if exists {
		t.Errorf("exists = true, want false: 02:30 does not exist on " + springForward)
	}
	if got.Hour() != 1 {
		t.Errorf("got %v, want the shifted 01:30", got)
	}
}
