package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type ListCmd struct {
	From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format  string `help:"Output format: tree (default), flat, json, csv, md." enum:"${LIST_FORMATS}" default:"${LIST_FORMAT_DEFAULT}"`
	Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
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
		from, err := timefmt.ParseDate(c.From)
		if err != nil {
			return opts, fmt.Errorf("--from: %w", err)
		}
		opts.From = &from
	}
	if c.To != "" {
		to, err := timefmt.ParseDate(c.To)
		if err != nil {
			return opts, fmt.Errorf("--to: %w", err)
		}
		end := to.AddDate(0, 0, 1)
		opts.To = &end
	}
	return opts, nil
}
