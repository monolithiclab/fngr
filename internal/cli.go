package internal

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
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

	id, err := AddEvent(db, c.Text, c.Parent, meta)
	if err != nil {
		return err
	}

	fmt.Printf("Added event %d\n", id)
	return nil
}

func (c *ListCmd) Run(db *sql.DB) error {
	events, err := ListEvents(db, ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}

	var output string
	switch c.Format {
	case "json":
		output = RenderJSON(events)
	case "csv":
		output = RenderCSV(events)
	default:
		if c.Tree {
			output = RenderTree(events)
		} else {
			output = RenderFlat(events)
		}
	}

	fmt.Print(output)
	return nil
}

func (c *ShowCmd) Run(db *sql.DB) error {
	if c.Tree {
		events, err := GetSubtree(db, c.ID)
		if err != nil {
			return err
		}
		fmt.Print(RenderTree(events))
		return nil
	}

	event, err := GetEvent(db, c.ID)
	if err != nil {
		return err
	}

	fmt.Printf("ID:     %d\n", event.ID)
	if event.ParentID != nil {
		fmt.Printf("Parent: %d\n", *event.ParentID)
	}
	fmt.Printf("Date:   %s\n", event.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("Text:   %s\n", event.Text)

	if len(event.Meta) > 0 {
		fmt.Println("Meta:")
		for _, m := range event.Meta {
			fmt.Printf("  %s=%s\n", m.Key, m.Value)
		}
	}

	return nil
}

func (c *DeleteCmd) Run(db *sql.DB) error {
	if err := DeleteEvent(db, c.ID); err != nil {
		return err
	}
	fmt.Printf("Deleted event %d\n", c.ID)
	return nil
}

func (c *MetaCmd) Run(db *sql.DB) error {
	counts, err := ListMeta(db)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		fmt.Println("No metadata found.")
		return nil
	}

	// Find max widths for aligned output.
	maxKey, maxVal := 0, 0
	for _, mc := range counts {
		if len(mc.Key) > maxKey {
			maxKey = len(mc.Key)
		}
		if len(mc.Value) > maxVal {
			maxVal = len(mc.Value)
		}
	}

	var b strings.Builder
	for _, mc := range counts {
		fmt.Fprintf(&b, "%-*s=%-*s  (%d)\n", maxKey, mc.Key, maxVal, mc.Value, mc.Count)
	}
	fmt.Print(b.String())

	return nil
}
