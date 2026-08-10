# fngr roadmap

Tool is pre-public; backward compatibility is **not** a constraint. Each
sub-project below ships through its own brainstorm → spec → plan → implement
cycle. Specs land under `docs/superpowers/specs/`, plans under
`docs/superpowers/plans/`.

## Done

- **`list` UX overhaul** — `fngr` ≡ `fngr list`, descending default, human
  time formats (`Dec 09 9.32pm`), streaming renderers, auto-pagination on TTY.
- **`event` namespace** — `fngr event N` shows event N; verbs
  `text` / `time` / `date` / `attach` / `detach` / `tag` / `untag` mutate.
  Old `fngr edit` and `fngr show` removed.
- **`meta` UX** — `fngr meta` ≡ `fngr meta list`; `-S` filter accepts bare
  key / `key=value` / `@person` / `#tag`; `meta rename` (was `meta update`)
  and `meta delete` mutate.
- **`add` body-input modes** — `fngr add foo bar` joins multi-arg into a
  single body; `cmd | fngr add` reads stdin; bare `fngr add` in a TTY (or
  with `-e`) launches `$VISUAL`/`$EDITOR`; empty editor save cancels
  cleanly. Conflicts (args+stdin, --edit+stdin) error loudly.
- **`add --format=json` import + meta JSON shape** — `fngr add --format=json`
  accepts a single event object or an array on stdin or args; per-record
  defaults flow JSON value > CLI flag > built-in; batches are atomic. JSON
  meta shape across both input and `fngr list --format=json` output is now
  `[[key, value], ...]` sorted by `(key, value)` — replaces the prior
  `{key: [values]}` map.
- **Markdown output** (`--format=md`) — `fngr list` and `fngr event N`
  emit a Markdown digest grouped by local date: one `## YYYY-MM-DD`
  header per date followed by `- <time> — <body>` bullets. Multi-line
  bodies and meta render as 2-space-indented continuation lines.
- **GitHub Actions CI + release pipeline** — every push to `main` and
  every PR validates against `make lint test` on a Linux + macOS
  matrix; every `v*.*.*` tag triggers a GoReleaser-driven multi-channel
  release (GitHub Release with cross-compiled binaries + per-archive SPDX
  SBOMs + cosign-signed
  SHA256SUMS, multi-arch container image on `ghcr.io/monolithiclab/fngr`,
  Homebrew formula on `monolithiclab/homebrew-tap`). Pre-release tags
  (`v*.*.*-rc1` etc.) skip the `:latest` Docker tag and the brew
  formula bump. CI also runs `govulncheck`. Everything either workflow
  fetches is pinned — actions to commit SHAs, the images those actions
  pull and the distroless `nonroot` base to digests, GoReleaser and the
  linters to exact versions — with `make lint-pins` guarding the two
  shapes a grep can see and the refresh procedure in
  `docs/PUBLISHING.md`.
- **CLI surface alignment** — `fngr help [<cmd>...]` is a verb-form alias
  for `--help` (multi-arg paths supported: `fngr help event show`); every
  help screen uses Kong's `HelpOptions{Compact: true}` layout (one line
  per command in the command list, with full per-command details on the
  `--help` of each command).
- **`add` time-input UX** — two faster ways to timestamp a new event.
  (1) Inline title prefix: `fngr add "9:30: had coffee"` parses a leading
  time/date token delimited by `": "` (colon+space, so `9:30`-style times
  survive), strips it from the title, and uses it as the timestamp; `--time`
  overrides and leaves the title verbatim; text mode only (JSON keeps its
  explicit `created_at`). (2) Relative timestamps accepted everywhere
  `timefmt` parses (`--time`, the title prefix, `event time` / `event date`):
  `now`, `today`, `yesterday`, `N {minute|hour|day|week|month}s ago`, and
  `<day> at <time>` (`a`/`an` count as 1). A bare relative day carries the
  current time of day but is treated as date-only so splicing still works.
- **Title + body data-model split** — `events.text` replaced by separate
  `title` + `body` columns. Split rule on input is the literal `". "`
  (dot+space): everything before is the title, after is the body, both
  trimmed; no `". "` means title-only with empty body. JSON wire shape
  carries `title` + `body` directly. Three event verbs: `event text`
  (re-splits), `event title`, `event body`. Lists and tree show titles
  only; `fngr event N` and `--format=md` show body too. Pure-SQL
  migration 3 splits via `INSTR`/`SUBSTR` and rebuilds FTS.

## Hardening (dogfooding-driven)

Fixes for real friction surfaced by using the tool, not new features.

