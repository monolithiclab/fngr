package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/render"
)

func TestMetaListCmd_Empty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, errBuf := newTestIOFull("", true)

	cmd := &MetaListCmd{}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(errBuf.String(), "No metadata") {
		t.Errorf("stderr = %q, want No metadata", errBuf.String())
	}
	// On stderr and nowhere else: the sentence is a note about the result,
	// not an entry, so a listing piped to a counter has to come back empty.
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

// TestMetaListCmd_EntryReadsTheSameFilteredOrNot pins the row layout, and pins
// it against the filter. Padding the key to the widest key made the `=` a
// column of its own, and the width of that column came from whichever rows the
// query returned: `fngr meta` printed `tag   =bugfix` beside `author=nico`,
// while `fngr meta -S tag` printed `tag=bugfix` — the same entry, two
// spellings.
func TestMetaListCmd_EntryReadsTheSameFilteredOrNot(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Two keys of different lengths, so a per-key column would be visible.
	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "author", Value: "nico"},
		{Key: "tag", Value: "bugfix"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for _, search := range []string{"", "tag"} {
		io, out := newTestIO("")
		cmd := &MetaListCmd{Search: search}
		if err := cmd.Run(s, io); err != nil {
			t.Fatalf("Run(-S %q): %v", search, err)
		}
		// The count is the other half of the row: one padded `key=value`
		// cell, then `(n)`.
		if !strings.Contains(out.String(), "tag=bugfix") || !strings.Contains(out.String(), "(1)") {
			t.Errorf("`meta -S %q` = %q, want it to contain %q with its count", search, out.String(), "tag=bugfix")
		}
	}
}

// TestMetaListCmd_ClampsWideColumns covers M9: the column width is the longest
// cell and applies to every row, so one oversized value used to pad all the
// others out to its length — 200 short rows beside a 1 MB one rendered 202 MB.
func TestMetaListCmd_ClampsWideColumns(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	huge := strings.Repeat("x", 5000)
	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: huge},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()

	// Every column is bounded, so the whole listing is: two rows of at most a
	// key and a value cell plus "=" and the count. The unclamped version wrote
	// ~10 KB for these same two rows.
	if len(got) > 2*(2*maxMetaCell+16) {
		t.Errorf("output is %d bytes for 2 rows, want it bounded by the clamp:\n%s", len(got), got)
	}
	if !strings.Contains(got, metaEllipsis) {
		t.Errorf("output = %q, want the cut value marked with %q", got, metaEllipsis)
	}
	// The short row keeps its value intact — clamping bounds the column, it
	// does not reformat everything to it.
	if !strings.Contains(got, "tag=ops") {
		t.Errorf("output = %q, want the short row unchanged", got)
	}
}

func TestClampCell(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"short", "ops", "ops"},
		{"exactly at the cap", strings.Repeat("x", maxMetaCell), strings.Repeat("x", maxMetaCell)},
		{"one past the cap", strings.Repeat("x", maxMetaCell+1), strings.Repeat("x", maxMetaCell-1) + metaEllipsis},
		{"far past the cap", strings.Repeat("x", 5000), strings.Repeat("x", maxMetaCell-1) + metaEllipsis},
		// The cut lands mid-rune if it counts bytes: 60 é are 120 bytes.
		{"multibyte cut on a rune boundary", strings.Repeat("é", maxMetaCell+1), strings.Repeat("é", maxMetaCell-1) + metaEllipsis},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := clampCell(tt.in)
			if got != tt.want {
				t.Errorf("clampCell(%d runes) = %q, want %q", utf8.RuneCountInString(tt.in), got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > maxMetaCell {
				t.Errorf("result is %d runes, want at most %d", n, maxMetaCell)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
		})
	}
}

