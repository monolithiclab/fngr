# Markdown output format (`--format=md`) — Design

**Status:** Draft
**Date:** 2026-04-22
**Roadmap item:** "Output format polish — Markdown format" (`docs/superpowers/roadmap.md`)

## Goal

Add a `--format=md` output to `fngr list` and `fngr event N` that renders
events as a Markdown digest: one `## YYYY-MM-DD` section header per local
date, followed by a bullet list of `- <time> — <body>` entries grouped
under it. Designed for prose-friendly export (paste into a wiki, generate
a daily/weekly digest, send to a markdown renderer).

## Non-goals

- Markdown is not a round-trippable wire format. Users who need lossless
  export use `--format=json` or `--format=csv`. Markdown will not escape
  user-supplied text.
- No tree topology in the output. Cross-date trees can't sensibly nest
  under a single date header; markdown is intentionally flat.
- No new CLI flags. The format is a pure rendering choice; existing
  `--from`/`--to`/`-S`/`-r`/`-n` flags govern selection and order.

## Output format

### Per event

```
- <time> — <body-line-1>
  <body-line-2>
  <body-line-3>
  <key>=<value> <key>=<value> ...
```

- **Time**: `9.32pm` style, fixed format. Always `timefmt.LayoutToday`
  (the time-of-day-only layout), because the section header already
  carries the date. No relative formatting.
- **Separator**: literal ` — ` (space, em-dash, space) between time and
  body's first line.
- **Body**: first line follows ` — ` on the bullet line; subsequent
  lines indented by exactly two spaces (markdown list-item continuation).
  Splitting is on `\n`; a trailing `\r` on each split chunk is stripped
  so Windows-origin text doesn't leak.
