# Markdown output format Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--format=md` output to `fngr list` and `fngr event N` that renders events as a Markdown digest grouped by local date.

**Architecture:** All rendering logic lives in a new file `internal/render/markdown.go` (functions `Markdown`, `MarkdownStream`, `renderMarkdownEvent`). The format identifier `FormatMarkdown = "md"` is appended to `ListFormats` and `EventFormats` slices in `render.go`, and switch arms are added to the three dispatchers (`Events`, `SingleEvent`, `EventsStream`). Kong's `${LIST_FORMATS}` / `${EVENT_FORMATS}` interpolation auto-extends the accepted CLI flag values; the only CLI-side change is bringing two `--format` help strings up to date.

**Tech Stack:** Go 1.26, `iter.Seq2[event.Event, error]` for streaming, `slices.SortFunc`+`cmp.Compare` for deterministic meta order, `timefmt.LayoutToday` and `timefmt.DateFormat` for time/date rendering.

**Spec:** `docs/superpowers/specs/2026-04-22-markdown-format-design.md`

**File map:**
- Create: `internal/render/markdown.go` — `Markdown`, `MarkdownStream`, `renderMarkdownEvent`
- Create: `internal/render/markdown_test.go` — unit tests for the above
- Modify: `internal/render/render.go` — add `FormatMarkdown`, extend `ListFormats`/`EventFormats`, add three switch arms
- Modify: `cmd/fngr/list.go` — update `--format` help string
- Modify: `cmd/fngr/event.go` — update `--format` help string
- Modify: `cmd/fngr/dispatch_test.go` — two new dispatch rows
- Modify: `CLAUDE.md` — describe new format in render bullet
- Modify: `README.md` — add a markdown example
- Modify: `docs/superpowers/roadmap.md` — move Markdown bullet from "Output format polish" to "Done"

---

## Task 1: `Markdown` (buffered) + `renderMarkdownEvent`

Implements the buffered list path and the shared per-event renderer. All format-spec behavior (date headers, em-dash separator, multi-line continuation, meta line, sort, CRLF normalization, verbatim specials) lives in `renderMarkdownEvent`. `Markdown` is a thin loop.

**Files:**
- Create: `internal/render/markdown.go`
- Create: `internal/render/markdown_test.go`

- [ ] **Step 1.1: Create `markdown.go` with stub functions**

The file declares `Markdown` and `renderMarkdownEvent` returning nil so the test file in Step 1.2 compiles and the failing-test step (1.3) can fail on assertions rather than build errors.

```go
// internal/render/markdown.go
package render

import (
	"io"

	"github.com/monolithiclab/fngr/internal/event"
)

// Markdown renders events as a Markdown digest grouped by local date.
// Section headers (## YYYY-MM-DD) are emitted when the local date changes
// between consecutive events. Iteration order is preserved.
func Markdown(w io.Writer, events []event.Event) error {
	return nil
}

// renderMarkdownEvent writes one event's bullet (and optional continuation
// lines and meta line). It updates *lastDate; when the local date of ev
// differs, it first writes a date header (with a leading blank line if
// *lastDate is non-empty).
func renderMarkdownEvent(w io.Writer, lastDate *string, ev event.Event) error {
	return nil
}
```

Run: `go build ./internal/render/...`
Expected: PASS

- [ ] **Step 1.2: Write the failing tests in `markdown_test.go`**

