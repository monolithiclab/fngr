package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// nowFunc lets tests pin the relative-stamp anchor.
var (
	nowMu   sync.RWMutex
	nowFunc = time.Now
)

func formatLocalStamp(t time.Time) string {
	nowMu.RLock()
	defer nowMu.RUnlock()
	return timefmt.FormatRelative(t, nowFunc())
}

func formatLocalDateTime(t time.Time) string {
	return t.Local().Format(timefmt.DateTimeFormat)
}

func metaValue(meta []parse.Meta, key string) string {
	for _, m := range meta {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

func eventAuthor(ev event.Event) string {
	return metaValue(ev.Meta, event.MetaKeyAuthor)
}

func formatEventLine(id int64, date, author, text string) string {
	return fmt.Sprintf("%-4d%s  %s  %s", id, date, author, text)
}

// Events writes a list of events in the requested format. Supported formats
// are "tree" (default), "flat", "json", "csv".
func Events(w io.Writer, format string, events []event.Event) error {
	switch format {
	case "csv":
		return CSV(w, events)
	case "flat":
		return Flat(w, events)
	case "json":
		return JSON(w, events)
	default:
		return Tree(w, events)
	}
}

// SingleEvent writes one event in the requested format. Supported formats are
// "text" (default), "json", "csv".
func SingleEvent(w io.Writer, format string, ev *event.Event) error {
	switch format {
	case "csv":
		return CSV(w, []event.Event{*ev})
	case "json":
		return JSON(w, []event.Event{*ev})
	default:
		return Event(w, ev)
	}
}

func Tree(w io.Writer, events []event.Event) error {
	if len(events) == 0 {
		return nil
	}

	byID := make(map[int64]int, len(events))
	children := make(map[int64][]int64)
	var roots []int64

	for i, ev := range events {
		byID[ev.ID] = i
	}
	for _, ev := range events {
		if ev.ParentID == nil {
			roots = append(roots, ev.ID)
			continue
		}
		if _, parentInSet := byID[*ev.ParentID]; parentInSet {
			children[*ev.ParentID] = append(children[*ev.ParentID], ev.ID)
		} else {
			roots = append(roots, ev.ID)
		}
	}

	for _, id := range roots {
		if err := renderNode(w, events, byID, children, id, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func renderNode(w io.Writer, events []event.Event, byID map[int64]int, children map[int64][]int64, id int64, linePrefix, childPrefix string) error {
	idx := byID[id]
	ev := events[idx]
	line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)

	if _, err := fmt.Fprintf(w, "%s%s\n", linePrefix, line); err != nil {
		return err
	}

	kids := children[id]
	for i, kidID := range kids {
		isLast := i == len(kids)-1
		var connector string
		var continuation string
		if isLast {
			connector = "\u2514\u2500 "
			continuation = "   "
		} else {
			connector = "\u251c\u2500 "
			continuation = "\u2502  "
		}
		if err := renderNode(w, events, byID, children, kidID, childPrefix+connector, childPrefix+continuation); err != nil {
			return err
		}
	}
	return nil
}

func Flat(w io.Writer, events []event.Event) error {
	for _, ev := range events {
		line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

type jsonEvent struct {
	ID        int64               `json:"id"`
	ParentID  *int64              `json:"parent_id,omitempty"`
	Text      string              `json:"text"`
	CreatedAt string              `json:"created_at"`
	Meta      map[string][]string `json:"meta,omitempty"`
}

func JSON(w io.Writer, events []event.Event) error {
	out := make([]jsonEvent, len(events))
	for i, ev := range events {
		meta := make(map[string][]string)
		for _, m := range ev.Meta {
			meta[m.Key] = append(meta[m.Key], m.Value)
		}
		out[i] = jsonEvent{
			ID:        ev.ID,
			ParentID:  ev.ParentID,
			Text:      ev.Text,
			CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
			Meta:      meta,
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func CSV(w io.Writer, events []event.Event) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "parent_id", "created_at", "author", "text"})
	for _, ev := range events {
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		_ = cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Text,
		})
	}
	cw.Flush()
	return cw.Error()
}

func Event(w io.Writer, ev *event.Event) error {
	if _, err := fmt.Fprintf(w, "ID:     %d\n", ev.ID); err != nil {
		return err
	}
	if ev.ParentID != nil {
		if _, err := fmt.Fprintf(w, "Parent: %d\n", *ev.ParentID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Date:   %s\n", formatLocalDateTime(ev.CreatedAt)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Text:   %s\n", ev.Text); err != nil {
		return err
	}

	if len(ev.Meta) > 0 {
		if _, err := fmt.Fprintln(w, "Meta:"); err != nil {
			return err
		}
		for _, m := range ev.Meta {
			if _, err := fmt.Fprintf(w, "  %s=%s\n", m.Key, m.Value); err != nil {
				return err
			}
		}
	}

	return nil
}
