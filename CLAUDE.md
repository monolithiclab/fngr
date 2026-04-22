# CLAUDE.md

## Project

fngr is a CLI tool for logging and tracking events, written in Go. It uses Kong for CLI parsing and
modernc.org/sqlite (pure-Go, CGo-free) for storage. Events support parent-child trees, key-value
metadata, and FTS5 full-text search.

## Commands

```bash
make build          # Build binary to build/fngr
make test           # Run tests with -race, -cover, coverage report
make lint           # Run all linters (gofmt, vet, staticcheck, golangci-lint, gosec, gocritic)
make format         # Format source code
make bench          # Run benchmarks
make ci             # codefix + format + lint + test
```

> Always use make targets for linting, testing, building, etc.

## Architecture

- `cmd/fngr/main.go` — Entrypoint. Wires Kong CLI parsing, resolves DB path, opens DB, constructs
  an `event.Store`, and dispatches to command handlers via Kong bindings (eventStore + ioStreams).
- `cmd/fngr/{add,list,event,delete,meta}.go` — Kong command structs with
  `Run(eventStore, ioStreams) error` methods, one file per top-level command. `list` is marked
  `default:"withargs"` so bare `fngr` dispatches to it; the filter is a `-S` / `--search` flag
  (Kong v1.x cannot mix positional args with branching subcommands on the same struct, so
  every list-ish command uses `-S`). `add` accepts variadic positional `Args` (joined with
  spaces); body source resolved by `cmd/fngr/body.go::resolveBody` via the
  (args, `-e`, stdin TTY-ness) dispatch table; `-e/--edit` forces the editor; bare `fngr add`
  in a TTY auto-launches `$VISUAL`/`$EDITOR`. With `--format=json` the body is parsed as a
  JSON event record (or array) by `cmd/fngr/add_json.go`; per-record defaults flow JSON value
  > CLI flag > built-in. `event` hosts a sub-command tree: `fngr event N`
  reads (shorthand for `event show N`); `text`, `time`, `date`, `attach`, `detach`, `tag`,
  `untag` mutate. Each verb owns its own `ID` arg, syntax `fngr event <verb> <id> [<args>]`.
  `meta` is a sub-command tree too: `fngr meta` lists with optional `-S` filter (bare key,
  key=value, @person, #tag), `meta rename` and `meta delete` mutate (both accept the same
  shorthand). None of the event verbs prompt; meta verbs prompt with the destructive-vs-additive
  defaults (rename `[Y/n]`, delete `[y/N]`).
- `cmd/fngr/store.go` — Defines the narrow `eventStore` interface that commands depend on plus the
  injectable `ioStreams` (`In io.Reader`, `Out io.Writer`, `Err io.Writer`, `IsTTY bool`).
- `cmd/fngr/prompt.go` — `confirm(in, out, prompt, defaultVal) (bool, error)` shared yes/no helper.
- `cmd/fngr/body.go` — Body-source dispatch for `fngr add`. `resolveBody` returns the body string
  from one of {joined args, stdin, editor} per the (args, `-e`, `IsTTY`) dispatch table.
  `launchEditor` is a `var` for test stubbing; `realLaunchEditor` execs `$VISUAL`/`$EDITOR` on a
  temp file; `errCancel` signals empty-save (handled as exit-0 by `AddCmd.Run`). `readStdin`
  caps reads at `maxStdinBytes` (16 MiB) via `io.LimitReader` so a runaway pipe can't OOM.
- `cmd/fngr/add_json.go` — `--format=json` import path. `jsonAddInput` is the wire shape
  `{text, parent_id?, created_at?, meta?: [[k,v],...]}`; `parseJSONAddInput` dispatches on the
  first non-whitespace char (`[` → array, else single object) and uses `json.Decoder` with
  `DisallowUnknownFields` so typos surface instead of being silently dropped. Batches are
  capped at `maxJSONBatchSize` (10 000 records). `jsonInputToAddInput` applies CLI defaults,
  runs body-tag extraction via `mergeMetaForJSON` (a small private helper that mirrors
  `event.CollectMeta` but suppresses default-author injection when explicit meta has an
  `author` key OR when defaultAuthor is empty), and validates that every record has an
  author from some source. `runJSON` calls `s.AddMany` for atomic batch insert.
- `cmd/fngr/pager.go` — `withPager(io, disabled) (ioStreams, closer)` wraps `Out` in a pipe to
  `$PAGER` (fallback `less -FRX`) when stdout is a TTY. Used by `list`.
- `internal/db/db.go` — DB path resolution (explicit > `.fngr.db` in cwd > `~/.fngr.db`), connection
  setup (FK + WAL + busy_timeout + synchronous=NORMAL).
- `internal/db/migrate.go` — Ordered list of migrations gated by `PRAGMA user_version`. Pre-migration
  databases are detected via the legacy v1 `events` table and bumped to `user_version = 1`.
  Migration 2 deduplicates `event_meta` and adds a UNIQUE index on `(key, value, event_id)` so
  `INSERT ... ON CONFLICT DO NOTHING` works in `AddTags` and the body-tag sync inside `Update`.
