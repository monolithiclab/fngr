package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal"
)

type ShowCmd struct {
	ID   int64 `arg:"" help:"Event ID."`
	Tree bool  `help:"Show subtree." default:"false"`
}

func (c *ShowCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Tree {
		events, err := internal.GetSubtree(ctx, db, c.ID)
		if err != nil {
			return err
		}
		return internal.RenderTree(os.Stdout, events)
	}

	event, err := internal.GetEvent(ctx, db, c.ID)
	if err != nil {
		return err
	}

	return internal.RenderEvent(os.Stdout, event)
}
