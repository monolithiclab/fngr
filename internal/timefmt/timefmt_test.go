package timefmt

import (
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
