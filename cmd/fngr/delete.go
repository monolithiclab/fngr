package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
)

type DeleteCmd struct {
	ID        int64 `arg:"" help:"Event ID."`
	Force     bool  `help:"Skip confirmation prompt." short:"f"`
	Recursive bool  `help:"Delete event and all children." short:"r"`
}

func (c *DeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	ev, err := event.Get(ctx, db, c.ID)
	if err != nil {
		return err
	}

	hasChildren, err := event.HasChildren(ctx, db, c.ID)
	if err != nil {
		return err
	}

	if hasChildren && !c.Recursive {
		return fmt.Errorf("event %d has child events; use -r to delete recursively", c.ID)
	}

	if !c.Force {
		if hasChildren {
			fmt.Printf("Delete event %d and all its children? [Y/n] ", ev.ID)
		} else {
			fmt.Printf("Delete event %d? [Y/n] ", ev.ID)
		}
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := event.Delete(ctx, db, c.ID); err != nil {
		return err
	}
	fmt.Printf("Deleted event %d\n", c.ID)
	return nil
}
