package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type AddCmd struct {
	Text   string   `arg:"" help:"Event text. Use @person and #tag for inline metadata extraction."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
	Time   string   `help:"Override event timestamp (YYYY-MM-DD, ISO 8601, RFC3339, or HH:MM for today)." short:"t"`
}

func (c *AddCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
	if c.Text == "" {
		return fmt.Errorf("event text cannot be empty")
	}

	meta, err := event.CollectMeta(c.Text, c.Meta, c.Author)
	if err != nil {
		return err
	}

	var createdAt *time.Time
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value: %w", err)
		}
		createdAt = &t
	}

	id, err := s.Add(ctx, c.Text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}

	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
