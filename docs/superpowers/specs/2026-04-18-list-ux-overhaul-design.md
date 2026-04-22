# `list` UX overhaul — design

Sub-project **S1** of the [roadmap](../roadmap.md). Scope is the listing
experience: default command, default sort, time display, pagination, and
streaming.

The tool is pre-public, so this spec drops removed flags and renamed fields
outright instead of carrying compatibility shims.

## Goals

- `fngr` (no args) lists events. `list` stays as an explicit alias for
  discoverability and scripting clarity.
- Default sort flips to newest-first. `--sort asc|desc` is removed.
  `-r` / `--reverse` flips back to oldest-first.
- Tree and flat outputs use a relative-aware compact timestamp:
  - today → `9.32pm`
  - this calendar year → `Dec 09 9.32pm`
  - older → `Dec 09 2024 9.32pm`
  - period as the hour/minute separator, lowercase `am`/`pm`.
  - Event detail (`fngr event 5` once S2 lands; `fngr show 5` today) keeps
    the full ISO timestamp.
- Internal data flow streams events from SQLite to renderer, so memory stays
  flat for flat/csv/json regardless of result size. Tree must still buffer
  to compute parent/child topology.
- When stdout is a TTY, `list`/default invocation pipes through the user's
  `$PAGER` (fallback `less -FRX`). `--no-pager` opts out.

## Non-goals

- No auto-`--limit` when output is huge. The user opts in.
- No built-in pager. We use the system pager via a child process.
- No paging of `add`, `show`, `delete`, `meta`, `edit` output. Only `list`
  pipes through the pager.
- No relative dates as filter inputs (`--from yesterday`); that belongs to
  a future timefmt extension.

## Architecture

### `internal/event` — streaming + sort flip

- New `func ListSeq(ctx context.Context, db *sql.DB, opts ListOpts)
  iter.Seq2[Event, error]`. Reads event rows from SQLite, accumulates them
  into batches of 500, calls the existing chunked metadata loader on each
  batch, then yields events one at a time. Errors are surfaced through the
  second yielded value with a zero-value `Event`.
- `List` stays available and becomes a thin collector around `ListSeq`,
  used by Tree and any caller that needs the full slice.
- `ListOpts.Desc` is renamed to `Ascending bool`. Zero value (`false`)
  means descending = newest first. The CLI maps `--reverse` to
  `Ascending: true`.
- `Store` gains a `ListSeq` method delegating to the package function.

### `internal/render` — streaming + new time format

- Add `FlatStream(w io.Writer, seq iter.Seq2[event.Event, error]) error`,
  `CSVStream(w, seq) error`, `JSONStream(w, seq) error`.
  - `JSONStream` writes `[` then comma-separated encoded events
    (`json.Encoder.Encode` per event) then `]\n`. The whole serialized blob
    is never held in memory.
  - `FlatStream` and `CSVStream` write line-by-line.
- `EventsStream(w io.Writer, format string, seq) error` dispatcher selects
  the right `*Stream` function. Tree is not in the dispatcher; tree callers
  go through the existing `Events`/`Tree` path with a fully materialized
  slice.
- `formatLocalDate` is replaced by `formatLocalStamp(t time.Time, now time.Time) string`
  that returns the relative-aware form. `now` is injected so tests are
  deterministic. The function is consumed by `formatEventLine` (used by
  Tree and Flat).
- Event detail output (`render.Event`) is unchanged: keeps full ISO via the
  existing `formatLocalDateTime`.

### `internal/timefmt` — display layouts

Add three layout constants and a single helper:

```go
const (
    LayoutToday    = "3.04pm"            // 9.32pm
    LayoutThisYear = "Jan 02 3.04pm"     // Dec 09 9.32pm
    LayoutOlder    = "Jan 02 2006 3.04pm" // Dec 09 2024 9.32pm
)

// FormatRelative returns t formatted relative to now. Day boundary is local
// midnight; year boundary is January 1 in the local zone. am/pm are
// lowercased after Format.
func FormatRelative(t, now time.Time) string
```

