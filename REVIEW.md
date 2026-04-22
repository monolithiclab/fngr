# Code Review — 2026-04-22 (post-`--format=md` + /simplify + security pass)

Snapshot taken after the `--format=md` epic shipped (commits
`7ed7cfc..25f4819`), a whole-codebase /simplify pass (`2b52748`), the
GitHub Actions roadmap entry (`e4ba501`), and a defensive input-bound
pass (`6059857`, `13da4e3`). The codebase is now ~3 100 LoC of
production Go (~9 100 with tests), 86.1% test coverage, all linters
green. Architecture is unchanged at the package boundaries.

What's new since the prior review (2026-04-20):

- **Markdown output** (`internal/render/markdown.go`, ~75 LoC) —
  buffered `Markdown` + streaming `MarkdownStream` reusing the shared
  `renderMarkdownEvent` helper; wired into all three render dispatchers
  via `FormatMarkdown` and into Kong via the existing `${LIST_FORMATS}`
  / `${EVENT_FORMATS}` interpolation.
- **Time splicing** (`internal/timefmt/timefmt.go`) — `SpliceTime` and
  `SpliceDate` mirror-image helpers extracted from `EventTimeCmd` /
  `EventDateCmd` (replaced two near-identical inline `time.Date(...)`
  calls). 100% direct unit-test coverage.
- **Meta-name regex consolidation** (`internal/parse/parse.go`) — three
  copies of `[\w][\w/\-]*` collapsed onto a single `metaNamePattern`
  string constant; the anchored form is now the exported `MetaNameRe`,
  reused from `cmd/fngr/meta.go::parseMetaFilter`.
- **Render hot-path tightening** — `toJSONEvent` and
  `renderMarkdownEvent` pre-allocate the meta `pairs` slice to exact
  capacity and use index assignment; the markdown path drops
  `fmt.Sprintf` per tuple in favor of `m.Key + "=" + m.Value` plus
  `slices.Sort` over the formatted strings.
- **Defensive input bounds** — `cmd/fngr/body.go::readStdin` now wraps
  the reader in `io.LimitReader` at 16 MiB (`maxStdinBytes`) so a
  runaway pipe can't OOM. `cmd/fngr/add_json.go::parseJSONAddInput`
  rejects array imports over 10 000 records, dispatches deterministically
  on the first non-whitespace character, and uses `json.Decoder` with
  `DisallowUnknownFields` so typos surface instead of being silently
  dropped.
- **Empty-list UX** — `fngr` (default tree path) now writes
  `"No events found."` to stderr instead of exiting silently; the
  message matches `fngr meta`'s convention.
- **GitHub Actions roadmap entry** — `Project infrastructure` section
  in `docs/superpowers/roadmap.md` describes the planned CI workflow
  (`make ci` on push/PR, matrix on ubuntu+macOS) and the release
  workflow (cross-compile on `v*.*.*` tags).

All findings from the 2026-04-17, 2026-04-19, and 2026-04-20 reviews
remain resolved. The compact list below carries forward only the items
that are actually open today.

## Resolved this round

- **F2** — godoc comments added across the listed surface: `event.go` (`ErrNotFound`, `Event`, `MetaCount`, `Add`, `Get`, `Delete`, `Update`, `HasChildren`, `UpdateMeta`, `DeleteMeta`, `CountMeta`, `ListOpts`, `GetSubtree`); `meta.go` (`MetaKeyAuthor`/`MetaKeyPeople`/`MetaKeyTag`, `CollectMeta`); `store.go` (`NewStore`); `render.go` (`Tree`, `Flat`, `JSON`, `CSV`, `Event`); `parse.go` (`MetaNameRe`). The remaining symbols on the original list (`AddMany`, `AddInput`, `Reparent`, `AddTags`, `RemoveTags`, `ErrCycle`, `ListMeta`, `ListMetaOpts`, `ListSeq`, `List`, `Store`, `FlatStream`, `CSVStream`, `JSONStream`) already had godoc; the F2 evidence list overstated the gap.
- **F6** — Makefile fallback drops `--always` and emits `dev-<short-SHA>` when no tag exists. Tagged builds keep `git describe`'s native `v0.1.0[-N-gXXXX][-dirty]` shape.
- **B10** — `parse.MetaArg` now wraps `KeyValue`'s error with `parse meta arg %q: %w`, matching `FlagMeta`'s pattern. (Defensive: today the path is unreachable because `MetaArg` pre-checks for `=`, but the wrap protects against future `KeyValue` validation tightening.)
- **S4** — `applyMigration` gained a doc comment reminding writers to use `CREATE ... IF NOT EXISTS` / `DROP ... IF EXISTS` clauses so manual recovery scripts stay re-runnable.

## Findings

