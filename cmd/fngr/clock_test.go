package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // embedded, so LoadLocation works with no system zoneinfo

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

// springForward is the 2026 US spring-forward date: 02:00 EST jumps to 03:00
// EDT in America/New_York, so no clock in [02:00, 03:00) exists that day.
const springForward = "2026-03-08"

// useNewYork points time.Local at a zone that actually has DST, since a skipped
// clock cannot be produced any other way. It mutates package-global state, so
// callers must NOT use t.Parallel — Go resumes parallel tests only after the
// sequential ones finish, which keeps the swap invisible to them.
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

func TestWarnSkippedClock(t *testing.T) {
	t.Parallel()
	stored := time.Date(2026, 3, 8, 1, 30, 0, 0, time.UTC)

	t.Run("existing clock says nothing", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		warnSkippedClock(&buf, true, "2:30", stored)
		if buf.Len() != 0 {
			t.Errorf("wrote %q, want nothing", buf.String())
		}
	})

	t.Run("skipped clock names both times", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		warnSkippedClock(&buf, false, "2:30", stored)
		got := buf.String()
		for _, want := range []string{"warning:", `"2:30"`, "daylight-saving", "2026-03-08 01:30:00"} {
			if !strings.Contains(got, want) {
				t.Errorf("warning = %q, want it to contain %q", got, want)
			}
		}
	})
}

// TestEventClockVerbs_WarnOnSkippedClock covers M13 at the two verbs that
// assemble a timestamp out of an existing one: each used to store a time an
// hour off what was asked for and print "Updated event N" as if it had worked.
// Both input shapes are exercised — a bare clock spliced into the stored date,
// and a full timestamp that replaces it — because they learn the answer from
// different places (SpliceTime/SpliceDate vs timefmt.ParsePartial).
func TestEventClockVerbs_WarnOnSkippedClock(t *testing.T) {
	loc := useNewYork(t)

	tests := []struct {
		name  string
		seed  time.Time
		argv  []string
		warns bool
	}{
		{
			name:  "time splices into the spring-forward day",
			seed:  time.Date(2026, 3, 8, 12, 0, 0, 0, loc),
			argv:  []string{"event", "time", "1", "2:30"},
			warns: true,
		},
		{
			name:  "time takes a full timestamp in the gap",
			seed:  time.Date(2026, 6, 1, 12, 0, 0, 0, loc),
			argv:  []string{"event", "time", "1", springForward + " 02:30"},
			warns: true,
		},
		{
			name:  "date takes a full timestamp in the gap",
			seed:  time.Date(2026, 6, 1, 12, 0, 0, 0, loc),
			argv:  []string{"event", "date", "1", springForward + " 02:30"},
			warns: true,
		},
		{
			name:  "date moves an existing clock into the gap",
			seed:  time.Date(2026, 6, 1, 2, 30, 0, 0, loc),
			argv:  []string{"event", "date", "1", springForward},
			warns: true,
		},
		{
			name:  "time outside the gap is silent",
			seed:  time.Date(2026, 3, 8, 12, 0, 0, 0, loc),
			argv:  []string{"event", "time", "1", "9:30"},
			warns: false,
		},
		{
			name:  "date that keeps a valid clock is silent",
			seed:  time.Date(2026, 6, 1, 9, 30, 0, 0, loc),
			argv:  []string{"event", "date", "1", springForward},
			warns: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, errBuf := newDispatcherErr(t, "", true)
			if _, err := run([]string{"add", "--time", tt.seed.Format("2006-01-02 15:04"), "seeded"}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			errBuf.Reset() // the seed itself may warn; only the verb is under test

			out, err := run(tt.argv)
			if err != nil {
				t.Fatalf("%v: %v", tt.argv, err)
			}
			// The update still lands — the warning is the whole remedy.
			if !strings.Contains(out, "Updated event 1") {
				t.Errorf("stdout = %q, want the update confirmed", out)
			}
			if got := strings.Contains(errBuf.String(), "daylight-saving"); got != tt.warns {
				t.Errorf("warned = %v, want %v (stderr: %q)", got, tt.warns, errBuf.String())
			}
		})
	}
}

