package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monolithiclab/fngr/internal"
)

type DeleteCmd struct {
	ID int64 `arg:"" help:"Event ID."`
}

func (c *DeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if err := internal.DeleteEvent(ctx, db, c.ID); err != nil {
		return err
	}
	fmt.Printf("Deleted event %d\n", c.ID)
	return nil
}