- **Meta line** (omitted entirely when meta is empty): an additional
  continuation line, indented two spaces, with each meta tuple rendered
  as `key=value`, tokens separated by single spaces. Sorted alphabetically
  by `(key, value)` for determinism (matches `JSON`'s sort).
- **Empty body**: bullet renders as `- <time> — \n` (literal trailing
  space after em-dash). Meta line follows on a separate line if present.
- **Verbatim text**: body and meta values emit verbatim. No markdown
  escaping. Markdown special characters (`#`, `*`, `_`, `[`, …) and
  meta values containing `=` or spaces pass through unchanged.

### Date headers

- `## YYYY-MM-DD` followed by one blank line, then the bullet list.
- Date is computed in **local time** from `ev.CreatedAt.Local()` —
  matches `formatLocalStamp` and `Event` text-format precedent. An event
  at `2026-04-22T01:30:00Z` viewed from PT (UTC-7) buckets into the
  `2026-04-21` section.
- The first event always gets a header (the renderer tracks `lastDate`
  starting at `""`, which differs from any real date). Subsequent events
  get a new header only when their local date differs from the prior
  event's. The output therefore preserves whatever order the upstream
  query produced (descending by default, ascending with `-r`).
- Sections are separated by a single blank line: `\n## next-date\n\n`.

### Empty input

Empty event list / empty stream produces no output at all (no leading
newline, no header). Matches Flat/JSON/CSV behavior.

### Worked example

```
## 2026-04-22

- 8.15am — quick standup
  author=nicolas location=cafe
- 9.32pm — long meeting recap
  next steps captured
  follow-up Friday
  author=nicolas location=cafe person=sarah tag=ops

## 2026-04-21

- 11.04am — bug fix landed
  author=nicolas tag=ship
```

## Architecture

### Constants and dispatcher wiring (`internal/render/render.go`)

```go
const FormatMarkdown = "md"

var ListFormats   = []string{FormatTree, FormatFlat, FormatJSON, FormatCSV, FormatMarkdown}
var EventFormats  = []string{FormatText, FormatJSON, FormatCSV, FormatMarkdown}
```

Switch arms added to all three dispatchers:

- `Events`: `case FormatMarkdown: return Markdown(w, events)`
- `SingleEvent`: `case FormatMarkdown: return Markdown(w, []event.Event{*ev})`
- `EventsStream`: `case FormatMarkdown: return MarkdownStream(w, seq)`

### New functions

```go
// Markdown renders events as a Markdown digest grouped by local date.
// Section headers (## YYYY-MM-DD) are emitted when the local date changes
// between consecutive events. Iteration order is preserved.
func Markdown(w io.Writer, events []event.Event) error

// MarkdownStream is the streaming counterpart to Markdown.
func MarkdownStream(w io.Writer, seq iter.Seq2[event.Event, error]) error
```

Internal helper avoids duplication:

```go
// renderMarkdownEvent writes one event's bullet (and optional continuation
// lines and meta line). It updates *lastDate; when the local date of ev
// differs, it first writes a date header (with a leading blank line if
// *lastDate is non-empty).
func renderMarkdownEvent(w io.Writer, lastDate *string, ev event.Event) error
```

`Markdown` loops `events` calling `renderMarkdownEvent` with an
initially-empty `lastDate`; `MarkdownStream` does the same against the
`iter.Seq2` while propagating any error from the seq immediately.

### CLI wiring

The Kong `enum:"${LIST_FORMATS}"` and `enum:"${EVENT_FORMATS}"`
interpolation already pulls from `render.ListFormats` /
`render.EventFormats` (via `kongVars` in `main.go`), so adding
`FormatMarkdown` to those slices automatically extends the accepted
values for both the `list` command's `--format` flag and the `event show`
verb's `--format` flag.

Two small one-line changes are still required: the `help:` strings on
`ListCmd.Format` (`list.go:16`) and `EventShowCmd.Format` (`event.go:30`)
enumerate accepted formats by hand and would otherwise drift from the
real enum. Update them to mention `md`.

The `if c.Format == render.FormatTree` carve-out in `list.go:38` is
unaffected; markdown flows through the streaming path like flat/json/csv.

## Edge cases

| Case                                | Behavior                                                                |
| ----------------------------------- | ----------------------------------------------------------------------- |
| Empty event list                    | Empty output                                                            |
| Single event                        | One date header + one bullet                                            |
| Empty body                          | `- <time> — \n` (trailing space), meta line follows if present          |
| Empty body and no meta              | Just `- <time> — \n`                                                    |
| Body with `\r\n` line endings       | Split on `\n`, strip trailing `\r` per line                             |
| Markdown specials in body           | Emit verbatim, no escaping                                              |
| Meta value with spaces or `=`       | Emit verbatim, no escaping                                              |
| Stream error mid-output             | Return error immediately; no closing structure required                 |
| Tree input topology                 | `parent_id` ignored; bullets emitted in iteration order                 |
| TZ change between near-midnight events | Date bucketing uses each event's own `Local()` date independently    |

## Testing

### Unit tests (`internal/render/markdown_test.go`, new file)

- `TestMarkdown_Empty` — empty slice → `""`
- `TestMarkdown_SingleEvent_NoMeta` — one event, no meta → header + bullet
- `TestMarkdown_SingleEvent_WithMeta` — mixed meta sorts alphabetically by
  `(key, value)`, renders as space-separated `key=value` continuation line
- `TestMarkdown_MultipleEventsSameDate` — two events on same local date →
  one header, two bullets
- `TestMarkdown_MultipleEventsDifferentDates` — two events spanning two
  dates → two headers, blank-line separator between sections
- `TestMarkdown_MultilineBody` — embedded `\n` → continuation indent;
  meta line follows on its own indented line
- `TestMarkdown_EmptyBody` — body=`""` → `- <time> — \n` (and meta if any)
- `TestMarkdown_LocalTimezoneBucketing` — pin `time.Local` (via
  `t.Setenv("TZ", ...)` and `time.Local = time.Now().Location()` in a
  helper, OR a `nowFunc`-style hook); assert near-midnight UTC events
  bucket into the correct local-date section
- `TestMarkdown_RespectsInputOrder` — descending and ascending input
  each produce headers in the corresponding direction
- `TestMarkdown_VerbatimSpecials` — body containing `#`, `*`, `[`, `_`
  emits unchanged
- `TestMarkdown_CRLFNormalization` — body `"a\r\nb"` → bullet line `a`,
  continuation `  b` (no `\r` leak)
- `TestMarkdownStream_*` — mirror the buffered tests against the
  `iter.Seq2` API
- `TestMarkdownStream_StreamError` — yield one event then an error;
  assert partial output written and error returned

### Dispatch tests (`cmd/fngr/dispatch_test.go`)

- `list --format=md` row — confirms Kong Parse → Run wires through and
  `EventsStream`'s switch hits the markdown case
- `event <id> --format=md` row — confirms `SingleEvent`'s switch hits
  the markdown case via the `event show` path

### Coverage

100% target on `Markdown`, `MarkdownStream`, and `renderMarkdownEvent`.
Verified via `make test` after each commit per the project's per-function
coverage discipline.

## Open questions

None. All clarifying questions resolved during brainstorming.

## Out of scope (deliberate)

- **Round-trippable markdown** — markdown is for reading, not import.
  `--format=json` exists for round-trip.
- **Markdown escaping of user text** — see CSV's "no formula injection
  sanitization" precedent. Local single-user tool; user owns downstream
  pasting.
- **Tree-aware nested bullets** — explicitly rejected during brainstorming
  (Q1 follow-up). Markdown intentionally flattens.
- **Custom date header format** — ISO chosen for sortability and machine
  pairing with `--from`/`--to` and CSV. Cosmetic alternatives can be
  added later as flags if real demand emerges.
- **`@`/`#` shorthand for body-derived tags in the meta line** —
  considered, rejected in favor of uniform `key=value` for consistency.
- **Inline meta after body text** — considered, rejected in favor of a
  separate continuation line so meta doesn't compete visually with the
  prose.
- **ID display** — pure prose digest; users referencing events follow
  with `fngr event N` after consulting another format.