// TestAddCmd_WarnsOnSkippedClock is the same guard on the other door: both ways
// of stamping a new event resolve a clock the user typed.
func TestAddCmd_WarnsOnSkippedClock(t *testing.T) {
	useNewYork(t)

	tests := []struct {
		name  string
		argv  []string
		warns bool
	}{
		{"--time in the gap", []string{"add", "--time", springForward + " 02:30", "standup"}, true},
		{"title prefix in the gap", []string{"add", springForward + " 02:30: standup"}, true},
		{"--time outside the gap", []string{"add", "--time", springForward + " 09:30", "standup"}, false},
		{"title prefix outside the gap", []string{"add", springForward + " 09:30: standup"}, false},
		{"no timestamp at all", []string{"add", "standup"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, errBuf := newDispatcherErr(t, "", true)

			out, err := run(tt.argv)
			if err != nil {
				t.Fatalf("%v: %v", tt.argv, err)
			}
			if !strings.Contains(out, "Added event 1") {
				t.Errorf("stdout = %q, want the event added", out)
			}
			if got := strings.Contains(errBuf.String(), "daylight-saving"); got != tt.warns {
				t.Errorf("warned = %v, want %v (stderr: %q)", got, tt.warns, errBuf.String())
			}
		})
	}
}

// TestAddJSON_WarnsOnSkippedClock covers the import path, which resolves
// timestamps from two places of its own — the shared --time default and each
// record's created_at — and used to store a shifted clock from either without
// saying so, the one door `fngr add` closed and `fngr add --format=json` did
// not.
func TestAddJSON_WarnsOnSkippedClock(t *testing.T) {
	useNewYork(t)

	// The batch cap is 10 000 records, so only the first offender is named and
	// the rest are counted; wantMore is that tail line.
	tests := []struct {
		name     string
		argv     []string
		warns    bool
		wantMore string
	}{
		{
			name:  "record created_at in the gap",
			argv:  []string{"add", "--format=json", `{"title":"standup","created_at":"` + springForward + ` 02:30"}`},
			warns: true,
		},
		{
			name:  "--time default in the gap",
			argv:  []string{"add", "--format=json", "--time", springForward + " 02:30", `{"title":"standup"}`},
			warns: true,
		},
		{
			name:  "record created_at outside the gap",
			argv:  []string{"add", "--format=json", `{"title":"standup","created_at":"` + springForward + ` 09:30"}`},
			warns: false,
		},
		{
			name:  "no timestamp at all",
			argv:  []string{"add", "--format=json", `{"title":"standup"}`},
			warns: false,
		},
		{
			name: "a batch names the first and counts the rest",
			argv: []string{"add", "--format=json", `[
				{"title":"a","created_at":"` + springForward + ` 02:00"},
				{"title":"b","created_at":"` + springForward + ` 02:15"},
				{"title":"c","created_at":"` + springForward + ` 09:00"},
				{"title":"d","created_at":"` + springForward + ` 02:45"}
			]`},
			warns:    true,
			wantMore: "plus 2 records in this batch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, errBuf := newDispatcherErr(t, "", true)

			out, err := run(tt.argv)
			if err != nil {
				t.Fatalf("%v: %v", tt.argv, err)
			}
			if !strings.Contains(out, "Imported") {
				t.Errorf("stdout = %q, want the import confirmed", out)
			}
			got := errBuf.String()
			if warned := strings.Contains(got, "daylight-saving"); warned != tt.warns {
				t.Errorf("warned = %v, want %v (stderr: %q)", warned, tt.warns, got)
			}
			// Exactly one record is ever quoted, however many are skipped.
			if n := strings.Count(got, "daylight-saving"); n > 1 {
				t.Errorf("stderr named %d records, want at most 1: %q", n, got)
			}
			if tt.wantMore != "" && !strings.Contains(got, tt.wantMore) {
				t.Errorf("stderr = %q, want it to contain %q", got, tt.wantMore)
			}
			if tt.wantMore == "" && strings.Contains(got, "in this batch") {
				t.Errorf("stderr = %q, want no tail count", got)
			}
		})
	}
}

// TestEventTimeCmd_StoresTheShiftedClock pins what the warning is warning
// about: fngr stores the resolved instant rather than refusing, so the entry
// exists and the user can correct it.
func TestEventTimeCmd_StoresTheShiftedClock(t *testing.T) {
	loc := useNewYork(t)
	s := newTestStore(t)
	io, _, errBuf := newTestIOFull("", true)

	at := time.Date(2026, 3, 8, 12, 0, 0, 0, loc)
	if _, err := s.Add(context.Background(), event.AddInput{Title: "seeded", CreatedAt: &at, Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &EventTimeCmd{ID: 1, Value: "2:30"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := ev.CreatedAt.In(loc); got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("stored %v, want the 01:30 EST the zone resolved to", got)
	}
	if !strings.Contains(errBuf.String(), "01:30:00") {
		t.Errorf("stderr = %q, want it to name the stored time", errBuf.String())
	}
}
