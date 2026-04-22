package internal

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

type AddCmd struct {
	Text   string   `arg:"" help:"Event text."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID."`
	Meta   []string `help:"Metadata key=value pairs." short:"m"`
}

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format." enum:"table,json,csv" default:"table"`
	Tree   bool   `help:"Show events as a tree." default:"true" negatable:""`
}

type ShowCmd struct {
	ID   int64 `arg:"" help:"Event ID."`
	Tree bool  `help:"Show subtree." default:"false"`
}

type DeleteCmd struct {
	ID int64 `arg:"" help:"Event ID."`
}

type MetaCmd struct{}

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

	meta, err := CollectMeta(c.Text, c.Meta, author)
	if err != nil {
		return err
	}

	id, err := AddEvent(ctx, db, c.Text, c.Parent, meta)
	if err != nil {
		return err
	}

	fmt.Printf("Added event %d\n", id)
	return nil
}

func (c *ListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	events, err := ListEvents(ctx, db, ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return RenderJSON(os.Stdout, events)
	case "csv":
		return RenderCSV(os.Stdout, events)
	default:
		if c.Tree {
			return RenderTree(os.Stdout, events)
		}
		return RenderFlat(os.Stdout, events)
	}
}

func (c *ShowCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Tree {
		events, err := GetSubtree(ctx, db, c.ID)
		if err != nil {
			return err
		}
		return RenderTree(os.Stdout, events)
	}

	event, err := GetEvent(ctx, db, c.ID)
	if err != nil {
		return err
	}

	return RenderEvent(os.Stdout, event)
}

func (c *DeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if err := DeleteEvent(ctx, db, c.ID); err != nil {
		return err
	}
	fmt.Printf("Deleted event %d\n", c.ID)
	return nil
}

func (c *MetaCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	counts, err := ListMeta(ctx, db)
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