- **`add` stdin detection in non-interactive contexts** — `resolveBody`
  used `!IsTTY` as a proxy for "body was piped in", so `fngr add "note"`
  run from a script, Makefile, cron job, or CI step (stdin = empty
  `/dev/null`) wrongly failed with `ambiguous: body via both args and
  stdin`. "Piped" now means non-TTY **with** data, via a `peekHasData`
  `bufio.Reader` peek; args win when stdin is empty, while a genuine
  `echo x | fngr add "y"` still errors. Spec amended in
  `docs/superpowers/specs/2026-04-20-add-body-input-modes-design.md`.
  **Superseded in v0.0.3** (review issue H4): the peek itself blocks
  forever on a pipe that is open but idle, which hung `fngr add "note"`
  under CI runners, process supervisors and `ssh host fngr add x`.
  `peekHasData` is gone; `resolveBody` is now precedence — args > `-e` >
  TTY > stdin — and reads stdin only when nothing else can supply a
  body. `echo x | fngr add "y"` no longer errors: the args win and the
  pipe goes unread, because noticing it costs the blocking read.
- **`-e` requires a terminal** — `fngr add -e` launched the editor even
  with no terminal to launch it in, handing `$EDITOR` a non-terminal
  stdin. A non-interactive `$EDITOR` saved nothing, so the run printed
  `cancelled (empty body)` at exit 0 and discarded the piped body. It now
  errors with `--edit needs a terminal`. It is the pre-H4 conflict pair
  reinstated in one broader form, keyed on *capability* (`IsTTY`, already
  known) rather than *content* (a read that may never return) — so it
  also catches `fngr add -e </dev/null`, which the old checks missed.
  Review issue H7.
- **Prompts refuse to assume a default when nobody is there** — `confirm`
  read `("", io.EOF)` from a closed or empty stdin and treated it as
  "pressed enter", taking the prompt's default. `meta rename` defaults to
  yes, so an unattended run that forgot `-f` rewrote metadata across every
  event and reported success. EOF with nothing typed now returns
  `errNoAnswer`; `-f` is required in a non-interactive run (review issue
  H5).
- **`event_meta.source` — a body edit only retracts what the body put
  there** — editing an event deleted every body tag of the old text and
  re-inserted those of the new, so a `people=bob` added with `event tag`
  or `--meta` vanished the first time an edit dropped an inline `@bob`.
  The review's own suggested fix — delete only `BodyTags(old) \
  BodyTags(new)` — was implemented first and proved a behavioural no-op:
  the tuples the delta spares are exactly the ones the insert re-adds, so
  final state is identical. Provenance has to be *recorded*, not derived
  after the fact. Migration 5 adds `event_meta.source` (`'body'` |
  `'explicit'`, defaulting to explicit) with a Go step that back-fills by
  demoting the rows each event's own text still yields; `Update`'s sync
  deletes `source = 'body'` only, `event tag` promotes a body row to
  explicit, and `meta rename` stamps the renamed row explicit. Review
  issue M1.
- **One author per event** — `--author x -m author=y` appended both, and
  every render picked whichever sorted first; `event tag`, `event untag`,
  `meta rename` and `meta delete` could then add, remove or rewrite the
  author of an existing event. `MergeMeta` now has an explicit author
  *replace* the default and rejects two differing explicit authors; the
  four mutation verbs refuse `author` at both ends via
  `protectedMetaKeys`. No migration repairs pre-existing duplicates on
  purpose — nothing on disk records which row was the auto-injected one.
  Review issue M3.
- **A corrupt parent chain terminates instead of hanging** — fngr cannot
  write a parent cycle, but it can be handed one: `db.ResolvePath` adopts
  `.fngr.db` from the current directory, so `cd` into an extracted
  tarball or a cloned repo and every invocation reads that database.
  Given `1 → 2 → 1`, bare `fngr` printed nothing at exit 0 (a total data
  blackout reported as success), `fngr event 1 -t` spun forever with no
  output, and `fngr event attach 3 1` did the same inside an open
  transaction. All three terminate now, behind an `ErrCorruptTree`
  sentinel kept distinct from `ErrCycle` — refusing a change and
  reporting an already-broken file are different messages. `GetSubtree`
  uses `UNION` rather than a depth cap, so there is no bound to get
  wrong and no legitimate tree is truncated however deep it is; the
  error tells the user to `fngr event detach <id>`, which walks nothing
  and so repairs the file the traversal could not read. Review issue M2.
