package internal

import (
	"testing"
	"time"
)

func ptr(i int64) *int64 { return &i }

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
		makeEvent(2, ptr(1), "First child", "2026-04-10", "nicolas"),
		makeEvent(3, ptr(1), "Second child", "2026-04-11", "nicolas"),
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
		makeEvent(2, ptr(1), "Child", "2026-04-10", "nicolas"),
		makeEvent(3, ptr(2), "Grandchild", "2026-04-11", "nicolas"),
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
		makeEvent(2, ptr(1), "Planning meeting", "2026-04-10", "nicolas"),
		makeEvent(4, ptr(2), "Decided on architecture", "2026-04-10", "nicolas"),
		makeEvent(3, ptr(1), "Deploy v2.0 #ops", "2026-04-11", "nicolas"),
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
		makeEvent(2, ptr(1), "Child event", "2026-04-11", "nicolas"),
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