// TestDisplayCell pins the composition clampCell alone cannot: escaping
// happens first and expands (`\x1b` is four columns for one byte), so a value
// that fits raw can still need cutting, and the raw pre-cut displayCell does
// for speed must not change what comes out.
func TestDisplayCell(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"plain", "ops", "ops"},
		{"escapes control characters", "a\x1bb", `a\x1bb`},
		{
			// 30 raw bytes are 120 escaped columns. The cut is by column, so it
			// can land inside an escape and print a partial `\x1` — accepted:
			// the alternative is a second notion of "cell" for a value that had
			// a control character in it to begin with.
			"escaping can push a fitting value past the cap",
			strings.Repeat("\x1b", maxMetaCell/2),
			strings.Repeat(`\x1b`, (maxMetaCell-1)/4) + `\x1b`[:(maxMetaCell-1)%4] + metaEllipsis,
		},
		{
			"pre-cut does not change the result",
			strings.Repeat("a", 5000),
			strings.Repeat("a", maxMetaCell-1) + metaEllipsis,
		},
		{
			"pre-cut lands on a rune boundary",
			strings.Repeat("é", 5000),
			strings.Repeat("é", maxMetaCell-1) + metaEllipsis,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := displayCell(tt.in); got != tt.want {
				t.Errorf("displayCell() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDisplayCell_PreCutMatchesTheFullWalk is the equivalence the pre-cut rests
// on: cutting the raw value to maxMetaCell+1 runes before escaping it must give
// the same answer as escaping the whole thing and clamping that.
func TestDisplayCell_PreCutMatchesTheFullWalk(t *testing.T) {
	t.Parallel()

	for _, in := range []string{
		"",
		"ops",
		strings.Repeat("x", maxMetaCell),
		strings.Repeat("x", maxMetaCell+1),
		strings.Repeat("é", 500),
		strings.Repeat("\x1b", 500),
		strings.Repeat("x", maxMetaCell-1) + "\x1b" + strings.Repeat("x", 500),
		"\x9b" + strings.Repeat("x", 500),
		"\xff" + strings.Repeat("x", 500),
		strings.Repeat("日", 500),
	} {
		if got, want := displayCell(in), clampCell(render.SanitizeLine(in)); got != want {
			t.Errorf("displayCell(%q…) = %q, want %q", in[:min(len(in), 8)], got, want)
		}
	}
}

// TestMetaListCmd_AlignsByRunesNotBytes pins the other half of M9: `%-*s` pads
// to a width in runes, so measuring the cells in bytes over-padded any that had
// a multibyte rune in it and stepped every row below it to the right.
func TestMetaListCmd_AlignsByRunesNotBytes(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	// "josé" is 4 runes in 5 bytes, "josie" 5 in 5: measured in bytes the two
	// look equally wide, and the é then buys an extra column of padding.
	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "people", Value: "josé"},
		{Key: "people", Value: "josie"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out.String())
	}
	widths := make([]int, len(lines))
	for i, line := range lines {
		before, _, ok := strings.Cut(line, "  (")
		if !ok {
			t.Fatalf("line %q has no count column", line)
		}
		widths[i] = utf8.RuneCountInString(before)
	}
	if widths[0] != widths[1] {
		t.Errorf("counts start at columns %v, want them aligned:\n%s", widths, out.String())
	}
}

func TestMetaListCmd_SearchByKey(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "tag"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "tag=ops") {
		t.Errorf("output = %q, want tag=ops", got)
	}
	if strings.Contains(got, "people=alice") {
		t.Errorf("output = %q, should not contain people=alice", got)
	}
}

func TestMetaListCmd_SearchByKeyValue(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for range 2 {
		if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
		}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := s.Add(context.Background(), event.AddInput{Title: "y", Meta: []parse.Meta{
		{Key: "tag", Value: "deploy"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "tag=ops"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "tag=ops") || !strings.Contains(got, "(2)") {
		t.Errorf("output = %q, want tag=ops with (2)", got)
	}
	if strings.Contains(got, "deploy") {
		t.Errorf("output = %q, should not contain deploy", got)
	}
}

func TestMetaListCmd_SearchPeopleShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "people", Value: "sarah"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "@sarah"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "people=sarah") {
		t.Errorf("output = %q, want people=sarah", out.String())
	}
}

func TestMetaListCmd_SearchTagShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "urgent"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "#urgent"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "tag=urgent") {
		t.Errorf("output = %q, want tag=urgent", out.String())
	}
}

