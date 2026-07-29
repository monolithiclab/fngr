# CLAUDE.md

## Project

fngr is a CLI tool for logging and tracking events, written in Go. It uses Kong for CLI parsing and
modernc.org/sqlite (pure-Go, CGo-free) for storage. Events support parent-child trees, key-value
metadata, and FTS5 full-text search.

When touching the release pipeline (`.goreleaser.yaml`, `.github/workflows/release.yml`,
`Dockerfile`, the brew tap, ghcr.io image, cosign signing), see `docs/PUBLISHING.md` for the full
operational playbook + every gotcha hit shipping v0.0.1.

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
  (args, `-e`, stdin-carries-data) dispatch table; `-e/--edit` forces the editor; bare `fngr add`
  in a TTY auto-launches `$VISUAL`/`$EDITOR`. In text mode, when `--time` is absent, a leading
  time/date token in the title delimited by `": "` (colon+space, so times like `9:30` survive)
  is parsed via `timefmt.SplitTimePrefix` and stripped — `fngr add "9:30: had coffee"` stores
  title `had coffee` at 09:30 today; `--time` overrides and leaves the title verbatim. With
  `--format=json` the body is parsed as a
  JSON event record (or array) by `cmd/fngr/add_json.go`; per-record defaults flow JSON value
  > CLI flag > built-in. `event` hosts a sub-command tree: `fngr event N`
  reads (shorthand for `event show N`); `text` (re-splits into title+body), `title`, `body`,
  `time`, `date`, `attach`, `detach`, `tag`, `untag` mutate. Each verb owns its own `ID` arg,
  syntax `fngr event <verb> <id> [<args>]`.
  `meta` is a sub-command tree too: `fngr meta` lists with optional `-S` filter (bare key,
  key=value, @person, #tag), `meta rename` and `meta delete` mutate (both accept the same
  shorthand). None of the event verbs prompt; meta verbs prompt with the destructive-vs-additive
  defaults (rename `[Y/n]`, delete `[y/N]`); `-f`/`--force` skips the prompt on both.
- `cmd/fngr/store.go` — Defines the narrow `eventStore` interface that commands depend on plus the
  injectable `ioStreams` (`In io.Reader`, `Out io.Writer`, `Err io.Writer`, `IsTTY bool`).
- `cmd/fngr/prompt.go` — `confirm(in, out, prompt, defaultVal) (bool, error)` shared yes/no helper.
- `cmd/fngr/body.go` — Body-source dispatch for `fngr add`. `resolveBody` returns the body string
  from one of {joined args, stdin, editor} per the (args, `-e`, stdin-carries-data) dispatch table.
  "Piped" means stdin is non-TTY **and** has ≥1 byte — detected via `peekHasData` (a `bufio.Reader`
  peek that replays the byte), so `fngr add "note"` works in scripts/CI/cron where stdin is an empty
  `/dev/null` (the old `!IsTTY` proxy mis-fired "ambiguous" there). The args+stdin and `--edit`+stdin
  conflicts only trip when stdin actually has data; bare non-interactive `add` with no body source
  errors `event text cannot be empty`.
  `launchEditor` is a `var` for test stubbing; `realLaunchEditor` execs `$VISUAL`/`$EDITOR` on a
  temp file; `errCancel` signals empty-save (handled as exit-0 by `AddCmd.Run`). `readStdin`
  caps reads at `maxStdinBytes` (16 MiB) via `io.LimitReader` so a runaway pipe can't OOM.
- `cmd/fngr/add_json.go` — `--format=json` import path. `jsonAddInput` is the wire shape
  `{id?, title, body?, parent_id?, created_at?, meta?: [[k,v],...]}`; `parseJSONAddInput` dispatches on the
  first non-whitespace char (`[` → array, else single object) and uses `json.Decoder` with
  `DisallowUnknownFields` so typos surface instead of being silently dropped. Batches are
  capped at `maxJSONBatchSize` (10 000 records). `jsonInputToAddInput` applies CLI defaults,
  runs body-tag extraction via `mergeMetaForJSON` (a small private helper that mirrors
  `event.CollectMeta` but suppresses default-author injection when explicit meta has an
  `author` key OR when defaultAuthor is empty), and validates that every record has an
  author from some source. `runJSON` calls `s.AddMany` for atomic batch insert.
  `id` is read but never inserted: it exists so `fngr --format=json` output pipes straight
  back in, and so `indexBySourceID` can build a source-id → batch-position map.
  `jsonInputToAddInput` resolves each `parent_id` against that map first, emitting
  `AddInput.ParentIndex` (batch-relative) when it hits and leaving `AddInput.ParentID`
  (a target-database id) when it misses — the `--parent` CLI default is always the latter.
  `created_at` goes through `timefmt.Parse`, a superset of the RFC 3339 that
  `--format=json` emits, so import files accept the same stamps as `--time`.
