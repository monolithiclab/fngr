package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func makeEvent(id int64, parentID *int64, text string, date string, author string) event.Event {
	// Parse in local time so fixtures render at 12.00am regardless of the
	// runner's timezone (FormatRelative converts via t.Local()).
	t, _ := time.ParseInLocation("2006-01-02", date, time.Local)
	return event.Event{
		ID:        id,
		ParentID:  parentID,
		Text:      text,
		CreatedAt: t,
		Meta:      []parse.Meta{{Key: event.MetaKeyAuthor, Value: author}},
	}
}

func renderTreeString(t *testing.T, events []event.Event) string {
	t.Helper()
	var b bytes.Buffer
	if err := Tree(&b, events); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	return b.String()
}

// pinNow forces formatLocalStamp to use a fixed anchor so tree/flat output
// is deterministic across runs and across calendar years. Tests calling
// pinNow must NOT use t.Parallel(), since nowFunc is package-global.
func pinNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = prev })
}

func TestTree_Empty(t *testing.T) {
	t.Parallel()
	if got := renderTreeString(t, nil); got != "" {
		t.Errorf("Tree(nil) = %q, want %q", got, "")
	}
	if got := renderTreeString(t, []event.Event{}); got != "" {
		t.Errorf("Tree([]) = %q, want %q", got, "")
	}
}

