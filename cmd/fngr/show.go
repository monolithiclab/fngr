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
		return render.Events(os.Stdout, c.Format, events)
	}

	ev, err := event.Get(ctx, db, c.ID)
	if err != nil {
		return err
	}
	return render.SingleEvent(os.Stdout, c.Format, ev)
}
