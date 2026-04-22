# `event` namespace + subcommands — design

Sub-project **S2** of the [roadmap](../roadmap.md). Replaces today's `fngr
show` (read) and `fngr edit` (write) with a single `fngr event <id>`
namespace. Bare `fngr event N` reads; sub-verbs mutate.

The tool is pre-public, so `show` and `edit` are removed outright instead of
aliased.

## Goals

- One verb namespace per event: `fngr event <id> [<sub-verb> [<args...>]]`.
- Bare `fngr event N` matches today's `fngr show N` exactly: prints event
  detail (text format default; `--format text|json|csv`) and supports
  `--tree` for subtree view.
- Seven mutating sub-verbs:
  - `text "..."` — replace event text. Re-parse body tags (`@person`,
    `#tag`) from the new text and add them with dedup against existing
    meta. FTS is rebuilt.
  - `time "..."` — accept either time-only (`09:30`, `2:15PM`) or full
    timestamps. Time-only preserves the event's existing date; full input
    replaces both.
  - `date "..."` — mirrors `time`: date-only preserves the existing clock
    components; full input replaces both.
  - `attach <id>` — set `parent_id`. Reject self-parent and any ancestry
    cycle.
  - `detach` — clear `parent_id`.
  - `tag <args...>` — add one or more meta entries. Each arg is `@person`,
    `#tag`, or `key=value`. Dedup against existing meta. FTS rebuilt.
  - `untag <args...>` — remove matching meta entries. Same arg grammar.
    FTS rebuilt.
- No confirmation prompts on any verb. Single-event scope makes
  inspection trivial via `fngr event N`.

## Non-goals