```go
// internal/render/markdown_test.go
package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

// mdEvent builds an event.Event with a fixed timestamp, body, and optional
// meta tuples. ID is a fixed dummy since markdown output never references it.
func mdEvent(ts time.Time, text string, meta ...parse.Meta) event.Event {
	return event.Event{
		ID:        1,
		Text:      text,
		CreatedAt: ts,
		Meta:      meta,
	}
}

func TestMarkdown_Empty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Markdown(&b, nil); err != nil {
		t.Fatalf("Markdown(nil): %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("Markdown(nil) = %q, want empty", got)
	}
	b.Reset()
	if err := Markdown(&b, []event.Event{}); err != nil {
		t.Fatalf("Markdown([]): %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("Markdown([]) = %q, want empty", got)
	}
}

func TestMarkdown_SingleEvent_NoMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "quick standup")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — quick standup\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_SingleEvent_WithMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "quick standup",
		parse.Meta{Key: "location", Value: "cafe"},
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — quick standup\n  author=nicolas location=cafe\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultipleEventsSameDate(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "first"),
		mdEvent(time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local), "second"),
	}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — first\n- 9.32pm — second\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultipleEventsDifferentDates(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "today"),
		mdEvent(time.Date(2026, 4, 21, 21, 32, 0, 0, time.Local), "yesterday"),
	}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — today\n\n## 2026-04-21\n\n- 9.32pm — yesterday\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultilineBody(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "line one\nline two\nline three",
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 9.32pm — line one\n  line two\n  line three\n  author=nicolas\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_EmptyBody(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — \n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_EmptyBodyWithMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "",
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — \n  author=nicolas\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_VerbatimSpecials(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "# heading [link](url) *bold* _underscore_")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.Contains(b.String(), "# heading [link](url) *bold* _underscore_") {
		t.Errorf("Markdown stripped or escaped specials:\n%s", b.String())
	}
}

func TestMarkdown_CRLFNormalization(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "first\r\nsecond")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if strings.Contains(b.String(), "\r") {
		t.Errorf("Markdown leaked \\r:\n%q", b.String())
	}
	want := "## 2026-04-22\n\n- 8.15am — first\n  second\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_RespectsInputOrder(t *testing.T) {
	t.Parallel()
	earlier := mdEvent(time.Date(2026, 4, 21, 11, 4, 0, 0, time.Local), "earlier")
	later := mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "later")

	tests := []struct {
		name   string
		events []event.Event
		first  string
		second string
	}{
		{"desc", []event.Event{later, earlier}, "## 2026-04-22", "## 2026-04-21"},
		{"asc", []event.Event{earlier, later}, "## 2026-04-21", "## 2026-04-22"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := Markdown(&b, tt.events); err != nil {
				t.Fatalf("Markdown: %v", err)
			}
			i1 := strings.Index(b.String(), tt.first)
			i2 := strings.Index(b.String(), tt.second)
			if i1 < 0 || i2 < 0 || i1 >= i2 {
				t.Errorf("expected %q before %q\noutput:\n%s", tt.first, tt.second, b.String())
			}
		})
	}
}

func TestMarkdown_LocalTimezoneBucketing(t *testing.T) {
	// Mutates package-global time.Local — must NOT use t.Parallel.
	prev := time.Local
	t.Cleanup(func() { time.Local = prev })

	time.Local = time.FixedZone("PT", -7*3600)

	// 02:00 UTC on April 22 is 19:00 PT on April 21.
	ts := time.Date(2026, 4, 22, 2, 0, 0, 0, time.UTC)
	events := []event.Event{mdEvent(ts, "near midnight UTC")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.Contains(b.String(), "## 2026-04-21") {
		t.Errorf("expected ## 2026-04-21 (PT view), got:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "7.00pm") {
		t.Errorf("expected 7.00pm time, got:\n%s", b.String())
	}
}

```

(`errors` and `iter` imports are added in Task 2 when streaming tests need them.)

- [ ] **Step 1.3: Run tests to verify they fail**

Run: `go test ./internal/render/ -run 'TestMarkdown_'`
Expected: FAIL — `Markdown` returns nil with empty buffer for every case, so all the non-empty assertions fail.

- [ ] **Step 1.4: Implement `Markdown` and `renderMarkdownEvent`**

Replace `internal/render/markdown.go` body with the real implementation (drop the dummy `_ =` lines added in Step 1.1):

```go
// internal/render/markdown.go
package render

import (
	"cmp"
	"fmt"
	"io"
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
		pairs := make([]string, 0, len(ev.Meta))
		for _, m := range ev.Meta {
			pairs = append(pairs, fmt.Sprintf("%s=%s", m.Key, m.Value))
		}
		slices.SortFunc(pairs, cmp.Compare)
		if _, err := fmt.Fprintf(w, "  %s\n", strings.Join(pairs, " ")); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 1.5: Run tests to verify they pass**

Run: `go test ./internal/render/ -run 'TestMarkdown_'`
Expected: PASS, all 12 tests (including subtests).

- [ ] **Step 1.6: Run full test suite and lint**

Run: `make test`
Expected: PASS, coverage report shows `Markdown` and `renderMarkdownEvent` at high coverage (~100%).

Run: `make lint`
Expected: PASS.

- [ ] **Step 1.7: Run /simplify before commit**

Invoke the `/simplify` skill on the current diff. Apply any actionable findings inline. Re-run `make test && make lint` after edits.

- [ ] **Step 1.8: Commit**

```bash
git add internal/render/markdown.go internal/render/markdown_test.go
git commit -m "$(cat <<'EOF'
feat(render): add Markdown buffered renderer

