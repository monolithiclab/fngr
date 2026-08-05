package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type ListCmd struct {
	From    string `help:"Start of range (inclusive). Date, timestamp, or relative (\"yesterday\")." placeholder:"WHEN"`
	To      string `help:"End of range (inclusive). Date, timestamp, or relative (\"today\")." placeholder:"WHEN"`
	Format  string `help:"Output format: tree (default), flat, json, csv, md." enum:"${LIST_FORMATS}" default:"${LIST_FORMAT_DEFAULT}"`
	Limit   int    `help:"Maximum events to return, newest first (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Display oldest first (default is newest first)." short:"r"`
	NoPager bool   `help:"Disable the pager even when stdout is a TTY."`
	Search  string `help:"Filter expression (#tag, @person, key=value, word, word*). Operators by precedence: ! (NOT), & (AND, also implied between adjacent terms), | (OR); no grouping parentheses." short:"S"`
}

func (c *ListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	// Validate before spawning a pager: nothing should start a $PAGER process
	// only to tear it down again over a typo in -S or --from.
	opts, err := c.toListOpts()
	if err != nil {
		return err
	}
	// An empty range is a typo often enough to be worth saying out loud: it
	// returns nothing at exit 0, which is indistinguishable from a journal
	// that genuinely has no events in the window.
	if opts.From != nil && opts.To != nil && !opts.From.Before(*opts.To) {
		fmt.Fprintf(io.Err, "warning: --from %q and --to %q describe an empty range; no events can match\n",
			c.From, c.To)
	}

	io, closePager := withPager(io, c.NoPager)
	defer func() {
		if err := closePager(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pager exited with error: %v\n", err)
		}
	}()

	if c.Format == render.FormatTree {
		events, err := s.List(ctx, opts)
		if err != nil {
			return withGrammarHint(err)
		}
		if len(events) == 0 {
			fmt.Fprintln(io.Err, "No events found.")
			return nil
		}
		return render.Tree(io.Out, events)
	}
	return withGrammarHint(render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts)))
}

// withGrammarHint points the user at the -S grammar when the filter itself is
// what failed. The filter parser reports its own errors, so this is a typed
// check — it used to sniff SQLite's message text for "fts5" and friends,
// because malformed filters only failed once SQLite tried to run them.
func withGrammarHint(err error) error {
	if !errors.Is(err, event.ErrFilter) {
		return err
	}
	return fmt.Errorf("%w (see --help for the -S grammar)", err)
}

func (c *ListCmd) toListOpts() (event.ListOpts, error) {
	// Trim once here so a blank -S means the same thing as an absent one:
	// the store treats "" as no filter, but the parser treats "   " as an
	// empty expression, and `fngr -S "$QUERY"` should not flip between the
	// two on a stray space.
	opts := event.ListOpts{
		Filter:    strings.TrimSpace(c.Search),
		Limit:     c.Limit,
		Ascending: c.Reverse,
	}
	// Validate up front: ListSeq compiles the filter lazily, so a syntax
	// error would otherwise surface after the JSON/CSV renderer has already
	// written its opening bytes.
	if opts.Filter != "" {
		if err := event.ValidateFilter(opts.Filter); err != nil {
			return opts, withGrammarHint(err)
		}
	}
	if c.From != "" {
		from, err := parseBound(c.From, false)
		if err != nil {
			return opts, fmt.Errorf("--from: %w", err)
		}
		opts.From = &from
	}
	if c.To != "" {
		to, err := parseBound(c.To, true)
		if err != nil {
			return opts, fmt.Errorf("--to: %w", err)
		}
		opts.To = &to
	}
	return opts, nil
}

// parseBound resolves a --from/--to value to the instant the store compares
// against. It accepts everything --time does, including relative forms and the
// RFC 3339 stamps fngr's own --format=json and --format=csv emit — pasting a
// created_at value straight back into --from used to be rejected.
//
// Both flags are inclusive of everything the user named, which needs opposite
// rounding at each end. --from falls to the start of what it names; --to rises
// past the end of it, because ListOpts.To is compared exclusively — so a bare
// date becomes the next midnight and a clock the next second.
func parseBound(s string, upper bool) (time.Time, error) {
	t, _, hasTime, _, err := timefmt.ParsePartial(s)
	if err != nil {
		return time.Time{}, err
	}
	switch {
	case !hasTime && upper:
		return startOfDay(t).AddDate(0, 0, 1), nil
	case !hasTime:
		return startOfDay(t), nil
	case upper:
		return t.Add(time.Second), nil
	}
	return t, nil
}

// startOfDay returns midnight local on t's date. A zone with no midnight on
// that date (some have shifted DST at 00:00) normalizes forward, which is still
// the earliest instant of the day and so still the right bound.
func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
