package main

import (
	"context"
	"fmt"
)

type DeleteCmd struct {
	ID        int64 `arg:"" help:"Event ID."`
	Force     bool  `help:"Skip confirmation prompt." short:"f"`
	Recursive bool  `help:"Delete event and all children." short:"r"`
}

func (c *DeleteCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	ev, err := s.Get(ctx, c.ID)
	if err != nil {
		return err
	}

	hasChildren, err := s.HasChildren(ctx, c.ID)
	if err != nil {
		return err
	}

	if hasChildren && !c.Recursive {
		return fmt.Errorf("event %d has child events; use -r to delete recursively", c.ID)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Delete event %d? [y/N] ", ev.ID)
		if hasChildren {
			prompt = fmt.Sprintf("Delete event %d and all its children? [y/N] ", ev.ID)
		}
		ok, err := confirm(io.In, io.Out, prompt, false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	if err := s.Delete(ctx, c.ID); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Deleted event %d\n", c.ID)
	return nil
}