Markdown(w, events) groups events by local date with ## YYYY-MM-DD
headers. Body lines split on \n, multi-line bodies use 2-space
continuation indent. Meta tuples render as space-separated key=value
on a separate continuation line, sorted alphabetically. Verbatim text
(no markdown escaping). Empty body keeps the bullet with trailing
em-dash + space.

Helper renderMarkdownEvent owns the per-event format and the lastDate
state machine; Markdown is a thin loop. Designed to be reusable from
the upcoming MarkdownStream variant.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `MarkdownStream`

Streaming counterpart that propagates errors mid-stream and produces output identical to the buffered variant for any sequence the upstream query yields.

**Files:**
- Modify: `internal/render/markdown.go` — add `MarkdownStream`
- Modify: `internal/render/markdown_test.go` — add streaming tests

- [ ] **Step 2.1: Write the failing tests**

Append to `internal/render/markdown_test.go`. First add `errors` and `iter` to the import block:

```go
import (
	"bytes"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)
```

Then append:

```go
// markdownSeq returns an iter.Seq2 that yields events with nil errors. Local
// to markdown_test.go; the existing staticSeq in render_test.go is fine to
// rely on, but a duplicate keeps the file self-contained.
func markdownSeq(events []event.Event) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// markdownErrAt yields events through index errAt-1, then yields an error.
func markdownErrAt(events []event.Event, errAt int, err error) iter.Seq2[event.Event, error] {
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

func TestMarkdownStream_Empty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := MarkdownStream(&b, markdownSeq(nil)); err != nil {
		t.Fatalf("MarkdownStream: %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("empty stream produced %q, want empty", got)
	}
}

func TestMarkdownStream_MatchesMarkdown(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "first",
			parse.Meta{Key: "author", Value: "nicolas"},
		),
		mdEvent(time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local), "second\nsecond line"),
		mdEvent(time.Date(2026, 4, 21, 11, 4, 0, 0, time.Local), "previous day",
			parse.Meta{Key: "tag", Value: "ship"},
		),
	}

	var slow, fast bytes.Buffer
	if err := Markdown(&slow, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if err := MarkdownStream(&fast, markdownSeq(events)); err != nil {
		t.Fatalf("MarkdownStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("MarkdownStream != Markdown\n--- Markdown ---\n%s\n--- Stream ---\n%s",
			slow.String(), fast.String())
	}
}

func TestMarkdownStream_PropagatesError(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "ok"),
	}
	wantErr := errors.New("boom")

	var b bytes.Buffer
	err := MarkdownStream(&b, markdownErrAt(events, 1, wantErr))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if !strings.Contains(b.String(), "ok") {
		t.Errorf("partial output not flushed:\n%s", b.String())
	}
}
```

- [ ] **Step 2.2: Run tests to verify they fail**

Run: `go test ./internal/render/ -run 'TestMarkdownStream_'`
Expected: FAIL — `MarkdownStream` is undefined; build error.

- [ ] **Step 2.3: Implement `MarkdownStream`**

Add the `iter` import to `internal/render/markdown.go` and append:

```go
import (
	"cmp"
	"fmt"
	"io"
	"iter"
	"slices"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// ... existing code unchanged ...

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
```

- [ ] **Step 2.4: Run tests to verify they pass**

Run: `go test ./internal/render/ -run 'TestMarkdown'`
Expected: PASS, all Markdown + MarkdownStream tests.

- [ ] **Step 2.5: Run full test suite and lint**

Run: `make test`
Expected: PASS.

Run: `make lint`
Expected: PASS.

