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

- `cmd/fngr/main.go` — Entrypoint. `main` builds the `ioStreams` and calls `run`, which owns the
  `kong.New`/`Parse` pair and *returns* a status instead of exiting on one. Both halves matter.
  Returning is what makes the deferred `Close` run — reaching `os.Exit` through Kong's
  `FatalIfErrorf` meant it never did, leaving a WAL database's `-wal`/`-shm` behind and its
  checkpoint undone. Owning the parse is what makes three further things fngr's decision rather
  than Kong's: the exit status, the stream a message about a bad command line goes to, and whether
  a mistyped command is diagnosed at all. The parser configuration is `kongOptions`, one list
  because the tests build their own parser and a difference between the two is a difference they
  cannot see; `exit` is a *parameter* rather than an appended override, since a test supplying its
  own `kong.Exit` steps over the mapping it came to check. Returning is also what lets `run` flush
  the buffered stdout it handed to Kong and to the command — the `exit` it passes down is wrapped
  as `exitFlushed` for the one path that does not return, Kong's `--help`/`--version` hooks
  calling it from inside `Parse`. See `cmd/fngr/output.go` for both.
  The status vocabulary is `exitOK` / `exitError` (`1`) / `exitUsage` (`2`) — see README
  "Exit codes" — and it is closed *structurally*, because `run` returns one of the three and
  nothing else. The version that mapped was the version that leaked: with `kong.Parse` the status
  came from `FatalIfErrorf`, which runs every error through `kong.ExitCoder` first, so an
  `$EDITOR` exiting 3 reached `AddCmd.Run` as an `*exec.ExitError` carrying `ExitCode()` and fngr
  exited 3. `exitCode` survives only as a clamp on the status Kong picks: now that fngr never
  calls `FatalIfErrorf`, the only live `kong.Exit` call sites are the `--help` and `--version`
  hooks and both pass 0, and the clamp is what keeps that an observation rather than an
  assumption. Nothing names 80 any more — `reportParseError` asks `errors.As(err,
  &parseErr)` instead, `*kong.ParseError` being exported where the status number is not.
  `reportParseError` also writes the short usage itself, on **stderr**: Kong's
  `FatalIfErrorf` writes it — plus an unconditional blank line — to *stdout*, so
  `fngr --bogus >/dev/null` showed a bare error with no usage and `fngr --bogus | cat` mixed usage
  into the data stream. The printer is still Kong's (`writeShortUsage` swaps `parser.Stdout` for
  the duration and calls `kong.DefaultShortHelpPrinter`); a private copy of those two lines would
  be a format the library owns, re-checked on every upgrade. It is the *short* printer, not
  `Context.PrintUsage`, which routes to the full one: the whole help is 30 lines of command list,
  so `unknown flag --bogus` arrived 25 lines below the fold. `kong.ShortUsageOnError()` is gone
  with the `FatalIfErrorf` it configured.
  A mistyped command is then diagnosed rather than relayed, by running the same
  `checkCommandPath` `fngr help` uses (see `help.go`) — which is what makes it cover
  `fngr <typo>` and not just `fngr help <typo>`. Its two arguments come from `unplaced`, which
  reads them off the *failed parse* (`kong.Path.Remainder()`) rather than re-scanning argv. A
  second argv scanner is the thing that does not work: the words that could name a command are
  the ones left once every flag *and its value* is removed, and only Kong knows which flags take
  a value — `list` is `default:"withargs"`, so `-S`/`-n`/`--format` sit on the list node and are
  spliced in at trace time, and a scan of the *root* node's flags read their values as bare
  words. `fngr -S ops --bogus` then answered `fngr has no command "ops"` and swallowed the
  unknown flag that was the actual complaint. `unplaced` stops scanning at the first word
  starting with `-` (a flag is nobody's mistyped command) and backs a default command out to its
  parent when it consumed nothing — `fngr meat` enters `list` without the name being typed, so
  the word belongs to *fngr*'s vocabulary, while `fngr list extra` consumed `list` on the way in
  and its stray positional stays Kong's to explain.
  The store is bound with `ctx.BindToProvider`, so *which* commands need a database is read off
  their own `Run` signatures: `HelpCmd.Run` declares no `eventStore`, nothing resolves one, and
  `fngr help` stays answerable with a `--db` that is missing or unreadable. Only whether a command
  may *create* the file is still declared, by `AddCmd.createsDB` through the `dbCreator` interface.
  Both used to be `strings.HasPrefix(ctx.Command(), …)`, true of any command whose name merely
  starts that way — `fngr addendum` would have started a second journal and `fngr helpers` would
  have skipped the open and then run a command with no store bound.
  Every diagnosed error goes out through `fail` → `parser.Errorf`, so the two `db.Open` messages
  that used to print a bare `error: …` now match the `fngr: error: …` everything else has.
  `kongVars` carries `${TIME_ABSOLUTE}` / `${TIME_RELATIVE}` alongside the `${*_FORMATS}`
  vocabularies, for the same reason: see `internal/timefmt`. `helpOptions` is a package var
  rather than a literal inside `kongOptions` because `writeShortUsage` must pass Kong the same
  options `kong.ConfigureHelp` got, and Kong keeps its copy unexported.
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
  returning nothing at exit 0. A negative `--limit` is refused there too, via
  `event.ValidateLimit` — the same two-altitude seam as `event.ValidateFilter`, checked in
  `toListOpts` before `withPager` spawns anything and enforced in `buildListQuery`, one function
  so the two wordings cannot drift. It is refused rather than clamped because clamping is the
  silent behaviour it exists to remove: `fngr -n -1` is a malformed short flag to Kong, whose
  error suggests `--limit="-1"`, and only the `LIMIT` clause reads the value and only on
  `Limit > 0` — so following that suggestion *widened* the result to the whole journal at exit
  0, where a bad `--from`/`--to` can only ever match nothing or everything as asked. With
  `--format=json` the body is parsed as a
  JSON event record (or array) by `cmd/fngr/add_json.go`; per-record defaults flow JSON value
  > CLI flag > built-in. `event` hosts a sub-command tree: `fngr event N`
  reads (shorthand for `event show N`); `text` (re-splits into title+body), `title`, `body`,
  `time`, `date`, `attach`, `detach`, `tag`, `untag` mutate. Each verb owns its own `ID` arg,
  syntax `fngr event <verb> <id> [<args>]`. The two verbs that walk the tree (`show -t`, `attach`)
  route their error through `withRepairHint`, which appends the way out of an `ErrCorruptTree` —
  `fngr event detach <id>` on any event the message names, since clearing `parent_id` walks
  nothing and so works on the very database the traversal could not survive. `attach` and `detach`
  both read the event before writing, so each can name what it displaced — the old parent, or the
  absence of one, which `detach` reports as a no-op instead of printing `Detached event N` over
  nothing done. `Get` fetches one row and walks no ancestry, so it is safe on exactly the tree
  those verbs exist to repair. `delete` names its subject the same way in the prompt and the
  result line via `deleteSubject`, which sizes the subtree with `CountSubtree` — and falls back to
  the un-counted `and all its children` on *any* error from it rather than failing, since a stored
  cycle stops that walk and `delete -r` (an FK cascade, which walks nothing) is one of the ways
  out of one.
  `meta` is a sub-command tree too: `fngr meta` lists with optional `-S` filter (bare key,
  key=value, @person, #tag), `meta rename` and `meta delete` mutate (both accept the same
  shorthand). None of the event verbs prompt; meta verbs prompt with the destructive-vs-additive
  defaults (rename `[Y/n]`, delete `[y/N]`); `-f`/`--force` skips the prompt on both, and is
  *required* in a non-interactive run — every prompt errors rather than assume its default when
  stdin has no answer to give (see `cmd/fngr/prompt.go`). An empty `meta` listing reports
  `No metadata found.` on stderr, through the same `reportNone` as `list`'s own empty-result
  note and for the same reason — it is a remark about the result, not a row of it, and
  `fngr meta -S tag | wc -l` should count entries. The `meta` listing pads the joined
  `key=value` cell to the widest one, not the key and the value separately — a padded key put
  the `=` in a column of its own (`tag   =bugfix` beside `author=nico`) whose width came from
  whichever rows the query returned, so `fngr meta -S tag` and the unfiltered listing spelled
  the same entry two ways. Each half still goes through `displayCell` first: escaped, then
  clamped to
  `maxMetaCell` (60) runes with the last spent on an ellipsis where it cut. The bound is a
  constant because meta is content-controlled — 200 short rows beside one 1 MB value printed
  202 MB, ~200x what was stored. `displayCell` cuts the *raw* value to one rune past the cap
  before escaping it, because `render.SanitizeLine` walks every byte it is handed and copies the
  lot when anything needs escaping; the extra rune is what makes that free of consequence, since
  escaping never shrinks a string. Widths are counted with `utf8.RuneCountInString`, which is
  what `fmt`'s `%-*s` pads to; `len` over-padded any cell holding a multibyte rune and stepped
  every row below it right.
- `cmd/fngr/help.go` — `fngr help [<command>...]`, which re-parses with `--help` appended so the
  output is byte-identical to `fngr <command> --help`. `checkCommandPath` vets the path first:
  `list` is `default:"withargs"`, so a misspelled verb was never a parse failure — it re-parsed as
  a stray positional *to list*, and the answer was list's whole usage block plus
  `unexpected argument bogus`. It stays quiet wherever the word might not be a command at all: a
  leaf with no sub-commands to suggest, and a node that takes an argument — `takesArgument` reads
  all three places Kong will look (the node's own positionals, its `default:"withargs"` child's,
  and an `arg:""` branch child), so `fngr help event 5` is still Kong's to explain. Reading only
  one of them is the bug: `withargs` puts `event`'s `<id>` on the `show` child, not on `event`.
  `commandChild` matches by name only — fngr declares no aliases, and Kong's alias precedence (an
  alias loses to a real command of that name anywhere among the siblings) is not worth guessing at
  from outside the framework. `checkCommandPath` is shared with the primary path: `main.go`'s
  `reportParseError` calls it on a failed parse, so `fngr <typo>` is diagnosed the same way
  `fngr help <typo>` is — the difference being where the node and the words come from (a walk of
  `c.Args` here, `unplaced` reading Kong's own trace there).
- `cmd/fngr/plural.go` — `plural(n, noun)`, the count+noun formatter behind `Renamed 1
  occurrence` / `Imported 3 events`. Regular `-s` only, deliberately: every noun fngr counts takes
  one, and a call site with an irregular noun should say something else — which is why `delete -r`
  reports a subtree in events rather than in children. Also `reportNone(w, noun)`, the other half
  of the family: `No events found.` / `No metadata found.`, on stderr, in one place so the choice
  of stream is made once — that choice is the whole reason the note can be printed for every
  format including the two (`[]`, a lone CSV header) that already say it themselves.
- `cmd/fngr/store.go` — Defines the narrow `eventStore` interface that commands depend on plus the
  injectable `ioStreams` (`In io.Reader`, `Out io.Writer`, `Err io.Writer`, `IsTTY bool`). `Out`
  is a `*bufferedOut` in every production run and a plain writer under test — see
  `cmd/fngr/output.go` for what that buys and what has to flush it. It stays an `io.Writer`
  rather than being narrowed to that type: the two places that care ask by assertion
  (`flushOut`, `withPager`) and both fall back rather than fail, which is what keeps a test
  handing a command a `bytes.Buffer` from being a special case anywhere else.
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
  delete '#wip'` waits (REVIEW H6, won't fix; `-f` is the unattended form). Blocking is also why
  `confirm` flushes `out` between writing the prompt and reading the answer: stdout is buffered
  (see `cmd/fngr/output.go`) and 16 KiB of nothing else is coming, so without it the user is
  answering a question they were never shown. In `confirm` itself rather than at the call sites,
  so no new prompt can forget.
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
  Both editor branches go through `editBody`, which flushes stdout before handing the terminal
  over: fngr's stdout is buffered (see `cmd/fngr/output.go`), and anything still held back when a
  child takes the screen surfaces after the editor exits, or not at all if the editor clears the
  screen on the way out. It is `confirm`'s rule in the other place a child takes over, in one
  function so neither branch can forget it. Nothing writes to `Out` before `resolveBody` today,
  which is a fact about `AddCmd.Run`'s statement order rather than a property of the stream.
  `launchEditor` is a `var` for test stubbing; `realLaunchEditor` execs `$VISUAL`/`$EDITOR` on a
  temp file and inherits `os.Stdin`, so the veto must stay upstream of it; `errCancel` signals
  empty-save (handled as exit-0 by `AddCmd.Run`). The argv comes from `envCommand` (see
  `cmd/fngr/envcmd.go`), which `pagerCommand` shares: the two used to answer the same kind of
  value two different ways, so `PAGER="less -R"` worked while `EDITOR="code -w"` failed with
  `fork/exec …/code -w: no such file or directory`, the whole value taken as one filename. One
  function rather than two conventions is also why the fix is not `strings.Fields` copied into a
  second place. `ignoreTerminalSignals` brackets the
  temp file — registered *above* `os.CreateTemp`, since defers are LIFO and a bracket lifted
  before the `os.Remove` protects nothing — suspending fngr's response to SIGINT/SIGQUIT, the
  same thing git does around its own editor launch. Ctrl-C reaches the whole foreground process
  group, and vim handles SIGINT itself and carries on, so fngr's default disposition killed the
  process that was going to read the file while the editor kept the terminal. Removing the file
  from a signal handler is the wrong shape for exactly that reason: it would yank the buffer out
  from under a live editor. The primitive is `signal.Notify` onto a buffered channel nobody
  reads, plus `signal.Stop` — *not* `signal.Ignore`, which fails twice over: `signal.Reset` only
  lifts a `Notify`, so the restore is a silent no-op and fngr never answers Ctrl-C again, and
  `exec` preserves an ignored disposition where it resets a caught one, so the editor inherits
  `SIG_IGN` and cannot itself be interrupted (least of all `EDITOR="code -w"`, a wrapper script
  with no SIGINT handling of its own). Both are pinned by tests that fail against the
  `Ignore`/`Reset` form; the first needs a re-exec of the test binary, because in-process
  "the restore works" is only observable as the test binary dying. No drain goroutine is
  needed — `Notify` never blocks sending, so one slot absorbs the first signal and the rest
  are dropped. The pager execs a child too and deliberately does *not* get this: it strands
  nothing a dead fngr would have to clean up, and Ctrl-C is how a long listing is meant to be
  abandoned. SIGKILL still leaks, mode 0600 in the per-user `$TMPDIR`. `readStdin`
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
  Both `Decode` calls route their error through `wireTypeError`, which restates a
  `json.UnmarshalTypeError` in the wire's own vocabulary — `field "meta": got object, want array
  (at byte 21)` rather than `cannot unmarshal object into Go struct field jsonAddInput.meta of
  type [][2]string`, a private Go type name and a Go declaration shown to someone holding a JSON
  file. Every other decode error passes through with the decoder's own wording, which is already
  about the input (`unknown field "ttile"`, `unexpected EOF`). A top-level mismatch (`42`, or an array of
  scalars) carries no `Field` at all, and is the case that leaked worst — `cannot unmarshal number
  into Go value of type main.jsonAddInput` names the private type outright — so the field name is
  a *prefix* (`input:` without one), never a precondition: an early return when it was missing
  left exactly the message the function exists to replace. `wireTypeName` is kind-level only:
  the exact shape of `meta` is the README's job, and "array of 2-element array of string"
  describes it no better than "array". It has no pointer case: `encoding/json` indirects before it
  reports, so a `*int64` field arrives as `int64` and the arm would be dead code carrying a
  recursion. The byte offset rides along because it is the only locator `encoding/json` keeps — a
  batch runs to 10 000 records and the field name alone cannot say which.
- `cmd/fngr/pager.go` — `withPager(io, disabled) closer` splices `$PAGER` (fallback `less -FRX`,
  tokenized by the `envCommand` the editor launcher shares) underneath the buffer `ioStreams`
  already carries, when stdout is a terminal and `--no-pager` is absent. Used by `list`. It
  *redirects* that buffer rather than installing one of its own — buffering is a stream property
  now (see `cmd/fngr/output.go`), so all the pager changes is where the flushed bytes land, and
  the alternative is a second buffer stacked on the first. An `Out` that is not a `*bufferedOut`
  therefore gets no pager: in production it always is, and a test writing into a `bytes.Buffer`
  has no terminal to page to. `withPager` binds `s.Err` and `buf.dest` into the closer rather
  than closing over the whole `ioStreams`, which would keep the stdin reader reachable for the
  life of the listing. The closer *returns* its flush error, and `ListCmd.Run` promotes it to the
  command's error through a named return (without masking an error already on its way out): the
  listing has to reach the pager before its pipe closes, so it flushes here even though `run`
  flushes again on the way out, and those bytes are written nowhere else. The pager's own exit
  status stays a stderr warning — a pager quit early is a decision, not a lost write.
  `newPagerCmd` takes that same `dest` and gives the child *its* stdout, rather than reaching for
  `os.Stdout`: they are the same file in every production run, and taking the parameter is what
  keeps that an observation rather than two independent guesses.
  `isTerminalWriter` asserts `*os.File` before consulting `isTerminal`, since only a file can be
  a terminal and that makes every pipe and every test buffer answer no without a syscall; the
  question itself is `isTerminalFile`, shared with `main`'s `IsTTY` for stdin, so the stub seam
  and the fd-overflow annotation exist once. `isTerminal` itself is a `var` for test stubbing
  (like `launchEditor`): it is the last gate on the pager branch and no test process has a
  terminal on stdout, so stubbing it is what lets the branch be tested at all.
- `cmd/fngr/output.go` — `newBufferedOut(os.Stdout)` is what `main` puts in `ioStreams.Out`, so
  every byte fngr writes to stdout goes through one 16 KiB `bufio.Writer`. Buffering belongs to
  the stream and is installed once where the process's streams are built, rather than by
  whichever command remembers to ask: it started inside `withPager`, and `list` was consequently
  the only command that had it — rendering writes one line per syscall, 250 000 of them for a
  250k list, and `fngr event N -t --format=json` paid 2N+1 for +19% wall clock over the same
  rows buffered. 16 KiB rather than the usual 64 because the buffer is also the latency floor for
  `fngr | head -3`. `bufferedOut` keeps its raw `dest` alongside the writer so `redirect(w)` can
  swap the destination underneath a command already holding the stream; both halves flush before
  switching, because `bufio.Writer.Reset` discards silently and the whole point of a buffer is
  that its contents exist nowhere else, and the restore reports the first error either flush saw
  via `cmp.Or`. The `*bufio.Writer` is a *field*, not an embed, and `point(w)` is the only thing
  that moves the sink: `Reset` promoted onto `bufferedOut` is the one method that moves it
  without telling `dest`, and a stale `dest` points the pager at the wrong file.
  `flushOut(w)` is a type assertion on `interface{ Flush() error }` rather than a
  field on `ioStreams`, because commands hold an `io.Writer` and tests hand them a
  `bytes.Buffer` — so, like `withPager`'s own assertion, it falls back rather than failing when
  the stream is not the one `main` installs. Five places flush, each for its own reason: `run`'s
  catch-all `defer`, which covers every way out and is why the others are about *when* rather
  than whether; `run` again explicitly before `fail`, since stderr is unbuffered and diagnosing
  first prints the verdict above the output it describes; `run`'s `exitFlushed` wrapper around
  Kong's exit (the `--help`/`--version` hooks call it from inside `Parse` and never return, so
  `fngr --help` would otherwise print nothing at all — and since `run` never regains control
  there, a failed flush can only be reported by bumping the status it hands Kong to `exitError`);
  `withPager`'s closer (see `cmd/fngr/pager.go`); and the two places a child takes the screen,
  `confirm` (see `cmd/fngr/prompt.go`) and `editBody` (see `cmd/fngr/body.go`). A failed flush is
  the command's error wherever it is seen — exiting 0 over output the user never received is the
  failure mode the buffer introduced — but an error already on its way out says more and wins.
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
  `key=value`; used by `event tag` / `event untag`), `ValidateMeta` — the one rule for a tuple
  fngr can store *and* find again, stated once in its doc comment and pointed at from every
  other site. A key must be exactly one `-S` term: not empty, no rune that ends one, and not
  opening with the one that negates. `-m 'a b=c d'` stored a row `-S 'a b=c d'` reads as three
  terms, `-m 'a&b=c'` one it reads as two, and `-m '!k=v'` one `-S '!k=v'` answers with every
  event *except* the tagged one — the silent complement of what was typed. Which runes those
  are is `IsFilterDelim`'s answer, not a hand-listed set: it is the same predicate
  `internal/event/filter.go`'s tokenizer uses, and lives in `parse` because `internal/event`
  imports `parse` rather than the reverse. A second copy of the list had already drifted,
  banning only whitespace while `&` and `|` split a term just as hard. The rule is one *term*,
  not `MetaNameRe` — `-m ticket.id=PROJ-42` is a key that regex refuses and `-S` finds
  perfectly, and testing anything narrower routes a storable key to a filter column that cannot
  answer for it (the same reasoning `internal/event/filter.go::ftsTerm` records). A value may
  not be empty (`-m 'k='` rendered as `k=  (1)`, an entry no verb but `event untag 'k='` could
  name) but *may* contain whitespace and any operator, because `author=Ada Lovelace` is what
  people mean to write and `-S author=Ada` still reaches the row — a term ends at the space, but
  `key=firstword` is enough, which is not true of a *key* split down the middle.

  Applied only where a tuple is **minted** — `FlagMeta`, each `meta` pair in
  `cmd/fngr/add_json.go`, and the writer (`internal/event.requireStorableMeta`,
  `requireRenamableMeta`'s new value). `KeyValue` and `MetaArg` are structural splitters and
  stay permissive, because the same functions serve the verbs that **name** an existing row:
  `event untag 'k='`, `meta delete 'k='`, `meta rename 'k=' 'k=v'`. Every released build could
  write such a row, so gating those too would make exactly the rows the rule exists to prevent
  into rows nothing can remove. Don't route the rule through `KeyValue`. `parseMetaFilter`'s
  bare-key form is on the naming side too even though it reads like a filter: it reaches
  `ListMeta`'s plain `WHERE key = ?`, where the `-S` *expression* tokenizer is nowhere on the
  path — so nothing a key can contain makes it unmatchable there, and a query path is what finds
  the rows the mint rule refuses to create. It used to test `MetaNameRe`, refusing
  `fngr meta -S ticket.id` from the one command that lists metadata. The CLI-side calls are
  pre-flights, for the message — `--meta` can name the flag, the JSON import the record and pair
  index; the guarantee is the writer's, for the reason `requireOneAuthor` is (an exported
  `parse.Meta` means a directly-built `AddInput` skips every parser). Also `FTSColumns` for the two `events_fts` column
  values (both stated in one function so a caller cannot write one and forget the other; the
  pre-migration-6 single-column join is *not* here — it is frozen as `db.legacyFTSContent`
  beside the migration that still writes it),
  `SplitTitleBody` for the `". "` title/body split, and `EventText` for the title+body join every
  body-tag derivation runs over — `addInTx`, `Update`'s sync and migration 5's back-fill each
  decide provenance from it, so a join that differs by a space would have them disagree about a
  tag at the boundary.
  Tag and meta-name regexes share the private `metaNamePattern` constant; the anchored form is
  exported as `MetaNameRe`, which matches a `@person` / `#tag` name in isolation and is
  deliberately *not* the `key=value` key rule — a sigil name has to be a clean token because the
  body patterns must find it unaided in running prose, a key spelled out in full does not. That pattern is
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
  `AbsoluteForms` / `RelativeForms` are the one place the accepted vocabulary is written down:
  the "unrecognized time" hint is built from them, and `kongVars` threads them into the `--time`
  and `event time` help as `${TIME_ABSOLUTE}` / `${TIME_RELATIVE}`, the same shape
  `render.ListFormats` reaches `--format` by. Every shape is a *placeholder*, `HH:MMpm` included:
  the 12-hour one used to be spelled `3:04PM` — Go's reference clock, a literal sitting in a list
  of patterns, and a Go layout shown to someone typing a time — and it was spelled that way in
  all three sites, so the one-token fix took three hand edits. Sites that gesture at the grammar
  without enumerating it (`event date`, list's `--from`/`--to`) are deliberately *not* built from
  these: there is nothing there to drift, and interpolating the full list would put 100 characters
  of placeholder in a flag summary.
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
- `internal/event/*` — Split at the read/write seam; the per-file bullets below say which file
  holds what, and so does the package doc on `event.go`. What only this bullet can say is *why*
  the two files that depart from the seam depart from it. It was `event.go` at 1 396 lines beside
  a 188-line `meta.go`; a verb's tests live in the matching `*_test.go`, with `testDB` and the
  other fixtures every one of them reaches for in `testhelpers_test.go`.
  `meta.go` is named for a **domain**, and it wins over the seam inside that domain: it holds the
  rules, the storage verbs *and* metadata's own reads (`ListMeta`, `CountMeta`), reading rules
  first and verbs after. The review suggested a new `meta.go` for the meta CRUD, but the name was
  already the domain file's, and two files a reader has to tell apart is worse than one that
  answers every metadata question.
  `tx.go` is named for a **role**, which is the thing `internal.go` (what it began as) was not: a
  visibility every declaration in a package already under `internal/` has is a criterion nothing
  can fail, so nothing would ever be moved out of it. Its rule is the transaction bracket plus the
  tx-taking helpers that are shared across files and belong to no one domain — a criterion that
  can refuse, and does at both ends: `formatTimestamp` opens nothing and went to `event.go`
  beside the `ErrTimeRange` it is the sole producer of, while `meta.go`'s `readMetaTx` and
  `execBodyMetaTuples` take a `*sql.Tx` and stay where they are, being about metadata.
- `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS),
  `AddMany` (batched same shape, atomic), `AddInput` value type. Both `Add` and `AddMany`
  delegate to a private `addInTx` that runs the per-record INSERT loop using a caller-owned
  `*sql.Tx`. That pairing is the package-wide shape: `inTx` (and `inTxVoid`, its no-result
  wrapper, both in `tx.go`) is the *only* `BeginTx` here, and every mutation is a public function
  that validates what it can without a write lock and then hands a private `fooInTx` to it. Two of the seven
  hand-written brackets it replaced returned `tx.Commit()` bare, so a write failing at the last
  step said `database is locked` with nothing to say which write it was; wording the begin and
  commit errors in one place is what makes that unforgettable rather than remembered. Keep the
  body in a named `fooInTx` rather than a closure — that is what lets a mutation be driven from
  a caller-owned transaction, and it is why the conversion re-indented nothing. `AddInput` names its parent either by database id (`ParentID`) or by position in
  the same batch (`ParentIndex`, mutually exclusive with the former) — the latter is what lets
  a JSON import re-create a tree whose ids don't exist in the target yet. Batch parents are
  wired up by an UPDATE pass *after* the insert loop, since a child may be inserted before its
  parent (`fngr --format=json` emits newest-first); `requireAcyclicParentIndexes` rejects
  cycles up front, because SQLite's FK check only proves the parent
  row exists and would happily commit a cycle unreachable from any root.
  Every other per-record check — the index range, `ParentID`/`ParentIndex` exclusivity, a
  non-empty title, `requireOneAuthor`, `requireStorableMeta` — runs in `validateAddInputs`, one
  pre-pass over the batch *before* the first INSERT, and each message names the record it came
  from. Both halves of that are the point. The checks used to sit inside the insert loop, so a
  10 000-record import failing on the last one wrote and rolled back 9 999 events to say
  `title cannot be empty` with nothing to say *which*. The record number comes from
  `recordPrefix`, which stays silent on a one-record batch: `Add` is the common caller and
  `record 0:` in front of a message about the only record there was is noise. The batch index
  lines up with `cmd/fngr/add_json.go`'s own `--format=json: record N:` because `addInputs` is
  built one-to-one and in order — those CLI checks run first and short-circuit, so the two
  prefixes never stack.
  `addInTx` stamps each meta row's `source`, re-deriving
  `parse.BodyTags(parse.EventText(...))` because `AddInput.Meta` arrives already merged; a tuple
  the text yields is recorded as body-derived even when `--meta` named it too — the same
  tie-break migration 5 makes, which also keeps a `--format=json` round trip from freezing every
  body tag as explicit.
  The `ErrNotFound`, `ErrCycle`, `ErrTimeRange` and `ErrCorruptTree` sentinels are declared here —
  the last one distinct from `ErrCycle` on purpose: `ErrCycle` refuses a requested change,
  `ErrCorruptTree` reports a chain that was already broken when fngr opened the file (a
  hand-edit, a half-written database, or someone else's `.fngr.db` that `db.ResolvePath` picked
  up from the current directory). Every function in the package that touches the database accepts
  a `context.Context`.
  `formatTimestamp` — the only writer of `created_at`, re-checking `timefmt.InRange` because a
  timestamp can bypass the CLI parser via `--format=json` or a directly-built `AddInput` — is
  here rather than in `tx.go` because it opens no transaction and produces the `ErrTimeRange`
  declared above it.
- `internal/event/query.go` — Every *standalone* read of an event. Two kinds are elsewhere and
  neither is an oversight: metadata's own reads (`ListMeta`, `CountMeta`) are in `meta.go` with
  the rest of the metadata, and the small `SELECT`s a mutation makes inside its own transaction
  before writing (`event.go`'s parent lookup, `mutate.go`'s title/body and parent_id reads,
  `tx.go`'s `requireEventExists` and `rebuildEventFTS`) stay beside the write they serve.
  `Get`, `HasChildren`, `List` / `ListSeq` (FTS5 filter + date range + `Limit` +
  `Ascending`, both built by the shared `buildListQuery`, which refuses a negative `Limit` —
  unlike `From`/`To`, deliberately un-range-checked because a bad bound matches nothing or
  everything *as asked*, whereas only the `LIMIT` clause reads `Limit` and it is emitted on
  `Limit > 0`, so a negative value silently *widens* to the whole journal. Refused rather than
  clamped, and restated here rather than left to `toListOpts` because a directly-built
  `ListOpts` skips the CLI. A `Limit` always keeps the newest N and
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
  root's own parent came back among its descendants),
  `CountSubtree` (the same recursion, the same `UNION` and the same loop test — restated in SQL as
  two scalar subqueries over one materialized CTE — for a caller that wants only the size.
  `delete -r` is that caller, and reading every title, body and meta row of a 100k subtree to
  print one integer cost ~950 ms and ~110 MB of live heap against ~120 ms and nothing).
  `loadMetaBatch` chunks the IN clause to stay under SQLite's parameter limit.
  `scanEventRow` is the only row reader — shared by
  `scanEvents` and `ListSeq`. `created_at` is read through a `timeScanner` rather than scanned
  straight into a `time.Time`: the driver hands back a raw string for a value it cannot parse, and
  the default conversion then failed the *entire* result set, so one bad row written by an older
  build broke list, show and delete alike (delete calls `Get` first). `timeScanner` never errors —
  such a row reads as the zero time and stays deletable.
- `internal/event/mutate.go` — `Update` (title, body, and/or timestamp; on title or body change body-derived tags are
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
  stored chain is already cyclic → `ErrCorruptTree`), and `Delete`.
- `internal/event/meta.go` — Domain meta key constants (`MetaKeyAuthor`, etc.), the
  `metaSourceBody` / `metaSourceExplicit` values of `event_meta.source`, and `MergeMeta`, which
  merges all meta sources for a new event (author, body tags, explicit entries) with dedup.
  `CollectMeta` is `MergeMeta` over `--meta key=value` strings. An explicit `author` *replaces*
  the default rather than joining it; two distinct explicit authors are an error, and so is an
  empty one (`-m author=` used to both blank the author and discard the real one). `author` is
  single-valued because `AuthorOf` — the one lookup, used by `render` and the JSON import —
  returns the first match in `ORDER BY key, value`, so a second row means the displayed author
  is alphabetical rather than true. `requireOneAuthor` restates that at the writer, so an
  `AddInput` built directly cannot create the state no meta verb is allowed to repair.
  `requireStorableMeta` is the same argument for `parse.ValidateMeta`: the CLI checks are
  pre-flights that can name a flag or a record index, this is the one nothing bypasses. Safe
  against the paths that never see an argument parser because `parse.BodyTags` is bounded by
  `metaNamePattern`, so neither a body-derived tuple nor a migration back-fill can carry an
  empty value or a whitespace key. Reached by the minting writers only — `AddTags` directly, the
  `Add` path through `validateAddInput`; `RemoveTags` is deliberately unchecked, so a row an older
  build wrote stays removable. Also `metaSet` (used by `addInTx`, `addTagsInTx` and
  `subtractMeta`) and `subtractMeta`, the tuple-set helpers behind `Add` and `Update`.

  The storage verbs live here too — reads included, which is the one place the package's
  read/write seam gives way to the domain one: `ListMeta` and `CountMeta` are queries, and they
  are here rather than in `query.go` because a reader asking anything about metadata should have
  one file to open. The rules above are what the verbs enforce. `AddTags` (inserts as
  `'explicit'`, *promoting* an existing body-derived row, since a tag named on the command line
  must survive the next body edit; the `DO UPDATE` is guarded on `source <> excluded.source` so
  only that promotion rewrites a row, and the added count comes from a pre-read because
  `RowsAffected` cannot tell the promotion from an insert) / `RemoveTags` (event-scoped meta CRUD
  with FTS resync; both refuse `protectedMetaKeys`), `ListMeta` (filtered via
  `ListMetaOpts{Key, Value}`), `CountMeta`, `UpdateMeta` (a *merge*, not a plain rename —
  `UPDATE OR REPLACE` drops the row
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
  nothing could repair. The *new* tuple goes through `parse.ValidateMeta` — for every key, not
  just a protected one, since a blank value is exactly the unrepairable state — which is also
  what keeps a rename from minting the whitespace key `-m` refuses. The *old* one is unchecked
  on purpose: a rename is how a row an older build wrote gets repaired.
  Both verbs are one call to `rewriteMetaTuple`, which runs a statement over every row carrying
  a `(key, value)` tuple and resyncs the FTS content of the events that carried it. They differ
  in that statement and were otherwise the same forty lines — including the ordering that makes
  them correct, which is the reason to share rather than merely the saving: the affected ids
  must be read *before* the statement runs, because afterwards there is no tuple left to find
  them by and an event's FTS content includes its `key=value` tokens. The protected-key gate
  stays at the call sites, since that is the one thing the two answer differently. Private
  helpers: `readMetaTx`, `metaEventIDs`, `execBodyMetaTuples` (both halves of `Update`'s
  body-tag sync, driven by `deleteBodyMetaSQL` / `insertBodyMetaSQL`; it and `subtractMeta` are
  what `mutate.go` reaches in here for), and `requireUnprotectedMeta` / `requireUnprotectedTags` /
  `requireRenamableMeta` (the `protectedMetaKeys` gate — currently just `author`, refused as the
  target of every meta verb because no insert path can produce zero or two of them).
- `internal/event/tx.go` — The transaction layer: `inTx` / `inTxVoid`, plus the two cross-file
  tx-taking helpers that belong to no one domain — `requireEventExists` (used by every mutation
  that reads before it writes: `Update`, `Reparent`, `AddTags`, `RemoveTags`. Not `Delete`, whose
  own `RowsAffected` already says whether the row was there) and `rebuildEventFTS` (used by
  `Update`, `AddTags`, `RemoveTags` and `rewriteMetaTuple` to resync `events_fts`). Nothing else
  opens a transaction here, which is what makes the begin/commit wording a single decision rather
  than one repeated at seven call sites — see `inTx`'s own comment.
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
  All three dispatchers switch on `Canonical(format)`, which resolves the `formatAliases`
  table — currently just `markdown` → `md` — and passes anything else through untouched.
  `AddCmd.Run` and `list.go`'s tree branch canonicalize too, being the two places that test a
  format outside a dispatcher: no alias resolves to `json` or `tree` today, and the point is
  that adding one cannot silently send a 10 000-record batch down the text path.
  `ListFormats` / `EventFormats` / `AddFormats` are the slices `main.go` joins into Kong's
  `enum:` tags *and* into the `--format` help text — one variable each, since Kong interpolates
  `${…}` in `help:` too and trims each comma-split enum value, so `", "` reads as prose in the
  help and parses the same as `","`. A spelling missing from the enum is refused at parse time
  however well `Canonical` would resolve it, so all three slices are *derived* by `withAliases`,
  which adds an alias exactly when the vocabulary already has the format it resolves to. Listing
  them by hand is what invites an alias into a vocabulary that cannot render it: the
  vocabularies stay different — `tree`/`flat` need a set of events and `text` is the one-event
  view — so `txt` → `text` in `ListFormats` would parse, resolve, miss every case in
  `EventsStream`'s switch and fall through to flat, at exit 0. Aliases are appended sorted
  because the joined slice is in the error text a rejected `--format` prints.
  Meta in JSON output is `[[key, value], ...]`, sorted by `(key, value)`. Each `event_meta`
  row maps to one tuple — multiple values for the same key produce multiple tuples.
  Every buffered writer but `Tree` *is* its streaming twin over `slicedSeq`, a slice-backed
  `iter.Seq2`: which one a command reaches for depends on whether it could stream, not on what
  the user asked for, so their bytes have to match — and they did not. Streamed JSON put each
  comma on a line of its own and a blank line before the `]`, and buffered `CSV` discarded the
  write errors `CSVStream` checked. `Tree` is the one exception and not by oversight: it needs
  the whole topology before it can draw a line, so it has no streaming form to delegate to.
  `JSONStream` writes the `[\n  ` / `,\n  ` leads itself and encodes each element at
  `SetIndent("  ", "  ")`, which is exactly how `MarshalIndent(slice, "", "  ")` renders one,
  trimming the newline `Encode` adds (that newline is what the old shape came from). The
  `bytes.Buffer` is what makes an encoder usable at all here rather than an optimization on top
  of one — `Encode` writes straight through, so its newline can only be trimmed off a buffer —
  and `TestJSONStream_MatchesMarshalIndent` pins the layout against `MarshalIndent` itself,
  since comparing the two dispatchers cannot say anything once one delegates to the other.
  Events are encoded through a hoisted `*jsonEvent`: the struct is 88 bytes, so passing it by
  value boxes a heap copy per event. `JSONEvent` is the odd one out and marshals directly — one
  object, what `SingleEvent` uses, since `fngr event 5 --format=json` describes one event and
  `jq '.title'` has to answer; a standalone object starts in column zero.
  An empty result is empty per format and deliberately so: tree/flat/md write nothing, JSON
  writes `[]`, CSV writes its header row. `No events found.` goes to stderr for *all* of them,
  so the two self-explanatory formats need no exception to keep in step; the streaming path
  learns it matched something from `noteAny`, a pass-through wrapper, rather than by
  materializing the result. Both that message and `fngr meta`'s go through
  `cmd/fngr/plural.go::reportNone`, which is what makes the choice of stream a single decision.
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
  `internal/event` package keeps its own `testDB` for data-access tests, in a
  `testhelpers_test.go` of its own — a fixture six test files reach for does not belong to
  whichever verb's file happened to declare it first. No persistent fixtures
  on disk. A CLI test that needs a parser builds it with `newTestParser` from that same file,
  which is `kongOptions` plus redirected writers and exit — never a hand-rolled `kong.New`. Three
  hand-rolled copies is what `kongOptions` was introduced to end, and the oldest had already
  drifted: it predated `ShortUsageOnError`, so it answered a parse error with the full command
  list, which is the difference the help tests exist to notice.
- Tests should be parallelized.
- Table-driven tests with `t.Run` subtests.
- Use modern Go idioms and features.
- Try hard to prevent duplicated code.
- Schema changes go in a new entry at the bottom of `migrations` in `internal/db/migrate.go`;
  never edit a published migration.
- Version injected via `-ldflags` at build time from git tags; surfaced via `--version`.
- `common-go.mk` is shared across repos — don't modify it here.
