package timefmt

import (
	"strings"
	"testing"
	"time"
)

func TestParse_FullFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  time.Time
	}{
		{"2026-04-15", time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)},
		{"2026-04-15T14:30", time.Date(2026, 4, 15, 14, 30, 0, 0, time.Local)},
		{"2026-04-15 14:30", time.Date(2026, 4, 15, 14, 30, 0, 0, time.Local)},
		{"2026-04-15T14:30:45", time.Date(2026, 4, 15, 14, 30, 45, 0, time.Local)},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("Parse(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParse_TimeOnlyFillsToday(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"21:30", "9.30PM", "9:30PM", "9:30 PM", "9.30pm", "9:30pm", "9:30 pm"} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", input, err)
			}
			now := time.Now()
			if got.Year() != now.Year() || got.Month() != now.Month() || got.Day() != now.Day() {
				t.Errorf("Parse(%q) = %v, expected today's date", input, got)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "not a time", "2026-13-40"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) expected error", input)
		}
	}
}

func TestParseDate(t *testing.T) {
	t.Parallel()
	got, err := ParseDate("2026-04-15")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("ParseDate = %v, want %v", got, want)
	}
}

func TestParseDate_RejectsNonDate(t *testing.T) {
	t.Parallel()
	if _, err := ParseDate("not-a-date"); err == nil {
		t.Error("expected error")
	}
}

func TestFormatRelative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 18, 14, 30, 0, 0, time.Local)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"same instant", now, "2.30pm"},
		{"earlier today", time.Date(2026, 4, 18, 9, 32, 0, 0, time.Local), "9.32am"},
		{"later today", time.Date(2026, 4, 18, 21, 30, 0, 0, time.Local), "9.30pm"},
		{"yesterday this year", time.Date(2026, 4, 17, 23, 59, 0, 0, time.Local), "Apr 17 11.59pm"},
		{"earlier this year", time.Date(2026, 1, 5, 8, 5, 0, 0, time.Local), "Jan 05 8.05am"},
		{"prior year", time.Date(2024, 12, 9, 21, 32, 0, 0, time.Local), "Dec 09 2024 9.32pm"},
		{"midnight today", time.Date(2026, 4, 18, 0, 0, 0, 0, time.Local), "12.00am"},
		{"midnight yesterday", time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local), "Apr 17 12.00am"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatRelative(tt.t, now); got != tt.want {
				t.Errorf("FormatRelative(%v, %v) = %q, want %q", tt.t, now, got, tt.want)
			}
		})
	}
}

