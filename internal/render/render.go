package render

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"slices"
	"strconv"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// Output format identifiers used by the CLI's --format flag and the
// render dispatchers below. cmd/fngr wires these into Kong via kongVars
// so the flag enum and default stay in lockstep with this package.
const (
	FormatTree     = "tree"
	FormatFlat     = "flat"
	FormatJSON     = "json"
	FormatCSV      = "csv"
	FormatText     = "text"
	FormatMarkdown = "md"
)

// ListFormats are the formats accepted by Events and EventsStream.
var ListFormats = []string{FormatTree, FormatFlat, FormatJSON, FormatCSV, FormatMarkdown}

// EventFormats are the formats accepted by SingleEvent.
var EventFormats = []string{FormatText, FormatJSON, FormatCSV, FormatMarkdown}

// nowFunc is the relative-stamp anchor. Production never reassigns it;
// tests swap it via pinNow inside non-parallel subtests.
var nowFunc = time.Now

func formatLocalStamp(t time.Time) string {
	return timefmt.FormatRelative(t, nowFunc())
}

func formatLocalDateTime(t time.Time) string {
	return t.Local().Format(timefmt.DateTimeFormat)
}

func eventAuthor(ev event.Event) string {
	for _, m := range ev.Meta {
		if m.Key == event.MetaKeyAuthor {
			return m.Value
		}
	}
	return ""
}

func formatEventLine(id int64, date, author, text string) string {
	return fmt.Sprintf("%-4d%s  %s  %s", id, date, author, text)
}

// Events writes a list of events in the requested format. Supported formats
// are FormatTree (default), FormatFlat, FormatJSON, FormatCSV, FormatMarkdown.
func Events(w io.Writer, format string, events []event.Event) error {
	switch format {
	case FormatCSV:
		return CSV(w, events)
	case FormatFlat:
		return Flat(w, events)
	case FormatJSON:
		return JSON(w, events)
	case FormatMarkdown:
		return Markdown(w, events)
	default:
		return Tree(w, events)
	}
}

// SingleEvent writes one event in the requested format. Supported formats
// are FormatText (default), FormatJSON, FormatCSV, FormatMarkdown.
func SingleEvent(w io.Writer, format string, ev *event.Event) error {
	switch format {
	case FormatCSV:
		return CSV(w, []event.Event{*ev})
	case FormatJSON:
		return JSON(w, []event.Event{*ev})
	case FormatMarkdown:
		return Markdown(w, []event.Event{*ev})
	default:
		return Event(w, ev)
	}
}

// Tree writes events as an indented parent/child tree. Events whose
// parent_id is not present in the input slice render as roots, so a
// `--limit`-truncated query still produces well-formed output.
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

// Flat writes one line per event in input order: `id  date  author  text`.
// Parent/child topology is ignored; for that, use Tree.
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
	ID        int64       `json:"id"`
	ParentID  *int64      `json:"parent_id,omitempty"`
	Text      string      `json:"text"`
	CreatedAt string      `json:"created_at"`
	Meta      [][2]string `json:"meta,omitempty"`
}

func toJSONEvent(ev event.Event) jsonEvent {
	out := jsonEvent{
		ID:        ev.ID,
		ParentID:  ev.ParentID,
		Text:      ev.Text,
		CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(ev.Meta) == 0 {
		return out
	}
	pairs := make([][2]string, len(ev.Meta))
	for i, m := range ev.Meta {
		pairs[i] = [2]string{m.Key, m.Value}
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	out.Meta = pairs
	return out
}

// JSON writes events as a single indented JSON array, suitable for
// round-tripping back through `fngr add --format=json`. Meta is emitted
// as `[[key, value], ...]` sorted by (key, value).
func JSON(w io.Writer, events []event.Event) error {
	out := make([]jsonEvent, len(events))
	for i, ev := range events {
		out[i] = toJSONEvent(ev)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// CSV writes events as a CSV table with the columns
// `id, parent_id, created_at, author, text`. Meta tuples beyond
// `author` are not represented; for full meta, use JSON or Markdown.
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

// Event writes a single event in the human-readable detail layout used
// by `fngr event N` (ID / Parent / Date / Text / Meta).
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

// FlatStream is the streaming counterpart to Flat. It writes one line per
// event as the iterator yields. The first error from seq aborts and is
// returned.
func FlatStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	for ev, err := range seq {
		if err != nil {
			return err
		}
		line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// CSVStream is the streaming counterpart to CSV. It writes the header
// followed by one row per event from seq.
func CSVStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "parent_id", "created_at", "author", "text"}); err != nil {
		return err
	}
	for ev, err := range seq {
		if err != nil {
			cw.Flush()
			return err
		}
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		if err := cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Text,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// JSONStream is the streaming counterpart to JSON. It writes a JSON array
// where each element is encoded individually, so the full serialized blob
// is never held in memory. On error mid-stream the array is still closed
// with "]\n" so any captured output is syntactically valid JSON.
func JSONStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	if _, err := fmt.Fprint(w, "["); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("  ", "  ")

	first := true
	var streamErr error
	for ev, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
		if !first {
			if _, werr := fmt.Fprint(w, ","); werr != nil {
				return werr
			}
		}
		if _, werr := fmt.Fprint(w, "\n  "); werr != nil {
			return werr
		}
		if err := enc.Encode(toJSONEvent(ev)); err != nil {
			return err
		}
		first = false
	}
	if !first {
		if _, err := fmt.Fprint(w, "\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(w, "]\n"); err != nil {
		return err
	}
	return streamErr
}

// EventsStream dispatches a streaming render. Tree is rejected because it
// requires the full slice for parent-child topology; callers that want
// tree must use Events with a materialized []Event.
func EventsStream(w io.Writer, format string, seq iter.Seq2[event.Event, error]) error {
	switch format {
	case FormatCSV:
		return CSVStream(w, seq)
	case FormatJSON:
		return JSONStream(w, seq)
	case FormatFlat:
		return FlatStream(w, seq)
	case FormatMarkdown:
		return MarkdownStream(w, seq)
	case FormatTree:
		return fmt.Errorf("EventsStream: tree format requires the full slice; use Events instead")
	default:
		return FlatStream(w, seq)
	}
}
