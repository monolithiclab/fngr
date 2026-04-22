# `meta` UX Design

Sub-project **S3** of the [roadmap](../roadmap.md). Three small wins in the
metadata namespace:

1. `fngr meta` (no subcommand) lists, optionally filtered.
2. `meta update` is renamed to `meta rename`.
3. The destructive verbs (`rename`, `delete`) accept the same `@person` /
   `#tag` / `key=value` shorthand the new `meta` filter does.

The tool is pre-public; no compatibility shims for the rename.

## Goals

- `fngr meta` (no args) keeps its current behaviour: list every
  `(key, value, count)` row, sorted.
- `fngr meta <filter>` lists only matching rows. Filter forms:
  - bare key — `fngr meta tag` lists every `tag=*` row
  - `key=value` — exact match
  - `@name` — shorthand for `people=name`
  - `#name` — shorthand for `tag=name`
- `fngr meta rename <old> <new>` is the renamed-but-otherwise-identical
  verb (was `meta update`). Both args accept `key=value` / `@name` / `#name`.
  Confirm `[Y/n]` (default yes), `--force` skips. Existing well-known-key
  guard stays.
- `fngr meta delete <entry>` accepts the same shorthand forms. Confirm
  `[y/N]` (default no), `--force` skips. Existing well-known-key guard
  stays.

## Non-goals

- No bare-key in `meta delete` (`fngr meta delete tag` would wipe every
  `tag=*` row). Footgun, won't fix.
- No value-only filter (`fngr meta =ops`). Rare; users can pipe through
  grep.
- No JSON / CSV output for the filter. `meta` is an inspection tool;
  scripting against it is unusual. Not worth the dispatcher.
- No regex / glob filter. Bare key + exact pair covers the realistic use
  cases.
- No new `MetaArg`-style helper in `parse`. The bare-key fallback is a
  CLI presentation choice and lives next to its only caller.

## Architecture

### `internal/event` — typed list options

`event.ListMeta` gains a struct-based filter to mirror `event.ListOpts`.
Final shape:

```go
// ListMetaOpts narrows the result of ListMeta. Both fields are optional:
// zero values mean "no filter on that field". When both are set, the
// query is an exact (key, value) match.
type ListMetaOpts struct {
    Key   string
    Value string
}

// ListMeta returns one MetaCount row per (key, value) tuple matching opts,
// sorted by key then value.
func ListMeta(ctx context.Context, db *sql.DB, opts ListMetaOpts) ([]MetaCount, error)
```

Implementation:

```sql
SELECT key, value, COUNT(*) AS count FROM event_meta
[ WHERE key = ? ]
[ AND value = ? ]
GROUP BY key, value
ORDER BY key, value
```

The `idx_event_meta_key_value_event_id` (key, value, event_id) UNIQUE
index added in S2's migration 2 covers `WHERE key = ?` (prefix scan) and
`WHERE key = ? AND value = ?` (full match). No new index needed.

`Store.ListMeta` and `eventStore.ListMeta` follow the new signature.

### `cmd/fngr/meta.go` — full rewrite

```go
type MetaCmd struct {
    List   MetaListCmd   `cmd:"" default:"withargs" help:"List metadata, optionally filtered (default)."`
    Rename MetaRenameCmd `cmd:"" help:"Rename a metadata entry across all events."`
    Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata entry across all events."`
}

type MetaListCmd struct {
    Filter string `arg:"" optional:"" help:"Filter: bare key (e.g. 'tag'), key=value, @person, or #tag."`
}

type MetaRenameCmd struct {
    Old   string `arg:"" help:"Old entry: key=value, @person, or #tag."`
    New   string `arg:"" help:"New entry: same forms as <old>."`
    Force bool   `help:"Skip confirmation prompt." short:"f"`
}

type MetaDeleteCmd struct {
    Meta  string `arg:"" help:"Entry to delete: key=value, @person, or #tag."`
    Force bool   `help:"Skip confirmation prompt." short:"f"`
}
```

Per Kong v1.x's "can't mix positional arguments and branching arguments"
constraint (same as S2): the filter lives on `MetaListCmd` (NOT on
`MetaCmd`). `default:"withargs"` lets `fngr meta tag` fall through to
`MetaListCmd` with the trailing positional consumed as `Filter`.

`MetaListCmd.Run`:

1. If `c.Filter` empty → call `s.ListMeta(ctx, ListMetaOpts{})`.
2. Else parse the filter via the new local helper `parseMetaFilter` (see
   below). Call `s.ListMeta(ctx, ListMetaOpts{Key: ..., Value: ...})`.
3. Render with the existing aligned `key=value (count)` block; on empty
   result print `No metadata found.` and return nil (not an error).

`MetaRenameCmd.Run` and `MetaDeleteCmd.Run`:

- Parse args via `parse.MetaArg` (existing). It already accepts
  `@person` / `#tag` / `key=value`.
- Same flow as today: `CountMeta` for preview → confirm → mutate.
- `meta rename` renames the underlying `(key, value)` tuple; if either
  arg's parsed key is in the well-known set (`MetaKeyAuthor` etc.),
  `event.UpdateMeta` rejects with the existing message.
- `meta delete` likewise.

### `cmd/fngr/meta.go` — `parseMetaFilter`

Lives next to its single caller (private). Roughly:

