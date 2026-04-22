package main

import (
	"context"
	"fmt"
	"os"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type ListCmd struct {
	Filter  string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format  string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
	Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
	NoPager bool   `help:"Disable the pager even when stdout is a TTY."`
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

	if c.Format == "tree" {
		events, err := s.List(ctx, opts)
		if err != nil {
			return err
		}
		return render.Tree(io.Out, events)
	}
	return render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts))
}

func (c *ListCmd) toListOpts() (event.ListOpts, error) {
	opts := event.ListOpts{
		Filter:    c.Filter,
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