Per-function coverage check: `go test ./internal/render/ -coverprofile=/tmp/cov.out && go tool cover -func=/tmp/cov.out | grep -E 'Markdown|renderMarkdownEvent'`
Expected: `Markdown`, `MarkdownStream`, `renderMarkdownEvent` at 100% (or near — `renderMarkdownEvent` has `if err != nil` returns on every Fprintf which can't all fire without a faulty writer).

- [ ] **Step 2.6: Run /simplify before commit**

Invoke the `/simplify` skill on the current diff. Apply any actionable findings inline. Re-run `make test && make lint` after edits.

- [ ] **Step 2.7: Commit**

```bash
git add internal/render/markdown.go internal/render/markdown_test.go
git commit -m "$(cat <<'EOF'
feat(render): add MarkdownStream

Streaming counterpart to Markdown. Reuses renderMarkdownEvent so
output is byte-identical to the buffered variant for the same input
sequence. Propagates the first error from seq and stops; partial
output already flushed remains in the writer.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Wire `md` into render dispatchers, Kong enums, and dispatch tests

Surfaces the new format through the CLI: `--format=md` accepted by both `fngr list` and `fngr event N --format=md`.

**Files:**
- Modify: `internal/render/render.go` — add `FormatMarkdown` constant, extend `ListFormats`/`EventFormats`, add three switch arms
- Modify: `cmd/fngr/list.go` — update `--format` help string
- Modify: `cmd/fngr/event.go` — update `--format` help string
- Modify: `cmd/fngr/dispatch_test.go` — two new dispatch rows
- Modify: `internal/render/render_test.go` — extend `TestEventsStream_Dispatch` with `md` case

- [ ] **Step 3.1: Write failing dispatcher test**

Open `internal/render/render_test.go` and find `TestEventsStream_Dispatch` (around line 537). Add an `md` case:

```go
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
	// ... rest unchanged
}
```

Also add a buffered-dispatcher test for the `md` arm. Find `TestEvents_Dispatch` (around line 217) and `TestSingleEvent_Dispatch` (around line 246), then append cases to each. If their existing structure is a table-driven loop, add `{"md", func(s string) bool { return strings.HasPrefix(s, "## ") }}` analogously.

If those tests don't already cover `md`-equivalent format strings via a table, add a small focused test instead:

```go
func TestEvents_DispatchMarkdown(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "x", "2026-04-10", "alice")}
	var b bytes.Buffer
	if err := Events(&b, FormatMarkdown, events); err != nil {
		t.Fatalf("Events(md): %v", err)
	}
	if !strings.HasPrefix(b.String(), "## ") {
		t.Errorf("Events(md) did not produce a markdown header:\n%s", b.String())
	}
}

func TestSingleEvent_DispatchMarkdown(t *testing.T) {
	t.Parallel()
	ev := makeEvent(1, nil, "x", "2026-04-10", "alice")
	var b bytes.Buffer
	if err := SingleEvent(&b, FormatMarkdown, &ev); err != nil {
		t.Fatalf("SingleEvent(md): %v", err)
	}
	if !strings.HasPrefix(b.String(), "## ") {
		t.Errorf("SingleEvent(md) did not produce a markdown header:\n%s", b.String())
	}
}
```

- [ ] **Step 3.2: Run tests to verify they fail**

Run: `go test ./internal/render/ -run 'Dispatch'`
Expected: FAIL — `FormatMarkdown` is undefined; build error.

- [ ] **Step 3.3: Add `FormatMarkdown` constant + slice entries + dispatcher arms**

Edit `internal/render/render.go`:

In the format constants block:

```go
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
```

In `Events`:

```go
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
```

In `SingleEvent`:

```go
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
```

In `EventsStream`:

```go
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
```

- [ ] **Step 3.4: Update Kong help strings**

Edit `cmd/fngr/list.go`, line 16:

```go
Format  string `help:"Output format: tree (default), flat, json, csv, md." enum:"${LIST_FORMATS}" default:"${LIST_FORMAT_DEFAULT}"`
```

Edit `cmd/fngr/event.go`, line 30:

```go
Format string `help:"Output format: text (default), json, csv, md." enum:"${EVENT_FORMATS}" default:"${EVENT_FORMAT_DEFAULT}"`
```

- [ ] **Step 3.5: Add dispatch test rows**

Edit `cmd/fngr/dispatch_test.go`, in the `cases` table inside `TestKongDispatch_AllCommands`, add two rows just after the existing `event-show-json` row:

```go
{name: "list-md", argv: []string{"list", "--format", "md"}, isTTY: true, want: ""},
{name: "event-show-md", argv: []string{"event", "show", "1", "--format", "md"}, isTTY: true, want: ""},
```

- [ ] **Step 3.6: Run tests to verify they pass**

Run: `make test`
Expected: PASS, including the new render dispatch tests and the two new dispatch rows.

Run: `make lint`
Expected: PASS.

- [ ] **Step 3.7: Manually verify end-to-end**

Build and exercise:

```bash
make build
./build/fngr --db /tmp/fngr-md-test.db add --author=tester "first event"
./build/fngr --db /tmp/fngr-md-test.db add --author=tester --meta location=home "second event"
./build/fngr --db /tmp/fngr-md-test.db --format=md
./build/fngr --db /tmp/fngr-md-test.db event 1 --format=md
rm /tmp/fngr-md-test.db
```

Expected output of the third command (approximately):
```
## 2026-04-22

