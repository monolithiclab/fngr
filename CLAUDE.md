# CLAUDE.md

## Project

fngr is a CLI tool for logging and tracking events, written in Go. It uses Kong for CLI parsing and
modernc.org/sqlite (pure-Go, CGo-free) for storage. Events support parent-child trees, key-value
metadata, and FTS5 full-text search.

When touching the release pipeline (`.goreleaser.yaml`, `.github/workflows/{ci,release}.yml`,
`Dockerfile`, the brew tap, ghcr.io image, cosign signing), see `docs/PUBLISHING.md` for the full
operational playbook + every gotcha hit shipping v0.0.1. Everything in there that reaches the
network is pinned — actions and the images they pull, the base image, GoReleaser, the lint tools
— so an edit that reintroduces a floating tag is a regression, not a tidy-up; the playbook's
"Refreshing the pins" section is how they move. `make lint-pins` (a prerequisite of `lint`,
so both CI and `make ci` run it) guards the two shapes a grep can see: `uses:` lines and the
Dockerfile `FROM`. The rest are literals — the tool versions in this `Makefile`, GoReleaser's
`version:` and the two `with:` image digests in `release.yml` — and are on review.

## Commands

```bash
make build          # Build binary to build/fngr
make test           # Run tests with -race, -cover, coverage report
make lint           # Run all linters (gofmt, vet, staticcheck, golangci-lint, gosec, gocritic, pins)
make lint-tools     # Install the pinned linter versions into GOPATH/bin
make vuln           # Report known vulnerabilities reachable from this module's code
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
  (args, `-e`, TTY) precedence table, behind a `-e`-needs-a-terminal veto that outranks it; bare
  `fngr add` in a TTY auto-launches `$VISUAL`/`$EDITOR`. In text mode, when `--time` is absent, a leading
  time/date token in the title delimited by `": "` (colon+space, so times like `9:30` survive)
  is parsed via `timefmt.SplitTimePrefix` and stripped — `fngr add "9:30: had coffee"` stores
  title `had coffee` at 09:30 today; `--time` overrides and leaves the title verbatim. `list`
  accepts the same grammar on `--from`/`--to` (`cmd/fngr/list.go::parseBound` delegates to
  `timefmt.ParsePartial`, so an RFC 3339 stamp copied out of `--format=json` pastes straight
  back in). `ListOpts.To` is exclusive, so `--to` is turned into the first instant *past* what
  was named — next second for a clock, next midnight for a bare date — which is what makes it
  read as inclusive at both granularities; `--from >= --to` warns on stderr rather than
  returning nothing at exit 0. With
  `--format=json` the body is parsed as a
  JSON event record (or array) by `cmd/fngr/add_json.go`; per-record defaults flow JSON value
  > CLI flag > built-in. `event` hosts a sub-command tree: `fngr event N`
  reads (shorthand for `event show N`); `text` (re-splits into title+body), `title`, `body`,
  `time`, `date`, `attach`, `detach`, `tag`, `untag` mutate. Each verb owns its own `ID` arg,
  syntax `fngr event <verb> <id> [<args>]`. The two verbs that walk the tree (`show -t`, `attach`)
  route their error through `withRepairHint`, which appends the way out of an `ErrCorruptTree` —
  `fngr event detach <id>` on any event the message names, since clearing `parent_id` walks
  nothing and so works on the very database the traversal could not survive.
  `meta` is a sub-command tree too: `fngr meta` lists with optional `-S` filter (bare key,
  key=value, @person, #tag), `meta rename` and `meta delete` mutate (both accept the same
  shorthand). None of the event verbs prompt; meta verbs prompt with the destructive-vs-additive
  defaults (rename `[Y/n]`, delete `[y/N]`); `-f`/`--force` skips the prompt on both, and is
  *required* in a non-interactive run — every prompt errors rather than assume its default when
  stdin has no answer to give (see `cmd/fngr/prompt.go`). The `meta` listing pads both columns to
  the widest cell, so each one goes through `displayCell` first: escaped, then clamped to
  `maxMetaCell` (60) runes with the last spent on an ellipsis where it cut. The bound is a
  constant because meta is content-controlled — 200 short rows beside one 1 MB value printed
  202 MB, ~200x what was stored. `displayCell` cuts the *raw* value to one rune past the cap
  before escaping it, because `render.SanitizeLine` walks every byte it is handed and copies the
  lot when anything needs escaping; the extra rune is what makes that free of consequence, since
  escaping never shrinks a string. Widths are counted with `utf8.RuneCountInString`, which is
  what `fmt`'s `%-*s` pads to; `len` over-padded any cell holding a multibyte rune and stepped
  every row below it right.
- `cmd/fngr/store.go` — Defines the narrow `eventStore` interface that commands depend on plus the
  injectable `ioStreams` (`In io.Reader`, `Out io.Writer`, `Err io.Writer`, `IsTTY bool`).
- `cmd/fngr/clock.go` — `warnSkippedClock(w, exists, asked, stored)`, the single formatter for the
  DST-gap warning described under `internal/timefmt`. Called by `add` (both `--time` and the title
  prefix), by `event time` / `event date`, and by the `--format=json` import (`buildCLIDefaults`
  for the shared `--time`, `runJSON` for each record's `created_at`) — every path that turns a
  wall clock the user typed into a stored timestamp. A warning on stderr, never a refusal: the
  shifted instant is a real one and almost certainly the intended entry, so failing would leave
  nothing useful to type instead. A caller that cannot be contradicted (no timestamp given, or an
  RFC 3339 stamp, which names an instant rather than a local clock) passes `exists = true` and it
  is a no-op. The import quotes only the *first* offending record and appends a count for the
  rest: a batch runs to 10 000 records and one line each would bury the result.
- `cmd/fngr/prompt.go` — `confirm(in, out, prompt, defaultVal) (bool, error)` shared yes/no helper.
  An empty answer takes `defaultVal`, but end-of-input with *nothing typed* returns `errNoAnswer`
  instead: a closed or empty stdin (cron, CI, `</dev/null`) is silence, not consent, and the
  `meta rename` prompt defaults to yes — an unattended run that forgot `-f` used to rewrite
  metadata across every event and report success. Both halves of the condition matter:
  `ReadString` also returns `io.EOF` for a final line with no trailing newline, so a bare `y`
  must still confirm. The read itself blocks by design — unlike `resolveBody`, a prompt reading
  stdin *is* the point, so `echo y | fngr delete 1` keeps working and `sleep 30 | fngr meta
  delete '#wip'` waits (REVIEW H6, won't fix; `-f` is the unattended form).
- `cmd/fngr/body.go` — Body-source dispatch for `fngr add`. `resolveBody` returns the body string
  from one of {joined args, stdin, editor}: args win, then `-e`, then a TTY opens the editor, and
  stdin is read only when nothing else can supply a body — all of it downstream of the
  `errEditNeedsTTY` veto below, which can fail a run that plain args would have satisfied. Stdin is
  *never* touched otherwise — reading it blocks until EOF, and an open-but-idle pipe delivers
  neither a byte nor EOF, so the old up-front `bufio` peek (which existed only to report
  args+stdin and `-e`+stdin as conflicts) could hang `fngr add "note"` forever. Those two
  conflicts are gone with it; extra stdin is silently unread. Blocking is still correct in the
  stdin-only branch, where the body is exactly what we are waiting for; an empty `/dev/null`
  hits EOF at once and `readStdin` reports `event title cannot be empty`.
  Both were reinstated in one broader, capability- rather than content-based check that costs no
  read: `-e` with `!IsTTY` returns `errEditNeedsTTY` (`--edit needs a terminal`), which also
  catches the `fngr add -e </dev/null` case the old checks never did. Launching anyway handed
  `$EDITOR` a non-terminal stdin — vim bails, a non-interactive `$EDITOR` saves nothing, and
  `fngr add` then reported a cancel at exit 0 while silently dropping the piped body.
  `launchEditor` is a `var` for test stubbing; `realLaunchEditor` execs `$VISUAL`/`$EDITOR` on a
  temp file and inherits `os.Stdin`, so the veto must stay upstream of it; `errCancel` signals
  empty-save (handled as exit-0 by `AddCmd.Run`). `readStdin`
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
- `cmd/fngr/pager.go` — `withPager(io, disabled) (ioStreams, closer)` wraps `Out` in a 16 KiB
  `bufio.Writer` over whatever `pagerWriter` hands back: a pipe to `$PAGER` (fallback
  `less -FRX`) when stdout is a TTY, else `Out` itself. Used by `list`. The buffer is *outside*
  the pager branch on purpose — rendering wrote one line per syscall, 250 000 of them for a 250k
  list and 10-19% of the wall clock (every format but CSV, whose `csv.Writer` buffers on its
  own), and the redirect/pipe path that pays that is exactly the one the old function
  early-returned on. 16 KiB rather than 64 because the buffer is also the
  latency floor for `fngr | head -3`. The closer *returns* its flush error, and `ListCmd.Run`
  promotes it to the command's error through a named return (without masking an error already on
  its way out): with `Out` buffered the tail of a listing is written there and nowhere else, so
  logging past it would exit 0 over output the user never received. The pager's own exit status
  stays a stderr warning — a pager quit early is a decision, not a lost write. `isTerminal` is a
  `var` for test stubbing (like `launchEditor`): it is the only gate on the pager branch and no
  test process has a terminal on stdout, so stubbing it is what lets the branch be tested at all.
- `internal/db/db.go` — DB path resolution (explicit > `.fngr.db` in cwd > `~/.fngr.db`) and
  connection setup. The FK + WAL + busy_timeout + synchronous=NORMAL pragmas ride in the DSN
  (`file:<path>?_pragma=...`, built with `net/url`) rather than post-open `db.Exec` calls —
  `*sql.DB` is a pool, so an Exec configures one connection and leaves the rest at SQLite
  defaults (`foreign_keys=OFF`, `busy_timeout=0`), which silently loses events under concurrent
  writes. Don't move them back. `Open` pings once so an unusable path fails there rather than
  from an arbitrary later query. That ping's error goes through `openHint` → `pathHint` →
  `dirHint`, which replace the two result codes that name the database whatever is actually
  wrong — SQLITE_CANTOPEN (`unable to open database file`) and, once the file exists,
  SQLITE_READONLY (`attempt to write a readonly database`, said of a directory a `fngr list`
  never meant to write) — with the filesystem object at fault: the path is a directory, the file
  is not readable, or its parent is missing, is not a directory, or is not writable. `openHint`
  masks the code to its low byte, since those arrive extended (`SQLITE_READONLY_DIRECTORY` is
  1544, not 8), and gates on the *type and code* rather than the message text, so a driver
  reword costs nothing. Any code outside those two keeps the driver's own text, which is
  sometimes the better answer even when the path is also broken — a corrupt file in a read-only
  directory should say `file is not a database (26)`. `dirHint` is called directly on the
  `create == false` missing-file branch (the stat there already settled the file half), so
  `fngr list --db /nope/x.db` says the directory does not exist instead of suggesting
  `fngr add`, which would fail the same way; it is also the only path that runs when nothing has
  failed yet, an accepted cost because a user who cannot write the directory needs telling on
  exactly that run. Its message names the `-wal`/`-shm` siblings, because the parent being
  writable is what a *read* needs too and no message about the database file explains that. The
  writability probe is a real `os.CreateTemp` rather than a mode-bit read, since ACLs, read-only
  mounts and a container UID that doesn't own the volume all deny a write the bits allow — but
  only `fs.ErrPermission` and `EROFS` are reported as "not writable", because a full disk or an
  fd-exhausted process fails the probe too (and the latter is itself a reason SQLite returns
  CANTOPEN), and the hint *replaces* the driver text rather than joining it, so a guess there
  would swap a true diagnosis for a false one.
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
  then rewrites every `events_fts` row through `legacyFTSContent` unconditionally, because
  migration 3's index rebuild was a second SQL transliteration of that same helper.
  `legacyFTSContent` is the one-column formula spelled out in this file rather than called from
  `parse`, for the same reason `legacyMetaNameRe` is: a migration has to keep writing what it
  wrote, and live code moved on at migration 6. (Migration 6 always runs in the same pass and
  drops the index this fills, so the value never reaches a query — which is not a licence to let
  it drift, and `TestLegacyFTSContent` is the only thing that would notice.) The SQL half
  (`4.sql`) is a single `ANALYZE event_meta`, refreshing statistics migration 2 left describing a
  dropped index.
- `internal/db/migrate5.go` — The Go half of migration 5, which adds `event_meta.source`
  (`'body'` | `'explicit'`) so `Update`'s body-tag sync can retract only what a body mention
  minted. `5.sql` defaults every existing row to `'explicit'`; `classifyMetaSource` then demotes
  the ones the event's own title+body still yield via `parse.BodyTags`. That is a reconstruction,
  not a lookup — provenance was never recorded — and it is lossy for exactly one case: a tuple
  added *both* ways is a single row, and it comes out `'body'`, the same tie-break `addInTx`
  makes for a new event. `event tag` promotes such a row back. `5.sql` carries a
  `CHECK (source IN ('body','explicit'))` because both values are written as bare literals from
  Go and SQL alike, and no index — every statement filtering on `source` also gives
  `(key, value, event_id)`, which migration 2's unique index already serves.
- `internal/db/migrate6.go` — The Go half of migration 6, which splits `events_fts` into a
  `content` column (the event's own text) and a `meta` one (its `key=value` tokens). Sharing one
  column made a body that *said* `tag=ops` indistinguishable from an event *tagged* that way, so
  any note could write itself into a tag view or (with `!`) out of one. A virtual table takes no
  `ALTER TABLE ADD COLUMN`, so `6.sql` drops and recreates the index and `splitFTSIndex`
  re-populates it through `parse.FTSColumns` — a Go step, not SQL, for the reason migration 4
  exists at all. `6.sql` does not recreate `trg_events_fts_delete`: the trigger is `ON events`,
  so the drop leaves it alone and its body still matches the new table.
- `internal/parse/parse.go` — `Meta` type, `BodyTags` for body-tag extraction (`@person` → people,
  `#tag` → tag), `KeyValue` helper for `key=value` strings, `FlagMeta` for `--meta` flag arrays
  (delegates to `KeyValue`), `MetaArg` for individual CLI tag args (`@person`, `#tag`, or
  `key=value`; used by `event tag` / `event untag`), `FTSColumns` for the two `events_fts` column
  values (both stated in one function so a caller cannot write one and forget the other; the
  pre-migration-6 single-column join is *not* here — it is frozen as `db.legacyFTSContent`
  beside the migration that still writes it),
  `SplitTitleBody` for the `". "` title/body split, and `EventText` for the title+body join every
  body-tag derivation runs over — `addInTx`, `Update`'s sync and migration 5's back-fill each
  decide provenance from it, so a join that differs by a space would have them disagree about a
  tag at the boundary.
  Tag and meta-name regexes share the private `metaNamePattern` constant; the anchored form is
  exported as `MetaNameRe` for reuse by `cmd/fngr/meta.go::parseMetaFilter`. That pattern is
  Unicode-class based (`\p{L}\p{N}_/-`), not `\w` — Go's `\w` is ASCII-only, so it truncated
  `@josé` to `people=jos` and collided with `@josa`. The body-tag patterns additionally require
  `metaNameBoundary` (start of text or a non-name rune) before the sigil, so `bob@example.com`
  and `.../guide#installation` no longer mint metadata. Don't reintroduce `\w` in either.
