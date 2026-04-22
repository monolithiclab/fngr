package main

import (
	"context"

	"github.com/monolithiclab/fngr/internal/render"
)

// EventCmd is the parent for all `fngr event <verb>` invocations.
type EventCmd struct {
	Show EventShowCmd `cmd:"" default:"withargs" help:"Show event detail (default)."`
}

// EventShowCmd reads the event. Honours --tree (subtree view) and --format.
type EventShowCmd struct {
	ID     int64  `arg:"" help:"Event ID."`
	Tree   bool   `help:"Show subtree." short:"t"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *EventShowCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Tree {
		events, err := s.GetSubtree(ctx, c.ID)
		if err != nil {
			return err
		}
		return render.Events(io.Out, c.Format, events)
	}

	ev, err := s.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	return render.SingleEvent(io.Out, c.Format, ev)
}
