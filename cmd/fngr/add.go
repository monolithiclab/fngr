package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type AddCmd struct {
	Args   []string `arg:"" optional:"" help:"Event text (joined with spaces). Omit and pipe to stdin, or use -e."`
	Edit   bool     `short:"e" help:"Open $VISUAL or $EDITOR for the body."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
	Time   string   `help:"Override event timestamp (YYYY-MM-DD, ISO 8601, RFC3339, or HH:MM for today)." short:"t"`
}

func (c *AddCmd) Run(s eventStore, io ioStreams) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}

	text, err := resolveBody(c.Args, c.Edit, io)
	if errors.Is(err, errCancel) {
		fmt.Fprintln(io.Err, "cancelled (empty body)")
		return nil
	}
	if err != nil {
		return err
	}

	meta, err := event.CollectMeta(text, c.Meta, c.Author)
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

	id, err := s.Add(context.Background(), text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
