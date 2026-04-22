package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// EventCmd is the parent for all `fngr event <verb>` invocations.
type EventCmd struct {
	Show EventShowCmd `cmd:"" default:"withargs" help:"Show event detail (default)."`
	Text EventTextCmd `cmd:"" help:"Replace event text."`
	Time EventTimeCmd `cmd:"" help:"Replace clock time (or full timestamp)."`
	Date EventDateCmd `cmd:"" help:"Replace date (or full timestamp)."`
}

// EventShowCmd reads the event. Honours --tree (subtree view) and --format.
type EventShowCmd struct {
	ID     int64  `arg:"" help:"Event ID."`
	Tree   bool   `help:"Show subtree." short:"t"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *EventShowCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Tree {
		events, err := s.GetSubtree(ctx, c.ID)
		if err != nil {
			return err
		}
		return render.Events(io.Out, c.Format, events)
	}

	ev, err := s.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	return render.SingleEvent(io.Out, c.Format, ev)
}

// EventTextCmd replaces the event's text. Body tags are synced.
type EventTextCmd struct {
	ID   int64  `arg:"" help:"Event ID."`
	Body string `arg:"" help:"New event text."`
}

func (c *EventTextCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Body == "" {
		return fmt.Errorf("event text cannot be empty")
	}
	if err := s.Update(ctx, c.ID, &c.Body, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}

// EventTimeCmd replaces the clock time (or both date+time when given a
// full timestamp).
type EventTimeCmd struct {
	ID    int64  `arg:"" help:"Event ID."`
	Value string `arg:"" help:"New time (HH:MM, 3:04PM, ...) or full timestamp."`
}

func (c *EventTimeCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	parsed, hasDate, hasTime, err := timefmt.ParsePartial(c.Value)
	if err != nil {
		return fmt.Errorf("--time: %w", err)
	}
	if !hasTime {
		return fmt.Errorf("event time: expected a time or full timestamp, got date-only %q", c.Value)
	}

	var when time.Time
	if hasDate {
		when = parsed
	} else {
		ev, err := s.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		orig := ev.CreatedAt.Local()
		when = time.Date(
			orig.Year(), orig.Month(), orig.Day(),
			parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(),
			orig.Location(),
		)
	}

	if err := s.Update(ctx, c.ID, nil, &when); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}

// EventDateCmd replaces the date (or both date+time when given a full
// timestamp).
type EventDateCmd struct {
	ID    int64  `arg:"" help:"Event ID."`
	Value string `arg:"" help:"New date (YYYY-MM-DD) or full timestamp."`
}

func (c *EventDateCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	parsed, hasDate, hasTime, err := timefmt.ParsePartial(c.Value)
	if err != nil {
		return fmt.Errorf("--date: %w", err)
	}
	if !hasDate {
		return fmt.Errorf("event date: expected a date or full timestamp, got time-only %q", c.Value)
	}

	var when time.Time
	if hasTime {
		when = parsed
	} else {
		ev, err := s.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		orig := ev.CreatedAt.Local()
		when = time.Date(
			parsed.Year(), parsed.Month(), parsed.Day(),
			orig.Hour(), orig.Minute(), orig.Second(), orig.Nanosecond(),
			orig.Location(),
		)
	}

	if err := s.Update(ctx, c.ID, nil, &when); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}
