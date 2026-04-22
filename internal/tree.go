package internal

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
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

// RenderTree renders events as an ASCII tree with parent-child indentation.
// Root events (ParentID == nil) appear at the top level; children are indented
// with box-drawing characters similar to git log --graph.
func RenderTree(events []Event) string {
	if len(events) == 0 {
		return ""
	}

	// Index events by ID for lookup and build parent-to-children mapping.
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

	var b bytes.Buffer
	for _, id := range roots {
		renderNode(&b, events, byID, children, id, "", "")
	}
	return b.String()
}

// renderNode writes one event line and recursively renders its children.
// linePrefix is the prefix for this node's own line (e.g. "├─ ").
// childPrefix is the prefix inherited by this node's children for continuation
// lines (e.g. "│  ").
func renderNode(b *bytes.Buffer, events []Event, byID map[int64]int, children map[int64][]int64, id int64, linePrefix, childPrefix string) {
	idx := byID[id]
	ev := events[idx]
	date := ev.CreatedAt.Format(dateFormat)
	author := eventAuthor(ev)

	fmt.Fprintf(b, "%s%-4d%s  %s  %s\n", linePrefix, ev.ID, date, author, ev.Text)

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
		renderNode(b, events, byID, children, kidID, childPrefix+connector, childPrefix+continuation)
	}
}

// RenderFlat renders events as a flat chronological list with no tree structure.
func RenderFlat(events []Event) string {
	if len(events) == 0 {
		return ""
	}

	var b bytes.Buffer
	for _, ev := range events {
		date := ev.CreatedAt.Format(dateFormat)
		author := eventAuthor(ev)
		fmt.Fprintf(&b, "%-4d%s  %s  %s\n", ev.ID, date, author, ev.Text)
	}
	return b.String()
}

type jsonEvent struct {
	ID        int64               `json:"id"`
	ParentID  *int64              `json:"parent_id,omitempty"`
	Text      string              `json:"text"`
	CreatedAt string              `json:"created_at"`
	Meta      map[string][]string `json:"meta,omitempty"`
}

func RenderJSON(events []Event) string {
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
	data, _ := json.MarshalIndent(out, "", "  ")
	return string(data) + "\n"
}

func csvSanitize(s string) string {
	if len(s) > 0 {
		switch s[0] {
		case '=', '+', '-', '@', '\t', '\r':
			return "'" + s
		}
	}
	return s
}

func RenderCSV(events []Event) string {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"id", "parent_id", "created_at", "author", "text"})
	for _, ev := range events {
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		author := eventAuthor(ev)
		_ = w.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.Format(time.RFC3339),
			csvSanitize(author),
			csvSanitize(ev.Text),
		})
	}
	w.Flush()
	return b.String()
}
