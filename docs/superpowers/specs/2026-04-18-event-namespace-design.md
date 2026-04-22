# `event` namespace + subcommands — design

Sub-project **S2** of the [roadmap](../roadmap.md). Replaces today's `fngr
show` (read) and `fngr edit` (write) with a single `fngr event <id>`
namespace. Bare `fngr event N` reads; sub-verbs mutate.

The tool is pre-public, so `show` and `edit` are removed outright instead of
aliased.

## Goals

- One verb namespace per event under `fngr event`. Verb syntax is
  **`fngr event <verb> <id> [<args...>]`** (verb before ID — see "Kong
  constraint" below).
- Bare `fngr event N` (no verb) is a shortcut for `fngr event show N`
  via Kong's `default:"withargs"` and matches today's `fngr show N`
  exactly: prints event detail (text format default; `--format
  text|json|csv`) and supports `--tree` for subtree view.
- Seven mutating sub-verbs (each takes the event ID as its first
  positional, then verb-specific args):
  - `text <id> "..."` — replace event text. The body-derived tags
    (`@person`, `#tag`) are *synced* to the new text: tags parsed from
    the previous text are removed first, then tags parsed from the new
    text are inserted with `ON CONFLICT DO NOTHING`. Non-body-derived
    meta (`author`, anything added via `tag` with a `key=value` shape,
    anything from `add --meta`) is untouched. FTS is rebuilt.
  - `time <id> "..."` — accept either time-only (`09:30`, `2:15PM`) or
    full timestamps. Time-only preserves the event's existing date;
    full input replaces both.
  - `date <id> "..."` — mirrors `time`: date-only preserves the
    existing clock components; full input replaces both.
  - `attach <id> <parent-id>` — set `parent_id`. Reject self-parent and
    any ancestry cycle.
  - `detach <id>` — clear `parent_id`.
  - `tag <id> <args...>` — add one or more meta entries. Each arg is
    `@person`, `#tag`, or `key=value`. Dedup against existing meta. FTS
    rebuilt.
  - `untag <id> <args...>` — remove matching meta entries. Same arg
    grammar. FTS rebuilt.
- No confirmation prompts on any verb. Single-event scope makes
  inspection trivial via `fngr event N`.

### Kong constraint (verb before ID)

Kong v1.x does not allow a struct to mix positional arguments with
branching subcommands ("can't mix positional arguments and branching
arguments"). The originally-brainstormed UX `fngr event <id> <verb>`
would put a positional ID alongside seven `cmd:""` siblings on the
same struct, which Kong rejects at construction time.

The pragmatic resolution: each verb owns its own `<id>` arg (read by
the verb's Run, no parent-context binding needed). The bare-read case
keeps its convenient `fngr event N` shortcut because the `show` verb
is marked `default:"withargs"` — so `event 1` and `event show 1` are
equivalent.

## Non-goals

- No `--force` flag (there's nothing to force past).
- No bulk `event` operations (e.g. `event 1,2,3 tag #ops`). Single ID per
  invocation.
- No way to keep a body-derived tag after removing it from the text. If
  the previous text mentioned `@Sarah` and the new text doesn't, Sarah
  is untagged. To re-add her, use `event N tag @Sarah` after the text
  edit. This is the cost of keeping body tags consistent with the text.
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

`event.AddTags` and the body-tag re-tag step in `event.Update` use
`INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)
ON CONFLICT DO NOTHING`. The pre-untag step in `event.Update` uses
`DELETE FROM event_meta WHERE event_id = ? AND key = ? AND value = ?`,
one statement per tag parsed from the old text.

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
// is non-nil, the body-derived tags (@person, #tag) are synced to the
// new text: tags parsed from the previous text are deleted first, then
// tags parsed from the new text are inserted via ON CONFLICT DO
// NOTHING. Non-body-derived meta (author, key=value entries) is never
// touched. FTS is rebuilt from the final text + final meta inside the
// same tx.
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

Create `cmd/fngr/event.go` containing one Kong parent struct plus eight
verb structs. Per the Kong constraint above, the parent struct holds
**only** the verb union; each verb struct owns its own `ID int64 arg:""`
field:

```go
type EventCmd struct {
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

Each verb struct starts with `ID int64 arg:""` and then its own flags /
positionals. Each verb's `Run(s eventStore, io ioStreams) error` reads
`c.ID` directly. `default:"withargs"` on `Show` lets `fngr event N`
fall through to `EventShowCmd` with the trailing positional consumed as
its ID.

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

| Invocation | Behavior |
| --- | --- |
| `fngr event <id>` (or `fngr event show <id>`) | Print event detail. Honours `--tree` (subtree) and `--format text\|json\|csv`. |
| `fngr event text <id> "..."` | Replace text. Body tags synced: untag what the old text had, retag from the new text. Non-body meta is untouched. FTS rebuilt. Empty text rejected. |
| `fngr event time <id> "..."` | `ParsePartial`. `hasTime=false` (input was date-only) ⇒ reject. `hasDate=true` ⇒ replace both. `hasDate=false` ⇒ replace clock components, keep event's date. |
| `fngr event date <id> "..."` | `ParsePartial`. `hasDate=false` (input was time-only) ⇒ reject. `hasTime=true` ⇒ replace both. `hasTime=false` ⇒ replace date components, keep event's clock. |
| `fngr event attach <id> <parent-id>` | Set parent. Reject self-parent and cycles. |
| `fngr event detach <id>` | Clear parent. |
| `fngr event tag <id> <args...>` (n ≥ 1) | Each arg via `MetaArg`. Dedup against existing meta. FTS rebuilt. |
| `fngr event untag <id> <args...>` (n ≥ 1) | Each arg via `MetaArg`. Delete matching rows. FTS rebuilt. Reports count. |

## Data flow example: `fngr event text 5 "fixed wording for @Sarah #urgent"`

1. Kong parses → `EventCmd{Text: EventTextCmd{ID: 5, Body: "..."}}`.
2. `EventTextCmd.Run` validates non-empty body and calls
   `s.Update(ctx, c.ID, &c.Body, nil)`.
3. `event.Update` (extended):
   - Begin tx.
   - Read event 5's current text (also confirms it exists; absence
     returns `ErrNotFound`).
   - `oldBodyTags := parse.BodyTags(oldText)`.
   - `newBodyTags := parse.BodyTags(newText)`.
   - For each tag in `oldBodyTags`: `DELETE FROM event_meta
     WHERE event_id = ? AND key = ? AND value = ?` (idempotent — if the
     user manually re-added it via `tag` it still belonged to the
     "derived from the old text" set).
   - `UPDATE events SET text = ? WHERE id = ?`.
   - For each tag in `newBodyTags`: `INSERT INTO event_meta (event_id,
     key, value) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`. The UNIQUE
     index from migration 2 silently drops a tag that already exists on
     the event (e.g. when both old and new text contain `@Sarah`,
     `oldBodyTags` deletes Sarah, then `newBodyTags` re-inserts her).
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
- `TestUpdate_TextSyncsBodyTags` — pre-existing non-body meta
  (`author`, `env=prod`) preserved; body tags from the old text
  (`@Sarah`, `#ops`) removed when missing from the new text; new body
  tags from the new text added; tags present in both old and new text
  end up exactly once. FTS reflects new text.
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
