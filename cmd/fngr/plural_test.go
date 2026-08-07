package main

import "testing"

func TestPlural(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int64
		noun string
		want string
	}{
		{1, "occurrence", "1 occurrence"},
		{2, "occurrence", "2 occurrences"},
		// Zero takes the plural, as English does: "0 events", not "0 event".
		{0, "event", "0 events"},
		// Nothing counts down, but a negative must not read as singular.
		{-1, "event", "-1 events"},
		{10000, "record", "10000 records"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := plural(tt.n, tt.noun); got != tt.want {
				t.Errorf("plural(%d, %q) = %q, want %q", tt.n, tt.noun, got, tt.want)
			}
		})
	}
}