func TestTree_FlatList(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	events := []event.Event{
		makeEvent(1, nil, "First event", "2026-04-10", "nicolas"),
		makeEvent(2, nil, "Second event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  First event\n" +
		"2   Apr 11 2026 12.00am  nicolas  Second event\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree flat list:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_NestedChildren(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	p1 := int64(1)
	events := []event.Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, &p1, "First child", "2026-04-10", "nicolas"),
		makeEvent(3, &p1, "Second child", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  Parent event\n" +
		"\u251c\u2500 2   Apr 10 2026 12.00am  nicolas  First child\n" +
		"\u2514\u2500 3   Apr 11 2026 12.00am  nicolas  Second child\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree nested:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_DeepNesting(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	p1 := int64(1)
	p2 := int64(2)
	events := []event.Event{
		makeEvent(1, nil, "Root", "2026-04-10", "nicolas"),
		makeEvent(2, &p1, "Child", "2026-04-10", "nicolas"),
		makeEvent(3, &p2, "Grandchild", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  Root\n" +
		"\u2514\u2500 2   Apr 10 2026 12.00am  nicolas  Child\n" +
		"   \u2514\u2500 3   Apr 11 2026 12.00am  nicolas  Grandchild\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree deep nesting:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_MixedRootsAndChildren(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	p1 := int64(1)
	p2 := int64(2)
	events := []event.Event{
		makeEvent(1, nil, "Sprint 12 #work", "2026-04-10", "nicolas"),
		makeEvent(2, &p1, "Planning meeting", "2026-04-10", "nicolas"),
		makeEvent(4, &p2, "Decided on architecture", "2026-04-10", "nicolas"),
		makeEvent(3, &p1, "Deploy v2.0 #ops", "2026-04-11", "nicolas"),
		makeEvent(5, nil, "Lunch with Sarah", "2026-04-12", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  Sprint 12 #work\n" +
		"\u251c\u2500 2   Apr 10 2026 12.00am  nicolas  Planning meeting\n" +
		"\u2502  \u2514\u2500 4   Apr 10 2026 12.00am  nicolas  Decided on architecture\n" +
		"\u2514\u2500 3   Apr 11 2026 12.00am  nicolas  Deploy v2.0 #ops\n" +
		"5   Apr 12 2026 12.00am  nicolas  Lunch with Sarah\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree mixed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_OrphanedChildren(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	missingParent := int64(99)
	another := int64(100)
	events := []event.Event{
		makeEvent(1, &missingParent, "Filtered child", "2026-04-10", "nicolas"),
		makeEvent(2, &another, "Another orphan", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  Filtered child\n" +
		"2   Apr 11 2026 12.00am  nicolas  Another orphan\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree orphaned children:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestFlat(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	p1 := int64(1)
	events := []event.Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, &p1, "Child event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  Parent event\n" +
		"2   Apr 11 2026 12.00am  nicolas  Child event\n"

	var b bytes.Buffer
	if err := Flat(&b, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	got := b.String()
	if got != want {
		t.Errorf("Flat:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestEvent_DetailIncludesParentAndMeta(t *testing.T) {
	t.Parallel()
	parent := int64(1)
	ev := makeEvent(2, &parent, "child entry", "2026-04-10", "alice")

	var b bytes.Buffer
	if err := Event(&b, &ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	for _, want := range []string{"ID:     2", "Parent: 1", "Date:", "Text:   child entry", "Meta:", "author=alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("Event output missing %q; got:\n%s", want, got)
		}
	}
}

func TestEvent_DetailWithoutParentOrMeta(t *testing.T) {
	t.Parallel()
	ev := event.Event{ID: 7, Text: "lone entry"}

	var b bytes.Buffer
	if err := Event(&b, &ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	if strings.Contains(got, "Parent:") {
		t.Errorf("Event output should omit Parent line; got:\n%s", got)
	}
	if strings.Contains(got, "Meta:") {
		t.Errorf("Event output should omit Meta line; got:\n%s", got)
	}
}

func TestEvents_Dispatch(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	events := []event.Event{makeEvent(1, nil, "hi", "2026-04-10", "nicolas")}

	tests := []struct {
		format string
		check  func(string) bool
	}{
		{"tree", func(s string) bool { return strings.Contains(s, "1   Apr 10 2026 12.00am  nicolas  hi") }},
		{"flat", func(s string) bool { return strings.Contains(s, "1   Apr 10 2026 12.00am  nicolas  hi") }},
		{"json", func(s string) bool { return strings.HasPrefix(s, "[\n") }},
		{"csv", func(s string) bool { return strings.HasPrefix(s, "id,parent_id,") }},
		{"md", func(s string) bool { return strings.HasPrefix(s, "## ") }},
		{"unknown", func(s string) bool { return strings.Contains(s, "1   Apr 10 2026 12.00am  nicolas  hi") }},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := Events(&b, tt.format, events); err != nil {
				t.Fatalf("Events: %v", err)
			}
			if !tt.check(b.String()) {
				t.Errorf("Events(%q) unexpected output:\n%s", tt.format, b.String())
			}
		})
	}
}

func TestSingleEvent_Dispatch(t *testing.T) {
	t.Parallel()
	ev := makeEvent(1, nil, "hi", "2026-04-10", "nicolas")

	tests := []struct {
		format string
		check  func(string) bool
	}{
		{"text", func(s string) bool { return strings.Contains(s, "ID:     1") }},
		{"json", func(s string) bool { return strings.HasPrefix(s, "[\n") }},
		{"csv", func(s string) bool { return strings.HasPrefix(s, "id,parent_id,") }},
		{"md", func(s string) bool { return strings.HasPrefix(s, "## ") }},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := SingleEvent(&b, tt.format, &ev); err != nil {
				t.Fatalf("SingleEvent: %v", err)
			}
			if !tt.check(b.String()) {
				t.Errorf("SingleEvent(%q) unexpected output:\n%s", tt.format, b.String())
			}
		})
	}
}

func TestEventAuthor(t *testing.T) {
	t.Parallel()
	ev := event.Event{Meta: []parse.Meta{
		{Key: "author", Value: "nicolas"},
		{Key: "tag", Value: "work"},
	}}
	if got := eventAuthor(ev); got != "nicolas" {
		t.Errorf("eventAuthor = %q, want %q", got, "nicolas")
	}
	if got := eventAuthor(event.Event{}); got != "" {
		t.Errorf("eventAuthor(empty) = %q, want %q", got, "")
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := JSON(&b, events); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := b.String()

	var parsed []json.RawMessage
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("JSON produced invalid JSON: %v\noutput:\n%s", err, got)
	}

	if len(parsed) != 1 {
		t.Errorf("JSON produced %d items, want 1", len(parsed))
	}

	if !strings.HasSuffix(got, "\n") {
		t.Error("JSON output missing trailing newline")
	}
}

func TestJSON_MultiValuePerKey(t *testing.T) {
	t.Parallel()

	ev := event.Event{
		ID:        1,
		Text:      "x",
		CreatedAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
			{Key: "tag", Value: "deploy"},
			{Key: "author", Value: "alice"},
		},
	}
	var buf bytes.Buffer
	if err := JSON(&buf, []event.Event{ev}); err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Re-marshal compactly so we can substring-match without caring about
	// MarshalIndent's whitespace.
	var parsed []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, buf.String())
	}
	compact, err := json.Marshal(parsed)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	got := string(compact)
	want := `"meta":[["author","alice"],["tag","deploy"],["tag","ops"]]`
	if !strings.Contains(got, want) {
		t.Errorf("got %s\nwant substring %s", got, want)
	}
}

func TestJSON_NoMetaOmitsField(t *testing.T) {
	t.Parallel()

	ev := event.Event{
		ID:        1,
		Text:      "x",
		CreatedAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
	}
	var buf bytes.Buffer
	if err := JSON(&buf, []event.Event{ev}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, `"meta"`) {
		t.Errorf("expected no meta field for event with no meta; got:\n%s", got)
	}
}

func TestCSV(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	if len(lines) != 2 {
		t.Errorf("CSV produced %d lines, want 2; output:\n%s", len(lines), got)
	}

	wantHeader := "id,parent_id,created_at,author,text"
	if lines[0] != wantHeader {
		t.Errorf("CSV header = %q, want %q", lines[0], wantHeader)
	}
}

func TestCSV_SpecialChars(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, `text with "quotes" and, commas`, "2026-04-10", "nicolas"),
		makeEvent(2, nil, "=formula", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	if len(lines) != 3 {
		t.Fatalf("CSV produced %d lines, want 3; output:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[1], `"text with ""quotes"" and, commas"`) {
		t.Errorf("csv.Writer should quote/escape special chars, got line: %s", lines[1])
	}
	if !strings.Contains(lines[2], "=formula") {
		t.Errorf("expected raw =formula (no sanitization prefix), got line: %s", lines[2])
	}
}

func staticSeq(events []event.Event) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func errorAtSeq(events []event.Event, errAt int, err error) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for i, ev := range events {
			if i == errAt {
				yield(event.Event{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if errAt >= len(events) {
			yield(event.Event{}, err)
		}
	}
}

func TestFlatStream_MatchesFlat(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	events := []event.Event{
		makeEvent(1, nil, "first", "2026-04-10", "alice"),
		makeEvent(2, nil, "second", "2026-04-11", "alice"),
	}

	var slow, fast bytes.Buffer
	if err := Flat(&slow, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	if err := FlatStream(&fast, staticSeq(events)); err != nil {
		t.Fatalf("FlatStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("FlatStream != Flat\n--- Flat ---\n%s\n--- Stream ---\n%s", slow.String(), fast.String())
	}
}

func TestCSVStream_MatchesCSV(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "x", "2026-04-10", "alice"),
		makeEvent(2, nil, "y", "2026-04-11", "alice"),
	}

	var slow, fast bytes.Buffer
	if err := CSV(&slow, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	if err := CSVStream(&fast, staticSeq(events)); err != nil {
		t.Fatalf("CSVStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("CSVStream != CSV\n--- CSV ---\n%s\n--- Stream ---\n%s", slow.String(), fast.String())
	}
}

func TestJSONStream_ProducesValidJSON(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "a", "2026-04-10", "alice"),
		makeEvent(2, nil, "b", "2026-04-11", "alice"),
	}

	var b bytes.Buffer
	if err := JSONStream(&b, staticSeq(events)); err != nil {
		t.Fatalf("JSONStream: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(b.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, b.String())
	}
	if len(parsed) != 2 {
		t.Errorf("got %d entries, want 2; output:\n%s", len(parsed), b.String())
	}
	if !strings.HasSuffix(b.String(), "\n") {
		t.Error("JSONStream missing trailing newline")
	}
}

func TestJSONStream_EmptyProducesEmptyArray(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := JSONStream(&b, staticSeq(nil)); err != nil {
		t.Fatalf("JSONStream: %v", err)
	}
	got := strings.TrimSpace(b.String())
	if got != "[]" {
		t.Errorf("empty stream produced %q, want %q", got, "[]")
	}
}

func TestJSONStream_ClosesOnError(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "ok", "2026-04-10", "alice")}
	wantErr := errors.New("boom")

	var b bytes.Buffer
	err := JSONStream(&b, errorAtSeq(events, 1, wantErr))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(b.String()), "]") {
		t.Errorf("JSONStream did not close array; got:\n%s", b.String())
	}
}

func TestEventsStream_Dispatch(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "x", "2026-04-10", "alice")}

	tests := []struct {
		format string
		check  func(string) bool
	}{
		{"flat", func(s string) bool { return strings.Contains(s, "x") }},
		{"json", func(s string) bool { return strings.HasPrefix(s, "[") }},
		{"csv", func(s string) bool { return strings.HasPrefix(s, "id,parent_id,") }},
		{"md", func(s string) bool { return strings.HasPrefix(s, "## ") }},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := EventsStream(&b, tt.format, staticSeq(events)); err != nil {
				t.Fatalf("EventsStream(%q): %v", tt.format, err)
			}
			if !tt.check(b.String()) {
				t.Errorf("EventsStream(%q) unexpected output:\n%s", tt.format, b.String())
			}
		})
	}
}

func TestEventsStream_RejectsTree(t *testing.T) {
	t.Parallel()
	if err := EventsStream(io.Discard, "tree", staticSeq(nil)); err == nil {
		t.Error("EventsStream(tree, ...) expected an error")
	}
}
