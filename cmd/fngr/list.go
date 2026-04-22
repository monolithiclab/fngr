package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal"
)

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format." enum:"table,json,csv" default:"table"`
	Tree   bool   `help:"Show events as a tree." default:"true" negatable:""`
}

func (c *ListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	events, err := internal.ListEvents(ctx, db, internal.ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return internal.RenderJSON(os.Stdout, events)
	case "csv":
		return internal.RenderCSV(os.Stdout, events)
	default:
		if c.Tree {
			return internal.RenderTree(os.Stdout, events)
		}
		return internal.RenderFlat(os.Stdout, events)
	}
}