- 9.32pm — second event
  author=tester location=home
- 9.32pm — first event
  author=tester
```

Expected output of the fourth command:
```
## 2026-04-22

- 9.32pm — first event
  author=tester
```

- [ ] **Step 3.8: Run /simplify before commit**

Invoke the `/simplify` skill on the current diff. Apply any actionable findings inline. Re-run `make test && make lint` after edits.

- [ ] **Step 3.9: Commit**

```bash
git add internal/render/render.go internal/render/render_test.go cmd/fngr/list.go cmd/fngr/event.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): wire --format=md into list and event N

Adds FormatMarkdown to ListFormats/EventFormats and switch arms in
Events/SingleEvent/EventsStream so --format=md flows through both
the buffered (Tree carve-out) and streaming list paths plus the
single-event path. Updates the two --format help strings so the
hand-written enumeration matches the real Kong enum.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Documentation

**Files:**
- Modify: `CLAUDE.md` — extend the `internal/render/render.go` bullet to mention markdown
- Modify: `README.md` — add a markdown example to Quick start
- Modify: `docs/superpowers/roadmap.md` — move Markdown bullet to "Done"

- [ ] **Step 4.1: Update CLAUDE.md**

Find the `internal/render/render.go` bullet and extend its description. The current bullet ends with the meta-tuple description. Append a sentence about markdown:

Locate the line containing `Meta in JSON output is `[[key, value], ...]`...` (around the bottom of the render bullet) and add after it:

```markdown
  Markdown output groups events by local date as `## YYYY-MM-DD` sections;
  bullets are `- <time> — <body>` with multi-line bodies indented two
  spaces and meta on a separate continuation line of space-separated
  `key=value` tokens.
```

- [ ] **Step 4.2: Update README.md**

Read the current README to find the Quick start section and add a markdown example. Insert near the existing format examples (json/csv) a paragraph and code block:

```markdown
### Markdown digest

```bash
fngr --format=md
fngr --from 2026-04-15 --to 2026-04-22 --format=md > week.md
```

Output groups by local date with `## YYYY-MM-DD` headers and bullet
entries; multi-line bodies and meta render as indented continuation
lines. Designed for paste-into-wiki workflows; for round-trip use
`--format=json`.
```

- [ ] **Step 4.3: Update roadmap.md**

In `docs/superpowers/roadmap.md`:

Remove the entire `## Output format polish` section (it becomes empty after this).

In the `## Done` section, replace the existing `add --format=json` Done bullet's neighborhood by appending a new bullet:

```markdown
- **Markdown output** (`--format=md`) — `fngr list` and `fngr event N`
  emit a Markdown digest grouped by local date: one `## YYYY-MM-DD`
  header per date followed by `- <time> — <body>` bullets. Multi-line
  bodies and meta render as 2-space-indented continuation lines.
```

- [ ] **Step 4.4: Run /simplify before commit**

Invoke the `/simplify` skill on the docs diff. Apply any actionable findings inline.

- [ ] **Step 4.5: Commit**

```bash
git add CLAUDE.md README.md docs/superpowers/roadmap.md
git commit -m "$(cat <<'EOF'
docs: README + CLAUDE.md + roadmap for --format=md

CLAUDE.md gains a markdown sentence in the render bullet. README
Quick start gains a markdown digest example showing list and date-
range usage. Roadmap moves the Markdown format bullet from Output
format polish to Done; the section is now empty so it's removed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist (after all tasks complete)

- [ ] `--format=md` accepted by `fngr list` and `fngr event N` (manual smoke test).
- [ ] `make ci` passes locally (codefix + format + lint + test).
- [ ] Per-function coverage on `Markdown`, `MarkdownStream`, `renderMarkdownEvent` at or near 100%.
- [ ] `docs/superpowers/roadmap.md` Output format polish section removed; new Done entry present.
- [ ] `CLAUDE.md` render bullet mentions markdown.
- [ ] `README.md` Quick start has a markdown example.
- [ ] No regression in existing format tests (tree/flat/json/csv/text).
