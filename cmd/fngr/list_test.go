package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

func TestListCmd_DefaultTree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "deploy #ops", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "tree"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "deploy #ops") {
		t.Errorf("output = %q, want contains event text", got)
	}
}

// TestListCmd_FilterSyntaxError covers both output paths — tree buffers via
// List, everything else streams via ListSeq — since each returns the error
// from a different place.
func TestListCmd_FilterSyntaxError(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"tree", "flat"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, _ := newTestIO("")

			cmd := &ListCmd{Format: format, Search: "#ops &"}
			err := cmd.Run(s, io)
			if err == nil {
				t.Fatal("expected an error for a dangling operator, got nil")
			}
			if !errors.Is(err, event.ErrFilter) {
				t.Errorf("err = %v, want it to wrap event.ErrFilter", err)
			}
			if !strings.Contains(err.Error(), "--help") {
				t.Errorf("err = %q, want it to point at --help", err)
			}
		})
	}
}

// TestListCmd_QueryErrorKeepsItsOwnMessage is the other half of the syntax-error
// test: a genuine query failure must reach the user unchanged. The predecessor
// of withGrammarHint matched SQLite's message text, so real database errors got
// dressed up as the user's typo and sent them to read the -S grammar.
func TestListCmd_QueryErrorKeepsItsOwnMessage(t *testing.T) {
	t.Parallel()
	database, err := db.Open(filepath.Join(t.TempDir(), "fngr.db"), true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s := event.NewStore(database)
	io, _ := newTestIO("")

	cmd := &ListCmd{Format: "tree", Search: "#ops"}
	err = cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected an error from the closed database, got nil")
	}
	if errors.Is(err, event.ErrFilter) {
		t.Errorf("err = %v, want it not to be reported as a filter error", err)
	}
	if strings.Contains(err.Error(), "--help") {
		t.Errorf("err = %q, want no -S grammar hint", err)
	}
}

func TestWithGrammarHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		in       error
		wantHint bool
	}{
		{"nil passes through", nil, false},
		{"unrelated error passes through", fmt.Errorf("disk full"), false},
		{"filter error gets the hint", fmt.Errorf("%w: nope", event.ErrFilter), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := withGrammarHint(tc.in)
			if tc.in == nil {
				if out != nil {
					t.Errorf("nil in, got %v", out)
				}
				return
			}
			if got := strings.Contains(out.Error(), "--help"); got != tc.wantHint {
				t.Errorf("hint=%v, want %v (out=%q)", got, tc.wantHint, out)
			}
		})
	}
}

func TestListCmd_TreeEmptyReportsNoEvents(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, errBuf := newTestIOFull("", false)

	cmd := &ListCmd{Format: "tree"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "No events found") {
		t.Errorf("stderr = %q, want 'No events found'", got)
	}
}

func TestListCmd_JSON(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "json me", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out.String())
	}
	if len(parsed) != 1 || parsed[0]["title"] != "json me" {
		t.Errorf("parsed = %v", parsed)
	}
}

