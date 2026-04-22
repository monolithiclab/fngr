package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
)

type AddCmd struct {
	Text   string   `arg:"" help:"Event text. Use @person and #tag for inline metadata extraction."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
	Time   string   `help:"Override event timestamp (ISO 8601, e.g. 2026-04-15T14:30:00)." short:"t"`
}

func (c *AddCmd) Run(db *sql.DB) error {
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
		t, err := parseTime(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value %q: %w", c.Time, err)
		}
		createdAt = &t
	}

	id, err := event.Add(ctx, db, c.Text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}

	fmt.Printf("Added event %d\n", id)
	return nil
}

var timeFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range timeFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("supported formats: YYYY-MM-DD, YYYY-MM-DDTHH:MM, YYYY-MM-DDTHH:MM:SS, RFC3339")
}