// TestMetaListCmd_InvalidSearch pins the one shape this filter still refuses:
// a sigil form whose name is not one. A *bare* key is deliberately unchecked —
// see parseMetaFilter — so `bad name` is a listing of the key `bad name`, not
// an error.
func TestMetaListCmd_InvalidSearch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaListCmd{Search: "@bad name"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestMetaRenameCmd_BadOldFormat(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaRenameCmd{Old: "bad name", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed old key")
	}
}

func TestMetaRenameCmd_NoMatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaRenameCmd{Old: "tag=missing", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Errorf("error = %v, want no-metadata error", err)
	}
}

func TestMetaRenameCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaRenameCmd{Old: "tag=ops", New: "tag=new"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	count, err := s.CountMeta(context.Background(), "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if count != 1 {
		t.Errorf("tag=ops count = %d after abort, want 1", count)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want Aborted", out.String())
	}
}

func TestMetaRenameCmd_ConfirmAppliesOnce(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")

	for range 3 {
		if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
			{Key: "tag", Value: "old"},
		}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	cmd := &MetaRenameCmd{Old: "tag=old", New: "tag=new"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Renamed 3 occurrences") {
		t.Errorf("output = %q, want Renamed 3 occurrences", out.String())
	}

	oldCount, err := s.CountMeta(context.Background(), "tag", "old")
	if err != nil {
		t.Fatalf("CountMeta old: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("tag=old count = %d, want 0", oldCount)
	}
	newCount, err := s.CountMeta(context.Background(), "tag", "new")
	if err != nil {
		t.Fatalf("CountMeta new: %v", err)
	}
	if newCount != 3 {
		t.Errorf("tag=new count = %d, want 3", newCount)
	}
}

func TestMetaRenameCmd_AcceptsShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "wip"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaRenameCmd{Old: "#wip", New: "#done", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wipCount, _ := s.CountMeta(context.Background(), "tag", "wip")
	if wipCount != 0 {
		t.Errorf("tag=wip count = %d after rename, want 0", wipCount)
	}
	doneCount, _ := s.CountMeta(context.Background(), "tag", "done")
	if doneCount != 1 {
		t.Errorf("tag=done count = %d after rename, want 1", doneCount)
	}
}

// TestMetaRenameCmd_PromptWarnsAboutMerge covers the prompt's one job in the
// consolidation case: a rename onto an existing entry destroys rows, and
// "Renamed N occurrences" alone reads as if nothing was lost.
func TestMetaRenameCmd_PromptWarnsAboutMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		newTag   string
		wantWarn bool
	}{
		{name: "target exists", newTag: "#done", wantWarn: true},
		{name: "target is new", newTag: "#shipped", wantWarn: false},
		// Old and new equal: the counts would say the target "already"
		// exists, which describes the tuple being renamed, not a collision.
		{name: "rename to itself", newTag: "#wip", wantWarn: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, out := newTestIO("y\n")

			if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
				{Key: "tag", Value: "wip"},
				{Key: "tag", Value: "done"},
			}}); err != nil {
				t.Fatalf("Add: %v", err)
			}

			cmd := &MetaRenameCmd{Old: "#wip", New: tt.newTag}
			if err := cmd.Run(s, io); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if got := strings.Contains(out.String(), "merge into one"); got != tt.wantWarn {
				t.Errorf("prompt warned about merging = %v, want %v:\n%s",
					got, tt.wantWarn, out.String())
			}
		})
	}
}

func TestMetaDeleteCmd_NoMatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaDeleteCmd{Meta: "tag=ghost"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Errorf("error = %v, want no-metadata error", err)
	}
}

func TestMetaDeleteCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "keep"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "tag=keep"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want Aborted", out.String())
	}
	count, _ := s.CountMeta(context.Background(), "tag", "keep")
	if count != 1 {
		t.Errorf("count after abort = %d, want 1", count)
	}
}

func TestMetaDeleteCmd_Force(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "obsolete"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "tag=obsolete", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Deleted 1 occurrence\n") {
		t.Errorf("output = %q, want Deleted 1 occurrence", out.String())
	}
}

func TestMetaDeleteCmd_AcceptsShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	if _, err := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "obsolete"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "#obsolete", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	count, _ := s.CountMeta(context.Background(), "tag", "obsolete")
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}
