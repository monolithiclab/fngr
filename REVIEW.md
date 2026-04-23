# Code Review — 2026-04-23 (post-v0.0.1 release pipeline ship)

Snapshot taken after the GitHub Actions CI + release pipeline shipped
end-to-end (commits `b229295..03244cf`). The codebase is now ~3 200
LoC of production Go (~9 300 with tests), 86.1% test coverage, all
linters green. **Architecture is unchanged at the package
boundaries** — this round's deliverable is operational
infrastructure, not Go code.

The project now publishes through three channels: `go install`,
`brew install monolithiclab/tap/fngr`, and `docker pull
ghcr.io/monolithiclab/fngr:<version>`. Every release artifact (binary
SHA256SUMS + multi-arch container manifest) is cosign-signed via
Sigstore keyless. v0.0.1 is the first stable release.

## What's new since the prior review (2026-04-22)

- **GitHub Actions CI workflow** (`.github/workflows/ci.yml`) — push
  to `main` + every PR runs `make lint test` on a Linux + macOS
  matrix; coverage profile uploaded as artifact from the ubuntu cell.
  Concurrency block kills in-flight runs on force-push. Branch
  protection on `main` requires both cells green before merge.
- **GitHub Actions release workflow** (`.github/workflows/release.yml`) —
  `v*.*.*` and `v*.*.*-*` tag pushes invoke goreleaser with QEMU +
  Buildx (multi-arch Docker), cosign-installer, ghcr.io login.
  Workflow-level `permissions: {contents: write, packages: write,
  id-token: write}`. `replace_existing_artifacts: true` makes
  partial-release re-runs idempotent.
- **GoReleaser config** (`.goreleaser.yaml`) — single source of truth
  for: 4-arch binaries (linux/darwin × amd64/arm64) with `CGO_ENABLED=0`
  + `-s -w` strip + git-derived `-X main.version`; tar.gz archives
  bundling `LICENSE` + `README.md`; `SHA256SUMS` checksum file with
  cosign keyless signing; multi-arch ghcr.io image via `docker_manifests`
  with cosign-signed manifest; Homebrew formula on
  `monolithiclab/homebrew-tap` with `skip_upload: auto` for
  pre-releases; Conventional-Commits-grouped changelog (Features /
  Bug fixes / Documentation / Other) excluding `chore:` and merge
  commits; `prerelease: auto` detects `-rc` / `-beta` / `-alpha`
  suffixes and marks the GitHub Release accordingly.
- **`Dockerfile`** — distroless-static-debian13 base (~2 MB), single
  COPY of the cross-compiled binary, `ENTRYPOINT ["/fngr"]`. Final
  image ~6 MB. Includes CA certs + `/usr/share/zoneinfo` + `/tmp`.
