package internal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

const dateFormat = "2006-01-02"

func metaValue(meta []Meta, key string) string {
	for _, m := range meta {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

func eventAuthor(ev Event) string {
	return metaValue(ev.Meta, MetaKeyAuthor)
}

func RenderTree(w io.Writer, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	byID := make(map[int64]int, len(events))
	children := make(map[int64][]int64)
	var roots []int64

	for i, ev := range events {
		byID[ev.ID] = i
		if ev.ParentID == nil {
			roots = append(roots, ev.ID)
		} else {
			children[*ev.ParentID] = append(children[*ev.ParentID], ev.ID)
		}
	}

	for _, id := range roots {
		if err := renderNode(w, events, byID, children, id, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func renderNode(w io.Writer, events []Event, byID map[int64]int, children map[int64][]int64, id int64, linePrefix, childPrefix string) error {
	idx := byID[id]
	ev := events[idx]
	date := ev.CreatedAt.Format(dateFormat)
	author := eventAuthor(ev)

	if _, err := fmt.Fprintf(w, "%s%-4d%s  %s  %s\n", linePrefix, ev.ID, date, author, ev.Text); err != nil {
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

func RenderFlat(w io.Writer, events []Event) error {
	for _, ev := range events {
		date := ev.CreatedAt.Format(dateFormat)
		author := eventAuthor(ev)
		if _, err := fmt.Fprintf(w, "%-4d%s  %s  %s\n", ev.ID, date, author, ev.Text); err != nil {
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

func RenderJSON(w io.Writer, events []Event) error {
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
			CreatedAt: ev.CreatedAt.Format(time.RFC3339),
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

func RenderCSV(w io.Writer, events []Event) error {
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
			ev.CreatedAt.Format(time.RFC3339),
			eventAuthor(ev),
			ev.Text,
		})
	}
	cw.Flush()
	return cw.Error()
}

func RenderEvent(w io.Writer, event *Event) error {
	if _, err := fmt.Fprintf(w, "ID:     %d\n", event.ID); err != nil {
		return err
	}
	if event.ParentID != nil {
		if _, err := fmt.Fprintf(w, "Parent: %d\n", *event.ParentID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Date:   %s\n", event.CreatedAt.Format("2006-01-02 15:04:05")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Text:   %s\n", event.Text); err != nil {
		return err
	}

	if len(event.Meta) > 0 {
		if _, err := fmt.Fprintln(w, "Meta:"); err != nil {
			return err
		}
		for _, m := range event.Meta {
			if _, err := fmt.Fprintf(w, "  %s=%s\n", m.Key, m.Value); err != nil {
				return err
			}
		}
	}

	return nil
}
