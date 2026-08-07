package main

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type ListCmd struct {
	From    string `help:"Start of range (inclusive). Date, timestamp, or relative (\"yesterday\")." placeholder:"WHEN"`
	To      string `help:"End of range (inclusive). Date, timestamp, or relative (\"today\")." placeholder:"WHEN"`
	Format  string `help:"Output format: one of ${LIST_FORMATS}." enum:"${LIST_FORMATS}" default:"${LIST_FORMAT_DEFAULT}"`
	Limit   int    `help:"Maximum events to return, newest first (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Display oldest first (default is newest first)." short:"r"`
	NoPager bool   `help:"Disable the pager even when stdout is a TTY."`
	Search  string `help:"Filter expression (#tag, @person, key=value, word, word*). Operators by precedence: ! (NOT), & (AND, also implied between adjacent terms), | (OR); no grouping parentheses." short:"S"`
}

func (c *ListCmd) Run(s eventStore, io ioStreams) (err error) {
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

	io, closeOut := withPager(io, c.NoPager)
	defer func() {
		// Out is buffered, so the tail of a listing is written only here:
		// reporting a failed flush as anything but an error would exit 0 over
		// truncated output. An error already on its way out says more, though.
		if cerr := closeOut(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	// Tree is the one format that has to see every row before it can draw a
	// line, so it takes the buffering dispatcher; everything else streams.
	// Through Canonical, not raw equality: an alias for tree would otherwise
	// fall through to EventsStream, which refuses tree outright — a rejection
	// at render time for a spelling Kong accepted.
	var found bool
	if render.Canonical(c.Format) == render.FormatTree {
		events, listErr := s.List(ctx, opts)
		if listErr != nil {
			return withGrammarHint(listErr)
		}
		found = len(events) > 0
		err = render.Events(io.Out, c.Format, events)
	} else {
		err = render.EventsStream(io.Out, c.Format, noteAny(s.ListSeq(ctx, opts), &found))
	}
	if err != nil {
		return withGrammarHint(err)
	}

	// Said out loud because no output at exit 0 is indistinguishable from a
	// journal with no events in the window, and three of the five formats write
	// nothing at all when empty. The two that do say it themselves — `[]`, a
	// lone CSV header — get it anyway, because the alternative is a per-format
	// table of what counts as self-explanatory, kept in step by hand. stderr is
	// what makes that free: a script reading stdout sees the same bytes either
	// way.
	if !found {
		reportNone(io.Err, "events")
	}
	return nil
}

// noteAny passes a result sequence through unchanged while recording whether
// any event reached the renderer, so an empty listing can be reported without
// materializing one that isn't. Errors don't count — a sequence that fails on
// its first read has produced no events, and the error is the report.
func noteAny(seq iter.Seq2[event.Event, error], found *bool) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for ev, err := range seq {
			if err == nil {
				*found = true
			}
			if !yield(ev, err) {
				return
			}
		}
	}
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
