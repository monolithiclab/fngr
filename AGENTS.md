# AGENTS.md

fngr is a Go command-line journal: Kong for parsing (`cmd/fngr`), pure-Go SQLite (`modernc.org/sqlite`, no CGo) for
storage, with event trees, `key=value` metadata and FTS5 search (`internal/*`). It is released through GoReleaser to
Homebrew, ghcr.io and signed tarballs. See [README.md](README.md) for what it does and how to run it, `docs/` (table
below) for the reasoning, and `make help` for every command.

## Hard rules

- **Never run fngr against a real journal.** Without `--db`, fngr resolves `.fngr.db` in the current directory, then
  `~/.fngr.db`, which is the owner's journal; `make run` lists it too. Use `--db "$TMPDIR/x.db"` (or `FNGR_DB`) for
  any manual check. Two agents in the 2026-07 audit wrote into `~/.fngr.db` this way.
- **Nothing in the release pipeline floats.** Actions are pinned by SHA, images by digest, GoReleaser and the lint
  tools by exact version, so a change that reintroduces a mutable tag is a regression, not a tidy-up. Pins move only
  through `docs/publishing.md` "Refreshing the pins", in their own commit. `make lint-pins` sees `uses:` lines and
  Dockerfile `FROM`s; the rest (`tools/go.mod`, GoReleaser's `version:`, the two image digests passed as `with:` in
  `release.yml`) are guarded only by review.
- **SQLite pragmas stay in the DSN** (`internal/db/db.go`, `file:<path>?_pragma=...`). Don't move them back to
  `db.Exec` after open: `*sql.DB` is a pool, an `Exec` configures one connection, and the others lose
  `foreign_keys` and `busy_timeout`. That silently lost events under concurrent writes.
- **Never edit a published migration** (`internal/db/migrations/<N>.sql`, or its `goMigrations[N]` step). A schema
  change is a new file one past the highest number.
- **The exit status is a closed set**: `exitOK` (0), `exitError` (1), `exitUsage` (2). `run` returns one of them, and
  nothing calls `os.Exit` but `main`, nor `kong.Parse` or `FatalIfErrorf` at all. Both skip the deferred `Close`, and
  `FatalIfErrorf` leaked a child's exit code (an `$EDITOR` exiting 3) as fngr's own.
- **`common.mk`, `go.mk` and `.golangci.yml` are byte-identical copies** of the lab skills' canonical files. Change
  them there and re-sync (`lab-standards`: `make sync REPO=...`), never in place.

## Constraints that look like bugs

- **stdin is read only when nothing else can supply a body.** `resolveBody` is args > `-e` > TTY > stdin. An open,
  idle pipe delivers neither a byte nor EOF, so any up-front peek hangs `fngr add "note"` under CI and supervisors.
  `echo x | fngr add y` ignoring the pipe is the price.