- `cmd/fngr/pager.go` — `withPager(io, disabled) (ioStreams, closer)` wraps `Out` in a pipe to
  `$PAGER` (fallback `less -FRX`) when stdout is a TTY. Used by `list`.
- `internal/db/db.go` — DB path resolution (explicit > `.fngr.db` in cwd > `~/.fngr.db`) and
  connection setup. The FK + WAL + busy_timeout + synchronous=NORMAL pragmas ride in the DSN
  (`file:<path>?_pragma=...`, built with `net/url`) rather than post-open `db.Exec` calls —
  `*sql.DB` is a pool, so an Exec configures one connection and leaves the rest at SQLite
  defaults (`foreign_keys=OFF`, `busy_timeout=0`), which silently loses events under concurrent
  writes. Don't move them back. `Open` pings once so an unusable path fails there rather than
  from an arbitrary later query.
- `internal/db/migrate.go` — Ordered list of migrations gated by `PRAGMA user_version`. Pre-migration
  databases are detected via the legacy v1 `events` table and bumped to `user_version = 1`.
  Migration 2 deduplicates `event_meta` and adds a UNIQUE index on `(key, value, event_id)` so
  `INSERT ... ON CONFLICT DO NOTHING` works in `AddTags` and the body-tag sync inside `Update`.
  A migration may carry an optional Go step alongside its SQL: `goMigrations[N]` runs against the
  same transaction right after `N.sql`. Reach for one only when SQL genuinely cannot express the
  step — transliterating Go rules into SQL is what left migration 3 writing values the Go code
  would never produce.
- `internal/db/migrate4.go` — The Go half of migration 4, repairing what migration 3's SQL split
  got wrong. `repairLegacyText` re-derives every event's title and body via `repairTitleBody`
  (`strings.TrimSpace`, which SQLite's U+0020-only `TRIM` cannot match; an empty title promotes
  the body's first line, falling back to `untitledPlaceholder` when there is nothing to promote,
  since no command can set an empty title back to something valid). `repairEventMeta` re-extracts
  body tags through the real `parse.BodyTags` and drops the truncated stubs the pre-Unicode
  extractor wrote — but only a value exactly equal to what the frozen `legacyMetaNameRe` would
  have produced, so a hand-added `#work` beside a derived `#workflow` survives. `rebuildAllFTS`
  then rewrites every `events_fts` row through `parse.FTSContent` unconditionally, because
  migration 3's index rebuild was a second SQL transliteration of that same helper. The SQL half
  (`4.sql`) is a single `ANALYZE event_meta`, refreshing statistics migration 2 left describing a
  dropped index.
- `internal/parse/parse.go` — `Meta` type, `BodyTags` for body-tag extraction (`@person` → people,
  `#tag` → tag), `KeyValue` helper for `key=value` strings, `FlagMeta` for `--meta` flag arrays
  (delegates to `KeyValue`), `MetaArg` for individual CLI tag args (`@person`, `#tag`, or
  `key=value`; used by `event tag` / `event untag`), `FTSContent` for FTS index content building,
  `SplitTitleBody` for the `". "` title/body split.
  Tag and meta-name regexes share the private `metaNamePattern` constant; the anchored form is
  exported as `MetaNameRe` for reuse by `cmd/fngr/meta.go::parseMetaFilter`. That pattern is
  Unicode-class based (`\p{L}\p{N}_/-`), not `\w` — Go's `\w` is ASCII-only, so it truncated
  `@josé` to `people=jos` and collided with `@josa`. The body-tag patterns additionally require
  `metaNameBoundary` (start of text or a non-name rune) before the sigil, so `bob@example.com`
  and `.../guide#installation` no longer mint metadata. Don't reintroduce `\w` in either.
- `internal/timefmt/timefmt.go` — Single source of truth for accepted time inputs. `Parse` returns
  just the parsed timestamp; `ParsePartial` also reports whether the input had a date and/or time
  component, so `event time` / `event date` can splice into an existing timestamp instead of
  replacing it. `ParsePartial` first tries `parseRelative` (now/today/yesterday, `N
  {minute|hour|day|week|month}s ago`, `<day> at <time>`; `a`/`an` count as 1) anchored on a passed-in
  `now` for testability; sub-day offsets and `now` are date+time, bare relative days carry now's
  time-of-day but report date-only so splicing still works. Then it falls back to the absolute
  `fullFormats` / time-only layouts (`parseClock` shared with the relative path). Splice via
  `SpliceTime` / `SpliceDate` (mirror-image helpers that mix orig/new
  date+time around the existing timezone). `SplitTimePrefix(s)` extracts a leading timestamp from
  free text delimited by `": "` (used by `fngr add` title parsing), delegating to `Parse`.
  `FormatRelative(t, now)` returns the compact list-line
  stamp via the layout constants `LayoutToday` / `LayoutThisYear` / `LayoutOlder`. Canonical
  `DateFormat` / `DateTimeFormat` layouts used for storage and event-detail display.
  `FormatStorage` / `ParseStorage` are the encode/decode pair for the `created_at` TEXT column —
  the write path *and* the `--from`/`--to` bounds (compared lexically against stored text) must
  both go through `FormatStorage` or they drift apart.
  Two bounds keep the parser from producing an unstorable timestamp: `relCountUnit` rejects counts
  above `maxRelCount` (1e6 — `time.Duration(n) * time.Hour` overflows int64 past ~2.56e6 and wraps
  to a *future* offset), and `ParsePartial` rejects any result outside year `[1, 9999]`, exported
  as the `InRange` predicate. Out-of-range matters because `DateTimeFormat` renders such a year
  into a string SQLite stores but the driver cannot scan back — one such row broke every read of
  the table. Month arithmetic goes through `addMonths`, which clamps the day to the target month's
  last day; plain `AddDate` normalizes Feb 31 forward to Mar 3.