- `internal/parse/parse.go` — `Meta` type, `BodyTags` for body-tag extraction (`@person` → people,
  `#tag` → tag), `KeyValue` helper for `key=value` strings, `FlagMeta` for `--meta` flag arrays
  (delegates to `KeyValue`), `MetaArg` for individual CLI tag args (`@person`, `#tag`, or
  `key=value`; used by `event tag` / `event untag`), `FTSContent` for FTS index content building.
  Tag and meta-name regexes share the private `metaNamePattern` constant; the anchored form is
  exported as `MetaNameRe` for reuse by `cmd/fngr/meta.go::parseMetaFilter`.
- `internal/timefmt/timefmt.go` — Single source of truth for accepted time inputs. `Parse` returns
  just the parsed timestamp; `ParsePartial` also reports whether the input had a date and/or time
  component, so `event time` / `event date` can splice into an existing timestamp instead of
  replacing it via `SpliceTime` / `SpliceDate` (mirror-image helpers that mix orig/new
  date+time around the existing timezone). `FormatRelative(t, now)` returns the compact list-line
  stamp via the layout constants `LayoutToday` / `LayoutThisYear` / `LayoutOlder`. Canonical
  `DateFormat` / `DateTimeFormat` layouts used for storage and event-detail display.
- `internal/event/meta.go` — Domain meta key constants (`MetaKeyAuthor`, etc.), `CollectMeta`
  merges all meta sources (author, body tags, flags) with dedup.
- `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS),
  `AddMany` (batched same shape, atomic), `AddInput` value type. Both `Add` and `AddMany`
  delegate to a private `addInTx` that runs the per-record INSERT loop using a caller-owned
  `*sql.Tx`. `Get`, `Update` (text and/or timestamp; on text change body-derived tags are *synced* —
  `parse.BodyTags(oldText)` deleted then `parse.BodyTags(newText)` inserted via
  `ON CONFLICT DO NOTHING`; FTS rebuilt), `Reparent` (set/clear `parent_id`; rejects self and
  ancestry cycles via `ErrCycle`), `AddTags` / `RemoveTags` (event-scoped meta CRUD with FTS
  resync), `Delete`, `HasChildren`, `List` / `ListSeq` (FTS5 filter + date range + `Limit` +
  `Ascending`), `GetSubtree` (recursive CTE), `ListMeta` (filtered via `ListMetaOpts{Key, Value}`),
  `CountMeta`, `UpdateMeta`, `DeleteMeta`. All functions accept `context.Context`. `ErrNotFound` and `ErrCycle` sentinels.
  `loadMetaBatch` chunks the IN clause to stay under SQLite's parameter limit. Private helpers:
  `requireEventExists` (existence check used by every mutation function), `rebuildEventFTS`
  (used by Update/AddTags/RemoveTags to resync `events_fts`), `deleteMetaTuples` /
  `insertMetaTuples` (used by Update's body-tag sync path).
- `internal/event/store.go` — `Store` wrapper that exposes the package functions as methods on a
  single `*sql.DB`, satisfying `cmd/fngr.eventStore`.
- `internal/event/filter.go` — Filter expression preprocessor: expands `#`/`@` shorthands and
  `&`/`|`/`!` operators into FTS5 MATCH syntax. Escapes embedded double quotes.
- `internal/render/render.go` — Output rendering to `io.Writer`. `Events(w, format, events)`,
  `SingleEvent(w, format, ev)`, and `EventsStream(w, format, seq)` are the dispatchers commands
  call; `Tree`, `Flat`/`FlatStream`, `JSON`/`JSONStream`, `CSV`/`CSVStream`,
  `Markdown`/`MarkdownStream`, `Event` are the underlying writers. List/flat use a relative-aware compact stamp via `timefmt.FormatRelative`;
  event detail keeps full ISO. Streaming variants consume `iter.Seq2[Event, error]` so memory
  stays flat regardless of result size; tree still buffers because it needs the topology.
  Meta in JSON output is `[[key, value], ...]`, sorted by `(key, value)`. Each `event_meta`
  row maps to one tuple — multiple values for the same key produce multiple tuples.
  Markdown output groups events by local date as `## YYYY-MM-DD` sections; bullets are `- <time> — <body>` with multi-line bodies indented two spaces and meta on a separate continuation line of space-separated `key=value` tokens.

## Conventions

- Tests use SQLite via per-test temp files (not bare `:memory:` — each pool connection sees its
  own empty in-memory database, which breaks streaming queries). CLI tests construct an
  `event.Store` via the `newTestStore` helper in `cmd/fngr/testhelpers_test.go`; the
  `internal/event` package keeps its own `testDB` for data-access tests. No persistent fixtures
  on disk.
- Tests should be parallelized.
- Table-driven tests with `t.Run` subtests.
- Use modern Go idioms and features.
- Try hard to prevent duplicated code.
- Schema changes go in a new entry at the bottom of `migrations` in `internal/db/migrate.go`;
  never edit a published migration.
- Version injected via `-ldflags` at build time from git tags; surfaced via `--version`.
- `common-go.mk` is shared across repos — don't modify it here.