`am`/`pm` come out uppercase from Go's `time.Format`; `FormatRelative`
lowercases them with `strings.ToLower` on the suffix portion only (cheaper
than lowercasing the whole string and avoids touching month names).

### `cmd/fngr` — default command, pager, flag changes

- `CLI.List` keeps the `cmd:""` tag and adds `default:"withargs"` so bare
  `fngr` dispatches to `ListCmd`.
- `ListCmd` flag changes:
  - Drop `Sort string`.
  - Add `Reverse bool` with `short:"r"` and `help:"Sort oldest first
    (default is newest first)."`.
  - Add `NoPager bool` with `help:"Disable the pager even when stdout is a
    TTY."`.
- `cmd/fngr/pager.go` — new file:

  ```go
  // withPager returns an ioStreams whose Out is the stdin of the user's
  // pager, plus a close function the caller MUST defer. When stdout is not
  // a TTY, when disabled is true, or when the pager fails to start,
  // withPager returns the original io and a no-op closer.
  func withPager(io ioStreams, disabled bool) (ioStreams, func() error)
  ```

  - TTY detection uses `golang.org/x/term.IsTerminal(int(f.Fd()))` against
    the `*os.File` underlying `io.Out`. If `io.Out` isn't an `*os.File`
    (tests pass `*bytes.Buffer`), no pager.
  - Pager command: `$PAGER` if set; else `less -FRX`. Tokenized with
    `strings.Fields` (no shell-quote handling). A `$PAGER` containing
    spaces inside quotes is not supported and falls outside scope.
  - On `exec.Command(...).Start()` failure: fall back silently to direct
    stdout. (Don't kill the command because the user has no pager.)
  - The closer waits for the pager to exit so output flushes before the
    process returns.
- `ListCmd.Run` (sketch; `ctx := context.Background()` and the existing
  `--from`/`--to` parsing are elided for brevity):

  ```go
  func (c *ListCmd) Run(s eventStore, io ioStreams) error {
      io, closePager := withPager(io, c.NoPager)
      defer closePager()  // closer warns to os.Stderr on failure; never promoted

      opts := c.toListOpts()  // small unexported helper, see below

      if c.Format == "tree" {
          events, err := s.List(ctx, opts)  // collects via ListSeq
          if err != nil { return err }
          return render.Tree(io.Out, events)
      }
      return render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts))
  }
  ```

  `toListOpts` is a new unexported method on `ListCmd` that builds an
  `event.ListOpts` from the flag fields (`Filter`, `From`/`To` with the
  existing `timefmt.ParseDate` + 1-day rollover, `Limit`, and
  `Ascending: c.Reverse`). Pulling it out of `Run` keeps the streaming /
  buffered branches readable.

- `eventStore` interface gains `ListSeq(ctx, opts) iter.Seq2[event.Event, error]`.

## Data flow

### `fngr -r --format json`, stdout is a TTY

1. Kong parses → `ListCmd{Reverse: true, Format: "json"}`.
2. `Run` calls `withPager(io, false)`. TTY detected → spawn `less -FRX`,
   wire its stdin to a pipe, return a wrapped `ioStreams{Out: pipeWriter}`.
3. `Run` calls `s.ListSeq(ctx, ListOpts{Ascending: true})`.
4. `render.EventsStream(io.Out, "json", seq)` writes `[`, then for each
   `(ev, err)` pair from `seq` it either bails on `err` or writes a comma +
   encoded event. Closes with `]\n`.
5. `Run` returns; deferred `closePager()` closes the pipe writer; `less`
   drains, exits, closer's `cmd.Wait()` returns.

### `fngr` (default), stdout not a TTY

1. Bare `fngr` dispatches to `ListCmd` with default flags.
2. `withPager` sees non-TTY → returns the original `ioStreams` and a
   no-op closer.
3. Tree path: `s.List` collects, `render.Tree` writes to `os.Stdout` directly.

## Error handling

- Pager start fails → write a single warning line to `os.Stderr` and
  proceed without a pager. Going through `os.Stderr` directly (instead of
  threading an `Err io.Writer` into `ioStreams`) is deliberate: extending
  `ioStreams` is a cross-cutting change that belongs to a separate sweep.
- Mid-stream SQL error → `ListSeq` yields `(Event{}, err)` and stops.
  Renderer closes its open structure first, *then* returns the error:
  - `JSONStream` appends `]\n` so a redirected file is still
    syntactically-valid JSON containing the events emitted before the
    failure.
  - `FlatStream` and `CSVStream` simply stop writing.

  The CLI returns the error and exits non-zero, so callers know the output
  is partial regardless.
- `closePager` error (pager process exited non-zero, broken pipe etc.) is
  reported to `os.Stderr` and not promoted to the command's exit code,
  matching `git`'s behaviour.

## Testing

- `internal/timefmt`:
  - Table test for `FormatRelative` covering: same instant as `now`,
    earlier today (`9.32pm`), yesterday (treated as this-year because day
    differs), distant this-year (`Dec 09 9.32pm`), prior year (`Dec 09 2024
    9.32pm`), midnight boundary (00:00 today vs 23:59 yesterday).
- `internal/event`:
  - `TestListSeq_Yields` verifies a small dataset is yielded in order and
    metadata is populated.
  - `TestListSeq_AcrossBatchBoundary` seeds 600 events, asserts all are
    yielded and meta is loaded for events on both sides of the 500
    boundary.
  - `TestListSeq_PropagatesError` injects a closed DB to force a row scan
    error and asserts the seq yields exactly one `(zero, err)` pair.
  - `TestList_Ascending` and `TestList_DescendingDefault` lock the new
    sort default.
- `internal/render`:
  - `TestFlatStream`, `TestCSVStream`, `TestJSONStream` produce identical
    output to the non-streaming versions on small inputs (golden compare).
  - `TestJSONStream_LargeInput` feeds 2000 events through a discarding
    writer and asserts max heap delta < N MB (skipped with `-short`).
  - `TestFormatEventLine_RelativeStamps` verifies the new column matches
    `FormatRelative` output.
- `cmd/fngr`:
  - Dispatch tests added for `fngr` (bare), `fngr -r`, `fngr --no-pager`,
    `fngr --format json` (verifies streaming path is exercised).
  - `pager_test.go`: spawn a fake pager (a small Go test binary or a shell
    script in a `t.TempDir()`) via `$PAGER`; assert (a) its stdin captured
    matches the rendered output, (b) `closePager` waits for it to exit.
  - `pager_test.go`: `withPager` returns a no-op closer when `io.Out` is a
    `*bytes.Buffer` (no fd).
  - `pager_test.go`: when `$PAGER` is set to a binary that exits non-zero
    immediately, command still succeeds and writes a stderr warning.

All tests parallel-safe; pager tests use `t.Setenv` (already
parallel-friendly per Go 1.17+).

## Out of scope (will not implement here)

- `ioStreams` gaining an `Err io.Writer`. Useful but spans every command;
  belongs to a separate sweep.
- Auto-limit on TTY. The user explicitly chose the "rely on `--limit` +
  pager" model.
- Streaming for tree. Topology requires the full set; no viable
  approximation that doesn't risk out-of-order output.
- Smart wrap of the new compact timestamp on narrow terminals. The format
  is fixed-width by year (today: 7 chars; this-year: 14; older: 19), which
  is acceptable.

## Migration notes

Pre-public, so the user-facing breaking changes are documented but not
gated:

- `--sort` is gone; use `-r` / `--reverse` to flip.
- `ListOpts.Desc` renamed to `ListOpts.Ascending` for any embedder
  (currently only `cmd/fngr`).
- `event.List` keeps its signature; new `event.ListSeq` is purely additive.
