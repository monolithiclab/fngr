package main

import (
	"context"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
)

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
}

func (c *ListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	events, err := s.List(ctx, event.ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}
	return render.Events(io.Out, c.Format, events)
}
