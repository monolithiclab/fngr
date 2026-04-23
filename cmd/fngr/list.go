package main

import (
	"context"
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
	Search  string `help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)." short:"S"`
}

func (c *ListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	io, closePager := withPager(io, c.NoPager)
	defer func() {
		if err := closePager(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pager exited with error: %v\n", err)
		}
	}()

	opts, err := c.toListOpts()
	if err != nil {
		return err
	}

	if c.Format == render.FormatTree {
		events, err := s.List(ctx, opts)
		if err != nil {
			return wrapFilterErr(c.Search, err)
		}
		if len(events) == 0 {
			fmt.Fprintln(io.Err, "No events found.")
			return nil
		}
		return render.Tree(io.Out, events)
	}
	return wrapFilterErr(c.Search, render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts)))
}

// wrapFilterErr surfaces filter-grammar errors with a pointer to --help.
// Only wraps when a filter was actually passed (no point steering empty-filter
// failures into the filter-syntax bucket) AND when the error looks like a
// parse failure — FTS5 sub-parser failures surface as "fts5:" prefixed
// messages, but unmatched quotes break SQLite's tokenizer first and surface
// as generic "SQL logic error: ... syntax error / unterminated string".
func wrapFilterErr(filter string, err error) error {
	if err == nil || filter == "" {
		return err
	}
	msg := err.Error()
	parseFailure := strings.Contains(msg, "fts5") ||
		strings.Contains(msg, "FTS5") ||
		strings.Contains(msg, "SQL logic error") ||
		strings.Contains(msg, "syntax error") ||
		strings.Contains(msg, "unterminated")
	if !parseFailure {
		return err
	}
	return fmt.Errorf("invalid filter syntax (%w); see --help for the -S grammar", err)
}

func (c *ListCmd) toListOpts() (event.ListOpts, error) {
	opts := event.ListOpts{
		Filter:    c.Search,
		Limit:     c.Limit,
		Ascending: c.Reverse,
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