- **A confirmation prompt with no answer is an error, not its default.** `confirm` returns `errNoAnswer` on EOF;
  `meta rename` defaults to yes, and taking that default unattended rewrote metadata across every event. Prompts
  still block on a live but silent pipe, by design (audit H6, won't fix).
- **stdout is buffered** (16 KiB, `cmd/fngr/output.go`). Anything that hands the terminal to a child (`confirm`,
  `editBody`, the pager) flushes first, and a failed flush is the command's error.
- **The editor launch uses `signal.Notify` + `signal.Stop`, never `signal.Ignore`**: `Reset` doesn't undo `Ignore`,
  and `exec` passes `SIG_IGN` on to the editor. The pager deliberately gets no such bracket.
- **`$FNGR_AUTHOR` has exactly one reader, `defaultAuthor`.** `add --author` carries no `env:` tag on purpose: Kong
  applies `env:` at parse time but interpolates `${AUTHOR_DEFAULT}` at construction, and the two disagreed.
- **`GetSubtree` / `CountSubtree` use `UNION`, not `UNION ALL`.** On a cyclic chain `UNION ALL` never returns.
- **Byte loops, not rune loops, in `internal/render/sanitize.go`**: ranging over a string turns a raw 0x9b (8-bit
  CSI) into `RuneError` and passes it through.
- **`metaNamePattern` is Unicode-class based, never `\w`** (Go's `\w` is ASCII-only and truncated `@josé`).
- **`parse.ValidateMeta` runs where a tuple is minted, never in `KeyValue` / `MetaArg`.** Those also name existing
  rows for `untag`, `meta delete` and `meta rename`, which must keep reaching rows an older build wrote.
- **A migration's helpers are frozen copies** (`legacyFTSContent`, `legacyMetaNameRe`): a migration must keep writing
  what it wrote, even after the live code moved on.
- **The tree renderer appends to one `[]byte` prefix.** Concatenating strings per node was O(depth²) live memory
  (7 GB on a 50k chain).

## Traps `make ci` won't explain

- **Go is pinned by go.mod's `toolchain` line**, which `go.mk` exports as `GOTOOLCHAIN`, so `make` and CI run the same
  patch. A plain `go test` uses your local Go; `GOTOOLCHAIN=local make test` is how to try a newer one on purpose.
  Encoding/json error text changed between 1.26 and 1.27, so assert the facts, not the std-lib wording.
- **Tests use per-test temp-file databases**, never bare `:memory:`: each pooled connection would see its own empty
  database, which breaks streaming queries.
- **A CLI test builds its parser with `newTestParser`** (`cmd/fngr/testhelpers_test.go`, `kongOptions` plus redirected
  writers), never a hand-rolled `kong.New`, which drifts from the real parser.
- **Signal tests re-exec the test binary** (`TestIgnoreTerminalSignals`): in-process, "the restore works" can only be
  observed as the test binary dying. A child signalled in a test is the program itself, not `sh -c`: a shell's own
  SIGINT handling made one flake on Ubuntu.
- **`internal/timefmt` tests that swap `time.Local` can't be parallel**, and need a real zone (`time.FixedZone` has no
  DST); `_ "time/tzdata"` embeds the zone database.
- **Coverage**: `make test` prints the total; after a change check it per function (`go tool cover -func=cover.out`).
  A function at 0% is a bug to fix, not a number to average away.

## Where things go

- **A new command**: one file per top-level command in `cmd/fngr/` with a `Run(eventStore, ioStreams) error` method.
  Its store dependency is read from that signature (`ctx.BindToProvider`); only a command that may *create* the
  database implements `dbCreator`.
- **A schema change**: `internal/db/migrations/<N+1>.sql`, picked up by `loadMigrations` from the embedded FS. Add a
  `goMigrations[N]` step (its own `migrate<N>.go`) only when SQL cannot express it.
- **A database mutation**: a public function in `internal/event` that validates without a write lock, then runs a
  private `fooInTx` through `inTx` / `inTxVoid` (`tx.go`, the package's only `BeginTx`). Metadata reads and writes
  live in `meta.go`; other standalone reads in `query.go`.
- **A new accepted time form**: `internal/timefmt`, and add it to `AbsoluteForms` / `RelativeForms`, which feed the
  error hint and the `--time` help.
- **A new output format or alias**: `internal/render`, through `Canonical` and the derived `ListFormats` /
  `EventFormats` / `AddFormats` (Kong's `enum:` and the help text both read them).
- **User-facing behaviour**: document it in `docs/usage.md` in the same commit.

## Documentation

| Document | Owns |
| --- | --- |
| [README.md](README.md) | What fngr is, status, install and verification, the docs index |
| [docs/usage.md](docs/usage.md) | The user manual: commands, filter grammar, JSON import, exit codes, troubleshooting |
| [docs/architecture.md](docs/architecture.md) | File by file: what each owns and why it is shaped that way |
| [docs/publishing.md](docs/publishing.md) | The release pipeline, refreshing the pins, release gotchas |
| [docs/decisions.md](docs/decisions.md) | Decision log (D-numbered, append-only) |
| `docs/superpowers/specs/`, `plans/` | Dated design specs and their plans, each ending in execution notes |
| `docs/reviews/` | Review rounds; their open findings live in ROADMAP.md |
| [ROADMAP.md](ROADMAP.md) | Ideas, parked follow-ups, open review findings |

## Workflow

1. Minimal change, with tests, plus the owning doc in the same commit (`docs/usage.md` for behaviour,
   `docs/architecture.md` for structure or the reason behind it).
2. `make ci` green.
3. Commit `type(scope): summary` with the session's trailers; push only when asked.
