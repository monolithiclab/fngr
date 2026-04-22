package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
)

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
}

func (c *ListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	events, err := event.List(ctx, db, event.ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return render.JSON(os.Stdout, events)
	case "csv":
		return render.CSV(os.Stdout, events)
	case "flat":
		return render.Flat(os.Stdout, events)
	default:
		return render.Tree(os.Stdout, events)
	}
}
