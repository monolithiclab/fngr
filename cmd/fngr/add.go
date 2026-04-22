package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/monolithiclab/fngr/internal"
)

type AddCmd struct {
	Text   string   `arg:"" help:"Event text. Use @person and #tag for inline metadata extraction."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
}

func (c *AddCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	author := c.Author
	if author == "" {
		author = os.Getenv("USER")
	}
	if author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
	if c.Text == "" {
		return fmt.Errorf("event text cannot be empty")
	}

	meta, err := internal.CollectMeta(c.Text, c.Meta, author)
	if err != nil {
		return err
	}

	id, err := internal.AddEvent(ctx, db, c.Text, c.Parent, meta)
	if err != nil {
		return err
	}

	fmt.Printf("Added event %d\n", id)
	return nil
}