func TestListCmd_LimitAndDefaultSort(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for _, text := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Add(context.Background(), event.AddInput{Title: text, Meta: []parse.Meta{
			{Key: "author", Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %s: %v", text, err)
		}
	}

	cmd := &ListCmd{Format: "flat", Limit: 1}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("limit=1 produced %d lines:\n%s", strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, "gamma") {
		t.Errorf("default sort: expected gamma first, got:\n%s", got)
	}
}

func TestListCmd_Reverse(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for _, text := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Add(context.Background(), event.AddInput{Title: text, Meta: []parse.Meta{
			{Key: "author", Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %s: %v", text, err)
		}
	}

	cmd := &ListCmd{Format: "flat", Reverse: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.HasPrefix(got, "1 ") || !strings.Contains(got, "alpha") {
		t.Errorf("reverse sort: expected alpha (id 1) first, got:\n%s", got)
	}
}

// TestListCmd_ReverseKeepsTheNewestUnderALimit pins the interaction the two
// flags used to get wrong together: -r is a display-order toggle, so `fngr -n 2
// -r` means the newest two shown oldest-first, not the two oldest events in the
// database.
func TestListCmd_ReverseKeepsTheNewestUnderALimit(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.Local)
	for i, text := range []string{"alpha", "beta", "gamma"} {
		at := base.AddDate(0, 0, i)
		if _, err := s.Add(context.Background(), event.AddInput{Title: text, CreatedAt: &at, Meta: []parse.Meta{
			{Key: "author", Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %s: %v", text, err)
		}
	}

	cmd := &ListCmd{Format: "flat", Limit: 2, Reverse: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "alpha") {
		t.Errorf("limit took the oldest events, not the newest:\n%s", got)
	}
	if i, j := strings.Index(got, "beta"), strings.Index(got, "gamma"); i < 0 || j < 0 || i > j {
		t.Errorf("want beta before gamma (oldest-first display), got:\n%s", got)
	}
}

func TestListCmd_InvalidFromDate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &ListCmd{From: "not-a-date"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--from") {
		t.Errorf("error = %v, want --from parse error", err)
	}
}

func TestListCmd_InvalidToDate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &ListCmd{To: "nope"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("error = %v, want --to parse error", err)
	}
}

func TestListCmd_FilterAndDateRange(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "match", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add(context.Background(), event.AddInput{Title: "skip", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// A range spanning today, so both events fall inside it and the filter is
	// what does the selecting. --to is inclusive of its whole day.
	today := time.Now().Format(timefmt.DateFormat)
	cmd := &ListCmd{Format: "flat", Search: "#ops", From: today, To: today}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "match") || strings.Contains(got, "skip") {
		t.Errorf("output = %q, want only 'match'", got)
	}
}

func TestListCmd_JSONUsesStreamingPath(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for i := range 3 {
		if _, err := s.Add(context.Background(), event.AddInput{Title: fmt.Sprintf("e%d", i), Meta: []parse.Meta{
			{Key: "author", Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	cmd := &ListCmd{Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, out.String())
	}
	if len(parsed) != 3 {
		t.Errorf("got %d entries, want 3; output:\n%s", len(parsed), out.String())
	}
}

func TestListCmd_NoPagerStillRendersToBuffer(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "evt", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "flat", NoPager: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "evt") {
		t.Errorf("expected 'evt' in output, got %q", out.String())
	}
}

// TestListCmd_ReportsAFailedFlush is the other half of M15: Out is buffered
// now, so the tail of a listing is written when Run returns and nowhere else.
// Swallowing that error would exit 0 over output the user never received.
func TestListCmd_ReportsAFailedFlush(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	if _, err := s.Add(context.Background(), event.AddInput{Title: "evt", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	wantErr := errors.New("disk full")
	streams := ioStreams{In: strings.NewReader(""), Out: errWriter{err: wantErr}, Err: io.Discard}

	cmd := &ListCmd{Format: "flat", NoPager: true}
	if err := cmd.Run(s, streams); !errors.Is(err, wantErr) {
		t.Errorf("Run err = %v, want %v", err, wantErr)
	}
}

// TestListCmd_FromBound pins M12 for --from: the flag used to go through a
// date-only parser, so it rejected relative forms *and* the RFC 3339 stamp
// fngr's own --format=json emits — a created_at value could not be pasted back.
func TestListCmd_FromBound(t *testing.T) {
	t.Parallel()
	yesterday := time.Now().AddDate(0, 0, -1)
	y, m, d := yesterday.Date()

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"bare date floors to midnight", "2026-04-22", time.Date(2026, 4, 22, 0, 0, 0, 0, time.Local)},
		{"clock is taken verbatim", "2026-04-22 14:30", time.Date(2026, 4, 22, 14, 30, 0, 0, time.Local)},
		{"fngr's own RFC 3339 output", "2026-04-22T14:30:00Z", time.Date(2026, 4, 22, 14, 30, 0, 0, time.UTC)},
		{"relative day floors to midnight", "yesterday", time.Date(y, m, d, 0, 0, 0, 0, time.Local)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts, err := (&ListCmd{From: tt.in}).toListOpts()
			if err != nil {
				t.Fatalf("--from %q: %v", tt.in, err)
			}
			if opts.From == nil || !opts.From.Equal(tt.want) {
				t.Errorf("--from %q = %v, want %v", tt.in, opts.From, tt.want)
			}
		})
	}
}

// TestListCmd_ToBound is the --from mirror, plus the inclusivity the help text
// promises: the store compares created_at < To, so each bound is the first
// instant past what was named.
func TestListCmd_ToBound(t *testing.T) {
	t.Parallel()
	y, m, d := time.Now().Date()

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"bare date ends at the next midnight", "2026-04-22", time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)},
		{"clock includes its own second", "2026-04-22 14:30", time.Date(2026, 4, 22, 14, 30, 1, 0, time.Local)},
		{"fngr's own RFC 3339 output", "2026-04-22T14:30:00Z", time.Date(2026, 4, 22, 14, 30, 1, 0, time.UTC)},
		{"relative day ends at the next midnight", "today", time.Date(y, m, d+1, 0, 0, 0, 0, time.Local)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts, err := (&ListCmd{To: tt.in}).toListOpts()
			if err != nil {
				t.Fatalf("--to %q: %v", tt.in, err)
			}
			if opts.To == nil || !opts.To.Equal(tt.want) {
				t.Errorf("--to %q = %v, want %v", tt.in, opts.To, tt.want)
			}
		})
	}
}

// TestListCmd_WarnsOnEmptyRange covers the other half of M12: an inverted or
// empty range returns nothing at exit 0, which reads exactly like a journal
// with no events in the window.
func TestListCmd_WarnsOnEmptyRange(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		from, to string
		wantWarn bool
	}{
		{"inverted", "2026-04-22", "2026-01-01", true},
		{"same clock", "2026-04-22 14:30", "2026-04-22 14:30", false},
		{"same day", "2026-04-22", "2026-04-22", false},
		{"ordered", "2026-01-01", "2026-04-22", false},
		{"only from", "2026-04-22", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, _, errBuf := newTestIOFull("", false)

			cmd := &ListCmd{Format: "flat", From: tt.from, To: tt.to, NoPager: true}
			if err := cmd.Run(s, io); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := strings.Contains(errBuf.String(), "empty range"); got != tt.wantWarn {
				t.Errorf("warned = %v, want %v (stderr: %q)", got, tt.wantWarn, errBuf.String())
			}
		})
	}
}

// TestListCmd_ToIsInclusiveOfItsSecond is the store-level consequence of the
// --to arithmetic: an event stamped exactly at the bound must come back.
func TestListCmd_ToIsInclusiveOfItsSecond(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	at := time.Date(2026, 4, 22, 14, 30, 0, 0, time.Local)
	if _, err := s.Add(context.Background(), event.AddInput{Title: "on the bound", CreatedAt: &at, Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "flat", From: "2026-04-22 14:30", To: "2026-04-22 14:30", NoPager: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "on the bound") {
		t.Errorf("event stamped at the bound was excluded: %q", out.String())
	}
}
