package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// EventCmd is the parent for all `fngr event <verb>` invocations.
type EventCmd struct {
	Show   EventShowCmd   `cmd:"" default:"withargs" help:"Show event detail (default)."`
	Text   EventTextCmd   `cmd:"" help:"Replace event text."`
	Time   EventTimeCmd   `cmd:"" help:"Replace clock time (or full timestamp)."`
	Date   EventDateCmd   `cmd:"" help:"Replace date (or full timestamp)."`
	Attach EventAttachCmd `cmd:"" help:"Set parent event."`
	Detach EventDetachCmd `cmd:"" help:"Clear parent."`
	Tag    EventTagCmd    `cmd:"" help:"Add tags (one or more @person, #tag, or key=value)."`
	Untag  EventUntagCmd  `cmd:"" help:"Remove tags (one or more @person, #tag, or key=value)."`
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
		return fmt.Errorf("event time: %w", err)
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
		return fmt.Errorf("event date: %w", err)
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

// EventAttachCmd sets parent_id.
type EventAttachCmd struct {
	ID     int64 `arg:"" help:"Event ID."`
	Parent int64 `arg:"" help:"Parent event ID."`
}

func (c *EventAttachCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()
	if err := s.Reparent(ctx, c.ID, &c.Parent); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Attached event %d to event %d\n", c.ID, c.Parent)
	return nil
}

// EventDetachCmd clears parent_id.
type EventDetachCmd struct {
	ID int64 `arg:"" help:"Event ID."`
}

func (c *EventDetachCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()
	if err := s.Reparent(ctx, c.ID, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Detached event %d\n", c.ID)
	return nil
}

// EventTagCmd adds one or more tags.
type EventTagCmd struct {
	ID   int64    `arg:"" help:"Event ID."`
	Args []string `arg:"" help:"Tags to add: @person, #tag, or key=value (one or more)."`
}

func (c *EventTagCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if len(c.Args) == 0 {
		return fmt.Errorf("at least one tag required")
	}
	tags, err := parseTagArgs(c.Args)
	if err != nil {
		return err
	}
	if err := s.AddTags(ctx, c.ID, tags); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Tagged event %d with %d tag(s)\n", c.ID, len(tags))
	return nil
}

// EventUntagCmd removes one or more tags. Reports the count removed.
type EventUntagCmd struct {
	ID   int64    `arg:"" help:"Event ID."`
	Args []string `arg:"" help:"Tags to remove: @person, #tag, or key=value (one or more)."`
}

func (c *EventUntagCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if len(c.Args) == 0 {
		return fmt.Errorf("at least one tag required")
	}
	tags, err := parseTagArgs(c.Args)
	if err != nil {
		return err
	}
	n, err := s.RemoveTags(ctx, c.ID, tags)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("nothing to untag: %s", strings.Join(c.Args, " "))
	}
	fmt.Fprintf(io.Out, "Untagged event %d (%d removed)\n", c.ID, n)
	return nil
}

// parseTagArgs validates every arg up front so a malformed last arg never
// triggers a partial DB write.
func parseTagArgs(args []string) ([]parse.Meta, error) {
	out := make([]parse.Meta, 0, len(args))
	for _, a := range args {
		m, err := parse.MetaArg(a)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
