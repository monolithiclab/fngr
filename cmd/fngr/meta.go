package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/monolithiclab/fngr/internal"
)

type MetaCmd struct{}

func (c *MetaCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	counts, err := internal.ListMeta(ctx, db)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		fmt.Println("No metadata found.")
		return nil
	}

	maxKey, maxVal := 0, 0
	for _, mc := range counts {
		if len(mc.Key) > maxKey {
			maxKey = len(mc.Key)
		}
		if len(mc.Value) > maxVal {
			maxVal = len(mc.Value)
		}
	}

	for _, mc := range counts {
		fmt.Fprintf(os.Stdout, "%-*s=%-*s  (%d)\n", maxKey, mc.Key, maxVal, mc.Value, mc.Count)
	}

	return nil
}