- No `--force` flag (there's nothing to force past).
- No bulk `event` operations (e.g. `event 1,2,3 tag #ops`). Single ID per
  invocation.
- No body-tag *removal* on `text` edits. The roadmap intentionally chose
  add-with-dedup so users don't lose meta they previously set; explicit
  `untag` removes.
- No alias for `show` or `edit`. Pre-public ⇒ straight removal.

## Architecture

### `internal/db` — migration 2: UNIQUE on event_meta

Add a new entry at the bottom of `migrations` in
`internal/db/migrate.go`:

```sql
-- Deduplicate any pre-existing rows (none expected in practice — Add
-- already dedups via parse.CollectMeta — but the migration must be
-- safe regardless).
DELETE FROM event_meta
 WHERE rowid NOT IN (
   SELECT MIN(rowid) FROM event_meta GROUP BY event_id, key, value
 );

-- Replace the non-unique (key, value) index with a UNIQUE index on
-- (key, value, event_id). Same prefix-lookup performance for
-- ListMeta / CountMeta plus DB-level uniqueness for ON CONFLICT.
DROP INDEX IF EXISTS idx_event_meta_key_value;
CREATE UNIQUE INDEX idx_event_meta_key_value_event_id
    ON event_meta(key, value, event_id);
```

The pre-existing `idx_event_meta_event_id` on `(event_id, key, value)`
stays as-is (still the right shape for `loadMetaBatch`).

`event.AddTags` and the body-tag re-parse path in `event.Update` now use
`INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING` — no app-side `SELECT existing → diff` step.

### `internal/timefmt` — partial parse

Add:

```go
// ParsePartial parses s using the same layouts as Parse but reports which
// components were present in the input. Time-only inputs (e.g. "9:30",
// "3:04PM") return hasDate=false; date-only inputs ("2026-04-15") return
// hasTime=false; full timestamps return both true.
//
// When hasDate is false, the returned t carries today's local date (so the
// time portion is a fully-formed time.Time the caller can either use as-is
// or splice into another date).
func ParsePartial(s string) (t time.Time, hasDate, hasTime bool, err error)
```

`Parse` becomes a thin wrapper: `t, _, _, err := ParsePartial(s); return t, err`. Existing callers (`add --time`, `list --from/--to`) keep
working unchanged.

### `internal/parse` — single-arg meta parser

Add:

```go
// MetaArg parses a single CLI argument into a Meta entry. Supported forms:
//   "@name"      -> {people, name}
//   "#name"      -> {tag, name}
//   "key=value"  -> {key, value}      (delegates to KeyValue)
// Names following @ or # must match the body-tag regex [\w][\w/\-]*.
// Returns an error with the message
//   "expected @person, #tag, or key=value"
// for any other shape.
func MetaArg(s string) (Meta, error)
```

Body-tag regex used by `BodyTags` is reused so CLI args and inline tags
follow the same rules.

### `internal/event` — verbs + helpers

`event.Update` (added in S1) is extended:

```go
// Update modifies an event's text and/or timestamp atomically. When text
// is non-nil, body tags (@person, #tag) are re-parsed from the new text
// and added to the event's meta with dedup against existing entries.
// Existing meta is never removed by Update (use RemoveTags for that).
// FTS is rebuilt from the final text + final meta inside the same tx.
func Update(ctx context.Context, db *sql.DB, id int64, text *string, createdAt *time.Time) error
```

New top-level functions:

```go
// ErrCycle is returned when Reparent would introduce a cycle (including
// the self-parent case).
var ErrCycle = errors.New("would create a parent cycle")

// Reparent sets event id's parent to newParent, or clears it when
// newParent is nil. Walks the candidate parent's ancestry chain and
// returns ErrCycle if id appears in it (including newParent == &id).
// Returns ErrNotFound if id or newParent does not exist.
func Reparent(ctx context.Context, db *sql.DB, id int64, newParent *int64) error

// AddTags inserts the given meta entries for event id. Duplicates are
// dropped at the database via INSERT ... ON CONFLICT DO NOTHING (the
// UNIQUE index on (key, value, event_id) added in migration 2). FTS
// rebuilt in the same transaction. Returns ErrNotFound if the event is
// missing.
func AddTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) error

// RemoveTags deletes (event_id, key, value) rows matching tags. Returns
// the number of rows removed. FTS rebuilt in the same transaction.
// Returns ErrNotFound if the event is missing; (0, nil) is a valid
// outcome when none of the tags were present.
func RemoveTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error)
```

A small private helper used by `Update`, `AddTags`, `RemoveTags`:

```go
// rebuildEventFTS reads the event's current text + meta inside tx and
// writes parse.FTSContent into events_fts.
func rebuildEventFTS(ctx context.Context, tx *sql.Tx, id int64) error
```

`Store` gains thin wrappers for `Reparent`, `AddTags`, `RemoveTags`.

### `cmd/fngr` — single `event.go`

Delete `cmd/fngr/show.go` and `cmd/fngr/edit.go` (and their test files).

Create `cmd/fngr/event.go` containing one Kong struct:

```go
type EventCmd struct {
    ID int64 `arg:"" help:"Event ID."`

    Show   EventShowCmd   `cmd:"" default:"withargs" help:"Show event detail (default)."`
    Text   EventTextCmd   `cmd:"" help:"Replace event text."`
    Time   EventTimeCmd   `cmd:"" help:"Replace clock time (or full timestamp)."`
    Date   EventDateCmd   `cmd:"" help:"Replace date (or full timestamp)."`
    Attach EventAttachCmd `cmd:"" help:"Set parent event."`
    Detach EventDetachCmd `cmd:"" help:"Clear parent."`
    Tag    EventTagCmd    `cmd:"" help:"Add tags (n args)."`
    Untag  EventUntagCmd  `cmd:"" help:"Remove tags (n args)."`
}
```

Each `EventXxxCmd` has its own `Run(parent *EventCmd, s eventStore, io ioStreams) error`. `EventCmd` exposes its parsed ID to the verbs via a
Kong `AfterApply` hook:

```go
func (c *EventCmd) AfterApply(kctx *kong.Context) error {
    kctx.Bind(c)
    return nil
}
```

Verbs read `parent.ID` and call `s.Update(ctx, parent.ID, ...)` /
`s.Reparent(...)` etc. This is the standard Kong idiom for parent-scoped
context.

`EventShowCmd` flag set:
- `Tree   bool   "Show subtree." short:"t"` (replaces today's `--tree` on `show`)
- `Format string "Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`

`EventTagCmd` and `EventUntagCmd`:
- Args struct: `Args []string "arg:\"\"" "help:\"Tags to add: @person, #tag, or key=value (one or more).\""`
- `Run` validates each arg via `parse.MetaArg`, then calls `s.AddTags`/`RemoveTags` once with the full list. Fail-fast on the first invalid arg before touching the DB.

`EventAttachCmd`:
- Args: `Parent int64 "arg:\"\"" "help:\"Parent event ID.\""`
- `Run` calls `s.Reparent(ctx, parent.ID, &c.Parent)`.

`EventDetachCmd`:
- No args.
- `Run` calls `s.Reparent(ctx, parent.ID, nil)`.

`EventTimeCmd` / `EventDateCmd`:
- Args: `Value string "arg:\"\"" "help:\"...\""`
- `Run` parses `c.Value` via `timefmt.ParsePartial`, fetches the event's current `created_at` if needed, splices, and calls `s.Update(ctx, parent.ID, nil, &spliced)`.

`EventTextCmd`:
- Args: `Body string "arg:\"\"" "help:\"New event text.\""`
- `Run` validates non-empty, then calls `s.Update(ctx, parent.ID, &c.Body, nil)`.

`eventStore` interface gains `Reparent`, `AddTags`, `RemoveTags`.

## Verb behavior summary

| Verb | Args | Behavior |
| --- | --- | --- |
| (none) | — | Print event detail. Honours `--tree` (subtree) and `--format text\|json\|csv`. |
| `text "..."` | required string | Replace text. Re-parse body tags, add with dedup. FTS rebuilt. Empty text rejected. |
| `time "..."` | required string | `ParsePartial`. `hasTime=false` (input was date-only) ⇒ reject (`event time` expects a time or full timestamp). `hasDate=true` ⇒ replace both. `hasDate=false` ⇒ replace clock components, keep event's date. |
| `date "..."` | required string | `ParsePartial`. `hasDate=false` (input was time-only) ⇒ reject (`event date` expects a date or full timestamp). `hasTime=true` ⇒ replace both. `hasTime=false` ⇒ replace date components, keep event's clock. |
| `attach <id>` | required int | Set parent. Reject self-parent and cycles. |
| `detach` | none | Clear parent. |
| `tag <args...>` | n ≥ 1 | Each arg via `MetaArg`. Dedup against existing meta. FTS rebuilt. |
| `untag <args...>` | n ≥ 1 | Each arg via `MetaArg`. Delete matching rows. FTS rebuilt. Reports count. |

## Data flow example: `fngr event 5 text "fixed wording for @Sarah #urgent"`

1. Kong parses → `EventCmd{ID: 5}` with `Text: EventTextCmd{Body: "..."}`.
2. `EventTextCmd.Run` validates non-empty body and calls
   `s.Update(ctx, 5, &body, nil)`.
3. `event.Update` (extended):
   - Begin tx.
   - Verify event 5 exists (`SELECT 1 FROM events WHERE id = ?`).
   - `UPDATE events SET text = ? WHERE id = ?`.
   - Re-parse body tags from the new text → `[{people, Sarah}, {tag, urgent}]`.
   - For each parsed tag, `INSERT INTO event_meta (event_id, key, value)
     VALUES (?, ?, ?) ON CONFLICT DO NOTHING`. The UNIQUE index added by
     migration 2 silently drops any tag that already exists on the event.
   - `rebuildEventFTS(ctx, tx, 5)` reads `text` + final meta and writes
     `parse.FTSContent(text, meta)` to `events_fts`.
   - Commit.
4. Print `Updated event 5\n` to stdout.

`tag` and `untag` reuse steps 3.b onward via the same `rebuildEventFTS`
helper.

## Error handling

- `event.Update` / `Reparent` / `AddTags` / `RemoveTags` all return
  `ErrNotFound` when the target event is missing.
- `Reparent` returns `ErrCycle` for self-parent and ancestry cycles. The
  CLI converts both to a clear error: `cannot attach event 5 to itself`
  / `cannot attach event 5 to event 3: would create a cycle`.
- `event.AddTags` and `RemoveTags` are atomic per call: validation
  (CLI-side) catches malformed args before the DB transaction begins.
- `RemoveTags` returning `(0, nil)` causes the CLI to emit
  `nothing to untag: <args>` and exit non-zero.
- `time` / `date` propagate `ParsePartial` errors with the standard
  format-error message. The wrong-shape rejection (date passed to `time`
  or vice versa) surfaces as: `event time: expected a time or full
  timestamp, got date-only "2026-04-15"` (and the mirrored phrasing for
  `event date`).
- All multi-step DB ops use a transaction; a mid-op failure rolls back.

## Testing

### `internal/timefmt`
- `TestParsePartial` (table) covers: full ISO timestamp, full RFC3339,
  date-only, time-only (HH:MM), 12-hour (3:04PM), invalid input. Asserts
  `hasDate` / `hasTime` per case.
- Existing `TestParse` continues to pass via the wrapper.

### `internal/parse`
- `TestMetaArg` (table) covers `@name`, `#tag`, `key=value`,
  `key=val=ue`, malformed (bare `urgent`, `@`, `#`, `=value`, `key=`),
  hierarchical `#work/project-x` (allowed by the regex).

### `internal/db`
- `TestMigrate_DedupesEventMetaAndAddsUniqueIndex` — seed event_meta
  with intentional duplicate rows on a fresh DB at user_version=1, run
  the migration, assert duplicates are gone and that a follow-up insert
  of the same tuple raises a UNIQUE-constraint error (or is silently
  dropped by `ON CONFLICT DO NOTHING`).

### `internal/event`
- `TestUpdate_TextRefreshesMetaAndFTS` — pre-existing meta preserved,
  new body tags added, removed body tags NOT removed, FTS reflects new
  text.
- `TestUpdate_TextDedupsRepeatedBodyTags` — text containing a tag the
  event already has produces no error and no duplicate row.
- `TestReparent_RejectsSelf`, `TestReparent_RejectsAncestryCycle` (3-deep
  cycle), `TestReparent_AllowsValidMove`, `TestReparent_DetachClearsParent`,
  `TestReparent_NotFound`, `TestReparent_NewParentNotFound`.
- `TestAddTags_Dedups`, `TestAddTags_RebuildsFTS`,
  `TestAddTags_NotFound`.
- `TestRemoveTags_ReturnsCount`, `TestRemoveTags_RebuildsFTS`,
  `TestRemoveTags_NoMatchReturnsZero`, `TestRemoveTags_NotFound`.

### `internal/event/store_test.go`
- Direct tests for `Store.Reparent`, `Store.AddTags`, `Store.RemoveTags`
  per the project's "always test wrappers in their own package" rule.

### `cmd/fngr/event_test.go`
- One test per verb: success path + at least one error path (invalid
  arg, missing event, cycle, empty text).
- Bare `fngr event N` text/JSON/CSV format dispatch.

### `cmd/fngr/dispatch_test.go`
- Add cases to `TestKongDispatch_AllCommands`: `event 1`, `event 1 text "x"`,
  `event 1 time "9:30"`, `event 1 date "2026-05-01"`, `event 1 attach 2`,
  `event 1 detach`, `event 1 tag #ops`, `event 1 untag #ops`.

## Out of scope (will not implement here)

- A confirmation/preview mechanism for the verbs. The user explicitly
  chose "never prompt" — `fngr event N` is the inspection tool.
- Multi-event verbs (`event 1,2,3 tag ...`). One ID per invocation.
- Removing body-derived meta when text changes. Add-only with explicit
  `untag` for removal.

## Migration notes

Pre-public, so the breaking changes are documented but not gated:

- `fngr show` is removed; use `fngr event N`.
- `fngr edit` is removed; use `fngr event N text|time|date`.
- New verbs `attach` / `detach` / `tag` / `untag` have no historical
  equivalent.
- `event.Update` gains the body-tag re-parse + dedup behaviour. Callers
  inside the repo (only `cmd/fngr/event.go`) will use it; no external
  consumers.
- Schema migration 2 deduplicates `event_meta` and adds a UNIQUE index
  on `(key, value, event_id)`. Existing databases run the dedupe step
  at first launch; the dedupe is a no-op when no duplicates are present
  (which is expected, since `Add` already dedups via
  `parse.CollectMeta`).