- `internal/timefmt/timefmt.go` — Single source of truth for accepted time inputs. `Parse` returns
  just the parsed timestamp; `ParsePartial` also reports whether the input had a date and/or time
  component, so `event time` / `event date` can splice into an existing timestamp instead of
  replacing it, plus whether the clock it resolved actually `exists` (below). `ParsePartial` first
  tries `parseRelative` (now/today/yesterday, `N
  {minute|hour|day|week|month}s ago`, `<day> at <time>`; `a`/`an` count as 1) anchored on a passed-in
  `now` for testability; sub-day offsets and `now` are date+time, bare relative days carry now's
  time-of-day but report date-only so splicing still works. Then it falls back to the absolute
  `fullFormats` / time-only layouts (`parseClock` shared with the relative path). Splice via
  `SpliceTime` / `SpliceDate` (mirror-image helpers that mix orig/new
  date+time around the existing timezone). `SplitTimePrefix(s)` extracts a leading timestamp from
  free text delimited by `": "` (used by `fngr add` title parsing), delegating to `ParsePartial`;
  it returns the consumed token as well as the rest, so the caller can name in a warning the very
  text the user typed.
  Every wall clock is assembled through `localClock`, which reports whether that clock exists —
  a DST spring-forward skips an hour and both `time.Date` and `time.ParseInLocation` resolve a
  clock inside the gap to the hour before it with no error, so `fngr event time N 2:30` stored a
  timestamp an hour off and reported success. `localClock` being the single gate is what makes the
  answer exhaustive: `parsePartial` reads the absolute layouts in UTC — which has no transitions,
  so the clock comes back exactly as written — and rebuilds those fields through it, and the
  `<day> at <time>` relative branch goes through it too. That last one is why `exists` is a return
  value of `ParsePartial` rather than the separate string predicate it started as: a predicate
  re-parsing the text is a second parser to keep in step, and it silently missed
  `yesterday at 2:30`. `layoutHasOffset` short-circuits RFC 3339 to true (an offset names an
  instant, not a local clock) and `layoutHasTime` does the same for the date-only layout (a bare
  date names no clock, and some zones shift DST at 00:00). Every other relative form takes its
  clock from `now`, a real instant. Fall-back ambiguity counts as existing. Tests need a real zone
  (`time.FixedZone` has no transitions): they swap
  `time.Local` in a **non-parallel** test with `t.Cleanup` restore, and `_ "time/tzdata"` embeds
  the zone database.
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
- `internal/event/meta.go` — Domain meta key constants (`MetaKeyAuthor`, etc.), the
  `metaSourceBody` / `metaSourceExplicit` values of `event_meta.source`, and `MergeMeta`, which
  merges all meta sources for a new event (author, body tags, explicit entries) with dedup.
  `CollectMeta` is `MergeMeta` over `--meta key=value` strings. An explicit `author` *replaces*
  the default rather than joining it; two distinct explicit authors are an error, and so is an
  empty one (`-m author=` used to both blank the author and discard the real one). `author` is
  single-valued because `AuthorOf` — the one lookup, used by `render` and the JSON import —
  returns the first match in `ORDER BY key, value`, so a second row means the displayed author
  is alphabetical rather than true. `requireOneAuthor` restates that at the writer, so an
  `AddInput` built directly cannot create the state no meta verb is allowed to repair. Also
  `metaSet` and `subtractMeta`, the tuple-set helpers `addInTx` and `Update` use.
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
  `addInTx` also enforces `requireOneAuthor` and stamps each meta row's `source`, re-deriving
  `parse.BodyTags(parse.EventText(...))` because `AddInput.Meta` arrives already merged; a tuple
  the text yields is recorded as body-derived even when `--meta` named it too — the same
  tie-break migration 5 makes, which also keeps a `--format=json` round trip from freezing every
  body tag as explicit.
  `Get`, `Update` (title, body, and/or timestamp; on title or body change body-derived tags are
  *synced* — `parse.BodyTags(old) \ parse.BodyTags(new)` deleted via `execBodyMetaTuples` with
  `deleteBodyMetaSQL`, which matches `source = 'body'` only, then `parse.BodyTags(new)` inserted
  with `insertBodyMetaSQL`'s `ON CONFLICT DO NOTHING` so an explicit row keeps its provenance;
  FTS rebuilt. The source filter is what carries the semantics — without it an edit that drops an
  inline `@bob` also deletes the `people=bob` an operator added by hand, the two being the same
  row. Subtracting the delta is an optimisation on top, same end state either way, worth it
  because most edits touch no tags and would otherwise rewrite every row), `Reparent` (set/clear
  `parent_id`; rejects self and ancestry cycles via `ErrCycle`. Its upward walk carries a `seen`
  set seeded with the starting node — that is a *bound*, not part of the cycle check: a cycle
  sitting upstream of the walk never contains the moving event, so the `parent == id` test never
  fires and the loop spun at full CPU forever inside an open transaction. A repeat means the
  stored chain is already cyclic → `ErrCorruptTree`), `AddTags` (inserts as
  `'explicit'`, *promoting* an existing body-derived row, since a tag named on the command line
  must survive the next body edit; the `DO UPDATE` is guarded on `source <> excluded.source` so
  only that promotion rewrites a row, and the added count comes from a pre-read because
  `RowsAffected` cannot tell the promotion from an insert) / `RemoveTags` (event-scoped meta CRUD with FTS
  resync; both refuse `protectedMetaKeys`), `Delete`, `HasChildren`, `List` / `ListSeq` (FTS5 filter + date range + `Limit` +
  `Ascending`, both built by the shared `buildListQuery`. A `Limit` always keeps the newest N and
  `Ascending` decides display order only, so the limited query sorts `DESC` and the ascending case
  wraps it — `SELECT * FROM (… ORDER BY e.created_at DESC LIMIT ?) ORDER BY created_at ASC`. One
  statement would apply `ORDER BY` before `LIMIT` and let the sort direction pick *which* rows
  survive, so `fngr -n 20 -r` returned the 20 oldest events in the database. `SELECT *` rather
  than restating the columns: the subquery already fixes them),
  `GetSubtree` (recursive CTE whose recursive term is `UNION`, not `UNION ALL` — on
  a cyclic chain an `UNION ALL` does not recurse deeply, it never returns at all: no output, no
  error, one core pinned. `UNION` discards a row already in the result, so a second lap adds
  nothing and the queue drains; on a well-formed tree the two are identical because `id` is the
  primary key, at ~15% for the dedup index. Don't switch it back. Terminating is not answering,
  so the scan after it reports `ErrCorruptTree` rather than passing a deduped loop off as a
  subtree: a cycle is reachable only from a member of it — every member's parent is another
  member, so nothing outside can descend in — which reduces the test to whether the queried
  root's own parent came back among its descendants), `ListMeta` (filtered via `ListMetaOpts{Key, Value}`),
  `CountMeta`, `UpdateMeta` (a *merge*, not a plain rename — `UPDATE OR REPLACE` drops the row
  colliding with migration 2's `UNIQUE(key, value, event_id)`, so an event carrying both tags ends
  up with one. Plain `UPDATE` aborts the whole transaction there and renames nothing; any
  two-statement formulation instead needs an `old == new` guard, because its delete half would
  take out the rows the update half just wrote; the renamed row is also stamped
  `source = 'explicit'`, since no event's text yields the new value and a body-derived row would
  be deleted by the next edit of any renamed event), `DeleteMeta`. `UpdateMeta` and `DeleteMeta`
  refuse `protectedMetaKeys` too. `UpdateMeta` goes through `requireRenamableMeta`, which is
  looser by one case: a rename that *changes* the key is refused at both ends (`author=x` →
  `k=v` strips the author off every event, `k=v` → `author=evil` mints a second one), but a
  same-key value rewrite leaves every event with the single row it had, so a mistyped
  `--author` stays correctable — protecting it against that too made `author` the one field
  nothing could repair. An empty new value is still refused.
  All functions accept
  `context.Context`. `ErrNotFound`, `ErrCycle`, `ErrTimeRange` and `ErrCorruptTree` sentinels —
  the last one distinct from `ErrCycle` on purpose: `ErrCycle` refuses a requested change,
  `ErrCorruptTree` reports a chain that was already broken when fngr opened the file (a
  hand-edit, a half-written database, or someone else's `.fngr.db` that `db.ResolvePath` picked
  up from the current directory).
  `loadMetaBatch` chunks the IN clause to stay under SQLite's parameter limit. Private helpers:
  `requireEventExists` (existence check used by every mutation function), `rebuildEventFTS`
  (used by Update/AddTags/RemoveTags to resync `events_fts`), `execBodyMetaTuples` (both halves
  of Update's body-tag sync, driven by `deleteBodyMetaSQL` / `insertBodyMetaSQL`),
  `requireUnprotectedMeta` / `requireUnprotectedTags` / `requireRenamableMeta` (the
  `protectedMetaKeys` gate — currently just `author`, refused as the target of every meta verb
  because no insert path can produce zero or two of them),
  `formatTimestamp` (the only writer of
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
  stays outside the quotes as a prefix search. Each term also carries an FTS5 column filter
  (`meta:` or `content:`) so the two halves of the index migration 6 split cannot be searched as
  one haystack — unscoped, an event whose body *said* `secret=classified` answered a filter for
  that tag as squarely as the event tagged with it. The trade is that a body *quoting* a
  `key=value` string is no longer reachable by searching for it; the words around it still are.
  `ftsTerm` routes a `@person` / `#tag` shorthand to `meta:` via `parse.MetaArg`, and any term
  that splits on `=` into a non-empty key to `meta:` — that is exactly what `parse.MetaArg` /
  `parse.FlagMeta` accept as a key, and testing anything narrower here (`parse.MetaNameRe`, say)
  routes a key those paths happily store, `-m ticket.id=PROJ-42`, to a column that cannot answer
  for it, leaving it findable by nothing. Only `=oops` and terms with no `=` are `content:`. The
  value half is deliberately unchecked, so `-S 'tag=*'` (text `tag=` plus a prefix star) stays the
  "everything tagged" query it reads as.
  Malformed input fails at parse time with an
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
  `Tree` drives a `treeWriter` whose `prefix []byte` is appended to on the way down and truncated
  on the way back up, and whose `line []byte` assembles prefix+connector+event so each node
  costs the writer exactly one `Write`. It used to concatenate two fresh strings per node, each
  pinned by every ancestor frame — O(depth²) live bytes, 7.0 GB on a 50k chain. Don't go back to
  strings. `node(id, connector, continuation)` is the only entry point: roots go through it too,
  passing an empty connector, so there is no separate root-drawing path. `top(id)` is the only
  caller that decides the marker, for the root loop and the sweep alike. An event whose parent is
  absent from the result set (`--limit`, or a filter that matched only the child) still renders
  at top level but carries the `⋯└─ ` `orphanConnector` rather than passing as a true root; it
  and `orphanBlank` are four columns wide so orphan subtrees stay aligned. Every input event
  reaches the output exactly once whatever the topology claims: `treeWriter.visited` (a `[]bool`
  indexed through `byID`, not a map — a map cost 148 KB/op on the 5k-deep benchmark this file
  already guards) skips a node already drawn, impossible in a real tree with one parent each, so
  a cyclic chain cannot recurse until the stack gives out; a final sweep then draws anything the
  root walk never reached, needing no test of its own because `node` skips what is drawn.
  Members of a cycle have no root above them, so `Tree` used to find no roots, write nothing and
  return nil — `fngr` printed an empty journal and exited 0.
- `internal/render/sanitize.go` — `SanitizeLine` / `sanitizeBlock` escape control characters
  (C0, DEL, and C1 — a bare U+009B is a CSI introducer) as `\n`, `\r`, `\xNN`. Tab always
  survives; newline survives only in `sanitizeBlock`, used for the body block of `fngr event N`.
  Applied in `formatEventLine` (so tree/flat/their streams cannot forget), `Event`, per-line in
  `Markdown`, and — via the exported name — in `cmd/fngr/meta.go`, which lays out its own
  columns and must escape *before* measuring them. Escaped, not dropped: fngr is a journal and
  silently rewriting stored text is worse than showing `\x1b`. Not TTY-gated — a forged row is
  just as misleading piped into a script. The walk is byte-oriented, not `for _, r := range s`:
  rune iteration turns a raw 0x9b (the 8-bit CSI, which a pipe can store and Kong cannot) into
  `utf8.RuneError` and passes it through, and silently rewrites every other malformed byte to
  U+FFFD. Don't go back to ranging. JSON and CSV are excluded for different reasons — JSON
  escapes control bytes itself and losslessly, so sanitizing would break the `--format=json`
  round trip; CSV does *not* escape them (`csv.Writer` quotes for structural safety only) and
  that is an accepted trade, since escaping a data export would leave a reader unable to tell a
  stored `\x1b` from an escaped one.

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
