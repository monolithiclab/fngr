package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/timefmt"
)

type EditCmd struct {
	ID    int64   `arg:"" help:"Event ID."`
	Text  *string `help:"Replace event text."`
	Time  string  `help:"Replace event timestamp." short:"t"`
	Force bool    `help:"Skip confirmation prompt." short:"f"`
}

func (c *EditCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Text == nil && c.Time == "" {
		return fmt.Errorf("nothing to edit; pass --text and/or --time")
	}
	if c.Text != nil && *c.Text == "" {
		return fmt.Errorf("event text cannot be empty")
	}

	current, err := s.Get(ctx, c.ID)
	if err != nil {
		return err
	}

	var newCreatedAt *time.Time
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return fmt.Errorf("--time: %w", err)
		}
		newCreatedAt = &t
	}

	if !c.Force {
		if c.Text != nil {
			fmt.Fprintf(io.Out, "  text: %q -> %q\n", current.Text, *c.Text)
		}
		if newCreatedAt != nil {
			fmt.Fprintf(io.Out, "  time: %s -> %s\n",
				current.CreatedAt.Local().Format(timefmt.DateTimeFormat),
				newCreatedAt.Local().Format(timefmt.DateTimeFormat),
			)
		}
		ok, err := confirm(io.In, io.Out, "Apply changes? [Y/n] ", true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	if err := s.Update(ctx, c.ID, c.Text, newCreatedAt); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}
