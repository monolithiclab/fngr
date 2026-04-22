package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
)

type ShowCmd struct {
	ID     int64  `arg:"" help:"Event ID."`
	Tree   bool   `help:"Show subtree." default:"false"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *ShowCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Tree {
		events, err := event.GetSubtree(ctx, db, c.ID)
		if err != nil {
			return err
		}
		switch c.Format {
		case "json":
			return render.JSON(os.Stdout, events)
		case "csv":
			return render.CSV(os.Stdout, events)
		default:
			return render.Tree(os.Stdout, events)
		}
	}

	ev, err := event.Get(ctx, db, c.ID)
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return render.JSON(os.Stdout, []event.Event{*ev})
	case "csv":
		return render.CSV(os.Stdout, []event.Event{*ev})
	default:
		return render.Event(os.Stdout, ev)
	}
}
