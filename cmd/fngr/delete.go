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

	if _, err := s.Get(ctx, c.ID); err != nil {
		return err
	}

	hasChildren, err := s.HasChildren(ctx, c.ID)
	if err != nil {
		return err
	}

	if hasChildren && !c.Recursive {
		return fmt.Errorf("event %d has child events; use -r to delete recursively", c.ID)
	}

	subject := deleteSubject(ctx, s, c.ID, hasChildren)

	if !c.Force {
		ok, err := confirm(io.In, io.Out, fmt.Sprintf("Delete %s? [y/N] ", subject), false)
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
	fmt.Fprintf(io.Out, "Deleted %s\n", subject)
	return nil
}

// deleteSubject names what the delete will take, so the prompt and the result
// line say the same thing — `Deleted event 1` after taking a three-node
// subtree with it was true of the row and wrong about the damage.
//
// The count comes from CountSubtree, which walks the tree and so stops on a
// stored cycle — where the delete itself, an FK cascade that walks nothing,
// works fine. Since `delete -r` is one of the ways out of a corrupt tree, a
// failed count degrades to the un-counted wording rather than failing the
// command: naming the subject must never be the reason a delete cannot run.
func deleteSubject(ctx context.Context, s eventStore, id int64, hasChildren bool) string {
	if !hasChildren {
		return fmt.Sprintf("event %d", id)
	}
	n, err := s.CountSubtree(ctx, id)
	if err != nil {
		return fmt.Sprintf("event %d and all its children", id)
	}
	return fmt.Sprintf("event %d and its subtree (%s)", id, plural(n, "event"))
}
