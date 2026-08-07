package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type AddCmd struct {
	Args   []string `arg:"" optional:"" help:"Event text (joined with spaces). A leading \"<time>: \" prefix sets the timestamp (e.g. \"9:30: had coffee\"), unless --time is given. Omit and pipe to stdin, or use -e."`
	Edit   bool     `short:"e" help:"Open $VISUAL or $EDITOR for the body."`
	Format string   `short:"f" help:"Input format: one of ${ADD_FORMATS}. Under json, body is parsed as one event object or an array; per-record fields override the matching CLI flag, and absent fields fall back to it." enum:"${ADD_FORMATS}" default:"${ADD_FORMAT_DEFAULT}"`
	Author string   `help:"Event author (used as default if JSON record omits meta.author)." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID (used as default if JSON record omits parent_id)."`
	Meta   []string `help:"Metadata key=value pairs (used as defaults if JSON record omits meta)." short:"m"`
	Time   string   `help:"Override event timestamp; absolute (YYYY-MM-DD, 3:04PM) or relative (\"2 days ago\", \"yesterday at 9am\", \"now\"). Used as default if JSON record omits created_at." short:"t"`
}

func (c *AddCmd) Run(s eventStore, io ioStreams) error {
	// Canonical, not raw equality, for the reason withAliases exists: an alias
	// resolving to json would pass the enum and then miss this test, sending a
	// 10 000-record batch down the text path to be stored as one event titled
	// `[{"title":…` at exit 0. No alias resolves here today; the point is that
	// adding one cannot make it so.
	format := render.Canonical(c.Format)

	if format == render.FormatJSON {
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

	if format == render.FormatJSON {
		return c.runJSON(s, io, text)
	}
	return c.runText(s, io, text)
}

func (c *AddCmd) runText(s eventStore, io ioStreams, text string) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
	title, body := parse.SplitTitleBody(text)

	// Resolve the timestamp. --time is the explicit override; absent it, a
	// leading time/date token in the title (e.g. "9:30: had coffee") is
	// parsed and stripped so the title stores just the note.
	var createdAt *time.Time
	switch {
	case c.Time != "":
		t, _, _, exists, err := timefmt.ParsePartial(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value: %w", err)
		}
		warnSkippedClock(io.Err, exists, c.Time, t)
		createdAt = &t
	default:
		if t, prefix, rest, exists, ok := timefmt.SplitTimePrefix(title); ok {
			warnSkippedClock(io.Err, exists, prefix, t)
			createdAt = &t
			title = rest
		}
	}

	if title == "" {
		return fmt.Errorf("event title cannot be empty")
	}
	meta, err := event.CollectMeta(parse.EventText(title, body), c.Meta, c.Author)
	if err != nil {
		return err
	}

	id, err := s.Add(context.Background(), event.AddInput{
		Title:     title,
		Body:      body,
		ParentID:  c.Parent,
		Meta:      meta,
		CreatedAt: createdAt,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
