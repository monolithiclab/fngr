package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"runtime"
	"slices"
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
		Title:     text,
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

// TestTree_OrphanedChildren pins the marker on events whose parent exists
// but is not in the result set — `fngr -n 2` where the parent fell outside
// the window. Rendering them flush left claimed they were roots, which the
// CSV of the same query contradicted.
func TestTree_OrphanedChildren(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	missingParent := int64(99)
	another := int64(100)
	events := []event.Event{
		makeEvent(1, &missingParent, "Filtered child", "2026-04-10", "nicolas"),
		makeEvent(2, &another, "Another orphan", "2026-04-11", "nicolas"),
	}

	want := "" +
		"⋯└─ 1   Apr 10 2026 12.00am  nicolas  Filtered child\n" +
		"⋯└─ 2   Apr 11 2026 12.00am  nicolas  Another orphan\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree orphaned children:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTree_OrphanSubtreeAligns checks that an orphan's own descendants
// indent under the marker rather than under column zero: orphanConnector is
// four display columns, so orphanBlank must be four spaces.
func TestTree_OrphanSubtreeAligns(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	missingParent := int64(99)
	id1 := int64(1)
	id2 := int64(2)
	events := []event.Event{
		makeEvent(1, &missingParent, "Orphan root", "2026-04-10", "nicolas"),
		makeEvent(2, &id1, "Child of orphan", "2026-04-10", "nicolas"),
		makeEvent(3, &id2, "Grandchild", "2026-04-10", "nicolas"),
		makeEvent(4, &id1, "Second child", "2026-04-10", "nicolas"),
	}

	want := "" +
		"⋯└─ 1   Apr 10 2026 12.00am  nicolas  Orphan root\n" +
		"    ├─ 2   Apr 10 2026 12.00am  nicolas  Child of orphan\n" +
		"    │  └─ 3   Apr 10 2026 12.00am  nicolas  Grandchild\n" +
		"    └─ 4   Apr 10 2026 12.00am  nicolas  Second child\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree orphan subtree:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTree_RootAfterOrphanResetsPrefix guards the buffer reuse in
// treeWriter: the prefix is truncated between roots, so a real root
// following an orphan must not inherit the orphan's indent.
func TestTree_RootAfterOrphanResetsPrefix(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	missingParent := int64(99)
	id2 := int64(2)
	events := []event.Event{
		makeEvent(1, &missingParent, "Orphan", "2026-04-10", "nicolas"),
		makeEvent(2, nil, "True root", "2026-04-11", "nicolas"),
		makeEvent(3, &id2, "Its child", "2026-04-11", "nicolas"),
	}

	want := "" +
		"⋯└─ 1   Apr 10 2026 12.00am  nicolas  Orphan\n" +
		"2   Apr 11 2026 12.00am  nicolas  True root\n" +
		"└─ 3   Apr 11 2026 12.00am  nicolas  Its child\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree root after orphan:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTree_CycleStillRendersEveryEvent is the M2 blackout guard. A cyclic
// parent chain leaves its members with no root above them; Tree used to find
// no roots at all, write nothing and return nil, so `fngr` on such a database
// printed an empty journal and exited 0. Every event must appear, and the
// walk must come back.
func TestTree_CycleStillRendersEveryEvent(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	id1, id2 := int64(1), int64(2)
	events := []event.Event{
		makeEvent(1, &id2, "First half", "2026-04-10", "nicolas"),
		makeEvent(2, &id1, "Second half", "2026-04-11", "nicolas"),
	}

	// Event 1 is reached first and drawn as an orphan — its parent is real
	// but not above it — and 2 hangs off it. Recursing from 2 back into 1
	// stops at the visited check, so 1 is not drawn twice.
	want := "" +
		"⋯└─ 1   Apr 10 2026 12.00am  nicolas  First half\n" +
		"    └─ 2   Apr 11 2026 12.00am  nicolas  Second half\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree over a cycle:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTree_CycleBesideRealRoots checks the sweep does not disturb the events
// that do have roots, and that a self-parenting event — the smallest possible
// cycle — is rendered rather than dropped.
func TestTree_CycleBesideRealRoots(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	id1, id3 := int64(1), int64(3)
	events := []event.Event{
		makeEvent(1, nil, "True root", "2026-04-10", "nicolas"),
		makeEvent(2, &id1, "Its child", "2026-04-10", "nicolas"),
		makeEvent(3, &id3, "Its own parent", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  True root\n" +
		"└─ 2   Apr 10 2026 12.00am  nicolas  Its child\n" +
		"⋯└─ 3   Apr 11 2026 12.00am  nicolas  Its own parent\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree with a self-parent:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestTree_CycleWriteError covers the error path out of the sweep, which the
// roots loop cannot reach: a failing write on a line only the sweep emits.
func TestTree_CycleWriteError(t *testing.T) {
	t.Parallel()
	id1, id2 := int64(1), int64(2)
	events := []event.Event{
		makeEvent(1, &id2, "First half", "2026-04-10", "nicolas"),
		makeEvent(2, &id1, "Second half", "2026-04-11", "nicolas"),
	}

	wantErr := errors.New("write failed")
	if err := Tree(&failWriter{failOn: 1, err: wantErr}, events); !errors.Is(err, wantErr) {
		t.Errorf("Tree over a cycle with a failing write = %v, want %v", err, wantErr)
	}
}

// TestTree_WriteError covers the failure paths of the recursion. Each node is
// exactly one Write, so failOn selects which line breaks: 1 is a root, 2 a
// child (the error must climb back out of the recursion), 3 the last root
// (reached only after a subtree completes).
func TestTree_WriteError(t *testing.T) {
	t.Parallel()
	id1 := int64(1)
	events := []event.Event{
		makeEvent(1, nil, "Root", "2026-04-10", "nicolas"),
		makeEvent(2, &id1, "Child", "2026-04-10", "nicolas"),
		makeEvent(3, nil, "Second root", "2026-04-10", "nicolas"),
	}
	wantErr := errors.New("write failed")

	for failOn := 1; failOn <= 3; failOn++ {
		t.Run(fmt.Sprintf("line %d", failOn), func(t *testing.T) {
			t.Parallel()
			err := Tree(&failWriter{failOn: failOn, err: wantErr}, events)
			if !errors.Is(err, wantErr) {
				t.Errorf("Tree with a failing write %d = %v, want %v", failOn, err, wantErr)
			}
		})
	}
}

// chain builds a single-file parent→child chain n events deep, the shape
// that made Tree quadratic.
func chain(n int) []event.Event {
	events := make([]event.Event, n)
	for i := range events {
		var parent *int64
		if i > 0 {
			p := int64(i)
			parent = &p
		}
		events[i] = makeEvent(int64(i+1), parent, "e", "2026-04-10", "nicolas")
	}
	return events
}

// TestTree_DeepChainAllocationIsLinear is the H2 regression guard (see
// REVIEW.md#h2). The depths are 8× apart so the two regimes are far apart:
// linear predicts ~8×, quadratic ~60×. The 16× ceiling sits between them,
// clear of allocator noise in either direction — reverting to per-node string
// concatenation measures 45.9×.
//
// Not parallel: it measures process-wide allocation.
func TestTree_DeepChainAllocationIsLinear(t *testing.T) {
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))

	// TotalAlloc is cumulative, so no GC is needed to make the delta
	// meaningful; ReadMemStats already flushes the per-P caches.
	measure := func(depth int) uint64 {
		events := chain(depth)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := Tree(io.Discard, events); err != nil {
			t.Fatalf("Tree(depth=%d): %v", depth, err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	const shallow, deep = 500, 4000
	small := measure(shallow)
	large := measure(deep)

	if large > 16*small {
		t.Errorf("depth %d allocated %d bytes vs %d at depth %d (%.1f× for an 8× "+
			"depth increase); Tree looks quadratic again",
			deep, large, small, shallow, float64(large)/float64(small))
	}
}

func BenchmarkTree_DeepChain(b *testing.B) {
	events := chain(5000)
	b.ReportAllocs()
	for b.Loop() {
		if err := Tree(io.Discard, events); err != nil {
			b.Fatalf("Tree: %v", err)
		}
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
	for _, want := range []string{"ID:     2", "Parent: 1", "Date:", "Title:  child entry", "Meta:", "author=alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("Event output missing %q; got:\n%s", want, got)
		}
	}
}

func TestEvent_DetailWithoutParentOrMeta(t *testing.T) {
	t.Parallel()
	ev := event.Event{ID: 7, Title: "lone entry"}

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
		{"markdown", func(s string) bool { return strings.HasPrefix(s, "## ") }},
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
		// One event is one object, not a one-element array — `jq '.title'`
		// has to answer.
		{"json", func(s string) bool { return strings.HasPrefix(s, "{\n") }},
		{"csv", func(s string) bool { return strings.HasPrefix(s, "id,parent_id,") }},
		{"md", func(s string) bool { return strings.HasPrefix(s, "## ") }},
		{"markdown", func(s string) bool { return strings.HasPrefix(s, "## ") }},
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
		Title:     "x",
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
		Title:     "x",
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

	wantHeader := "id,parent_id,created_at,author,title,body"
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

// failWriter fails the failOn-th Write call (1-based), letting tests target
// a specific write in a multi-write renderer deterministically regardless of
// payload size. Earlier writes succeed.
type failWriter struct {
	calls, failOn int
	err           error
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failOn {
		return 0, w.err
	}
	return len(p), nil
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
	if err := FlatStream(&fast, slicedSeq(events)); err != nil {
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
	if err := CSVStream(&fast, slicedSeq(events)); err != nil {
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
	if err := JSONStream(&b, slicedSeq(events)); err != nil {
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
	if err := JSONStream(&b, slicedSeq(nil)); err != nil {
		t.Fatalf("JSONStream: %v", err)
	}
	got := strings.TrimSpace(b.String())
	if got != "[]" {
		t.Errorf("empty stream produced %q, want %q", got, "[]")
	}
}

// parityCases are the event sets the buffered/streaming parity tests below
// run over: nothing, one row, and a set with a parent link and two authors.
func parityCases() map[string][]event.Event {
	parent := int64(1)
	return map[string][]event.Event{
		"empty": nil,
		"one":   {makeEvent(1, nil, "a", "2026-04-10", "alice")},
		"many": {
			makeEvent(1, nil, "a", "2026-04-10", "alice"),
			makeEvent(2, &parent, "b", "2026-04-11", "alice"),
			makeEvent(3, nil, "c", "2026-04-12", "bob"),
		},
	}
}

// TestEventsStream_MatchesEvents is the point of there being two dispatchers:
// which one a command reaches for depends on whether it could stream, not on
// what the user asked for, so `fngr --format=json` and
// `fngr event 1 -t --format=json` over the same rows have to diff clean. They
// did not — the streaming JSON put each `,` on a line of its own and left a
// blank line before the closing bracket. Every streamable format now delegates
// to its streaming form, so this holds by construction; the test is what says
// the delegation is the requirement rather than an implementation detail free
// to be undone. Tree is absent because it has no streaming form.
func TestEventsStream_MatchesEvents(t *testing.T) {
	t.Parallel()
	for _, format := range []string{FormatFlat, FormatJSON, FormatCSV, FormatMarkdown} {
		for name, events := range parityCases() {
			t.Run(format+"/"+name, func(t *testing.T) {
				t.Parallel()
				var buffered, streamed bytes.Buffer
				if err := Events(&buffered, format, events); err != nil {
					t.Fatalf("Events: %v", err)
				}
				if err := EventsStream(&streamed, format, slicedSeq(events)); err != nil {
					t.Fatalf("EventsStream: %v", err)
				}
				if buffered.String() != streamed.String() {
					t.Errorf("EventsStream differs from Events:\n--- Events ---\n%s\n--- EventsStream ---\n%s",
						buffered.String(), streamed.String())
				}
			})
		}
	}
}

// TestJSONStream_MatchesMarshalIndent pins the array layout JSONStream
// assembles by hand against the one it has to reproduce. `fngr --format=json`
// has always emitted MarshalIndent's shape and the import side reads it back,
// so the separators written around the encoder are a contract rather than a
// style — and comparing the two dispatchers cannot say so, since JSON *is*
// JSONStream and agrees with it whatever they both write.
func TestJSONStream_MatchesMarshalIndent(t *testing.T) {
	t.Parallel()
	for name, events := range parityCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Non-nil so an empty set marshals to `[]` rather than `null`.
			records := make([]jsonEvent, 0, len(events))
			for _, ev := range events {
				records = append(records, toJSONEvent(ev))
			}
			want, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				t.Fatalf("MarshalIndent: %v", err)
			}

			var got bytes.Buffer
			if err := JSONStream(&got, slicedSeq(events)); err != nil {
				t.Fatalf("JSONStream: %v", err)
			}
			if got.String() != string(want)+"\n" {
				t.Errorf("JSONStream:\n--- got ---\n%s\n--- want ---\n%s\n", got.String(), want)
			}
		})
	}
}

// TestJSON_WriteError covers the slice adapter JSON streams over. A failed
// write abandons the array, and that has to stop the sequence too rather than
// keep feeding events to a writer that is finished with them.
func TestJSON_WriteError(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "a", "2026-04-10", "alice"),
		makeEvent(2, nil, "b", "2026-04-11", "alice"),
	}
	wantErr := errors.New("boom")

	if err := JSON(&failWriter{failOn: 1, err: wantErr}, events); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
}

// TestJSONEvent_IsAnObject pins the shape `jq '.title'` needs. Against the
// one-element array this used to emit, every field query returned null.
func TestJSONEvent_IsAnObject(t *testing.T) {
	t.Parallel()
	ev := makeEvent(1, nil, "hi", "2026-04-10", "nicolas")

	var b bytes.Buffer
	if err := JSONEvent(&b, &ev); err != nil {
		t.Fatalf("JSONEvent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON object: %v\noutput:\n%s", err, b.String())
	}
	if parsed["title"] != "hi" {
		t.Errorf("title = %v, want %q; output:\n%s", parsed["title"], "hi", b.String())
	}
	if !strings.HasSuffix(b.String(), "\n") {
		t.Error("JSONEvent missing trailing newline")
	}
}

// TestJSONEvent_WriteError covers the encode path's only failure mode.
func TestJSONEvent_WriteError(t *testing.T) {
	t.Parallel()
	ev := makeEvent(1, nil, "hi", "2026-04-10", "nicolas")
	wantErr := errors.New("boom")

	if err := JSONEvent(&failWriter{failOn: 1, err: wantErr}, &ev); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
}

// TestCanonical covers the alias table both dispatchers and the Kong enums
// read. `md` stays canonical; `markdown` has to resolve to it, and anything
// else has to pass through untouched so the dispatchers keep their defaults.
func TestCanonical(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{FormatMarkdown, FormatMarkdown},
		{"markdown", FormatMarkdown},
		{FormatJSON, FormatJSON},
		{"", ""},
		{"nonsense", "nonsense"},
	}
	for _, tt := range tests {
		if got := Canonical(tt.in); got != tt.want {
			t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Each vocabulary has to offer an alias exactly when it offers the format
	// the alias resolves to — not simply every alias, which would push one
	// into a vocabulary that cannot render it. The two vocabularies are
	// deliberately different (`tree`/`flat` need a set of events, `text` is the
	// one-event view), so an alias for `text` in ListFormats would parse,
	// resolve, miss every case in EventsStream's switch and fall through to
	// flat: the user asks for one format and gets another at exit 0. The other
	// direction is the harmless half — a missing alias is refused at parse time
	// however well Canonical resolves it. withAliases makes both unspellable.
	for name, vocab := range map[string][]string{"ListFormats": ListFormats, "EventFormats": EventFormats} {
		for alias, canonical := range formatAliases {
			if slices.Contains(vocab, alias) != slices.Contains(vocab, canonical) {
				t.Errorf("%s = %v: %q and its canonical %q must be offered together or not at all",
					name, vocab, alias, canonical)
			}
		}
	}
}

// No pinNow: this asserts the error, not the stamp. Pinning from a parallel
// test writes the package-global nowFunc while other parallel tests are
// reading it — a real data race, and the only one in the package.
func TestFlatStream_WriteError(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "x", "2026-04-10", "alice")}
	wantErr := errors.New("boom")

	err := FlatStream(&failWriter{failOn: 1, err: wantErr}, slicedSeq(events))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestCSVStream_WriteError(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "x", "2026-04-10", "alice")}
	wantErr := errors.New("boom")

	// csv.Writer buffers; the underlying failure surfaces on Flush and is
	// returned via cw.Error().
	err := CSVStream(&failWriter{failOn: 1, err: wantErr}, slicedSeq(events))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestCSVStream_PropagatesError(t *testing.T) {
	t.Parallel()
	parent := int64(1)
	events := []event.Event{makeEvent(2, &parent, "ok", "2026-04-10", "alice")}
	wantErr := errors.New("boom")

	var b bytes.Buffer
	err := CSVStream(&b, errorAtSeq(events, 1, wantErr))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
	// The successfully-yielded row is flushed before the error returns.
	if !strings.Contains(b.String(), "ok") {
		t.Errorf("partial output not flushed:\n%s", b.String())
	}
}

func TestJSONStream_WriteError(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "a", "2026-04-10", "alice"),
		makeEvent(2, nil, "b", "2026-04-11", "alice"),
	}
	wantErr := errors.New("boom")

	// A lead and an object per event plus the closing bracket: five writes for
	// a two-event stream, and every one must propagate. The count is exact
	// rather than an over-shoot so that the writes-per-event stay pinned —
	// the failOn=6 case below is what makes an unnoticed extra write fail here
	// instead of quietly dropping out of the loop's reach.
	const writes = 5
	for failOn := 1; failOn <= writes; failOn++ {
		err := JSONStream(&failWriter{failOn: failOn, err: wantErr}, slicedSeq(events))
		if !errors.Is(err, wantErr) {
			t.Errorf("failOn=%d: err = %v, want boom", failOn, err)
		}
	}
	if err := JSONStream(&failWriter{failOn: writes + 1, err: wantErr}, slicedSeq(events)); err != nil {
		t.Errorf("failOn=%d: err = %v, want nil — the stream makes exactly %d writes", writes+1, err, writes)
	}
}

func TestEvent_WriteError(t *testing.T) {
	t.Parallel()
	parent := int64(1)
	ev := makeEvent(2, &parent, "child entry", "2026-04-10", "alice")
	ev.Body = "a body line"
	wantErr := errors.New("boom")

	// Event with parent + body + meta makes seven Fprintf writes; failing
	// each in turn exercises every branch (id, parent, date, title, body,
	// meta header, meta row).
	for failOn := 1; failOn <= 7; failOn++ {
		err := Event(&failWriter{failOn: failOn, err: wantErr}, &ev)
		if !errors.Is(err, wantErr) {
			t.Errorf("failOn=%d: err = %v, want boom", failOn, err)
		}
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
		{"markdown", func(s string) bool { return strings.HasPrefix(s, "## ") }},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := EventsStream(&b, tt.format, slicedSeq(events)); err != nil {
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
	if err := EventsStream(io.Discard, "tree", slicedSeq(nil)); err == nil {
		t.Error("EventsStream(tree, ...) expected an error")
	}
}

func TestEvent_BodyEmpty(t *testing.T) {
	t.Parallel()
	ev := &event.Event{
		ID:        1,
		Title:     "headline",
		Body:      "",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}
	var b bytes.Buffer
	if err := Event(&b, ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "Title:  headline") {
		t.Errorf("missing title line: %q", got)
	}
	// Body block is omitted when empty; meta should still appear.
	if !strings.Contains(got, "Meta:") || !strings.Contains(got, "author=alice") {
		t.Errorf("expected Meta block: %q", got)
	}
}

func TestEvent_BodyPresent(t *testing.T) {
	t.Parallel()
	ev := &event.Event{
		ID:        1,
		Title:     "headline",
		Body:      "the full story\nspans two lines",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}
	var b bytes.Buffer
	if err := Event(&b, ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "Title:  headline") {
		t.Errorf("missing title line: %q", got)
	}
	if !strings.Contains(got, "\n\nthe full story\nspans two lines\n") {
		t.Errorf("body block missing or mislaid: %q", got)
	}
}

func TestJSON_TitleBodyShape(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "the body",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
	}}
	var b bytes.Buffer
	if err := JSON(&b, events); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, `"title": "headline"`) || !strings.Contains(got, `"body": "the body"`) {
		t.Errorf("missing title/body in JSON: %q", got)
	}
	if strings.Contains(got, `"text"`) {
		t.Errorf("JSON should not have text field: %q", got)
	}
}

func TestCSV_TitleBodyColumns(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "body line",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}}
	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "id,parent_id,created_at,author,title,body\n") {
		t.Errorf("unexpected CSV header: %q", got)
	}
	if !strings.Contains(got, ",alice,headline,body line\n") {
		t.Errorf("missing title/body in CSV row: %q", got)
	}
}

func TestFlat_ShowsTitleNotBody(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "secret body",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}}
	var b bytes.Buffer
	if err := Flat(&b, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "headline") {
		t.Errorf("title not in flat: %q", got)
	}
	if strings.Contains(got, "secret body") {
		t.Errorf("body leaked into flat: %q", got)
	}
}