- **`--from`/`--to` accept everything `--time` does** — the range flags
  had their own stricter grammar, rejecting relative forms *and* the
  RFC 3339 stamps `--format=json` and `--format=csv` emit, so a
  `created_at` copied out of fngr's own output could not be pasted back
  in. Both now go through `timefmt.ParsePartial`, and the second grammar
  (`ParseDate`) is gone with its last caller. `--to` is turned into the
  first instant past what was named — next second for a clock, next
  midnight for a bare date — so it reads as inclusive at either
  granularity, and an empty range (`--from >= --to`) warns rather than
  returning nothing at exit 0. Review issue M12.
- **A skipped wall clock is reported, not swallowed** — a DST
  spring-forward removes an hour, and `time.Date` and
  `time.ParseInLocation` both resolve a clock inside the gap to the hour
  before it with no error. `fngr event time N 2:30` on that day stored
  01:30 and printed `Updated event N`. Every wall clock the package
  builds now goes through one gate, so every door that stores one a user
  typed reports it — `event time`, `event date`, `add --time`, `add`'s
  title prefix, and the `--format=json` import — and a mismatch warns on
  stderr naming both times. Still stored, never refused: the shifted
  instant is a real one and almost certainly what was meant, so failing
  would leave nothing useful to type instead. Fall-back ambiguity stays
  silent; a clock that happens twice still gives the user the stamp they
  typed. Review issue M13.
- **`-n N -r` keeps the newest N** — `ORDER BY` ran before `LIMIT`, so
  the sort direction chose *which* rows survived and `fngr -n 20 -r`,
  read by anyone as "recent activity, chronological", returned the 20
  **oldest** events in the database. The limited query now sorts
  descending inside a subquery and the ascending case re-sorts the
  survivors outside it. `-r` is a display-order toggle everywhere else
  it appears; documenting the interaction instead would have documented
  a trap rather than removed one. Review issue M14.
- **`fngr meta` cannot print more than it stores** — both columns pad to
  the widest cell in the listing, so a single oversized value padded
  every other row out to its length: 200 events with short meta beside
  one 1 MB value printed 202 MB, ~200x what was on disk. Meta is
  content-controlled — body tags, `--meta`, `--format=json` — so the cap
  is a constant (60 runes, ellipsis on the cut) rather than a function
  of the data; `fngr -S key=value --format=json` still prints a clamped
  value in full. The value is cut one rune past the cap *before* it is
  escaped, which takes the 1 MB case from ~2 ms and ~1 MB of allocation
  per row down to ~260 ns and none — the output amplification was only
  half of the cost. Widths are counted in runes, which is what `%-*s`
  pads to; counting bytes over-padded any cell holding a multibyte rune
  and stepped every row below it right. Review issue M9.
- **List output is buffered** — rendering wrote one line per syscall,
  250 000 of them for a 250k list and 10-19% of the wall clock (every
  format but CSV, which buffers on its own). `Out` now goes through a
  16 KiB `bufio.Writer` on every path, not just the paged one: the
  redirect-and-pipe case is where the cost was measured and it is
  exactly the case the pager wrapper used to skip. 16 KiB rather than 64
  keeps `fngr | head -3` imperceptible. A failed flush is the command's
  error, since the tail of a listing is written there and nowhere else.
  `fngr meta` and `fngr event N -t` still write one line per syscall —
  giving them the same wrapper switches paging on for them too, so that
  is a product decision rather than a perf fix. Review issue M15.
- **Search index separates text from metadata** — `events_fts` held both
  in one column, so an event whose body spelled out `tag=ops` produced a
  token no query could tell from a real tag: a note could write itself
  into any tag view, or with `!` out of one. Migration 6 splits the index
  into `content` and `meta` (a virtual table takes no `ALTER TABLE ADD
  COLUMN`, so it is a drop, a recreate and a Go re-populate through
  `parse.FTSColumns`), and every `-S` term now carries an FTS5 column
  filter. A term is metadata when it splits on `=` into a non-empty key,
  which is what the write path accepts as a key — a narrower test left
  `-m ticket.id=PROJ-42` storable but findable by nothing. The value
  half is unchecked so `-S 'tag=*'` stays the "everything tagged" query
  it reads as. The accepted cost: a body *quoting* a `key=value` string
  is no longer reachable by searching for it. Review issue M7.
