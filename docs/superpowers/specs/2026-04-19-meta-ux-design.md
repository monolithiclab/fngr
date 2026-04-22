# `meta` UX + `list` filter harmonization

Sub-project **S3** of the [roadmap](../roadmap.md). Originally three small
wins in the metadata namespace; expanded to also harmonize the existing
`list` filter onto the same `-S` / `--search` shape so the two commands
stay consistent.

1. `fngr meta` (no subcommand) lists, optionally filtered via `-S`.
2. `fngr list` / bare `fngr` filter migrates from positional to `-S`
   (matches `git log -S`; sidesteps Kong's "can't mix positional + branching
   subcommands" constraint we already hit in S2; frees the positional arg
   slot for future use).
3. `meta update` is renamed to `meta rename`.
4. The destructive verbs (`meta rename`, `meta delete`) accept the same
   `@person` / `#tag` / `key=value` shorthand the new `-S` filter does.

The tool is pre-public; no compatibility shims for the rename or the
positional → flag move.

## Goals

- `fngr meta` (no args) keeps its current behaviour: list every
  `(key, value, count)` row, sorted.
- `fngr meta -S <filter>` lists only matching rows. Filter forms (parsed
  by the new `parseMetaFilter`):
  - bare key — `-S tag` lists every `tag=*` row
  - `key=value` — exact match
  - `@name` — shorthand for `people=name`
  - `#name` — shorthand for `tag=name`
- `fngr list -S <filter>` (and bare `fngr -S <filter>` via `default:"withargs"`)
  filters the FTS expression. Same flag shape, same parser entry point.
  Existing FTS expression syntax (`#tag`, `@person`, `&`, `|`, `!`,
  bare words) is unchanged — `-S` just moves the *delivery mechanism*
  from a positional arg to a flag. The list command keeps its richer
  expression grammar; the meta command keeps its narrower
  bare-key/exact/shorthand grammar.
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
- No value-only filter (`fngr meta -S =ops`). Rare; users can pipe through
  grep.
- No JSON / CSV output for the filter. `meta` is an inspection tool;
  scripting against it is unusual. Not worth the dispatcher.
- No regex / glob filter. Bare key + exact pair covers the realistic use
  cases.
- No new `MetaArg`-style helper in `parse`. The bare-key fallback is a
  CLI presentation choice and lives next to its only caller.
- No second flag for "value-only" search on either command. One flag, one
  parser entry per command.
- No `-S` on `fngr event N` — `event` is a fetch by ID, not a search.

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
    Search string `help:"Filter: bare key (e.g. 'tag'), key=value, @person, or #tag." short:"S"`
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

The filter lives on `MetaListCmd` as a `-S` / `--search` flag — no
positional, so Kong's "can't mix positional + branching" constraint is
not engaged at all. `default:"withargs"` is still useful so that
`fngr meta -S tag` works whether the user types `meta` or omits it
(once we wire `meta` as the `MetaCmd` default; today `meta` is its
own subcommand under the root, and `MetaListCmd` is `MetaCmd`'s
default — that's unchanged).

`MetaListCmd.Run`:

1. If `c.Search` empty → call `s.ListMeta(ctx, ListMetaOpts{})`.
2. Else parse via the new local helper `parseMetaFilter` (see below).
   Call `s.ListMeta(ctx, ListMetaOpts{Key: ..., Value: ...})`.
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

### `cmd/fngr/list.go` — positional `Filter` becomes `-S`/`--search` flag

```go
type ListCmd struct {
    From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
    To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
    Format  string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
    Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
    Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
    NoPager bool   `help:"Disable the pager even when stdout is a TTY."`
    Search  string `help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)." short:"S"`
}
```

The struct loses `Filter string arg:"" optional:""` and gains
`Search string short:"S"`. `ListCmd.toListOpts` reads `c.Search` instead
of `c.Filter` and threads it into `event.ListOpts.Filter` (the
internal struct field name doesn't change — only the CLI surface).

The FTS expression grammar in `internal/event/filter.go` is unchanged;
this is a pure delivery-mechanism move from positional to flag.

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
| `fngr meta -S tag` | Bare key. List every `tag=*` row. |
| `fngr meta -S tag=ops` | Exact match. One row or empty. |
| `fngr meta -S @sarah` | ≡ `fngr meta -S people=sarah`. |
| `fngr meta -S '#ops'` | ≡ `fngr meta -S tag=ops`. (Quote `#` so the shell doesn't strip it.) |
| `fngr meta rename tag=wip tag=done` | As today's `meta update`. `[Y/n]`. `--force` skips. |
| `fngr meta rename '#wip' '#done'` | Same as above (shorthand). |
| `fngr meta delete tag=obsolete` | As today. `[y/N]`. `--force` skips. |
| `fngr meta delete '#obsolete'` | Same as above (shorthand). |
| `fngr` (or `fngr list`) | List all events (current behaviour). |
| `fngr -S '#ops'` | List events matching the FTS expression. (Was `fngr '#ops'`.) |
| `fngr -S '@sarah & #ops' --format flat` | Same expression grammar; just delivered via the flag. |

## Data flow example: `fngr meta -S @sarah`

1. Kong parses → `MetaCmd{List: MetaListCmd{Search: "@sarah"}}`.
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
- New: `TestMetaListCmd_SearchByKey`, `TestMetaListCmd_SearchByKeyValue`,
  `TestMetaListCmd_SearchShorthand` (one each for `@`, `#`).
- New: `TestMetaListCmd_InvalidSearch`.
- Rename `TestMetaUpdateCmd_*` → `TestMetaRenameCmd_*` and update verb name.
- New: `TestMetaRenameCmd_AcceptsShorthand`, `TestMetaDeleteCmd_AcceptsShorthand`.
- Existing prompt-flow tests stay; verify them against the renamed verb.

### `cmd/fngr/list_test.go`
- Existing `TestListCmd_*` that constructed `&ListCmd{Filter: "..."}`
  switch to `&ListCmd{Search: "..."}`. No new tests required — the
  filter behaviour is unchanged; only the field name moves.

### `cmd/fngr/dispatch_test.go`
- Replace `meta-update` entry with `meta-rename`.
- Add `meta-search-key` (`["meta", "-S", "tag"]`),
  `meta-search-keyvalue` (`["meta", "-S", "tag=ops"]`),
  `meta-search-shorthand` (`["meta", "-S", "#ops"]`).
- Existing list-related entries (e.g. any `["list", "#ops"]`-style
  positional arg cases) move to `["list", "-S", "#ops"]`.

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
- `fngr list` (and bare `fngr`) loses its positional filter arg. Old
  invocation `fngr '#ops'` becomes `fngr -S '#ops'`. Same expression
  grammar; same output; only the delivery mechanism changes.
- `event.ListMeta` signature changes from `(ctx, db) ([]MetaCount, error)`
  to `(ctx, db, ListMetaOpts) ([]MetaCount, error)`. Internal — only
  `cmd/fngr/meta.go` and `cmd/fngr/store.go` update.
- The `eventStore` interface's `ListMeta` follows.
- `cmd/fngr/list.go::ListCmd` loses `Filter string arg:""` and gains
  `Search string short:"S"`. `event.ListOpts.Filter` (the internal
  field) is unchanged.
