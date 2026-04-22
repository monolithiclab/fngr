package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type AddCmd struct {
	Args   []string `arg:"" optional:"" help:"Event text (joined with spaces). Omit and pipe to stdin, or use -e."`
	Edit   bool     `short:"e" help:"Open $VISUAL or $EDITOR for the body."`
	Format string   `short:"f" help:"Input format: text (default) or json. Under json, body is parsed as one event object or an array; per-record fields override the matching CLI flag, and absent fields fall back to it." enum:"${ADD_FORMATS}" default:"${ADD_FORMAT_DEFAULT}"`
	Author string   `help:"Event author (used as default if JSON record omits meta.author)." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID (used as default if JSON record omits parent_id)."`
	Meta   []string `help:"Metadata key=value pairs (used as defaults if JSON record omits meta)." short:"m"`
	Time   string   `help:"Override event timestamp (used as default if JSON record omits created_at)." short:"t"`
}

func (c *AddCmd) Run(s eventStore, io ioStreams) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}

	if c.Format == render.FormatJSON {
		if c.Edit {
			return fmt.Errorf("--edit conflicts with --format=json")
		}
		if io.IsTTY && len(c.Args) == 0 {
			return fmt.Errorf("--format=json requires JSON via args or piped stdin")
		}
	}

	text, err := resolveBody(c.Args, c.Edit, io)
	if errors.Is(err, errCancel) {
		fmt.Fprintln(io.Err, "cancelled (empty body)")
		return nil
	}
	if err != nil {
		return err
	}

	if c.Format == render.FormatJSON {
		return c.runJSON(s, io, text)
	}
	return c.runText(s, io, text)
}

func (c *AddCmd) runText(s eventStore, io ioStreams, text string) error {
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