- **Exit codes are a contract, and a short one** — three classes were
  in use and none documented: `1` for an error fngr diagnosed, `2` for a
  Go panic, and `80` for a parse failure. That 80 is Kong's number,
  unguessable and meaningless outside it, so documenting it would have
  documented the leak; a refused command line exits `2`. Keeping the set
  *closed* took two more passes, and the lesson is that a contract you
  enforce by *mapping* is a contract you do not own. Mapping every
  unrecognized status to `2` by elimination — so the literal 80 need
  never be named — quietly caught statuses that were not parse failures
  at all: `FatalIfErrorf` runs every error through `kong.ExitCoder`, so
  an `$EDITOR` exiting `3` arrived as an `*exec.ExitError` and was
  reported as "the command line could not be parsed, nothing was
  attempted" for a command that ran. Sending everything but 80 to `1`
  fixed that and left a shim around an unexported constant. The
  `main.go` restructure removed the mapping instead: `run` owns the
  `kong.New`/`Parse` pair and *returns* one of three statuses, so the
  set is closed structurally, and `*kong.ParseError` — exported where
  the number is not — is what a parse failure is recognized by.
  The same failure used to print the whole 30-line command list before
  the message, putting `unknown flag --bogus` 25 lines below the fold;
  it is now the short usage, and on **stderr**, where it belongs. That
  last part was Kong's until fngr owned the call.
- **A typo is diagnosed on the path people actually type** — `fngr evnt 5`
  answered `unexpected argument evnt`, because `list` is
  `default:"withargs"` and an unrecognized word re-parses as a stray
  positional to it. `checkCommandPath` had been fixing that for
  `fngr help evnt` for a while; the gap was that `main` called the
  package-level `kong.Parse` and never saw the failing context. Owning
  the parse closed it. The interesting part is where the candidate words
  come from: re-scanning argv looks obvious and cannot work, because
  knowing which words *could* be commands means knowing which flags take
  a value, and `list`'s are spliced onto the trace at parse time rather
  than sitting on the root node. That version answered `fngr -S ops
  --bogus` with `no command "ops"` and swallowed the real complaint.
  Reading Kong's own trace instead (`kong.Path.Remainder()`) is exact —
  including the tell that separates `fngr meat` from `fngr list extra`,
  which is whether the default command consumed a token on the way in.
- **Which commands need a database is not a list** — it used to be two
  `strings.HasPrefix(ctx.Command(), …)` tests, one for "skip the open"
  and one for "may create the file", both true of any command whose name
  merely started that way: `fngr helpers` would have run with no store
  bound and `fngr addendum` would have started a second journal. The
  store is now bound lazily with `ctx.BindToProvider`, so the answer is
  read off each command's own `Run` signature — `fngr help` declares no
  `eventStore`, so no path is resolved and no file is opened, which is
  what keeps it answerable with exactly the broken `--db` it gets reached
  for. Only "may create it" is still declared, as one method on `AddCmd`.
  The same restructure made `defer database.Close()` run at all:
  `FatalIfErrorf` called `os.Exit` past it, so a WAL database never got
  its checkpoint.
- **Errors stop naming Go** — a JSON import with `"meta": {...}` answered
  `cannot unmarshal object into Go struct field jsonAddInput.meta of type
  [][2]string`, a private type name and a Go declaration shown to someone
  holding a JSON file. `wireTypeError` restates it as `field "meta": got
  object, want array (at byte 21)`; the offset is there because a batch
  runs to 10 000 records and `encoding/json` keeps no record index. The
  "unrecognized time" hint had the same shape of leak, offering `3:04PM`
  — Go's reference clock — in a list of placeholders, and now says
  `HH:MMpm`. That one token had to be fixed in three files, which was the
  tell: the accepted vocabulary is now `timefmt.AbsoluteForms` /
  `RelativeForms`, built into the hint and threaded into the `--time` and
  `event time` help through `kongVars`, the way `render.ListFormats`
  already reached `--format`.

## Proposed (not yet scheduled)

Real gaps from review 2026-07 that are new surface rather than fixes.
Listed so they are not rediscovered as bugs; each needs a use case
before it is worth the CLI surface.

- **`fngr meta --format`** — the one output surface with no
  machine-readable form, which is where you would script a tag audit or
  a bulk rename. `ListMeta` already returns structured rows, so this is
  a flag plus a `render.Meta(w, format, rows)`. Note that it also props
  up an existing rejection: "bulk operations" below rests on
  `fngr -S … --format=json | jq | xargs`, and that composition works for
  events but not for metadata.
- **`snippet()` in search results** — `-S kubernetes` on an event whose
  *body* mentions it shows only the title, with no indication of why it
  matched, so every hit in a body-heavy journal needs a follow-up
  `fngr event N`. FTS5 has `snippet()` built in, so it is a query change
  rather than new machinery. The open question is which formats get it:
  a snippet column is a listing affordance, and `json`/`csv` already
  carry the whole body.

