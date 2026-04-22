// Package render contains output formatting functions.
package render

import (
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// Markdown renders events as a Markdown digest grouped by local date.
// Section headers (## YYYY-MM-DD) are emitted when the local date changes
// between consecutive events. Iteration order is preserved.
func Markdown(w io.Writer, events []event.Event) error {
	var lastDate string
	for _, ev := range events {
		if err := renderMarkdownEvent(w, &lastDate, ev); err != nil {
			return err
		}
	}
	return nil
}

// renderMarkdownEvent writes one event's bullet (and optional continuation
// lines and meta line). It updates *lastDate; when the local date of ev
// differs, it first writes a date header (with a leading blank line if
// *lastDate is non-empty).
func renderMarkdownEvent(w io.Writer, lastDate *string, ev event.Event) error {
	local := ev.CreatedAt.Local()
	date := local.Format(timefmt.DateFormat)
	if date != *lastDate {
		if *lastDate != "" {
			if _, err := fmt.Fprint(w, "\n"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "## %s\n\n", date); err != nil {
			return err
		}
		*lastDate = date
	}

	timeStr := local.Format(timefmt.LayoutToday)

	lines := strings.Split(ev.Text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}

	if _, err := fmt.Fprintf(w, "- %s — %s\n", timeStr, lines[0]); err != nil {
		return err
	}
	for _, line := range lines[1:] {
		if _, err := fmt.Fprintf(w, "  %s\n", line); err != nil {
			return err
		}
	}

	if len(ev.Meta) > 0 {
		pairs := make([]string, len(ev.Meta))
		for i, m := range ev.Meta {
			pairs[i] = m.Key + "=" + m.Value
		}
		slices.Sort(pairs)
		if _, err := fmt.Fprintf(w, "  %s\n", strings.Join(pairs, " ")); err != nil {
			return err
		}
	}
	return nil
}

// MarkdownStream is the streaming counterpart to Markdown. It writes one
// bullet per event as the iterator yields, emitting a new ## YYYY-MM-DD
// section header whenever the local date changes between consecutive
// events. The first error from seq aborts and is returned.
func MarkdownStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	var lastDate string
	for ev, err := range seq {
		if err != nil {
			return err
		}
		if rerr := renderMarkdownEvent(w, &lastDate, ev); rerr != nil {
			return rerr
		}
	}
	return nil
}