- `internal/event/meta.go` — Domain meta key constants (`MetaKeyAuthor`, etc.), `CollectMeta`
  merges all meta sources (author, body tags, flags) with dedup.
- `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS),
  `AddMany` (batched same shape, atomic), `AddInput` value type. Both `Add` and `AddMany`
  delegate to a private `addInTx` that runs the per-record INSERT loop using a caller-owned
  `*sql.Tx`. `AddInput` names its parent either by database id (`ParentID`) or by position in
  the same batch (`ParentIndex`, mutually exclusive with the former) — the latter is what lets
  a JSON import re-create a tree whose ids don't exist in the target yet. Batch parents are
  wired up by an UPDATE pass *after* the insert loop, since a child may be inserted before its
  parent (`fngr --format=json` emits newest-first); `validateParentIndexes` rejects
  out-of-range indexes and cycles up front, because SQLite's FK check only proves the parent
  row exists and would happily commit a cycle unreachable from any root.
  `Get`, `Update` (title, body, and/or timestamp; on title or body change body-derived tags are
  *synced* — `parse.BodyTags(oldTitle+" "+oldBody)` deleted then
  `parse.BodyTags(newTitle+" "+newBody)` inserted via
  `ON CONFLICT DO NOTHING`; FTS rebuilt), `Reparent` (set/clear `parent_id`; rejects self and
  ancestry cycles via `ErrCycle`), `AddTags` / `RemoveTags` (event-scoped meta CRUD with FTS
  resync), `Delete`, `HasChildren`, `List` / `ListSeq` (FTS5 filter + date range + `Limit` +
  `Ascending`), `GetSubtree` (recursive CTE), `ListMeta` (filtered via `ListMetaOpts{Key, Value}`),
  `CountMeta`, `UpdateMeta`, `DeleteMeta`. All functions accept `context.Context`. `ErrNotFound`,
  `ErrCycle` and `ErrTimeRange` sentinels.
  `loadMetaBatch` chunks the IN clause to stay under SQLite's parameter limit. Private helpers:
  `requireEventExists` (existence check used by every mutation function), `rebuildEventFTS`
  (used by Update/AddTags/RemoveTags to resync `events_fts`), `deleteMetaTuples` /
  `insertMetaTuples` (used by Update's body-tag sync path), `formatTimestamp` (the only writer of
  `created_at`; re-checks `timefmt.InRange` because a timestamp can bypass the CLI parser via
  `--format=json` or a directly-built `AddInput`), and `scanEventRow` (the only reader — shared by
  `scanEvents` and `ListSeq`). `created_at` is read through a `timeScanner` rather than scanned
  straight into a `time.Time`: the driver hands back a raw string for a value it cannot parse, and
  the default conversion then failed the *entire* result set, so one bad row written by an older
  build broke list, show and delete alike (delete calls `Get` first). `timeScanner` never errors —
  such a row reads as the zero time and stays deletable.
- `internal/event/store.go` — `Store` wrapper that exposes the package functions as methods on a
  single `*sql.DB`, satisfying `cmd/fngr.eventStore`.
- `internal/event/filter.go` — `-S` filter expressions: tokenizer → precedence-climbing parser
  (`!` > `&` > `|`, adjacent terms are an implicit AND) → SQL emitter. `compileFilter` returns a
  boolean condition over `e.id` plus its bind args; each leaf term becomes its own
  `e.id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)`, so `&`/`|`/`!` are SQL
  operators over id sets rather than FTS5 syntax — FTS5 has no unary NOT, which is why the
  string-rewriting preprocessor this replaced could not express `!a & b`. Terms are always
  quoted on emit, so punctuation (hyphens, stray quotes) is text, not syntax; a trailing `*`
  stays outside the quotes as a prefix search. Malformed input fails at parse time with an
  `ErrFilter`-wrapped message naming the rune position, never at SQLite.
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
