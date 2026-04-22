package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal"
)

type ShowCmd struct {
	ID     int64  `arg:"" help:"Event ID."`
	Tree   bool   `help:"Show subtree." default:"false"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *ShowCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Tree {
		events, err := internal.GetSubtree(ctx, db, c.ID)
		if err != nil {
			return err
		}
		switch c.Format {
		case "json":
			return internal.RenderJSON(os.Stdout, events)
		case "csv":
			return internal.RenderCSV(os.Stdout, events)
		default:
			return internal.RenderTree(os.Stdout, events)
		}
	}

	event, err := internal.GetEvent(ctx, db, c.ID)
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return internal.RenderJSON(os.Stdout, []internal.Event{*event})
	case "csv":
		return internal.RenderCSV(os.Stdout, []internal.Event{*event})
	default:
		return internal.RenderEvent(os.Stdout, event)
	}
}