```go
// metaNameRe matches the body-tag character class — same shape that
// parse.MetaArg uses internally for @/# names. Local copy avoids
// exporting parse.metaArgRe.
var metaNameRe = regexp.MustCompile(`^[\w][\w/\-]*$`)

// parseMetaFilter accepts the same shorthand as parse.MetaArg
// (@person, #tag, key=value) plus a bare key (e.g. "tag") that filters
// by key only. Returned Meta carries an empty Value for the bare-key
// case.
func parseMetaFilter(s string) (parse.Meta, error) {
    if s == "" {
        return parse.Meta{}, nil
    }
    if s[0] != '@' && s[0] != '#' && !strings.Contains(s, "=") {
        if !metaNameRe.MatchString(s) {
            return parse.Meta{}, fmt.Errorf("invalid filter %q: bare key must match [\\w][\\w/\\-]*", s)
        }
        return parse.Meta{Key: s}, nil
    }
    return parse.MetaArg(s)
}
```

## Verb behaviour summary

| Invocation | Behaviour |
| --- | --- |
| `fngr meta` | List every meta row. Empty = "No metadata found." |
| `fngr meta tag` | Bare key. List every `tag=*` row. |
| `fngr meta tag=ops` | Exact match. One row or empty. |
| `fngr meta @sarah` | ≡ `fngr meta people=sarah`. |
| `fngr meta #ops` | ≡ `fngr meta tag=ops`. |
| `fngr meta rename tag=wip tag=done` | As today's `meta update`. `[Y/n]`. `--force` skips. |
| `fngr meta rename #wip #done` | Same as above (shorthand). |
| `fngr meta delete tag=obsolete` | As today. `[y/N]`. `--force` skips. |
| `fngr meta delete #obsolete` | Same as above (shorthand). |

## Data flow example: `fngr meta @sarah`

1. Kong parses → `MetaCmd{List: MetaListCmd{Filter: "@sarah"}}`.
2. `MetaListCmd.Run`:
   - `parseMetaFilter("@sarah")` → `parse.MetaArg("@sarah")` →
     `{Key: "people", Value: "sarah"}`.
   - `s.ListMeta(ctx, ListMetaOpts{Key: "people", Value: "sarah"})`.
3. `event.ListMeta`:
   - Builds `SELECT … FROM event_meta WHERE key = ? AND value = ? GROUP BY …`.
   - Returns 0 or 1 rows.
4. `MetaListCmd.Run` renders the result block (same alignment code as today)
   or prints `No metadata found.`.

## Error handling

- `parseMetaFilter` invalid input → CLI returns the error before any DB
  call, exit non-zero.
- `parse.MetaArg` invalid input in `rename`/`delete` → same.
- `event.UpdateMeta` / `event.DeleteMeta` rejecting a well-known key →
  surfaced unchanged.
- `meta rename` matching zero rows → existing error
  `no metadata matching <key>=<value>` (CountMeta=0 path).
- `meta delete` matching zero rows → existing error.
- `meta` filter matching zero rows → `No metadata found.` + exit 0
  (informational; reads, not writes).

## Testing

### `internal/event`
- `TestListMeta` already covers no-filter (passes empty struct now).
- `TestListMeta_FilterByKey` — seed mixed keys; assert only matching rows.
- `TestListMeta_FilterByKeyValue` — seed two events with same tag; assert
  count = 2 in the single row returned.
- `TestListMeta_FilterEmptyResult` — assert empty slice for no match
  (not an error).

### `internal/event/store_test.go`
- Existing `TestStore_*` for `ListMeta` updated for new signature; add one
  filtered case.

### `cmd/fngr/meta_test.go`
- Update `TestMetaListCmd_Format` for the new signature.
- New: `TestMetaListCmd_FilterByKey`, `TestMetaListCmd_FilterByKeyValue`,
  `TestMetaListCmd_FilterShorthand` (one each for `@`, `#`).
- New: `TestMetaListCmd_InvalidFilter`.
- Rename `TestMetaUpdateCmd_*` → `TestMetaRenameCmd_*` and update verb name.
- New: `TestMetaRenameCmd_AcceptsShorthand`, `TestMetaDeleteCmd_AcceptsShorthand`.
- Existing prompt-flow tests stay; verify them against the renamed verb.

### `cmd/fngr/dispatch_test.go`
- Replace `meta-update` entry with `meta-rename`.
- Add `meta-filter-key` (`["meta", "tag"]`), `meta-filter-keyvalue`
  (`["meta", "tag=ops"]`), `meta-filter-shorthand` (`["meta", "#ops"]`).

All tests parallel-safe; per-test temp SQLite files (existing helpers).

## Out of scope (will not implement here)

- Value-only filter (`fngr meta =ops`). Realistic use case is rare; pipe
  through grep.
- Bare-key delete (`fngr meta delete tag`). Footgun; let users specify the
  full pair.
- JSON/CSV output for `meta`. Inspection tool; not a scripting target.
- Glob/regex in the filter. Out of proportion for the CLI surface.
- Splitting `parseMetaFilter` into the `parse` package as a public helper.
  Used by exactly one caller; keep it private and local.

## Migration notes

Pre-public, so the breaking changes are documented but not gated:

- `fngr meta update` is removed. Use `fngr meta rename` (same args).
- `event.ListMeta` signature changes from `(ctx, db) ([]MetaCount, error)`
  to `(ctx, db, ListMetaOpts) ([]MetaCount, error)`. Internal — only
  `cmd/fngr/meta.go` and `cmd/fngr/store.go` update.
- The `eventStore` interface's `ListMeta` follows.
