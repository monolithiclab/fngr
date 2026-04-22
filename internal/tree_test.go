package internal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func makeEvent(id int64, parentID *int64, text string, date string, author string) Event {
	t, _ := time.Parse("2006-01-02", date)
	return Event{
		ID:        id,
		ParentID:  parentID,
		Text:      text,
		CreatedAt: t,
		Meta:      []Meta{{Key: MetaKeyAuthor, Value: author}},
	}
}

func TestRenderTree_Empty(t *testing.T) {
	if got := RenderTree(nil); got != "" {
		t.Errorf("RenderTree(nil) = %q, want %q", got, "")
	}
	if got := RenderTree([]Event{}); got != "" {
		t.Errorf("RenderTree([]) = %q, want %q", got, "")
	}
}

func TestRenderTree_FlatList(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "First event", "2026-04-10", "nicolas"),
		makeEvent(2, nil, "Second event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  First event\n" +
		"2   2026-04-11  nicolas  Second event\n"

	got := RenderTree(events)
	if got != want {
		t.Errorf("RenderTree flat list:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTree_NestedChildren(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "First child", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(1)), "Second child", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Parent event\n" +
		"\u251c\u2500 2   2026-04-10  nicolas  First child\n" +
		"\u2514\u2500 3   2026-04-11  nicolas  Second child\n"

	got := RenderTree(events)
	if got != want {
		t.Errorf("RenderTree nested:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTree_DeepNesting(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Root", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Child", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(2)), "Grandchild", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Root\n" +
		"\u2514\u2500 2   2026-04-10  nicolas  Child\n" +
		"   \u2514\u2500 3   2026-04-11  nicolas  Grandchild\n"

	got := RenderTree(events)
	if got != want {
		t.Errorf("RenderTree deep nesting:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderTree_MixedRootsAndChildren(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Sprint 12 #work", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Planning meeting", "2026-04-10", "nicolas"),
		makeEvent(4, new(int64(2)), "Decided on architecture", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(1)), "Deploy v2.0 #ops", "2026-04-11", "nicolas"),
		makeEvent(5, nil, "Lunch with Sarah", "2026-04-12", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Sprint 12 #work\n" +
		"\u251c\u2500 2   2026-04-10  nicolas  Planning meeting\n" +
		"\u2502  \u2514\u2500 4   2026-04-10  nicolas  Decided on architecture\n" +
		"\u2514\u2500 3   2026-04-11  nicolas  Deploy v2.0 #ops\n" +
		"5   2026-04-12  nicolas  Lunch with Sarah\n"

	got := RenderTree(events)
	if got != want {
		t.Errorf("RenderTree mixed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderFlat(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Child event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Parent event\n" +
		"2   2026-04-11  nicolas  Child event\n"

	got := RenderFlat(events)
	if got != want {
		t.Errorf("RenderFlat:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMetaValue(t *testing.T) {
	meta := []Meta{
		{Key: "author", Value: "nicolas"},
		{Key: "tag", Value: "work"},
	}

	if got := metaValue(meta, "author"); got != "nicolas" {
		t.Errorf("metaValue(author) = %q, want %q", got, "nicolas")
	}
	if got := metaValue(meta, "tag"); got != "work" {
		t.Errorf("metaValue(tag) = %q, want %q", got, "work")
	}
	if got := metaValue(meta, "missing"); got != "" {
		t.Errorf("metaValue(missing) = %q, want %q", got, "")
	}
	if got := metaValue(nil, "author"); got != "" {
		t.Errorf("metaValue(nil, author) = %q, want %q", got, "")
	}
}

func TestRenderJSON(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	got := RenderJSON(events)

	// Must be valid JSON.
	var parsed []json.RawMessage
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("RenderJSON produced invalid JSON: %v\noutput:\n%s", err, got)
	}

	// Must contain exactly 1 item.
	if len(parsed) != 1 {
		t.Errorf("RenderJSON produced %d items, want 1", len(parsed))
	}

	// Must end with a trailing newline.
	if !strings.HasSuffix(got, "\n") {
		t.Error("RenderJSON output missing trailing newline")
	}
}

func TestRenderCSV(t *testing.T) {
	events := []Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	got := RenderCSV(events)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	// Must have header + 1 data row = 2 lines total.
	if len(lines) != 2 {
		t.Errorf("RenderCSV produced %d lines, want 2; output:\n%s", len(lines), got)
	}

	// Header must be correct.
	wantHeader := "id,parent_id,created_at,author,text"
	if lines[0] != wantHeader {
		t.Errorf("RenderCSV header = %q, want %q", lines[0], wantHeader)
	}
}