| #  | Severity | Finding                                                                                                                                                          | Evidence                                                                                                                                                                                                                | Recommendation                                                                                                                                                                                                                                            |
| -- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F7 | low      | Filter syntax errors bubble raw FTS5 / SQLite error text. Users hitting `fngr -S '"unmatched'` see `fts5: syntax error near ...` with no hint to consult `--help`. | `cmd/fngr/list.go::Run` returns the error from `s.ListSeq` / `s.List` unchanged; same for `cmd/fngr/event.go::EventShowCmd.Run` when filtering would apply.                                                              | Wrap match errors at the command layer with `fmt.Errorf("invalid filter syntax (%w); see --help for the -S grammar", err)` when the underlying error mentions FTS. Small change; gates on detecting "FTS5"/"fts5" in the error text or a sentinel.        |
| P6 | low      | Migration 2 deduplicates `event_meta` and rebuilds the unique index but doesn't run `ANALYZE event_meta`. SQLite's planner uses stale stats until the auto-analyze threshold (10% row change) trips. | `internal/db/migrations/2.sql`                                                                                                                                                                                          | Append `ANALYZE event_meta;` to migration 2 only if shipping a new migration anyway (don't bump for ANALYZE alone — existing users have already drifted past the 10% threshold). Riding along with migration 3 is fine.                                |

## Documentation Gaps

Already applied in this review's change set:

- `CLAUDE.md` — `parse` bullet mentions `metaNamePattern` constant and exported `MetaNameRe`; `timefmt` bullet mentions `SpliceTime` / `SpliceDate`; `body` bullet mentions the 16 MiB stdin cap; `add_json` bullet mentions dispatch-by-first-char + 10 000-record cap + DisallowUnknownFields; `render` bullet already mentions Markdown/MarkdownStream.
- `README.md` — explicit note that JSON is the only round-trip format (flat/csv/md are lossy output-only); markdown explanation moved adjacent to its example.

Not yet addressed (intentional):

- README has no troubleshooting section (e.g. "DB corruption: `cp` recovery"). Single-user tool; document on the first real user request.

## Won't Fix / Out of Scope

Carried forward from prior reviews. New entries (this review) marked **(new)**. Each entry states why so we don't re-propose them.

| Topic                                                              | Reason                                                                                                                                                                  |
| ------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| CSV "formula injection" sanitization                               | Intentional. `TestCSV_SpecialChars` asserts the raw `=formula` is preserved. Local single-user export; user owns downstream pasting.                                    |
| Path traversal via `--db`                                          | Not exploitable. The user already controls the process and the filesystem; `--db /etc/passwd` simply fails on open.                                                      |
| Tighter default file permissions on the SQLite DB                  | Low value for a single-user tool. Users who care can `chmod 0600 ~/.fngr.db`. Auto-chmod inside `db.Open` would be a surprising side effect.                              |
| Recursive CTE depth cap in `GetSubtree`                            | FK constraints + Reparent's cycle check prevent write-path cycles; trees are user-curated. No untrusted input path.                                                      |
| Soft delete / undo                                                 | Adds schema complexity and a parallel "alive" view path for marginal benefit. Backups (`cp ~/.fngr.db`) cover recovery.                                                  |
| Config file (`~/.fngr.config`)                                     | Env vars + CLI flags already cover persistent defaults. A config layer means precedence rules, parsing, and a third place to look for behavior.                          |
| Stats / summary command                                            | Anything useful is a one-liner against the SQLite file. Bloats the CLI surface for a workflow most users will run rarely.                                                |
| Author normalization / user registry                               | Belongs to user data hygiene, not the tool. `meta rename` already exists for cleanup.                                                                                    |
| Shell completion                                                   | Kong supports it natively if needed. Not load-bearing; revisit on user request.                                                                                          |
| Multiple databases / workspaces                                    | `cd` plus `FNGR_DB` already implements this. No need for a workspace concept.                                                                                            |
| Snapshot / backup command                                          | A copy of the SQLite file is the backup. Don't reinvent.                                                                                                                 |
| Database maintenance commands (`vacuum`, etc.)                     | Single SQL statement; not worth a CLI surface. Document in README only if a user actually asks.                                                                           |
| Two `testDB` helpers (in `internal/db` and `internal/event`)       | Different scopes (raw connection vs. `db.Open`-wrapped). Sharing them would couple test packages without removing real duplication.                                       |
| FTS triggers for INSERT/UPDATE on `events`                         | FTS content combines event text *and* meta tokens, so triggers can't see the full picture. `event.Add`/`Update`/`AddTags`/`RemoveTags` keep FTS in sync.                  |
| Bulk operations / filtered delete                                  | Composes from `fngr -S '...' --format json | jq | xargs fngr delete` for the rare case. Adding `--filter` to `delete` adds destructive surface area for marginal value.   |
| Relative dates (`today`, `yesterday`)                              | `timefmt` is the natural place to add them later if requested. Not load-bearing for current usage; do not preempt. **(new — confirmed)** Shell already handles this fine: `--time "$(date -d yesterday +%F)"`. |
| Splitting `cmd/fngr/event.go` per verb                             | Spec deliberately put all eight verbs in one file (single cohesive responsibility). 244 LoC is fine.                                                                     |
| Extra exit-code signaling on not-found                             | Kong's `ctx.FatalIfErrorf` already propagates non-zero on every returned error.                                                                                          |
| Drop `idx_event_meta_event_id` after migration 2                   | The two indexes have different prefix orders: `(event_id, key, value)` for `loadMetaBatch`/per-event lookups; `(key, value, event_id)` for `ListMeta`/`CountMeta` and uniqueness. Not redundant. |
| Tune `loadMetaBatch` chunk size                                    | 500 is well under SQLite's default `SQLITE_MAX_VARIABLE_NUMBER` (999). No measured benefit to changing without profiling.                                                |
| Defer pager spawn until first output line                          | `less -F` already quits-if-fits-on-screen. Spawn cost is sub-100ms on local exec; not worth refactoring.                                                                  |
| Unify confirm-prompt defaults across delete/meta verbs             | Deliberate asymmetry: destructive verbs (`delete`, `meta delete`) default `[y/N]`; rename verbs (`meta rename`) default `[Y/n]`. Pattern is consistent within categories. |
| Show before/after diff before `event text` commits                 | `event N` is the canonical inspection tool. The user explicitly chose "no prompts on event verbs" during the S2 brainstorm.                                              |
| `deleteMetaTuples` / `insertMetaTuples` vs `RemoveTags`/`AddTags`  | The private helpers run inside an existing `tx`; the public functions own the tx + existence check + FTS rebuild. Sharing them would leak `*sql.Tx` into the public API. |
| Recursive-CTE rewrite of `event.Reparent`'s ancestry loop          | SQLite is in-process; the per-row `SELECT parent_id` calls are microseconds, not network round-trips. The loop is clearer than a recursive CTE for the cycle-detection semantics. |
| Tokenize `$EDITOR` / `$VISUAL` for `vim -u NONE`-style values      | Plausible follow-up (matches `pagerCommand`'s tokenization), but not in the body-input modes spec. Most users set `EDITOR=vim` (single token); revisit on real demand.   |
| Comment-strip `git commit`-style editor template                   | Deliberately rejected during brainstorming (Q4 of body-input modes). Adds parsing surface for marginal gain — the user typed the flags themselves seconds ago.          |
| `-` as explicit stdin form (`fngr add -`)                          | Auto-detect via non-TTY pipe handles every real workflow; explicit form would only force stdin in a TTY, no use case today.                                              |
| Hardcoded editor fallback (`vi`/`nano`)                            | Minimal containers / CI may lack the chosen fallback; better to fail loudly with "set $EDITOR or $VISUAL" than wedge the user into an unfamiliar editor.                |
| Drop `t.Parallel()` from add-editor dispatch case                  | Race detector clean across 10+ iterations; the swap window is narrow and `TestResolveBody` inner subtests are sequential. Contingency documented in the body-modes plan. |
| `withTx(ctx, db, fn)` helper around the six `BeginTx` blocks       | **(new)** The closure indirection costs more clarity than the literal four-line pattern saves; six explicit copies read fine and each one is an obvious atomic boundary. |
| Move `MetaKeyAuthor` / `MetaKeyPeople` / `MetaKeyTag` to `parse`   | **(new)** Considered: parse.go would gain type safety for body-tag keys. Costs: parse needs to import event (it doesn't today), or constants move *into* parse (forces every caller to update). Trade-off favors leaving the three string literals where they sit — the test surface catches typos. |
| `--format` flag on `fngr delete`                                   | **(new)** Destructive verb; adding a "preview" format would compound with the existing confirmation UX without removing the prompt. `fngr event N --format=json` already shows what would be deleted.                                                              |
| Tree format on bare `fngr event N`                                 | **(new)** Single events have no topology; the `--tree` flag explicitly opts into the subtree view. Adding tree as a default format would invalidate `EventFormats`'s text-default contract.                                                                       |
| `slices.Sort(pairs)` over `slices.SortFunc(pairs, cmp.Compare)` for `[][2]string` in `toJSONEvent` | **(new)** The tuple version needs the lambda comparator (`(key, value)` order); `slices.Sort` only works on `cmp.Ordered`. The plain `slices.Sort` IS already used in markdown.go where pairs are pre-formatted strings. |
| Drop redundant meta sort in `toJSONEvent`                          | **(new)** SQL queries already `ORDER BY key, value` — but the in-Go sort guarantees deterministic output regardless of meta source (e.g. tests building `event.Event` literals directly). Cheap belt-and-suspenders. |
| Single-line fast path around `strings.Split(ev.Text, "\n")` in `renderMarkdownEvent` | **(new)** Saves one slice allocation per event but doubles the function's branching. The path is already dominated by `Fprintf` overhead; clarity wins.                                                                       |
| `sync.Once`-memoize `loadMigrations()` at startup                  | **(new)** Two migrations today; `embed.FS.ReadDir` + sort is sub-millisecond. Becomes interesting at ~10 migrations; profile-then-ship.                                                                                              |
| CSV header row dedup between `CSV` and `CSVStream`                 | **(new)** Five-element string slice repeated twice; extracting `var csvHeader = []string{...}` saves five tokens. Not worth the import scope.                                                                                                                       |
| Test helper duplication between `markdownSeq`/`markdownErrAt` and `staticSeq`/`errorAtSeq` | **Already resolved** in `25f4819`: markdown_test.go now uses the shared helpers from render_test.go.                                                                                                                                                                |
| Cache `pagerCommand()` via `sync.OnceValue`                        | **(new — confirmed)** Memoization breaks `t.Setenv` isolation in `TestWithPager_PagerStartFailureSurfaces` and `TestWithPager_PipesToPagerProcess`. Magnitude is sub-microsecond per one-shot CLI invocation (one call per `fngr list`); a test-only reset hatch isn't justified by the saving. |

## Next Review Pointers

Areas most likely to drift between now and the next review:

- **`internal/event/event.go`** — at ~830 LoC after `AddMany`/`addInTx`, still cohesive but
  creeping toward "too much in one place". Watch for new helpers proliferating; consider
  splitting along read vs. write boundaries if it grows past ~1 000 LoC.
- **`internal/db/migrate.go`** — schema changes drop a new `internal/db/migrations/<N>.sql`
  file rather than appending to a Go slice. `loadMigrations` asserts contiguity from 1; tests
  (`TestMigrate_BumpsUserVersion`, `TestMigrate_DetectsLegacyV1`,
  `TestMigrate_V2DedupesAndAddsUnique`) all assert against `migrations[len(migrations)-1].version`
  — keep that invariant when migration 3 lands. Use `IF NOT EXISTS` / `IF EXISTS` clauses
  in new migration SQL to keep the failure mode safe (see S4).
- **`cmd/fngr/event.go`** — the per-verb pattern (own ID arg, no parent context) is locked in
  by Kong's constraint. New verbs should follow it. Time-splice helpers now live in `timefmt`,
  not inline.
- **`cmd/fngr/meta.go`** — `MetaRenameCmd`/`MetaDeleteCmd` shape duplication is acceptable today;
  if a third "mutate by `(key, value)`" verb lands, revisit extraction. Filter parsing now
  reuses `parse.MetaNameRe` — if a third caller appears, the regex is already shared.
- **`cmd/fngr/body.go`** — `resolveBody`'s 8-row dispatch table is the load-bearing UX
  contract for `fngr add`. The ordering (`hasArgs && piped` MUST fire before any `useEditor`
  branch) is exercised by `TestResolveBody:args-editor-stdin-error`. `readStdin` now caps at
  16 MiB — bump `maxStdinBytes` if a real workflow hits it.
- **`cmd/fngr/add_json.go`** — dispatch on first non-whitespace char + `DisallowUnknownFields`.
  Schema additions need a wire-format version bump or a `--strict=false` escape hatch
  (currently no escape; intentional for the pre-1.0 invariant).
- **`internal/render/markdown.go`** — owns the per-event meta sort and the `lastDate` state
  machine. Markdown is intentionally not round-trippable; if that changes it'd belong in
  `cmd/fngr/add.go`'s import surface, not here.
- **`internal/timefmt`** — central enough that any "we accept relative dates now" request
  belongs here, not scattered across commands. `SpliceTime` / `SpliceDate` are the splice
  primitives — reuse from there.
- **`cmd/fngr/store.go::eventStore`** — keep it narrow. If a new command needs new methods,
  add them; if an existing command stops using a method, prune it.
- **`cmd/fngr/dispatch_test.go`** — every new top-level command or verb needs an entry. The
  `isTTY bool` per-case toggle and the per-case `launchEditor` swap pattern (currently scoped
  to `add-editor`) are the template for any future stdin/editor-touching dispatch entry.
- **GitHub Actions workflows** — once added (per `roadmap.md` "Project infrastructure"),
  CI green should be a hard prerequisite for merge. Until then, `make ci` is the local gate.
