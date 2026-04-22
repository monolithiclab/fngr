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
	for _, input := range []string{"09:30", "9:30AM", "2:15PM"} {
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