## Publishing pipeline polish

Follow-ups from the v0.0.1 release rollout (full context in
`docs/PUBLISHING.md` "Gotchas"). Each is functional today; the
migrations are quality-of-life cleanups that can wait until the
deprecated keys are actually removed by upstream.

- **`dockers:` + `docker_manifests:` → `dockers_v2:`** — current
  config uses the deprecated GoReleaser keys (warnings every release).
  The new shape needs a multi-stage Dockerfile that uses buildx's
  `TARGETOS` / `TARGETARCH` build args to pick the right per-platform
  binary. Non-trivial Dockerfile rewrite; defer until removal of
  `dockers:` becomes urgent.
- **`brews:` → `homebrew_formulas:`** — `brews:` is deprecated in
  favor of `homebrew_casks:`, but Casks are macOS-only and require
  `brew install --cask`, which would break the cross-platform install
  path we promise (`brew install monolithiclab/tap/fngr` from Linux
  too). Wait for GoReleaser to ship a `homebrew_formulas:` key.
- **Cosign `signs:` → bundle format** — pinned to the
  `sigstore/cosign-installer` **v3** line (by SHA), which installs
  cosign v2.x; the installer's v4 line installs cosign v3.x, which
  deprecated the
  `--output-signature` / `--output-certificate` flags in favor of a
  single `.sigstore.json` bundle. Migration touches the
  `.goreleaser.yaml` `signs:` block, the README's verification
  example (current `cosign verify-blob --signature SHA256SUMS.sig
--certificate SHA256SUMS.pem` would become a single `--bundle`
  flag), and `docs/PUBLISHING.md`'s downstream-verification section.
- **Brew formula path** — GoReleaser writes `<name>.rb` at the tap
  root by default. Both layouts work for `brew install`, but
  `Formula/<name>.rb` is the conventional Homebrew tap structure.
  Add `directory: Formula` to the `brews:` block and re-tag.

## Considered (not pursued)

Feature ideas that have come up across reviews and brainstorms.
Each was deliberately deferred or rejected with the reasoning below;
re-listed here so future passes don't re-propose them without new
information. Not commitments — items move to a real section above
only on real demand.

- **Config file (`~/.fngr.config`)** — env vars + CLI flags already
  cover persistent defaults. A config layer means precedence rules,
  parsing, and a third place to look for behavior.
- **Multiple databases / workspaces** — `cd` plus `FNGR_DB` already
  implements this. No need for a separate workspace concept.
- **Auto-tag character expansion** — explore whether other shorthand
  symbols (e.g. `^location`, `+company`, `~mood`) are worth adding
  alongside the existing `@person` / `#tag` system, and which symbols
  are unambiguous enough. Open question; brainstorm separately
  before commitment.
- **Soft delete / undo** — adds schema complexity and a parallel
  "alive" view path for marginal benefit. Backups (`cp ~/.fngr.db`)
  cover recovery.
- **Stats / summary command** — anything useful is a one-liner
  against the SQLite file. Bloats the CLI surface for a workflow
  most users will run rarely.
- **Author normalization / user registry** — belongs to user data
  hygiene, not the tool. `meta rename` already exists for cleanup.
- **Shell completion** — Kong supports it natively if needed. Not
  load-bearing; revisit on user request.
- **Snapshot / backup command** — a copy of the SQLite file is the
  backup. Don't reinvent.
- **Database maintenance commands** (`vacuum`, etc.) — single SQL
  statement; not worth a CLI surface. Document in README only if a
  user actually asks.
- **Bulk operations / filtered delete** — composes from
  `fngr -S '...' --format json | jq | xargs fngr delete` for the
  rare case. Adding `--filter` to `delete` adds destructive surface
  area for marginal value. The composition covers events only; the
  metadata half needs `fngr meta --format` (see "Proposed" above).
- **`fngr add -` as explicit stdin form** — auto-detect via non-TTY
  pipe handles every real workflow; explicit form would only force
  stdin in a TTY, no use case today.
- ~~**Tokenize `$EDITOR` / `$VISUAL` for `vim -u NONE`-style values**~~ —
  **Done in v0.0.3.** Deferred here as a feature ("revisit on real
  demand"), which read the situation wrong: `pagerCommand` already
  tokenized, so the two variables answered the same kind of value two
  ways and `EDITOR="code -w"` failed where `PAGER="less -R"` worked.
  Both now go through `envCommand`.