func TestParsePartial(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name        string
		input       string
		wantHasDate bool
		wantHasTime bool
		check       func(t *testing.T, got time.Time)
	}{
		{
			name:        "date only",
			input:       "2026-04-15",
			wantHasDate: true,
			wantHasTime: false,
			check: func(t *testing.T, got time.Time) {
				want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:        "datetime",
			input:       "2026-04-15T14:30",
			wantHasDate: true,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				want := time.Date(2026, 4, 15, 14, 30, 0, 0, time.Local)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:        "RFC3339",
			input:       now.UTC().Format(time.RFC3339),
			wantHasDate: true,
			wantHasTime: true,
			check:       nil,
		},
		{
			name:        "24h time only",
			input:       "09:30",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 30 {
					t.Errorf("got h=%d m=%d, want 9:30", got.Hour(), got.Minute())
				}
				today := time.Now()
				if got.Year() != today.Year() || got.Month() != today.Month() || got.Day() != today.Day() {
					t.Errorf("got date %v, want today", got)
				}
			},
		},
		{
			name:        "12h pm",
			input:       "2:15PM",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 14 || got.Minute() != 15 {
					t.Errorf("got h=%d m=%d, want 14:15", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12h pm hour only lower",
			input:       "4pm",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 16 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 16:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12h am hour only upper",
			input:       "10AM",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 10 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 10:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12h pm hour only with space",
			input:       "4 pm",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 16 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 16:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12h am hour only with space",
			input:       "10 AM",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 10 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 10:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12pm noon",
			input:       "12pm",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 12 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 12:00 (noon)", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "12am midnight",
			input:       "12am",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 0 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 0:00 (midnight)", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "garbage",
			input:       "not a time",
			wantHasDate: false,
			wantHasTime: false,
			check:       nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hasDate, hasTime, err := ParsePartial(tt.input)
			if tt.input == "not a time" {
				if err == nil {
					t.Fatalf("ParsePartial(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePartial(%q) err = %v", tt.input, err)
			}
			if hasDate != tt.wantHasDate || hasTime != tt.wantHasTime {
				t.Errorf("ParsePartial(%q) hasDate=%v hasTime=%v, want %v/%v",
					tt.input, hasDate, hasTime, tt.wantHasDate, tt.wantHasTime)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestParseRelative(t *testing.T) {
	t.Parallel()
	// Fixed anchor: Wed 2026-06-10 14:30:45.
	now := time.Date(2026, 6, 10, 14, 30, 45, 0, time.Local)

	tests := []struct {
		name        string
		input       string
		wantOK      bool
		wantHasDate bool
		wantHasTime bool
		want        time.Time // only checked when wantOK
	}{
		{"now", "now", true, true, true, now},
		{"today keeps time of day", "today", true, true, false, now},
		{"yesterday keeps time of day", "yesterday", true, true, false,
			time.Date(2026, 6, 9, 14, 30, 45, 0, time.Local)},
		{"2 days ago", "2 days ago", true, true, false,
			time.Date(2026, 6, 8, 14, 30, 45, 0, time.Local)},
		{"1 day ago singular", "1 day ago", true, true, false,
			time.Date(2026, 6, 9, 14, 30, 45, 0, time.Local)},
		{"a week ago", "a week ago", true, true, false,
			time.Date(2026, 6, 3, 14, 30, 45, 0, time.Local)},
		{"2 weeks ago", "2 weeks ago", true, true, false,
			time.Date(2026, 5, 27, 14, 30, 45, 0, time.Local)},
		{"1 month ago", "1 month ago", true, true, false,
			time.Date(2026, 5, 10, 14, 30, 45, 0, time.Local)},
		{"3 hours ago", "3 hours ago", true, true, true,
			time.Date(2026, 6, 10, 11, 30, 45, 0, time.Local)},
		{"an hour ago", "an hour ago", true, true, true,
			time.Date(2026, 6, 10, 13, 30, 45, 0, time.Local)},
		{"15 minutes ago", "15 minutes ago", true, true, true,
			time.Date(2026, 6, 10, 14, 15, 45, 0, time.Local)},
		{"yesterday at 9am", "yesterday at 9am", true, true, true,
			time.Date(2026, 6, 9, 9, 0, 0, 0, time.Local)},
		{"today at 3:30pm", "today at 3:30pm", true, true, true,
			time.Date(2026, 6, 10, 15, 30, 0, 0, time.Local)},
		{"2 days ago at 18:00", "2 days ago at 18:00", true, true, true,
			time.Date(2026, 6, 8, 18, 0, 0, 0, time.Local)},
		{"case and spacing insensitive", "  Yesterday  AT  9AM ", true, true, true,
			time.Date(2026, 6, 9, 9, 0, 0, 0, time.Local)},
		// Rejected: not relative, or nonsensical combinations.
		{"empty", "", false, false, false, time.Time{}},
		{"plain text", "not a time", false, false, false, time.Time{}},
		{"sub-day with at", "3 hours ago at 9am", false, false, false, time.Time{}},
		{"unknown unit", "2 fortnights ago", false, false, false, time.Time{}},
		{"negative count", "-2 days ago", false, false, false, time.Time{}},
		{"day with bad time", "yesterday at noon", false, false, false, time.Time{}},
		{"absolute date untouched", "2026-04-15", false, false, false, time.Time{}},
		{"count above maxRelCount", "1000001 days ago", false, false, false, time.Time{}},
		// Used to wrap int64 in `time.Duration(n) * time.Hour` and resolve one
		// hour into the *future* from an "ago" expression.
		{"int64 overflow", "9223372036854775807 hours ago", false, false, false, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hasDate, hasTime, ok := parseRelative(tt.input, now)
			if ok != tt.wantOK {
				t.Fatalf("parseRelative(%q) ok=%v, want %v", tt.input, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if hasDate != tt.wantHasDate || hasTime != tt.wantHasTime {
				t.Errorf("parseRelative(%q) hasDate=%v hasTime=%v, want %v/%v",
					tt.input, hasDate, hasTime, tt.wantHasDate, tt.wantHasTime)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseRelative(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParsePartial_Relative(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name        string
		input       string
		wantHasDate bool
		wantHasTime bool
		check       func(t *testing.T, got time.Time)
	}{
		{
			name:        "yesterday is date-only at current time",
			input:       "yesterday",
			wantHasDate: true,
			wantHasTime: false,
			check: func(t *testing.T, got time.Time) {
				y := now.AddDate(0, 0, -1)
				if got.Year() != y.Year() || got.Month() != y.Month() || got.Day() != y.Day() {
					t.Errorf("date = %v, want yesterday %v", got, y)
				}
				if got.Hour() != now.Hour() {
					t.Errorf("hour = %d, want now's hour %d", got.Hour(), now.Hour())
				}
			},
		},
		{
			name:        "yesterday at 9am has time",
			input:       "yesterday at 9am",
			wantHasDate: true,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 0 {
					t.Errorf("got %02d:%02d, want 09:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "3 hours ago is date+time",
			input:       "3 hours ago",
			wantHasDate: true,
			wantHasTime: true,
			check:       nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hasDate, hasTime, err := ParsePartial(tt.input)
			if err != nil {
				t.Fatalf("ParsePartial(%q): %v", tt.input, err)
			}
			if hasDate != tt.wantHasDate || hasTime != tt.wantHasTime {
				t.Errorf("ParsePartial(%q) hasDate=%v hasTime=%v, want %v/%v",
					tt.input, hasDate, hasTime, tt.wantHasDate, tt.wantHasTime)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestSplitTimePrefix(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name     string
		input    string
		wantOK   bool
		wantRest string
		check    func(t *testing.T, got time.Time)
	}{
		{
			name:     "time only prefix uses today",
			input:    "9:30: had coffee",
			wantOK:   true,
			wantRest: "had coffee",
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 30 {
					t.Errorf("got h=%d m=%d, want 9:30", got.Hour(), got.Minute())
				}
				if got.Year() != now.Year() || got.Month() != now.Month() || got.Day() != now.Day() {
					t.Errorf("got date %v, want today", got)
				}
			},
		},
		{
			name:     "12h prefix",
			input:    "3pm: lunch",
			wantOK:   true,
			wantRest: "lunch",
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 15 || got.Minute() != 0 {
					t.Errorf("got h=%d m=%d, want 15:00", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:     "date prefix",
			input:    "2026-04-15: trip",
			wantOK:   true,
			wantRest: "trip",
			check: func(t *testing.T, got time.Time) {
				want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:     "hh:mm:ss prefix",
			input:    "15:04:05: deploy",
			wantOK:   true,
			wantRest: "deploy",
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 15 || got.Minute() != 4 || got.Second() != 5 {
					t.Errorf("got %v, want 15:04:05", got)
				}
			},
		},
		{
			name:     "only first delimiter splits",
			input:    "9:30: Meeting: discuss",
			wantOK:   true,
			wantRest: "Meeting: discuss",
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 30 {
					t.Errorf("got h=%d m=%d, want 9:30", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:     "non-time prefix unchanged",
			input:    "Meeting: discuss roadmap",
			wantOK:   false,
			wantRest: "Meeting: discuss roadmap",
		},
		{
			name:     "no delimiter unchanged",
			input:    "just a plain title",
			wantOK:   false,
			wantRest: "just a plain title",
		},
		{
			name:     "colon without space unchanged",
			input:    "9:30:coffee",
			wantOK:   false,
			wantRest: "9:30:coffee",
		},
		{
			name:     "empty remainder",
			input:    "9:30: ",
			wantOK:   true,
			wantRest: "",
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 30 {
					t.Errorf("got h=%d m=%d, want 9:30", got.Hour(), got.Minute())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, rest, ok := SplitTimePrefix(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("SplitTimePrefix(%q) ok=%v, want %v", tt.input, ok, tt.wantOK)
			}
			if rest != tt.wantRest {
				t.Errorf("SplitTimePrefix(%q) rest=%q, want %q", tt.input, rest, tt.wantRest)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestSpliceTime(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("PT", -7*3600)
	orig := time.Date(2026, 4, 22, 9, 0, 0, 0, loc)
	newTime := time.Date(2030, 12, 1, 21, 32, 7, 123, time.UTC)

	got := SpliceTime(orig, newTime)

	if got.Year() != 2026 || got.Month() != 4 || got.Day() != 22 {
		t.Errorf("date not preserved: %v", got)
	}
	if got.Hour() != 21 || got.Minute() != 32 || got.Second() != 7 || got.Nanosecond() != 123 {
		t.Errorf("time not spliced: %v", got)
	}
	if got.Location() != loc {
		t.Errorf("location not preserved: %v", got.Location())
	}
}

func TestSpliceDate(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("PT", -7*3600)
	orig := time.Date(2026, 4, 22, 21, 32, 7, 123, loc)
	newDate := time.Date(2030, 12, 1, 9, 0, 0, 0, time.UTC)

	got := SpliceDate(orig, newDate)

	if got.Year() != 2030 || got.Month() != 12 || got.Day() != 1 {
		t.Errorf("date not spliced: %v", got)
	}
	if got.Hour() != 21 || got.Minute() != 32 || got.Second() != 7 || got.Nanosecond() != 123 {
		t.Errorf("time not preserved: %v", got)
	}
	if got.Location() != loc {
		t.Errorf("location not preserved: %v", got.Location())
	}
}

// TestAddMonths_ClampsToMonthEnd covers the Feb-31 rollover: time.AddDate
// normalizes overflow, so subtracting a month from the 31st used to land back
// in the *current* month (2026-03-31 -> 2026-03-03).
func TestAddMonths_ClampsToMonthEnd(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		from   time.Time
		months int
		want   time.Time
	}{
		{"Mar 31 minus 1 month", time.Date(2026, 3, 31, 9, 15, 30, 0, time.Local), -1,
			time.Date(2026, 2, 28, 9, 15, 30, 0, time.Local)},
		{"Mar 31 minus 1 month in a leap year", time.Date(2024, 3, 31, 9, 15, 30, 0, time.Local), -1,
			time.Date(2024, 2, 29, 9, 15, 30, 0, time.Local)},
		{"May 31 minus 3 months", time.Date(2026, 5, 31, 0, 0, 0, 0, time.Local), -3,
			time.Date(2026, 2, 28, 0, 0, 0, 0, time.Local)},
		{"Jul 31 minus 1 month lands on a 30-day month", time.Date(2026, 7, 31, 0, 0, 0, 0, time.Local), -1,
			time.Date(2026, 6, 30, 0, 0, 0, 0, time.Local)},
		{"Jan 31 minus 1 month crosses the year", time.Date(2026, 1, 31, 0, 0, 0, 0, time.Local), -1,
			time.Date(2025, 12, 31, 0, 0, 0, 0, time.Local)},
		{"day that needs no clamp is untouched", time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local), -1,
			time.Date(2026, 2, 15, 12, 0, 0, 0, time.Local)},
		{"12 months back is the same date a year earlier", time.Date(2026, 6, 10, 8, 0, 0, 0, time.Local), -12,
			time.Date(2025, 6, 10, 8, 0, 0, 0, time.Local)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := addMonths(tt.from, tt.months); !got.Equal(tt.want) {
				t.Errorf("addMonths(%v, %d) = %v, want %v", tt.from, tt.months, got, tt.want)
			}
		})
	}
}

// TestParsePartial_RejectsOutOfRangeYear guards the storage format: a year
// outside 1-9999 formats to a string SQLite stores but the driver cannot read
// back, which used to poison every read of the table.
func TestParsePartial_RejectsOutOfRangeYear(t *testing.T) {
	t.Parallel()
	// Within maxRelCount, so the count bound does not catch these — only the
	// year check does.
	inputs := []string{
		"999999 days ago",
		"24313 months ago", // the smallest month count that goes negative
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, _, _, err := ParsePartial(in)
			if err == nil {
				t.Fatalf("ParsePartial(%q) = %v, want an out-of-range error", in, got)
			}
			if !strings.Contains(err.Error(), "outside the supported range") {
				t.Errorf("ParsePartial(%q) err = %v, want an out-of-range message", in, err)
			}
		})
	}
}

// TestSplitTimePrefix_OutOfRangeIsNotATimestamp is the untrusted-content path:
// piped text whose first ": "-delimited token is an absurd relative time must
// be left alone as a title, not parsed into an unstorable timestamp.
func TestSplitTimePrefix_OutOfRangeIsNotATimestamp(t *testing.T) {
	t.Parallel()
	const in = "2147483647 months ago: meeting notes from a scraped page"
	got, rest, ok := SplitTimePrefix(in)
	if ok {
		t.Fatalf("SplitTimePrefix(%q) parsed a prefix (%v), want it left verbatim", in, got)
	}
	if rest != in {
		t.Errorf("SplitTimePrefix(%q) rest = %q, want the input unchanged", in, rest)
	}
}

// TestStorageRoundTrip pins the encoding shared by the write path and the
// date-range bounds: UTC, fixed width, and readable back by ParseStorage.
func TestStorageRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"utc", time.Date(2026, 7, 27, 14, 7, 26, 0, time.UTC), "2026-07-27 14:07:26"},
		{"converted to utc", time.Date(2026, 7, 27, 14, 7, 26, 0, time.FixedZone("x", 2*60*60)),
			"2026-07-27 12:07:26"},
		{"sub-second truncated", time.Date(2026, 7, 27, 14, 7, 26, 999_000_000, time.UTC),
			"2026-07-27 14:07:26"},
		{"first representable year", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "0001-01-01 00:00:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatStorage(tt.in)
			if got != tt.want {
				t.Fatalf("FormatStorage(%v) = %q, want %q", tt.in, got, tt.want)
			}
			back, ok := ParseStorage(got)
			if !ok {
				t.Fatalf("ParseStorage(%q) failed", got)
			}
			if want := tt.in.UTC().Truncate(time.Second); !back.Equal(want) {
				t.Errorf("round trip = %v, want %v", back, want)
			}
		})
	}
}

// TestParseStorage_Rejects covers what a pre-guard build could leave behind.
func TestParseStorage_Rejects(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "-0712-08-29 14:07:26", "2026-07-27T14:07:26Z", "2026-07-27"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			if got, ok := ParseStorage(in); ok {
				t.Errorf("ParseStorage(%q) = %v, true; want ok=false", in, got)
			}
		})
	}
}

// TestInRange pins the bounds the storage layer re-checks.
func TestInRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"typical", time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), true},
		{"first representable year", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"last representable year", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"year zero", time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"negative year", time.Date(-712, 8, 29, 0, 0, 0, 0, time.UTC), false},
		{"five digit year", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := InRange(tt.t); got != tt.want {
				t.Errorf("InRange(%v) = %v, want %v", tt.t, got, tt.want)
			}
		})
	}
}