- **`LICENSE`** — MIT, attached to every release archive.
- **External infrastructure**:
  - `monolithiclab/homebrew-tap` repo created (public, README only;
    fngr formula lands at root as `fngr.rb`).
  - `monolithiclab` org packages policy updated to allow public
    container packages (UI-only setting, no REST API).
  - Fine-grained PAT (`fngr-release-homebrew-tap-token`, expires
    2027-04-22) scoped to Contents:R+W on the tap repo.
  - `HOMEBREW_TAP_TOKEN` set as **repo-level** secret on
    `monolithiclab/fngr` (not org-level — see Won't Fix below).
  - `monolithiclab/fngr` made public (required for branch protection
    on free plan; consistent with the open-source distribution
    posture).
  - `ghcr.io/monolithiclab/fngr` package visibility flipped to
    public (UI-only).
- **`docs/PUBLISHING.md`** (~445 lines) — reproducible playbook for
  shipping any sibling repo through the same pipeline. Captures
  prerequisites, org-level one-time setup, per-repo checklist, the
  exact verification commands, and a comprehensive "Gotchas" section
  recording every failure mode hit during the v0.0.1 rollout.
- **README** restructured: Install section reordered (brew → go
  install → pre-built binaries with cosign verify → build from
  source); Container usage promoted to a top-level section
  documenting the must-mount-DB rule, FNGR_DB pattern, IANA timezone
  via TZ, common one-liners, and the "no editor / no pager / no
  prompts" limitations.

## Findings

| #  | Severity | Finding                                                                                                                                                          | Evidence                                                                                                                                                                                                                | Recommendation                                                                                                                                                                                                                                            |
| -- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F7 | low      | Filter syntax errors bubble raw FTS5 / SQLite error text. Users hitting `fngr -S '"unmatched'` see `fts5: syntax error near ...` with no hint to consult `--help`. | `cmd/fngr/list.go::Run` returns the error from `s.ListSeq` / `s.List` unchanged; same for `cmd/fngr/event.go::EventShowCmd.Run` when filtering would apply.                                                              | Wrap match errors at the command layer with `fmt.Errorf("invalid filter syntax (%w); see --help for the -S grammar", err)` when the underlying error mentions FTS. Small change; gates on detecting "FTS5"/"fts5" in the error text or a sentinel.        |
| P6 | low      | Migration 2 deduplicates `event_meta` and rebuilds the unique index but doesn't run `ANALYZE event_meta`. SQLite's planner uses stale stats until the auto-analyze threshold (10% row change) trips. | `internal/db/migrations/2.sql`                                                                                                                                                                                          | Append `ANALYZE event_meta;` to migration 2 only if shipping a new migration anyway (don't bump for ANALYZE alone — existing users have already drifted past the 10% threshold). Riding along with migration 3 is fine.                                |

Both findings carry forward from the previous round. No new code-review-level findings emerged from this round (the rollout was infrastructure + docs; no production Go code shipped).

## Documentation Gaps

Already applied in this review's change set:

- `README.md` — Install section reordered (Homebrew first); new Container usage subsection with the must-mount-DB rule.
- `docs/PUBLISHING.md` (new) — full reproducible publishing playbook.
- `docs/superpowers/roadmap.md` — new "Publishing pipeline polish" section captures the four deferred GoReleaser / Homebrew / cosign / brew-formula-path migrations from the rollout.

Not yet addressed (intentional):

- README has no troubleshooting section for the `fngr` CLI itself (e.g. "DB corruption: `cp` recovery"). Single-user tool; document on the first real user request.
- `CLAUDE.md` doesn't reference `docs/PUBLISHING.md`; that's by design — CLAUDE.md is the architecture/conventions reference for in-repo work, not a publishing how-to.

## Won't Fix / Out of Scope

Carried forward from prior reviews. New entries (this round) marked **(new)**. Each entry states why so we don't re-propose them.

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
| Relative dates (`today`, `yesterday`)                              | `timefmt` is the natural place to add them later if requested. Shell already handles this fine: `--time "$(date -d yesterday +%F)"`.                                       |
| Splitting `cmd/fngr/event.go` per verb                             | Spec deliberately put all eight verbs in one file (single cohesive responsibility).                                                                                       |
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
| Drop `t.Parallel()` from add-editor dispatch case                  | Race detector clean across 10+ iterations; the swap window is narrow and `TestResolveBody` inner subtests are sequential.                                                |
| `withTx(ctx, db, fn)` helper around the six `BeginTx` blocks       | The closure indirection costs more clarity than the literal four-line pattern saves; six explicit copies read fine and each one is an obvious atomic boundary.            |
| Move `MetaKeyAuthor` / `MetaKeyPeople` / `MetaKeyTag` to `parse`   | Considered: parse.go would gain type safety for body-tag keys. Costs: parse needs to import event (it doesn't today), or constants move *into* parse (forces every caller to update). The three string literals stay where they are — the test surface catches typos. |
| `--format` flag on `fngr delete`                                   | Destructive verb; adding a "preview" format would compound with the existing confirmation UX without removing the prompt. `fngr event N --format=json` already shows what would be deleted.                                                              |
| Tree format on bare `fngr event N`                                 | Single events have no topology; the `--tree` flag explicitly opts into the subtree view.                                                                                                                                                                                |
| `slices.Sort(pairs)` over `slices.SortFunc(pairs, cmp.Compare)` for `[][2]string` in `toJSONEvent` | The tuple version needs the lambda comparator; `slices.Sort` only works on `cmp.Ordered`. The plain `slices.Sort` IS already used in markdown.go where pairs are pre-formatted strings. |
| Drop redundant meta sort in `toJSONEvent`                          | SQL queries already `ORDER BY key, value` — but the in-Go sort guarantees deterministic output regardless of meta source. Cheap belt-and-suspenders.                       |
| Single-line fast path around `strings.Split(ev.Text, "\n")` in `renderMarkdownEvent` | Saves one slice allocation per event but doubles the function's branching. The path is already dominated by `Fprintf` overhead; clarity wins.                            |
| `sync.Once`-memoize `loadMigrations()` at startup                  | Two migrations today; `embed.FS.ReadDir` + sort is sub-millisecond. Becomes interesting at ~10 migrations; profile-then-ship.                                              |
| CSV header row dedup between `CSV` and `CSVStream`                 | Five-element string slice repeated twice; extracting `var csvHeader = []string{...}` saves five tokens. Not worth the import scope.                                       |
| Cache `pagerCommand()` via `sync.OnceValue`                        | Memoization breaks `t.Setenv` isolation in pager tests. Magnitude is sub-microsecond per one-shot CLI invocation; a test-only reset hatch isn't justified.               |
| **(new)** Org-level Actions secret with `--visibility selected` for `HOMEBREW_TAP_TOKEN` | Despite passing all visibility checks (`gh secret list --org`, repo appears in `/orgs/.../actions/secrets/<NAME>/repositories`), the env var arrived **empty** in the workflow runner. Confirmed via a debug step printing `${#HOMEBREW_TAP_TOKEN}` (was 0). Workaround: set as a repo-level secret. Root cause unknown; documented in `docs/PUBLISHING.md`. |
| **(new)** Migrate `dockers:` + `docker_manifests:` to `dockers_v2:` now | Tracked in roadmap "Publishing pipeline polish". `dockers_v2` needs a multi-stage Dockerfile rewrite using buildx's `TARGETOS`/`TARGETARCH` build args. Defer until removal of `dockers:` becomes urgent. |
| **(new)** Migrate `brews:` to `homebrew_casks:` now                | Tracked in roadmap. `homebrew_casks` generates Cask DSL files (macOS-only, `brew install --cask`), which would break our cross-platform install path. Wait for GoReleaser to ship a `homebrew_formulas:` key. |
| **(new)** Upgrade `sigstore/cosign-installer` to `@v4`             | Tracked in roadmap. Cosign v4 deprecated `--output-signature`/`--output-certificate` in favor of a single `.sigstore.json` bundle. Migration touches `signs:`, README verification example, and `docs/PUBLISHING.md`. |
| **(new)** Move brew formula from tap root to `Formula/` subdir     | Tracked in roadmap. Both layouts work for `brew install`; `Formula/` is the conventional layout. One-line `directory: Formula` change in `brews:` block + re-tag. |
| **(new)** REST API for ghcr.io package visibility flip              | None exists. The "public/private" toggle is UI-only. `gh api -X PATCH /orgs/.../packages/container/<name>` returns 404. Documented in `docs/PUBLISHING.md`.              |
| **(new)** Auto-tag character expansion (`^location`, `+company`, `~mood`) | Open question; brainstorm separately before commitment. Existing `@person` / `#tag` covers the use case; expansion adds CLI vocabulary without measured demand.       |

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
  in new migration SQL to keep the failure mode safe.
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
- **`.goreleaser.yaml`** — load-bearing for releases; behavior is exercised on every tag push
  + by local `goreleaser check` / `goreleaser release --snapshot --skip=publish,sign`. Two
  intentional deprecation warnings (`dockers:`, `brews:`) — both have inline rationale
  comments AND tracking entries in roadmap "Publishing pipeline polish".
- **`.github/workflows/release.yml`** — pin discipline: `sigstore/cosign-installer` is on
  `@v3` (NOT a moving major-alias issue; v4 has a real behavior break — see roadmap). Other
  actions track major-alias tags. New action additions should prefer major-alias tags where
  the maintainer ships one.
- **`docs/PUBLISHING.md`** — the "Gotchas" section is the institutional memory of this
  rollout. Add to it when you hit a new failure mode shipping a sibling repo through this
  pipeline.
