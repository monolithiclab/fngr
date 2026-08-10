# Code Review — 2026-07-27 (deep multi-angle audit, v0.0.2 @ `b817ca5`)

Full-codebase audit across seven angles: Go idioms, architecture, code smell,
correctness, performance, security, and product/UX. Unlike the 2026-04-23
round — which reviewed release infrastructure and found no open code
findings — this one is an empirical hunt against a running binary. Nearly
every finding below carries a reproduction transcript.

**Baseline (verified):** `make test` green, `make lint` green, total coverage
**87.7%** (cmd 86.2 / event 83.7 / render 90.1 / db 74.7 / parse 98.1 /
timefmt 100). `govulncheck` clean — 0 called vulnerabilities.

**Headline:** the test suite and linters are green and the architecture is
sound, but the tool has **five distinct silent-data-loss or silent-wrong-answer
paths**, three of which are one-line-class fixes. None are caught by the
current tests because all five live in the gap between "the code does what it
says" and "what it says is right."

The single most important sentence in this document: **`fngr` loses events
under concurrent writes, and the README states the opposite.**

---

## Severity summary

| ID | Severity | Area | One-line | Status |
| --- | --- | --- | --- | --- |
| [C1](#c1) | **Critical** | db | Pooled-connection PRAGMAs → concurrent `add` silently loses events; `foreign_keys=OFF` on most connections | ✅ fixed |
| [C2](#c2) | **Critical** | timefmt | Unbounded relative offset writes a negative-year timestamp that bricks every read; reachable from piped content | ✅ fixed |
| [C3](#c3) | **Critical** | json | Documented JSON round-trip silently corrupts the event tree | ✅ fixed |
| [C4](#c4) | **Critical** | parse | `\w` is ASCII-only → `@josé` silently stored as `people=jos` | ✅ fixed |
| [C5](#c5) | **Critical** | filter | Leading `!` discards the rest of the expression; bare `!` panics; hyphens error | ✅ fixed |
| [H1](#h1) | High | migrate | Migration 3's SQL `TRIM()` ≠ `strings.TrimSpace` → corrupted legacy titles/bodies | ✅ fixed |
| [H2](#h2) | High | render | O(n²) prefix concatenation in `Tree` — 50k-deep chain: 67.85 s / 7.0 GB | ✅ fixed |
| [H3](#h3) | High | event | `meta rename` fails with a raw UNIQUE error on its primary use case | ✅ fixed |
| [H4](#h4) | High | cmd | `fngr add` hangs forever when stdin is an open, idle pipe | ✅ fixed |
| [H5](#h5) | High | cmd | `confirm` treats EOF as consent → non-interactive `meta rename` acts without `-f` | ✅ fixed |
| [H6](#h6) | High | cmd | `confirm` still blocks forever on an idle pipe — H4's hazard, surviving in the other stdin reader | ⛔ won't fix |
| [H7](#h7) | High | cmd | `-e` under a non-TTY launches an editor that cannot run, discarding the piped body | ✅ fixed |
| [M1](#m1) | Medium | event | Body-tag sync silently deletes operator-added meta | ✅ fixed |
| [M2](#m2) | Medium | event | Parent cycle → two non-terminating loops + a silent total data blackout | ✅ fixed |
| [M3](#m3) | Medium | event | Nothing enforces a single `author`; display picks whichever sorts first | ✅ fixed |
| [M4](#m4) | Medium | parse | Email addresses mint bogus `people` tags | ✅ fixed |
| [M5](#m5) | Medium | timefmt | `"1 month ago"` on the 31st lands in the wrong month; int64 overflow yields a *future* time | ✅ fixed |
| [M6](#m6) | Medium | render | Newlines and ANSI/OSC escapes in titles forge output rows | ✅ fixed |
| [M7](#m7) | Medium | event | FTS conflates content with metadata → body text forges tag matches | ✅ fixed |
| [M8](#m8) | Medium | render | `--limit` on tree format promotes orphaned children to roots, unmarked | ✅ fixed |
| [M9](#m9) | Medium | cmd | `fngr meta` output amplification: 1 MB stored → 202 MB printed | ✅ fixed |
| [M10](#m10) | Medium | parse | `". "` split eats abbreviations — `Dr. Smith` → title `Dr` | open |
| [M11](#m11) | Medium | db | `fngr add` never creates a project-local `.fngr.db`; first add lands in `~/.fngr.db` | open |
| [M12](#m12) | Medium | timefmt | `--from`/`--to` reject both relative forms and fngr's own emitted timestamps | ✅ fixed |
| [M13](#m13) | Medium | timefmt | DST spring-forward silently shifts `event time` to the prior hour | ✅ fixed |
| [M14](#m14) | Medium | event | `-n N -r` returns the N **oldest** events | ✅ fixed |
| [M15](#m15) | Medium | perf | Unbuffered stdout — one `write(2)` per event; 11-19% on large lists | ✅ fixed |
| [M16](#m16) | Medium | supply-chain | Release workflow: broad privileges on seven mutable-tag actions | ✅ fixed |

Plus 18 low-severity items, an architecture section, and a measured
performance section — all below.

---

## Critical

<a name="c1"></a>
### C1 — Pooled-connection PRAGMAs: concurrent `add` silently loses events

**`internal/db/db.go:40-53`**

```go
pragmas := []struct{ key, value string }{
	{"foreign_keys", "ON"},
	{"journal_mode", "WAL"},   // needs a brief exclusive lock...
	{"busy_timeout", "5000"},  // ...but the busy handler isn't installed until here
	{"synchronous", "NORMAL"},
}
for _, p := range pragmas {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA %s = %s", p.key, p.value)); err != nil {
```

Two independent defects in eight lines:

1. **Ordering.** `busy_timeout` is set *after* `journal_mode=WAL`. WAL
   activation takes a short exclusive lock; with no busy handler installed
   yet, it fails instantly instead of waiting.
2. **Scope.** These are `db.Exec` calls on a `*sql.DB` — a *pool*. Each
   PRAGMA lands on whatever connection is free. There is no
   `SetMaxOpenConns(1)`, no DSN-embedded pragmas, and no per-connection init
   hook. The four pragmas can spread across four different connections, and
   **every connection the pool opens later starts at SQLite defaults:
   `foreign_keys=OFF`, `busy_timeout=0`.**

Proven directly — a two-connection probe shows `conn#1: foreign_keys=1
busy_timeout=5000` / `conn#2: foreign_keys=0 busy_timeout=0`. And the
user-visible consequence:

```
$ for i in $(seq 1 10); do fngr --db conc.db add "c$i" & done; wait
$ fngr --db conc.db --format=csv | tail -n +2 | wc -l
       6                      # seed + 5.  Five of ten events lost.
$ sort conc.err | uniq -c
   1 error: cannot set pragma journal_mode: database is locked (261)
   2 error: cannot set pragma journal_mode: database is locked (5) (SQLITE_BUSY)
   2 fngr: error: insert event: database is locked (5) (SQLITE_BUSY)
```

Note the inserts fail at *zero* wait despite a nominal 5-second timeout —
that's the scope bug, not the ordering bug.

The README's Troubleshooting section currently says: *"fngr opens the DB with
WAL + `busy_timeout=5000ms`, so transient locks self-heal; only a stuck holder
produces this error."* Neither half is true, and it sends users hunting a
stuck holder that doesn't exist.

There is a latent non-concurrent consequence too: CLAUDE.md notes that
streaming queries open additional connections (the stated reason tests can't
use `:memory:`). Those extra connections run with `foreign_keys=OFF`. FK
cascade was nonetheless observed working end-to-end from the CLI, because the
delete happens to land on the initialized connection — but that is luck, not
design.

**Fix** — move the pragmas into the DSN. `modernc.org/sqlite` applies
`_pragma=` DSN parameters on *every* new connection, resolving ordering and
scope together:

```go
dsn := "file:" + path +
	"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)" +
	"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
db, err := sql.Open("sqlite", dsn)
```

**Resolved.** `internal/db/db.go` now builds the DSN with `_pragma=`
parameters (busy_timeout first), and `Open` pings once so an unusable path
reports at open time rather than from an arbitrary later query. Two regression
tests were added and both were confirmed to fail against the old code with
exactly the predicted symptoms: `TestOpen_PragmasApplyToEveryPooledConnection`
(conns 1-3 reported `foreign_keys=0 busy_timeout=0 synchronous=2`) and
`TestOpen_ConcurrentWritersAllSucceed` (4 of 10 events lost to instant
`SQLITE_BUSY`). The pool test is the important one — the four pre-existing
single-connection pragma tests pass either way, because the pool hands back
the one connection `Open` configured.

<a name="c2"></a>
### C2 — Unbounded relative offset writes a timestamp that bricks the database

**`internal/timefmt/timefmt.go:181-225`** (`relOffset`, `relCountUnit`) →
stored at **`internal/event/event.go:141`** → read fails at
**`internal/event/event.go:791,908`**

`relCountUnit` accepts any non-negative `int64` from `strconv.Atoi` with no
range check. `now.AddDate(0, -n, 0)` then produces a year far outside the
storage format's assumptions, and `Format("2006-01-02 15:04:05")` emits a
variable-width negative-year string. On read, the driver cannot scan it back
into `time.Time`, and **every read command aborts for the entire result set**:

```
$ fngr --db brick.db add 'x' -t '1000000 days ago'
Added event 1
$ sqlite3 brick.db 'select created_at from events'
-0712-08-29 14:07:26
$ fngr --db brick.db
fngr: error: scan event: sql: Scan error on column index 4, name "created_at":
  unsupported Scan, storing driver.Value type string into type *time.Time
$ fngr --db brick.db event 1        # same
$ fngr --db brick.db delete 1 -f    # same — DeleteCmd.Run calls s.Get first
$ fngr --db brick.db add 'y'        # still works → user keeps appending to a dead DB
Added event 2
```

**There is no CLI recovery path.** `delete` is blocked by its own `Get`;
`event date` is blocked by the read in `EventDateCmd.Run`. Only raw `sqlite3`
can fix it.

Critically, this is **reachable from untrusted content**, not just from a
`-t` typo. `SplitTimePrefix` parses a leading `": "`-delimited token out of
piped text (`cmd/fngr/add.go:68`):

```
$ printf '2147483647 months ago: meeting notes from a scraped page\n' | fngr add
Added event 1
$ sqlite3 t3.db "select created_at from events"
-178954945-12-27 15:55:51
```

Threshold is any `n` producing a negative year (~`24313 months ago`,
~`740000 days ago`). The JSON import path is safe — `time.Parse(RFC3339, …)`
rejects out-of-range years.

**Fix:** bound `n` in `relCountUnit`/`relOffset` and reject results outside
year `[1, 9999]`, returning the normal "unrecognized time" error. Add a
defensive year check in `addInTx`/`Update` before formatting. Independently,
make `scanEvents` degrade — skip or flag the bad row — rather than abort the
whole query, so a poisoned row remains deletable.

**Resolved.** Fixed together with [M5](#m5) — both bound the same `n`. Three
independent layers, because each one alone leaves a hole:

1. **Parse time.** `relCountUnit` rejects counts above `maxRelCount` (1e6), and
   `ParsePartial` rejects any result outside year `[1, 9999]`, both with the
   normal "unrecognized time" error. The year check is a wrapper around the
   former body (now private `parsePartial`), so every caller — `Parse`,
   `SplitTimePrefix`, `event time`, `event date`, `--from`/`--to` — inherits it.
   `SplitTimePrefix` consequently leaves an absurd prefix in the title verbatim
   instead of parsing it, which closes the piped-content path.
2. **Write time.** `formatTimestamp` re-checks `timefmt.InRange` in `addInTx`
   and `Update` and returns `ErrTimeRange`. Not redundant: `--format=json` and
   any direct `AddInput` bypass the CLI time parser entirely.
3. **Read time.** `created_at` is read through a `timeScanner`, a `sql.Scanner`
   that cannot fail. The driver returns a `time.Time` for a parseable value and
   a raw `string` otherwise; the old direct scan turned that string into an
   error that killed the *whole* result set. A poisoned row now reads as the
   zero time, so an already-bricked database becomes listable, showable and
   deletable again — no raw `sqlite3` needed, which the review noted was the
   only recovery path. README's Troubleshooting section documents the
   `Jan 01 0001` symptom and the `event date` / `event time` repair.

The encoding itself moved into `timefmt` as `FormatStorage` / `ParseStorage`,
because `buildListQuery` was rendering the `--from`/`--to` bounds with its own
copy of `.UTC().Format(DateTimeFormat)`. Those bounds are compared lexically
against the stored text, so a layout change had to be found in two places to
avoid silently breaking date filtering.

Regression tests, each verified to fail against the pre-fix code with exactly
the symptom above: `TestParseRelative` rejection rows for `maxRelCount` and
both int64 overflows, `TestParsePartial_RejectsOutOfRangeYear`,
`TestSplitTimePrefix_OutOfRangeIsNotATimestamp`, `TestInRange`,
`TestAdd_RejectsOutOfRangeTimestamp`, `TestUpdate_RejectsOutOfRangeTimestamp`,
`TestScan_PoisonedTimestampStaysUsable` (writes a raw `-0712-08-29 14:07:26`
row, then asserts List, ListSeq, Get and Delete all still work and the
neighbouring rows survive), `TestTimeFromDriverValue`, `TestStorageRoundTrip`,
and two Kong-level tests. `internal/timefmt` is now at 100% statement coverage.

Four alternatives were considered and **not** taken:

- *`SELECT CAST(created_at AS TEXT)` so the driver never auto-converts.* This
  trades one undocumented driver behaviour (parses `DATETIME` decltypes) for
  its inverse (won't parse an empty decltype), touches all five read queries,
  and the type switch already tolerates both.
- *Surface the corruption instead of degrading silently.* A poisoned row and a
  legitimately stored year-1 row now render identically. Carrying the raw
  string on `Event` and printing it in `event show` is the honest fix, but it
  is new surface for a case the Troubleshooting note already covers.
- *One consistent error for every out-of-range count.* `1000001 days ago`
  reports "unrecognized time" while `999999 days ago` reports the year range.
  Unifying them means threading an error out of `relCountUnit` /
  `relOffset` / `parseRelative`, all three of which use `ok=false` to mean
  "not a relative form, try the next layout". Both inputs are rejected; the
  churn isn't worth the wording.
- *Route `cmd/fngr/add_json.go`'s `time.Parse(time.RFC3339, …)` through
  `timefmt.Parse`.* Correct — it is the one import path with its own timestamp
  parser — but it widens the accepted JSON input set, so it belongs with
  [C3](#c3), which is already rewriting that file. *(Done there.)*

<a name="c3"></a>
### C3 — The documented JSON round-trip silently corrupts the event tree

**`cmd/fngr/add_json.go`** (`jsonInputToAddInput`, `runJSON`),
**`internal/event/event.go`** (`AddMany`/`addInTx`)

README promises this in three places ("JSON is the only round-trip format",
a quick-start pipe recipe, and a container recipe). It fails three ways, and
the third is silent:

```
$ fngr --db t.db --format=json | fngr --db rt.db add --format=json
fngr: error: --format=json: unknown field "id"

$ fngr --db t.db --format=json | jq 'map(del(.id))' | fngr --db rt.db add --format=json
fngr: error: parent event 4: not found          # whole batch rolls back; rt.db empty
```

Work around both and it "succeeds" with exit 0 and a **different tree**:

```
$ fngr --db t.db -r --format=json | jq 'map(del(.id))' | fngr --db rt2.db add --format=json
Imported 7 events

# source t.db topology:            # rt2.db after import:
3 deployed                         4 deployed
└─ 4 rollback                      └─ 6 postmortem      <-- wrong child
   └─ 5 postmortem                 3 fixed login bug
2 fixed login bug                  └─ 5 rollback        <-- wrong parent
```

`parent_id` is carried across as a **literal integer** into a database with
different autoincrement values. There is no ID remapping at all. Whether you
get a rollback or a plausible-but-wrong tree depends purely on whether source
IDs happen to collide favorably.

**There is currently no working way to move events between fngr databases.**

**Fix:** accept (and ignore for insertion) an `id` field in `jsonAddInput`.
In `addInTx`, maintain a `map[int64]int64` of source-id → new-id as rows
insert; resolve each record's `parent_id` against that map first, falling back
to an existing DB row only if unmatched. Records arriving out of topological
order need a second pass — cheap, since `AddMany` already owns the
transaction: insert all rows with `parent_id` NULL, then a single `UPDATE`
loop from the map before commit. ~60 LoC, fully contained inside an existing
tx.

**Resolved.** All three failure modes are gone: `id` is accepted and ignored
for insertion, batch-internal `parent_id`s are remapped, and the round trip
reproduces the source tree exactly.

The fix is not quite the one proposed above. Putting a source-id map inside
`addInTx` would make `AddInput.ParentID` mean "an id in the target database"
for `fngr add --parent N` and "an id in whatever database this JSON came
from" for the import path — the same field reinterpreted by context, which is
how the bug got in. Instead `AddInput` gained a second, mutually exclusive
field, `ParentIndex *int`, meaning "the parent is record N of *this batch*".
The domain layer never learns that source ids exist; all of that bookkeeping
lives in `cmd/fngr/add_json.go`, where `indexBySourceID` builds the source-id
→ batch-position map and `jsonInputToAddInput` picks `ParentIndex` when the
`parent_id` names a sibling and `ParentID` when it doesn't. A `parent_id`
pointing outside the batch is still validated against the target database, so
importing a subtree under an existing event keeps working.

Mechanically it is the two-pass insert the review describes — every row is
inserted first, then one `UPDATE` loop wires the batch-relative parents —
because `fngr --format=json` emits newest-first, so on a re-import the child
almost always precedes its parent. What the two-pass shape does *not* give
you is cycle rejection: SQLite's foreign key only checks that the parent row
exists, and in a cycle every row does, so a self-parenting record would
commit silently and become invisible to every root-anchored query.
`validateParentIndexes` runs before the first INSERT and rejects out-of-range
indexes, `ParentID`+`ParentIndex` together, and cycles — the last via a
three-colour walk that stays linear because each record has at most one
parent.

Two things rode along, both blocking the round trip in practice:

- Duplicate source ids in one batch are now an error
  (`record 1: id 1 already used by record 0`) rather than a last-one-wins
  remap.
- `created_at` is parsed with `timefmt.Parse` instead of
  `time.Parse(time.RFC3339, …)`, so hand-written import files can use every
  layout `--time` accepts (this also closes the [Documentation](#documentation)
  note about RFC3339-only stamps).

Verified end-to-end on scratch databases with deliberately divergent id
counters: export → import → export produces a byte-identical normalisation
(title, body, `created_at`, meta, and parent-by-title) for a three-level tree,
in both `-r` and default order, and survives a second round trip. Failure
paths (`parent_id` naming a nonexistent target event, duplicate ids, self- and
two-record cycles) each exit 1 and leave the target database empty. The
remapping is mutation-tested — reverting the `byIndex` lookup fails five
subtests of `TestAddJSON_ParentIDResolution` plus the Kong-level
`TestKongDispatch_JSONRoundTrip`.

**Known limit, deliberate:** piping a full export into a database while also
passing `--parent N` re-roots the records that have no `parent_id`, because
`encoding/json` cannot distinguish an omitted field from an explicit `null`.
Combining a whole-tree import with `--parent` is not a documented workflow;
the alternative is a `json.RawMessage` dance that costs more clarity than the
case is worth.

<a name="c4"></a>
### C4 — Non-ASCII `@person` / `#tag` names are silently truncated

**`internal/parse/parse.go:17`**

```go
const metaNamePattern = `[\w][\w/\-]*`
```

Go's `regexp` treats `\w` as ASCII-only (`[0-9A-Za-z_]`), so matching stops at
the first non-ASCII byte:

```
$ fngr add "café ☕ with @josé #niño"
$ fngr meta
author=nico  (1)
people=jos   (1)      <-- josé
tag   =ni    (1)      <-- niño
```

No error. The stored metadata is corrupt, and truncated forms **collide** —
`@josé` and `@josa` both become `people=jos`. For a personal journal whose
headline feature is `@person` tagging, on a tool used by people with names
like José, Zoë, Müller, Renée, or 田中, this is data corruption rather than a
cosmetic limit.

Blast radius: `metaNamePattern` is shared by the body-tag extractor,
`MetaArg`, and the exported `MetaNameRe` used by
`cmd/fngr/meta.go::parseMetaFilter` — so the same truncation hits `event tag`,
`--meta`, and `meta -S` filters. Event *text* round-trips fine (FTS handles
UTF-8; `-S 'café'` matches); only extracted metadata is damaged.

**Fix:** one line —

```go
const metaNamePattern = `[\p{L}\p{N}_][\p{L}\p{N}_/\-]*`
```

Already-truncated rows can be cleaned up with `meta rename`; worth a note in
the release changelog rather than a migration.

**Resolved.** Fixed together with [M4](#m4) — both edit `metaNamePattern`.
The pattern is now `[\p{L}\p{N}_][\p{L}\p{N}_/\-]*`, with a comment saying
why `\w` must not come back. The fix reaches `BodyTags`, `MetaArg` and
`MetaNameRe` at once, so `event tag`, `--meta` and `meta -S` are all covered;
`cmd/fngr/meta.go::parseMetaFilter`'s error text no longer quotes the old
ASCII pattern either.

Regression tests, verified against the pre-fix code: `TestBodyTags` gained
Unicode person/tag cases and the `@josé` vs `@josa` collision (old code
returned a single `people=jos`), `TestMetaArg` gained `@josé`, `#déploiement`
and `@田中` (old code rejected all three outright — the CLI path errored where
the body path silently truncated), and `TestKongDispatch_UnicodeMetaNames`
walks `add` → `event tag` → `meta` → `meta -S` through Kong.

Already-truncated rows still need `meta rename`; no migration ships for them.
A repair migration was considered and deferred to [H1](#h1)'s migration 4 rather
than rejected: the source text survives in `events.title`/`events.body`, so
re-derivation is lossless *for body-derived tags*, but a blanket
delete-and-re-derive would also destroy `people`/`tag` rows added by
`fngr event tag`, which never appeared in any body. The safe form has to
delete only rows whose value is a strict prefix of a freshly-derived value on
the same event, and it needs Go-side migration machinery (the current
migrations are pure embedded SQL and cannot call `parse.BodyTags`). That
belongs with the migration H1 already schedules, not bolted onto a regex fix.
*(Shipped there — see [H1](#h1). The deletion rule ended up stricter than
"strict prefix": only a value exactly equal to what the frozen legacy pattern
would have produced, which spares a hand-added `#work` next to a derived
`#workflow`.)*

<a name="c5"></a>
### C5 — Filter parser: silently wrong results, a panic, and unusable hyphens

**`internal/event/filter.go:26,36-40`** + **`internal/event/event.go:833`**

Three defects in one preprocessor, which does string substitution where it
needs a tokenizer.

**(a) Leading `!` discards the rest of the expression.**
`preprocessFilter("!alpha & gamma")` → `"NOT alpha AND gamma"`; then
`buildListQuery`'s `strings.CutPrefix(matchExpr, "NOT ")` strips the leading
`NOT ` and runs `id NOT IN (MATCH "alpha AND gamma")` — i.e. `NOT (alpha AND
gamma)` instead of `(NOT alpha) AND gamma`:

```
$ fngr -S '!#bugfix & #work' --format=flat
7   4.23pm  sarah  sarah note
5   4.22pm  nico  postmortem scheduled
4   3.22pm  nico  rollback needed
3   2.22pm  nico  deployed v1.2 to staging #ops
2   10.15am  nico  fixed login bug        <-- the #bugfix event it was told to exclude
1   9.30am  nico  standup with @alice #work
6   Jul 26 9.00am  nico  lunch
                                          # all 7 events — no filter applied at all
$ fngr -S '#work & !#bugfix' --format=flat
                                          # (correct: empty)
```

Same expression, operands reordered, opposite answer. No error, no warning. A
user can conclude an event doesn't exist when it does — the worst possible
failure mode for a search tool.

**(b) Bare `!` panics.** `convertTerm` recurses on `tok[1:]` and then indexes
`tok[0]` on an empty string:

```
$ fngr -S '!'
panic: runtime error: index out of range [0] with length 0
	.../internal/event/filter.go:40
exit 2
```

Also fires on `!!!`. A fix must guard the empty string *after* stripping, not
merely reject a lone `"!"`.

**(c) Ordinary text breaks the parser.** Any hyphenated term is unsearchable,
and a stray quote leaks the tokenizer:

```
$ fngr -S 'session-handler'
fngr: error: invalid filter syntax (query events: SQL logic error:
  no such column: handler (1)); see --help for the -S grammar
$ fngr -S '"'
fngr: error: invalid filter syntax (query events: SQL logic error:
  unterminated string (1)); see --help for the -S grammar
```

`session-handler`, `e-mail`, `pre-prod`, `follow-up` — all report a SQLite
column error.

**Fix:** replace the preprocessor with a real parser in
`internal/event/filter.go`: tokenizer → precedence-climbing parser
(`!` > `&` > `|`) → FTS5 emitter. Quoting every bare term on emit fixes
hyphens and embedded quotes for free. Reject malformed input at parse time
with a term-position message instead of letting SQLite fail. ~200 LoC plus
table-driven tests, one file, no schema or CLI change. Grouping parens become
nearly free afterward — currently documented as unsupported; leave them out
unless wanted.

**Resolved.** `internal/event/filter.go` is now tokenizer → precedence-climbing
parser → SQL emitter, and the emit target changed: instead of one FTS5 MATCH
string for the whole expression, every leaf term gets its own
`e.id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)` and `&`/`|`/`!`
become SQL operators over id sets. That is what makes (a) fixable at all —
FTS5 has no unary NOT, so no amount of token rewriting can express `!a & b`
inside a single MATCH. `buildListQuery` loses the `strings.CutPrefix(matchExpr,
"NOT ")` special case and the FTS join along with it; it now returns an error,
propagated through `ListSeq` and `List`.

(b) is gone because `!` is a token, not a string prefix: `!`, `!!!` and `a & &`
all report `invalid filter syntax: ... at position N` before any SQL runs, via
the new `event.ErrFilter` sentinel. (c) is gone because every term is quoted on
emit — `session-handler` and `v1.2` search for themselves, and a lone `"` now
returns no results instead of `unterminated string` (FTS5's tokenizer drops
punctuation, so there is nothing to match; "no results" is an answer).

`cmd/fngr/list.go`'s `wrapFilterErr` — which sniffed SQLite's message text for
`fts5` / `SQL logic error` / `unterminated` to guess whether a failure was the
user's fault — is replaced by `withGrammarHint`, a one-line
`errors.Is(err, event.ErrFilter)` check. Guessing is no longer necessary now
that filter errors are typed and raised before the query.

Two behaviours were preserved deliberately, both previously undocumented and
both now in the README table: adjacent terms are an implicit AND (`-S 'walk
dog'`), which fell out of FTS5 juxtaposition before, and a trailing `*` is a
prefix search (`-S 'releas*'`), which needed explicit handling once terms
became quoted. Grouping parens stay out per the note above — `unary` would
need one `'(' expr ')'` case, but adding syntax is a feature, not a fix.

Two smaller inconsistencies surfaced during the rewrite and were fixed with it.
`-S '@bob@example.com'` used to compile to a phrase that could never match, so
the search reported "no results" for input that `event tag` and `meta -S` both
reject as invalid; `ftsTerm` now resolves shorthands through `parse.MetaArg`, so
all three agree. And `list --format=json -S 'a &'` printed `[` to stdout before
erroring (CSV printed its header row), because `ListSeq` compiles the filter
lazily — after the renderer has begun. `ListCmd.toListOpts` now calls
`event.ValidateFilter` up front, before the pager is even spawned.

**Cost measured, accepted.** Per-term MATCH subqueries mean FTS5 no longer
intersects posting lists internally: a 3-term AND over 200k events goes from
89 µs to ~154 ms, and bind order now affects speed because SQLite has no size
estimate for a virtual-table subquery. At this tool's scale — single-user
journals, ~5k events — the same query costs 2-3 ms, and single-term queries are
within 15%. Correct answers are worth 2 ms; folding the positive terms back into
one MATCH would re-add exactly the coupling this rewrite removed. Revisit only
if a real database gets large enough to notice.

**Considered and skipped:** routing `meta -S` errors through `ErrFilter` too
(its grammar is different and its errors are already specific — that is a
unification, not a fix); an EOF sentinel token in the parser (line-neutral, and
`len([]rune(expr))` already allocates nothing).

---

## High

<a name="h1"></a>
### H1 — Migration 3 corrupts legacy titles and bodies

**`internal/db/migrations/3.sql:9,13,20,28`**

SQLite's `TRIM(X)` strips **only U+0020**. `parse.SplitTitleBody` uses
`strings.TrimSpace`, which strips `\t \n \r \v \f`, NBSP, and all Unicode
space. Every legacy `events.text` with non-space whitespace at a split
boundary migrates to a value the Go rule would never produce.

Verified by building a v1-schema DB with 20 edge cases, migrating, and diffing
against a Go program running the real `SplitTitleBody`:

| legacy `text` | Go `SplitTitleBody` | migration 3 result |
| --- | --- | --- |
| `"title. \n  body after newline"` | body `"body after newline"` | body **`"\n  body after newline"`** |
| `"\ttab title no sep"` | title `"tab title no sep"` | title **`"\ttab title no sep"`** |
| `"\n\nleading newlines. body"` | title `"leading newlines"` | title **`"\n\nleading newlines"`** |
| `"body has trailing newline. line1\nline2\n"` | body `"line1\nline2"` | body **`"line1\nline2\n"`** |
| `"unicode nbsp . body"` | title `"unicode nbsp"` | title **`"unicode nbsp "`** |

Visible, not cosmetic — embedded newlines break the one-line-per-event
contract of `flat`/`tree`:

```
$ fngr --db work.db --format=flat | od -c | head -2
0000000  2  0     J  a  n     0  1     1  .  0  0  p
0000020  m           \n \n  l  e  a  d  i  n  g     n
```

The rebuilt `events_fts` content inherits the same bad values.

Related, same file: the split can produce **`title = ''`** (legacy `". body"`
→ title `""`), which every other code path treats as invalid — `fngr add
". body"` is rejected with `event title cannot be empty`, and `fngr event
title 12 ''` refuses to fix it. Confirmed: `fngr event 12` prints `Title:`
empty.

**Fix:** a new migration 4 that re-derives title/body from the still-
recoverable concatenation, using
`TRIM(x, char(32)||char(9)||char(10)||char(13)||char(11)||char(12))` — or
doing the split in Go during migration, which is the only way to match
`strings.TrimSpace` exactly (it also strips NBSP and U+2000-200A). Per
CLAUDE.md, never edit the published `3.sql`.

**Resolved.** Migration 4 does the repair in Go
(`internal/db/migrate4.go::repairLegacyText`), run against the same transaction
as `4.sql` via a new optional `goMigrations[N]` hook in `migrate.go`. The SQL
alternative above was rejected: an explicit character list still misses NBSP and
U+2000–200A, and the meta repair below needs the real extractor, not a third
transliteration of it.

*Recoverability.* The original `text` is gone, but nothing was lost that
matters: SQLite's space-only trim removes a strict subset of what
`strings.TrimSpace` removes, so `TrimSpace(TRIM_sp(x)) == TrimSpace(x)`.
Re-trimming migration 3's output in Go reproduces `SplitTitleBody` exactly.
`TestMigrate4_MatchesSplitTitleBody` asserts that against `SplitTitleBody`
itself as the oracle — including real NBSP and em-space rows — so the two
cannot drift.

*Empty titles.* An empty title is promoted from the body's first line, which is
what the split would have produced had the text not opened with the separator;
`(untitled)` when there is nothing to promote. Justified by the report's own
observation that the row is otherwise unfixable — `add ". body"` errors and
`event title N ''` refuses.

*Truncated meta (the data half of [C4](#c4)).* C4 fixed the extractor going
forward; migration 4 repairs the rows already written. It re-inserts the full
name and deletes the stub only when the stub is **exactly** what the frozen
`legacyMetaNameRe` would have produced from a name the current extractor reads
in full. A blanket prefix rule would destroy a hand-added `#work` sitting beside
a derived `#workflow`; this one provably cannot, since the old ASCII pattern
would have captured `workflow` whole.

*Deliberately not repaired:* the rows [M4](#m4) describes, where the old sigil
boundary minted `people=example` from `bob@example.com`. Those are
indistinguishable from tags someone added on purpose — additive noise, not
corruption.

*FTS.* Rebuilt unconditionally rather than for touched rows only. Migration 3's
rebuild was itself a SQL transliteration of `parse.FTSContent`, so every row it
wrote is suspect; one pass restores the guarantee that the index says exactly
what the Go helper would write, and drops the change-tracking bookkeeping.

*Rider:* `4.sql` is a single `ANALYZE event_meta` — the long-deferred statistics
refresh the closing notes ask to ride along with migration 4, since migration 2
swapped the index without rebuilding `sqlite_stat1`. Verified experimentally
that `ANALYZE` is transaction-safe and writes nothing for an empty table, so a
freshly created database is not saddled with zero-row statistics.

Also fixed here because writing migration tests on top of it would have been
unsound: `internal/db`'s `testDB` helper used bare `:memory:`, where every
pooled connection sees its own empty database — `migrate()` reads
`user_version` on one connection and opens its transaction on another, so it
only ever passed by the accident of the pool reusing a single connection. Now a
temp file, as CLAUDE.md's conventions already required everywhere else.

<a name="h2"></a>
### H2 — O(n²) prefix concatenation in tree rendering

**`internal/render/render.go:149`** (in `renderNode`, lines 128-154)

```go
return renderNode(w, events, byID, children, kidID,
	childPrefix+connector, childPrefix+continuation)
```

Two fresh O(depth)-length strings per node, and every ancestor frame holds its
copy **live** on the recursion stack — live heap at max depth is Σd =
O(depth²). GC can reclaim none of it.

Measured on single-chain DBs (each event parented to the previous):

| chain depth | wall | peak RSS | output |
| --- | --- | --- | --- |
| 2,000 | 0.12 s | 28.9 MB | 6.1 MB |
| 5,000 | 0.70 s | 93.6 MB | 37.7 MB |
| 10,000 | 2.74 s | 304.7 MB | 150.4 MB |
| 50,000 | **67.85 s** | **7,024.8 MB** | ~3.7 GB |

10k→50k is 5× the depth for 24.8× the time and 23× the RSS — textbook
quadratic. The same 50k DB via `--format=flat`: **0.21 s / 26.0 MB**. The tree
path is 323× slower on 270× the memory.

The output is inherently O(depth²) bytes — a tree must print `depth` indent
characters per line — but peak RSS is ~2× the output, and the *allocation* is
what's fixable.

**Fix:** thread a single reusable `[]byte` prefix buffer through `renderNode`
— append the connector before recursing, truncate on return — instead of
concatenating two new strings per node. Live memory becomes O(depth); at depth
50k that's ~7 GB → tens of MB, and the wall time collapses with it (the 67 s
is almost entirely allocation and GC). No change at depth ≤100. Recursion
depth itself is safe: Go grew the stack to 50k frames without incident.

**Resolved.** `renderNode`'s six parameters became a `treeWriter` struct
holding the shared state once, with `prefix []byte` appended to on the way
down and truncated on the way back up. Splitting the old `prefix`/`childPrefix`
pair into one buffer meant moving the connector into the callee's signature:
`node(id, connector, continuation)` writes prefix + connector + event as one
line and then recurses. That is also what makes the orphan marker in
[M8](#m8) fall out for free — it is genuinely just another connector, passed
by the root loop instead of the child loop, with no second drawing path.

Assembling the line in a reusable `line []byte` rather than writing prefix,
connector and text separately keeps each node at exactly one `Write`. On
`io.Discard` that is invisible; on a real file descriptor three syscalls per
node cost 3.37 ms per 2 000 events against 1.83 ms for one.

Allocation now tracks the bytes written rather than the tree's shape. The
guard is `TestTree_DeepChainAllocationIsLinear`, which measures `TotalAlloc`
across an 8× depth increase (500 → 4 000) and fails above 16×; linear predicts
~8×. Reverting to per-node string concatenation puts it at 45.9×, so the test
is load-bearing rather than decorative. `BenchmarkTree_DeepChain` covers the
5 000-deep case for anyone measuring.

<a name="h3"></a>
### H3 — `meta rename` fails with a raw UNIQUE error on its primary use case

**`internal/event/event.go:588-591`**

A plain `UPDATE event_meta SET key=?, value=?` against the
`UNIQUE(key, value, event_id)` index added in migration 2:

```
$ fngr add "alpha task" -m tag=a          # event 1: tag=a
$ fngr add "beta task" -m tag=a -m tag=b  # event 2: tag=a, tag=b
$ fngr meta rename '#a' '#b' -f
fngr: error: update meta: constraint failed: UNIQUE constraint failed:
  event_meta.key, event_meta.value, event_meta.event_id (2067)
$ fngr meta
author=nicolasm  (2)
tag   =a         (2)      <-- nothing renamed, including event 1
tag   =b         (1)
```

Consolidating two tags (`#wip` → `#done`, `#staging` → `#stage`) is the single
most likely reason to run this verb, and it is impossible whenever *any* event
carries both. Because the tx is atomic, non-colliding events aren't renamed
either. Cross-event renames succeed, so the bug stays invisible until a user
happens to have both tags on one event — then it never works again.

Migration 2 added that UNIQUE index specifically so `INSERT … ON CONFLICT DO
NOTHING` would work in `AddTags` and the body-tag sync; `UpdateMeta` never got
the same treatment. The error also leaks table and column names plus the
SQLite errcode.

**Fix:** inside the same tx, `UPDATE OR IGNORE …` followed by
`DELETE FROM event_meta WHERE key=? AND value=?` (rows-affected becomes the
union count) — or pre-delete rows whose event already carries the target
tuple.

**Resolved** as a merge rather than an error, and by one word rather than the
two statements suggested above: `UPDATE OR REPLACE`. The conflict clause
deletes the row standing in the way and completes the update, which *is* the
merge, and `RowsAffected` counts only the rows updated — replaced-away rows
are not counted, which is the honest number for "renamed".

The suggested `OR IGNORE` + `DELETE` pair also works, but it needs a guard
that `OR REPLACE` makes unnecessary: with old and new equal, its delete half
removes exactly the rows the update half just wrote, so `fngr meta rename
'#a' '#a'` would silently strip the tag from every event. SQLite checks
uniqueness against the *other* rows, so under `OR REPLACE` that case is a
plain rewrite-in-place. `TestUpdateMeta_RenameToItself` stays in the suite to
catch a future rewrite that reintroduces the two-statement shape.

`TestUpdateMeta_Merges` fails with the original raw UNIQUE error under a plain
`UPDATE`, and with a leftover `tag=wip` row under a bare `OR IGNORE`, so it
separates all three spellings. `TestKongDispatch_MetaRenameMerges` drives the
same consolidation through Kong.

One thing the fix opens that the report did not raise: a merge *destroys*
rows, and `Renamed N occurrence(s)` reads as if nothing were lost. When the
target entry already exists, the prompt now says so —
`(tag=done already on 8; events with both merge into one)` — so the
confirmation describes what will actually happen. `-f` still skips it, and
`TestMetaRenameCmd_PromptWarnsAboutMerge` covers the three cases (target
exists, target is new, rename to itself).

<a name="h4"></a>
### H4 — `fngr add` hangs forever when stdin is an open, idle pipe

**`cmd/fngr/body.go:35-40,69-75`** (`peekHasData`)

`peekHasData` calls `bufio.Reader.Peek(1)`, which **blocks** on a non-TTY
stdin that is open but idle — it waits for either a byte or EOF, and an
idle-open pipe gives neither. CLAUDE.md correctly documents `peekHasData` as
the fix for the empty-`/dev/null` case in CI; an *inherited open pipe* is a
different shape that still hangs.

```
$ ( sleep 25 ) | fngr add "note text"
STILL RUNNING after 4s -> HANG CONFIRMED
$ fngr add "note text" </dev/null     # control
Added event 1
```

Three separate agents hit this **by accident** during this review — one twice,
one blocking until a 10-minute timeout. Real-world stdin shapes that trigger
it: CI runners, process supervisors, `ssh host fngr add "x"`, editor
integrations, agent harnesses.

**Fix:** skip the peek entirely when `len(args) > 0` — args already determine
the body, and the only reason to peek is to detect the args+stdin conflict,
which is not worth an unbounded block. Same for `useEditor && len(args)==0`.
Alternatively bound the peek with a deadline on the fd.

**Resolved** by deleting `peekHasData` outright. `resolveBody` is now plain
precedence — args > `-e` > TTY > stdin — and reads stdin only in the last
branch, where it is the sole possible body source and blocking to wait for
the body is the whole point. An empty `/dev/null` hits EOF at once and
`readStdin` already reports `event title cannot be empty`, which is the same
message the peek's "non-TTY, no data" branch produced, so the emptiness check
the helper existed to perform was redundant with the read that followed it.

The deadline alternative was not taken: it needs a real `*os.File` (the
`ioStreams.In` seam that makes every command testable would have to leak),
and it trades a hang for an arbitrary timeout on the path where waiting is
correct.

Both conflict errors go with it. Reporting "ambiguous: body via both args and
stdin" costs exactly the unbounded read that caused the hang, so extra stdin
is now silently unread. It is a genuine loss of feedback for a real mistake,
accepted because the check could not be performed safely and the mistake is
self-evident (the event gets the args, verbatim, right there in the output).

`TestResolveBody_NeverTouchesStdinWhenBodyIsDecided` runs each of {args,
args+`-e`, `-e`, TTY} against a reader that never returns from `Read`, and
fails on a 5 s timeout if any of them touches stdin; restoring the peek fails
three of the four. End-to-end, the report's own reproducer now finishes in
10 ms:

```
$ mkfifo f; ( sleep 30 > f & ); time fngr add "note text" < f
Added event 2
0.010 total
```

<a name="h5"></a>
### H5 — `confirm` treats EOF as consent

**`cmd/fngr/prompt.go:17-23`** + **`cmd/fngr/meta.go:86-97`**

`confirm` treats an empty read (EOF) identically to "user pressed Enter" and
returns `defaultVal`. `meta rename` passes `defaultVal=true`, so a
non-interactive invocation without `-f` performs the destructive rename with
no confirmation:

```
$ fngr meta rename '#ops' '#pwned' </dev/null
Rename 1 occurrence(s) of tag=ops to tag=pwned? [Y/n] Renamed 1 occurrence(s)
rc=0
```

`delete` and `meta delete` pass `defaultVal=false` and correctly abort — but
that means the safety of a destructive operation depends on which default the
call site happens to pass. This is **distinct** from the Won't-Fix entry on
`[Y/n]`-vs-`[y/N]` asymmetry: the issue is EOF being read as consent at all.

Related, opposite direction: non-TTY `fngr delete N` with empty stdin prints
the prompt then `Aborted.` at **exit 0** — a scripted delete silently no-ops
with success.

**Fix:** have `confirm` distinguish `io.EOF` from an empty line and return an
error on EOF, requiring `-f` for non-interactive runs. That fixes both
directions at once.

**Resolved** as suggested: `confirm` returns the `errNoAnswer` sentinel when
the read ends in `io.EOF` **and** nothing was typed, so all three call sites
change behaviour from one place and no call site's safety depends on which
default it passes any more.

```
$ fngr meta rename '#ops' '#pwned' </dev/null
Rename 1 occurrence(s) of tag=ops to tag=pwned? [Y/n] fngr: error: no answer on stdin; re-run with --force to skip the prompt
rc=1
```

Both halves of the condition carry weight. `bufio.Reader.ReadString('\n')`
returns `io.EOF` for a final line with no trailing newline too, so keying on
the error alone would reject `printf y | fngr delete 1` — a legitimate
scripted confirmation. `TestConfirm` pins `y`/`n` without a newline next to
the two empty-input rows, and each mutation of the condition fails a
different pair.

`TestKongDispatch_PromptsRefuseEmptyStdin` drives all three prompting verbs
(`delete`, `meta rename`, `meta delete`) through Kong with a non-TTY empty
stdin, asserts `errNoAnswer` from each, and then asserts the tag and the
event are both still there — the refusal has to be a refusal, not just a
different exit code. That test also covers the report's "opposite direction"
note: `fngr delete N </dev/null` used to print `Aborted.` at exit 0, and now
exits 1.

---

<a name="h6"></a>
### H6 — `confirm` still blocks forever on an idle pipe

*Found by the altitude review of the H4+H5 fix, not in the original report.*

H4 removed a blocking stdin read from `resolveBody`. `confirm`
(`cmd/fngr/prompt.go:22`) still does one, and it is the same hazard:

```
$ sleep 30 | fngr meta delete '#wip'
Delete 1 occurrence(s) of tag=wip? [y/N]        # hangs until the writer closes
```

H5 hardened the *EOF-arrives-immediately* half of this helper and left the
*EOF-never-arrives* half untouched, so the command is now safe under
`</dev/null` and still unbounded under a live-but-silent pipe: `docker run -i`
without `-t`, CI runners, process supervisors, `ssh host 'fngr …'` without
`-n`.

The structural reason is that `confirm(in io.Reader, out io.Writer, …)` cannot
see `ioStreams.IsTTY` — the free, non-blocking signal for "is a human here?" —
so H5 had to *infer* absence from the read instead of knowing it beforehand.

**The fix is a real behaviour decision, which is why it is filed rather than
folded into H5.** Passing `ioStreams` and returning `errNoAnswer` before
touching `In` whenever `!IsTTY` eliminates the hang outright and collapses the
sentinel's ambiguity (EOF on a TTY then unambiguously means the human pressed
^D, and can be a clean cancel at exit 0). But it also breaks
`echo y | fngr delete 1` and `yes | fngr …`, which are idiomatic, currently
supported, and pinned by `TestConfirm`. Decide explicitly whether piped
answers stay supported; `-f` already covers the scripted case, which argues
they need not.

Note this is the same capability-vs-content generalisation as [H7](#h7) — but
only [H7](#h7) gets it, because only there is the capability check free of a
behaviour loss.

**Resolution: won't fix.** Piped answers stay supported. `echo y | fngr
delete 1` and `yes | fngr …` are idiomatic and in use; breaking them to
foreclose a hang that needs a pipe nobody ever writes to is the worse trade.
A prompt waiting for an answer is a prompt doing its job — unlike H4, where
`fngr add "note"` had no reason to consult stdin at all. That asymmetry is
the whole distinction: H4 read stdin to answer a question it did not need to
ask; `confirm` reads it because the answer is the point.

`-f` remains the reliable form for any unattended run, and both the Docker
limitations list and the troubleshooting entry in the README now say the
prompt will wait indefinitely rather than implying it always errors out.

<a name="h7"></a>
### H7 — `-e` under a non-TTY launches an editor that cannot run

*Found by the altitude review of the H4+H5 fix. Introduced by that fix.*

H4 deleted both conflict checks along with the peek. Dropping the args+stdin
one is right: telling a piped body apart from cron's `/dev/null` genuinely
requires consuming stdin, and accepting silently-unread stdin is the correct
trade. The `-e`+stdin one was collateral — `-e` asks for an interactive
editor, and `io.IsTTY` (already computed in `main.go`) says whether one is
possible, at no cost and with no read.

```
$ echo "piped body that matters" | VISUAL= EDITOR=true fngr add -e
cancelled (empty body)     # exit 0, no event, body gone
```

With a real terminal editor it is louder but still wrong: `realLaunchEditor`
(`cmd/fngr/body.go`) sets `cmd.Stdin = os.Stdin`, handing vim the pipe.

Fix: key the error on capability rather than content — `useEditor && !IsTTY`
→ `--edit needs a terminal`. No read, no hang, and it additionally catches the
pre-existing `fngr add -e </dev/null` case that the old content-based check
never did. Costs one branch and flips the `-e`/non-TTY rows in
`TestResolveBody` and the spec's resolution table from "editor opened empty"
to an error.

Severity is High for the silent-data-loss shape, but the blast radius is
narrow: it needs `-e` *and* a pipe *and* a non-interactive `$EDITOR`.

**Resolution: fixed.** `resolveBody` now leads with
`useEditor && !io.IsTTY` → `--edit needs a terminal; stdin is not a TTY`.
It is the first branch precisely so that `fngr add foo -e </dev/null` is
caught too — `-e` cannot run anywhere without a terminal, args or no args.
The check reads nothing, so it does not walk back [H4](#h4); the guard test
proves that by running the error path against a reader that fails on any
read.

```
$ echo "piped body that matters" | VISUAL= EDITOR=true fngr add -e
fngr: error: --edit needs a terminal; stdin is not a TTY
rc=1
```

With the guard in place `-e` implies a TTY, so the old
`case useEditor, io.IsTTY` collapses to `case io.IsTTY`.

Four test sites: two rows in `TestResolveBody_NeverTouchesStdinWhenBodyIsDecided`
(with and without args, both against `forbiddenReader`, so the refusal is
proven to land before anything consults stdin), the `args-editor-no-terminal`
row in `TestResolveBody`, `TestAddCmd_EditFlagRequiresTerminal` (asserts no
event was written and the stub editor was never called), and
`TestKongDispatch_EditFlagRequiresTerminal` through Kong. The spec's
resolution table gains the error as its first row.

---

## Medium

<a name="m1"></a>
### M1 — Body-tag sync silently deletes operator-added meta

**`internal/event/event.go:251-255,283-286`**

`Update` unconditionally deletes `parse.BodyTags(oldTitle + " " + oldBody)`.
It cannot distinguish a tuple derived from a body mention from one added
explicitly via `--meta` or `event tag`; both live in the same
`(people|tag, value)` namespace.

```
$ fngr add "plain note"                      # id 3
$ fngr event tag 3 @bob                      -> Tagged event 3 (1 added)
   Meta: author=tester, people=bob
$ fngr event body 3 "now mentions @bob inline"
$ fngr event body 3 "no more mention"
   Meta: author=tester            # people=bob is gone
```

Untrusted content routinely contains `@handle`, so an imported note mentioning
`@bob` is enough to arm this.

*Note:* an earlier hypothesis that merely editing a **title** destroys manual
tags was tested and **disproved** — `BodyTags` on the old text finds nothing
to delete, so manual tags survive. The bug requires the token to appear in the
text at some point.

**Fix:** compute the delete set as `BodyTags(old) \ BodyTags(new)` — a small
change with no migration that fixes the common case. The thorough version is a
`source` column on `event_meta` (`'body'` vs `'explicit'`) scoping the delete.

**Resolved.** The `source` column, because the small change turned out to fix
nothing at all. `BodyTags(old) \ BodyTags(new)` was implemented first and the
repro above still reproduced: the delete set it narrows away is exactly the
set the *next* statement re-inserts, so delete-all-then-insert-new and
delete-the-delta-then-insert-new leave the database in the same state. The
delta is not wrong — it stops the sync churning rows pointlessly — but it is
unobservable on its own, and only provenance can tell `people=bob` from
`people=bob`.

Migration 5 adds `event_meta.source TEXT NOT NULL DEFAULT 'explicit' CHECK
(source IN ('body', 'explicit'))`:

- `addInTx` stamps `'body'` on any tuple `parse.BodyTags(parse.EventText(title,
  body))` yields, re-deriving them because `AddInput.Meta` arrives already
  merged.
- `Update` deletes only the removed tags whose row says `'body'` and re-inserts
  the new set as `'body'` with `ON CONFLICT DO NOTHING`, so an explicit row
  keeps its provenance. The `source` filter is what carries the semantics; the
  delta against the old text is an optimisation on top, which is why the delta
  alone changed nothing.
- `AddTags` inserts as `'explicit'` and *promotes* an existing `'body'` row —
  `event tag @bob` on a body that already says `@bob` is still the operator
  claiming the tag. Its "N added" count moved to a pre-read, since
  `RowsAffected` counts that promotion as a change. The `DO UPDATE` is guarded
  by `WHERE event_meta.source <> excluded.source` so it fires only for the
  promotion it exists for.
- `UpdateMeta` stamps `'explicit'` on the renamed row: no event's text yields
  the new value, so a body-derived one would be deleted by the next edit.

The `CHECK` pins the enum in the schema, not only in Go: both values are
written as bare string literals from Go *and* from this migration's SQL, and a
typo would mint a row `deleteBodyMetaTuples` could never match, silently and
forever. There is deliberately **no index** on `source` — every statement that
filters on it also supplies `(key, value, event_id)`, which migration 2's
unique index already covers. Measured on a 300k-row database, adding one cost
+24% file size and +17% insert time for zero appearances in any query plan.

The Go step `classifyMetaSource` back-fills existing databases by demoting the
rows an event's own text still yields. It is a reconstruction — provenance was
never recorded — and it is lossy in exactly one place: a tuple added *both*
ways is a single row and comes out `'body'`. That matches how `addInTx`
resolves the same tie for a new event, and re-asserting with `event tag`
promotes such a row back to `'explicit'`.

Text-wins is the deliberate choice over carrying the merged partition down from
`MergeMeta` (which would resolve the tie as `'explicit'` and skip the
re-derivation). Carrying it would freeze every body tag as explicit on a
`fngr --format=json | fngr add -f json` round trip — the JSON wire shape has
one flat `meta` list and no provenance field — so the common path would go
permanently stale to fix the rare `-m people=bob` beside an inline `@bob`.

`parse.EventText(title, body)` is now the single title+body join contract: the
add path, `Update`'s sync and migration 5's back-fill all classify tuples as
body-derived, and a join differing by so much as a space would have them
disagree about a tag near the boundary — one stamping `'body'`, another
deleting it.

Guarded by `TestUpdate_RetractsOnlyBodyDerivedTags` (four claim paths including
the report's exact ordering, one of them the baseline where the tag *should*
go), `TestAddTags_CountsPromotionAsAlreadyPresent`,
`TestKongDispatch_TagSurvivesBodyEdit` through Kong, `TestEventText`, and three
migration tests including `TestMigrate5_RejectsUnknownSource` for the `CHECK`.
Each was mutation-checked against the fix removed.

*Note for [M7](#m7):* the `events_fts` split is now migration 6.

<a name="m2"></a>
### M2 — Parent cycle: two non-terminating loops and a silent data blackout

**`internal/event/event.go:318-336`** (`Reparent` ancestry walk),
**`:878`** (`GetSubtree` CTE), **`internal/render/render.go:96-126`** (`Tree`)

With `events(1→parent 2, 2→parent 1)`:

- `fngr` (default tree): prints **nothing**, exit 0. `Tree` finds no roots and
  every event vanishes — a silent total data blackout with no error.
- `fngr event 1 -t`: the recursive CTE never terminates. Still spinning after
  12 s, 0 bytes output, must be killed.
- `fngr event attach 3 1` (target outside the cycle): the ancestry walk loops
  forever at 172% CPU **inside an open transaction**.

`Reparent`'s cycle check is the only guard and is not enforced at the schema
level. A concurrent-`Reparent` TOCTOU race was attempted (60 iterations of
simultaneous `attach 1 2` / `attach 2 1`) — **no cycle created**; SQLite write
serialization holds. So this needs an externally-supplied or corrupted DB.

**This partially reverses a prior Won't-Fix.** The 2026-04-23 entry declining
a `GetSubtree` depth cap reasoned "no untrusted input path." There is one:
`db.ResolvePath` (`internal/db/db.go:18`) silently uses `.fngr.db` from the
current directory, so `cd` into an attacker-supplied tree (extracted tarball,
cloned repo) and every `fngr` invocation reads their database. Manual
`sqlite3` edits and partial-write corruption are the other routes. Note also
that a depth cap is the *wrong* fix — the failure is a non-terminating loop,
not deep recursion.

*Nuance worth recording:* two reviewers reached opposite conclusions on
`Reparent`. One found the walk exits cleanly on a pre-existing cycle ("exits as
soon as it sees `id`"); the other found it spinning at 172% CPU. Both are
likely right — it depends on whether the walk **starts inside** the cycle
(terminates on the `id` check) or **upstream of it** (`attach 3 1` walks into
the cycle and never sees `3` again). The upstream case is the dangerous one.

**Fix:** bound the `Reparent` walk with a visited-set or an iteration cap at
the event count, returning a "corrupt parent chain" error; add a depth column
+ `WHERE depth < N` to the `GetSubtree` CTE; and in `render.Tree`, when
`roots` is empty but `events` is not, promote all events to roots so data can
never silently disappear.

**Resolved,** all three, behind a new `ErrCorruptTree` sentinel kept distinct
from `ErrCycle`: one says the operation was refused, the other says the file
was already broken before anyone asked for anything.

- **`Reparent`** carries a `seen` set, *seeded with the starting node* so the
  one-event self-parent cycle is caught on its first hop. The nuance recorded
  above is exactly right and is now written into the code comment, because the
  walk reads as safe until you notice that the `parent == id` check only fires
  for a cycle the moving event is *in*.
- **`GetSubtree`** switches its recursive term from `UNION ALL` to `UNION`,
  which is SQLite's own answer to recursion over a cyclic graph: a row already
  in the result is discarded, so a second lap adds nothing and the queue
  drains. On a well-formed tree the two are identical — `id` is the primary
  key, so no two rows can collide — and the dedup index costs ~15% (0.175 s vs
  0.152 s over a 200 000-event subtree).

  The report's suggested `WHERE depth < N` was implemented first and then
  replaced. It works, but it terminates by *exhaustion* rather than by
  noticing the loop: measured on a 5 000-row table with a 3-cycle and 300
  events hanging below it, the bounded `UNION ALL` materialises 505 104 rows
  where `UNION` returns 303 — and every one of those rows becomes a Go struct
  and goes through `loadMetaBatch` before anything checks for the cycle. The
  cost curve is O(N × subtree), the same shape as the bug. It also needed a
  second, unsynchronised `SELECT COUNT(*)`, whose stale-low result would
  silently truncate a legitimate deep subtree — the one outcome the fix was
  supposed to make impossible.

  So the report is right that "a depth cap is the wrong fix", and more broadly
  right than first credited: with `UNION` there is no cap to get wrong.
  Detection is separate and exact. A cycle is reachable only when the queried
  root is itself a member — every member's parent is another member, so no
  descent from outside can enter one — which reduces the whole test to whether
  the root's own parent came back among its descendants. (The first draft of
  the test got this wrong and built an unreachable case.) Terminating is not
  answering: a "subtree" of a cyclic graph is not a well-defined thing, so it
  returns `ErrCorruptTree` rather than a plausible deduped loop.
- **`render.Tree`** no longer has a root-less blackout. `treeWriter.visited`
  skips a node already drawn — impossible in a real tree, one parent each —
  so the recursion cannot follow a cycle round forever, and a sweep over
  `events` after the root walk draws anything it never reached as an orphan.
  That is the same claim the orphan marker already makes ("this has a parent
  you cannot see from here") rather than a special case for `len(roots) == 0`.
  `visited` is a `[]bool` indexed through the existing `byID`, not a map: a map
  put 148 KB/op back on the 5 000-deep benchmark that [H2](#h2) exists to hold
  down, and the slice gives that back (1 490 469 B/op against a pre-fix
  1 485 048).

The one thing the report did not ask for and the fix needs: a way out.
`withRepairHint` in `cmd/fngr/event.go` — mirroring the existing
`withGrammarHint` — appends `fngr event detach <id>` to any `ErrCorruptTree`.
Every such message names an event on the loop, and detaching one breaks it;
`Reparent`'s nil branch clears `parent_id` without walking anything, so the
repair works on exactly the database the traversal could not survive. Without
it the user is left holding a journal they cannot read and no next move. No
`fngr doctor` command: `detach` already is one.

Guarded by `TestReparent_CorruptAncestryTerminates` (two shapes, both under a
deadline context so a regression reports the wrong error in seconds instead of
hanging the package for ten minutes), `TestGetSubtree_CorruptChainTerminates`
(three shapes), `TestGetSubtree_DeepChainIsNotTruncated` (a whole table in one
chain — the deepest recursion the row count allows),
`TestTree_CycleStillRendersEveryEvent`, `TestTree_CycleBesideRealRoots`,
`TestTree_CycleWriteError`, and `TestKongDispatch_CorruptParentChain`, which
reproduces all three original symptoms end to end and asserts the hint reaches
the user.

<a name="m3"></a>
### M3 — Nothing enforces a single `author`

**`internal/event/meta.go:34`** (`CollectMeta` unconditionally adds `$USER`),
**`internal/event/event.go:571,615`** (`wellKnownMetaKeys` checked only against
`oldKey`, never the rename *target*), **`:361`** (`AddTags` has no guard),
**`internal/render/render.go:47`** (`eventAuthor` returns the first match in
`ORDER BY key, value`).

```
$ fngr add 'note one' -m author=aaa
$ sqlite3 t.db 'select key,value from event_meta where event_id=1'
author|aaa
author|nicolasm            <-- two authors, silently
$ fngr --format=flat
1   2.09pm  aaa  note one  <-- 'aaa' wins because it sorts first

$ fngr event tag 1 author=zzz
Tagged event 1 (1 added)   <-- third author, no guard

$ fngr meta rename 'k=v' 'author=evil' -f
Renamed 1 occurrence(s)    <-- guard inspects only oldKey
```

The guard is also inconsistent in the other direction: `fngr meta delete
author=x` is refused, but `fngr event untag 1 author=nicolasm` succeeds and
leaves a blank author column in `flat`/`csv`.

**Fix:** treat `author` as single-valued — `CollectMeta` should skip its
default when an explicit `author` is present (mirroring what
`mergeMetaForJSON` already does correctly), and `AddTags`/`UpdateMeta` should
reject `author` as a *target* key. Pick one behavior for `event untag author=…`
and apply it consistently.

**Resolved.** Closed at both ends.

At creation: `CollectMeta`'s merge moved into an exported `MergeMeta(text,
explicit, defaultAuthor)`, where an explicit `author` *replaces* the default
instead of joining it, and two differing explicit authors are an error rather
than a merge. `mergeMetaForJSON` — the private near-copy in
`cmd/fngr/add_json.go` that got this right for the JSON path only — is gone;
both paths now call the one function.

An **empty** explicit author is rejected there too, rather than ignored. `-m
author=` used to do two wrong things at once: the blank value suppressed the
synthesised default *and* was then appended by the explicit pass, so the event
stored a blank author and lost the real one — permanently, since `author` is
protected against every meta verb.

At the writer: `requireOneAuthor` runs inside `addInTx` on the already-merged
slice. `MergeMeta` is the only *CLI* path to the database, but it is not the
only path — a directly-built `AddInput` (a library caller, a future importer)
bypasses it entirely, and the data layer should not refuse to repair an
invariant it does not also enforce. `AuthorOf(meta)` is the matching single
reading of that rule, now called by `render.eventAuthor` and by the JSON
import's "every record needs an author" check, so the two cannot drift on what
happens when the guarantee is somehow broken.

At mutation: `wellKnownMetaKeys` became `protectedMetaKeys`, applied by every
meta verb as a *target* — `AddTags`, `RemoveTags`, `DeleteMeta`, and
`UpdateMeta` on both the source key and the destination key. That settles the
inconsistency the report names by picking the refusing side for all of them:
an event with no author renders a blank column everywhere, so `event untag 1
author=…` now errors like `meta delete author=…` always did.

`UpdateMeta`'s gate (`requireRenamableMeta`) is looser by exactly one case: a
*same-key* value rewrite is allowed. `fngr meta rename author=nicolass
author=nicolas` leaves every affected event with the single author row it
already had, and refusing it would make a typo in `--author` unfixable through
the CLI. Any rename that changes the key is still refused at both ends, as is
an empty new value.

**No migration for existing duplicates, deliberately.** A migration cannot
tell which of two `author` rows was auto-injected and which the operator
asked for, so dropping one would be a coin flip on which name the journal
attributes an entry to — worse than showing both. Every path that could
create the second row is closed, so a database only carries duplicates if it
was written by ≤ v0.0.2 or edited by hand; `fngr meta -S author` lists them
and `sqlite3` removes them.

Guarded by `TestMergeMeta_Author` (seven cases, including the two-explicit-
authors pair that slipped past a first draft comparing against the default
instead of tracking "an explicit one was seen", and the blank-author row),
`TestProtectedMetaKey_RefusedByEveryVerb` (six verbs, each asserting exactly
one unchanged author survives), `TestAdd_RejectsBadAuthorMeta` (the writer
gate, asserting no event is created either), `TestAuthorOf`,
`TestUpdateMeta_AuthorValueIsCorrectable` (the permitted rewrite plus the
three still-blocked renames), three new `TestJSONInputToAddInput` rows, and
`TestKongDispatch_AuthorIsNotMutable` / `TestKongDispatch_AuthorValueIs
Correctable` through Kong.

<a name="m4"></a>
### M4 — Email addresses mint bogus `people` tags

**`internal/parse/parse.go:23`** — `@([\w][\w/\-]*)` has no left boundary
assertion, so it fires mid-token:

```
$ fngr add 'emailed bob@example.com about the thing'
$ fngr meta
people=example       (1)     <-- a person named "example"
```

Same function mines `#` out of URL fragments and hex colors:

```
$ fngr add 'see https://docs.example.com/guide#installation'  -> tag=installation
$ fngr add 'set the button color to #ff8800 today'            -> tag=ff8800
$ fngr add 'fixed issue #1234 for release'                    -> tag=1234
```

The `#1234` case is arguably intended; `#ff8800` and the email case are not.
These are permanent — they land in `event_meta`, in `events_fts`, and in
`fngr meta`.

**Fix:** require a word boundary before the sigil — e.g. skip `@` matches whose
preceding rune is `\w` or `.`. Combine with [C4](#c4), since both edit the same
pattern.

**Resolved.** Shipped with [C4](#c4). The two body-tag patterns are now
prefixed with `metaNameBoundary` = `(?:^|[^\p{L}\p{N}_])` — start of text, or
any rune that cannot be part of a name. The group is non-capturing, so `m[1]`
is still the name and no index shifts. `bob@example.com` and
`.../guide#installation` mint nothing; `(@alice)`, a leading `@alice`, and
`@alice` after a newline all still match.

Two deliberate non-changes: `.` was **not** added to the disallowed preceding
set, and `#ff8800` / `#1234` still mint tags. A hex colour is
indistinguishable from a legitimate short tag without a heuristic that would
also eat real tags, and the review already calls `#1234` arguably intended.

One behaviour change worth knowing: `#a#b` now yields only `tag=a`, because the
second `#` is preceded by a letter. Covered by a test so it stays deliberate.

<a name="m5"></a>
### M5 — Two arithmetic defects in relative-time parsing

**`internal/timefmt/timefmt.go:201-202`** — `now.AddDate(0, -n, 0)` relies on
Go's day normalization, so Feb 31 rolls forward to Mar 3:

```
now=2026-03-31  "1 month ago"   -> 2026-03-03    <-- 28 days back, still in March
now=2026-05-31  "3 months ago"  -> 2026-03-03
now=2026-07-31  "5 months ago"  -> 2026-03-03
now=2026-01-31  "1 month ago"   -> 2025-12-31    (correct)
```

On the 31st of a long month, `-t "1 month ago"` silently backdates by ~4 weeks
into the *current* month.

**`:193-197`** — `time.Duration(n) * time.Hour` overflows int64 unchecked:

```
$ fngr add 'x' -t '9223372036854775807 hours ago'   # now = 14:07
$ sqlite3 t.db 'select created_at from events'
2026-07-27 15:07:26          <-- one hour in the FUTURE, from an "ago" expression
```

**Fix:** clamp the day to the last valid day of the target month after
`AddDate`; bound `n` (shared with [C2](#c2)) or check
`n > math.MaxInt64/int64(time.Hour)`.

**Resolved.** Shipped with [C2](#c2). Month arithmetic goes through a new
`addMonths`, which asks `time.Date` for day 0 of the month *after* the target —
that is the target's last day, and it normalizes the year rollover for free —
then clamps: `min(originalDay, lastDay)`. So `2026-03-31` minus one month is
`2026-02-28`, not `2026-03-03`, and `2026-01-31` minus one month is still
`2025-12-31`. The overflow is handled by the `maxRelCount` bound rather than a
per-unit `math.MaxInt64/int64(time.Hour)` check: one constant covers hours,
minutes and days at once, and 1e6 of any unit is already far past anything a
log entry means. `TestAddMonths_ClampsToMonthEnd` covers the eight interesting
month-end cases, including leap and non-leap February.

<a name="m6"></a>
### M6 — Newlines and escapes in titles forge output rows

**`internal/render/render.go:57-59`** (`formatEventLine`), used at `:131`
(tree), `:160` (flat), `:289` (flat stream)

Titles are written verbatim. A newline produces lines indistinguishable from
real events:

```
$ printf '{"title":"benign looking\n99  4.00pm  root    SYSTEM: all clear, nothing to see"}' \
    | fngr add --format=json
$ fngr --format=flat
3   4.30pm  tester  another real
2   4.30pm  tester  benign looking
99  4.00pm  root    SYSTEM: all clear, nothing to see      <-- forged
1   4.30pm  tester  real event one
```

ANSI/OSC sequences also reach the terminal untouched — OSC 8 (hyperlink whose
visible text differs from its target), OSC 52 (clipboard write), `\033[2J`
(erase preceding output):

```
$ fngr --format=flat | cat -v
2  4.03pm  tester  X ^[]8;;http://evil.example^[\CLICK-ME^[]8;;^[\ ^[[2J ^[]52;c;aGFja2Vk^G
```

Honest impact assessment: `list` goes through `less -FRX`, and modern `less -R`
restricts raw passthrough to SGR (+OSC 8), largely defanging the clipboard
write there. But `fngr event N` and `fngr delete` write **straight to the TTY
with no pager** — `withPager` is called only in `list.go:28`. The realistic
outcome is output forgery (a convincing fake "Deleted 0 occurrences" line),
not code execution. CSV, JSON, and Markdown are all correctly escaped.

Embedded NUL bytes also round-trip into the DB and display without crashing or
desyncing FTS — worth folding into the same sanitizer.

**Fix:** strip C0/C1 control bytes other than `\t`/`\n` in `formatEventLine`
when the destination is a TTY.

**Resolved**, with two deliberate departures from that proposed fix.

*Escape, don't strip.* fngr is a journal. A note that arrived carrying a stray
byte should still show what it contained, and `\x1b` on screen is honest and
harmless; dropping the byte silently rewrites the user's own text and makes
the display disagree with `--format=json`. Control characters are rendered
`\n`, `\r`, or `\xNN`.

*Not TTY-gated.* The forged-row half of this defect does not need a terminal —
piping `fngr --format=flat` into a script or a log delivers exactly the same
fake row. Gating on `IsTTY` would also mean plumbing it from `ioStreams` into
`render`, which currently takes nothing but an `io.Writer`. Sanitizing
unconditionally in the human formats is both simpler and more correct.

The new `internal/render/sanitize.go` exposes `SanitizeLine` (escapes
newlines too — for the contexts where one event is exactly one line) and
`sanitizeBlock` (keeps them — for the body block of `fngr event N`). Tab
always survives: it is whitespace, it cannot introduce an escape sequence, and
bodies legitimately contain it. C1 (U+0080–U+009F) is escaped alongside C0 and
DEL, because a bare U+009B is a CSI introducer on some terminals. Clean text —
nearly everything — is returned unchanged, asserted at zero allocations.

*The walk is byte-oriented.* The obvious `for _, r := range s` decodes a raw
0x9b to `utf8.RuneError`, so the 8-bit CSI — the exact byte the C1 range
exists to catch — would pass through untouched, and every other malformed byte
would be silently rewritten to U+FFFD, which is the same silent rewriting this
fix set out to avoid. That byte is reachable in practice: Kong replaces
invalid UTF-8 in argv, but a pipe does not, and SQLite stores and returns the
bytes verbatim. `printf 'piped note \23331m tail' | fngr add` is the
reproduction, and `TestKongDispatch_RawC1FromStdin` is the guard.

Wiring is in `formatEventLine`, so tree, flat and both streams cannot forget;
plus `Event`, per line in `Markdown`, and — the fifth human-facing path, found
only when this fix was reviewed — `cmd/fngr/meta.go`, which lays out its own
columns and has no pager in front of it. There the escaping has to happen
*before* the widths are measured, or padding computed from the raw string
lands in the wrong place on the escaped one.

JSON and CSV stay unsanitized, for two different reasons that the first draft
of this fix wrongly collapsed into one. JSON escapes control bytes itself and
losslessly, so a second pass would only break the
`fngr --format=json | fngr add --format=json` round trip. CSV does **not**
escape them — `csv.Writer` quotes for structural safety and nothing else, so
an ESC goes out raw and `less -FRX` will pass it to the terminal. That is
accepted rather than fixed: CSV is a data export with no import path, and
escaping there would be lossy, leaving a reader unable to tell a stored
`\x1b` from an escaped one. Fidelity wins in the machine-readable formats,
safety in the human-readable ones. `TestJSONCSV_KeepFullFidelity` asserts both
halves, so the CSV trade stays a decision rather than an oversight.

One correction to the finding above: `fngr delete` prints no stored text at
all — `delete.go:32,34` interpolate only the event id — so the pager-less
surface is `fngr event N`, `fngr meta`, and anything piped anywhere.

<a name="m7"></a>
### M7 — FTS conflates content with metadata

**`internal/parse/parse.go:FTSContent`** + **`internal/event/filter.go`**

Meta is flattened into the same FTS column as title and body as `key=value`
tokens, and `-S key=value` / `#tag` / `@person` match that literal string.
Content containing the literal text is indistinguishable from a real tag:

```
$ fngr add "real note" -m secret=classified                                # id 1
$ fngr add "fake note mentioning secret=classified and tag=ops literally"  # id 2
$ fngr -S 'secret=classified'  ->  1 AND 2      # 2 has no such meta
$ fngr -S '#ops'               ->  2            # nothing is tagged ops
$ fngr meta                    ->  author=tester (2), secret=classified (1)   # ground truth
```

A crafted import can inject itself into any tag view, or (with `!`) evade one.
`fngr meta` and `fngr event N` stay honest, so this is search-integrity only.

**Fix:** give `events_fts` a second column (`content`, `meta`) and have
shorthand / `key=value` terms use an FTS5 column filter `meta:"tag=ops"` while
bare words stay on `content`. Needs a migration — schedule it rather than
rushing it.

**Resolved** as proposed. `parse.FTSColumns` now returns both column values from
one function, so a caller cannot write one and forget the other. The pre-split
one-column formula did not stay in `parse` as a second export: migration 4 still
indexes a database that has not reached the split, so the formula is frozen as
`db.legacyFTSContent` beside that migration, the way `legacyMetaNameRe` already
is. A virtual table takes no
`ALTER TABLE ADD COLUMN`, so `migrations/6.sql` drops and recreates `events_fts`
and `migrate6.go::splitFTSIndex` re-populates it — a Go step rather than SQL for
the reason [migration 4](#h1) exists at all. `6.sql` does not recreate
`trg_events_fts_delete`: that trigger is `AFTER DELETE ON events`, not on the
index, so the drop leaves it alone and its body still matches the new table
(`TestMigrate6_DeleteTriggerSurvivesTheDrop` pins it, since getting it wrong
would leave every deleted event searchable).

Routing in `ftsTerm` turns on the *key* half, and asks the same question the
write path asks: `@person` / `#tag` go to `meta:` through `parse.MetaArg`, and
so does any term that splits on `=` into a non-empty key, because non-empty is
precisely what `parse.MetaArg` / `parse.FlagMeta` require of a key. A narrower
test here — `parse.MetaNameRe`, the first thing tried — routed
`-S 'ticket.id=PROJ-42'` to `content:` for a key `-m ticket.id=PROJ-42` stores
without complaint, so the tag was findable by nothing at all. Only `=oops` and
terms with no `=` are content. The value half is not checked on purpose, so
`-S 'tag=*'` (text `tag=` plus a prefix star) remains the "everything tagged"
query it looks like.

The cost of the scoping, accepted: a body that *quotes* a `key=value` string is
no longer reachable by searching for that string. Making it reachable is the
whole bug. The words around it still match.

The four queries in the reproduction above now answer 1, nothing, 2 and 2
respectively.

<a name="m8"></a>
### M8 — `--limit` on tree format promotes orphans to roots

```
$ fngr -n 2
7   4.23pm  sarah  sarah note
5   4.22pm  nico  postmortem scheduled     <-- flush left
$ fngr --format=csv | cut -d, -f1,2
id,parent_id
7,
5,4          <-- event 5 is a child of 4
```

Event 5's parent fell outside the limit window, so it is silently promoted to
a root. No marker, no stderr note. The default command + the default format +
the most obvious flag combine to misrepresent the data.

**Fix:** in `render.Tree`, detect nodes whose `parent_id` is non-null but
absent from the set and render them with a distinguishing prefix (e.g.
`⋯└─`), or emit a one-line stderr note. Pulling in missing ancestors would
inflate the result past `--limit`, so marking is the honest cheap fix. ~15 LoC.

**Resolved** by the marker, not the stderr note: the marker sits on the row it
describes, survives redirection, and needs no second stream. No bookkeeping is
needed to find the orphans — a root that still carries a non-null `parent_id`
is one whose parent fell outside the set, since nothing else can put it in the
root list — so the root loop passes `⋯└─ ` as that node's connector. The connector is four display columns to the tree's three, and
`orphanBlank` matches at four, so an orphan's own descendants stay aligned
under it — `TestTree_OrphanSubtreeAligns` covers that, and
`TestTree_RootAfterOrphanResetsPrefix` covers the buffer truncation between
roots that the [H2](#h2) rewrite made necessary.

`TestTree_OrphanedChildren` previously asserted the flush-left output; that
expectation *was* the bug, so it was updated rather than worked around.

<a name="m9"></a>
### M9 — `fngr meta` output amplification

**`cmd/fngr/meta.go:45-56`** — column width is `max(len(value))` over all rows
and is applied via `%-*s` to *every* row. One large meta value (importable via
`--format=json`, content-controlled) pads all other rows to that width:

```
$ # 200 events with tiny meta + 1 event with a 1 MB meta value
$ fngr meta | wc -c
202002628                      # 202 MB from ~1 MB stored — ~200x
```

**Fix:** clamp the padding width (e.g. `min(maxVal, 60)`) and truncate long
values with an ellipsis. Also switch to rune/display width rather than `len()`
bytes, which currently mis-aligns multibyte values.

**Resolved** — `cmd/fngr/meta.go` now runs every cell through `displayCell`
before measuring it: escaped, then clamped to `maxMetaCell` (60) runes with the
last spent on an ellipsis where it cut. The clamp is a constant rather than a
function of the data because meta is content-controlled — body tags, `--meta`,
`--format=json` — so `min(maxVal, 60)` and a hard 60 differ only in whether the
bound is a hope. `fngr -S key=value --format=json` prints a clamped value in
full; the README says so.

The output amplification turned out to be only half of it. `render.SanitizeLine`
walks every byte it is handed and allocates a copy of the lot when anything
needs escaping, so a 1 MB value cost ~2 ms and ~1 MB *per row* to print sixty
characters. `displayCell` therefore cuts the raw value to `maxMetaCell+1` runes
before escaping it — 1 MB clean goes 2.09 ms → 260 ns, and a value with one
`\x1b` in it drops 1,056,842 B of allocation to 144 B, with no regression on
short cells. One rune past the cap is what makes the shortcut free of
consequence: escaping never shrinks a string, so a prefix that long still
sanitizes past the cap and still gets clamped, and escaping is per-rune, so the
runes that survive are the same either way. `TestDisplayCell_PreCutMatchesTheFullWalk`
pins that equivalence against the unshortened composition.

The alignment half went the other way from what the finding assumed: `fmt`'s
`%-*s` pads to a width in **runes**, so measuring the cells with `len()` was
*over*-padding a multibyte cell and stepping every row below it to the right,
not left. The fix is one call — `utf8.RuneCountInString` where `len` was — and
no width arithmetic at the format call at all. Rune count is still not terminal
cells: a double-width CJK value pads short, which needs a width table nothing
else in fngr wants and leaves a ragged column at worst, where the unclamped
width was a 200x amplification.

<a name="m10"></a>
### M10 — The `". "` split eats abbreviations

```
$ fngr --format=csv | cut -d, -f5,6
title,body
Dr,Smith called about the thing    <-- "Dr. Smith called about the thing"
e.g,this is a note                 <-- "e.g. this is a note"
a,b. c. d                          <-- "a. b. c. d"
released v1.2,shipped to prod      <-- (correct)
trailing period.,                  <-- (correct)
```

Any abbreviation, initial, or honorific in the first few words silently becomes
the title. Decimals survive (`v1.2` has no space after the dot), which is what
the design targeted; the abbreviation case wasn't considered. There is no
escape hatch at add time — no flag, no quoting convention. The only workaround
is a second command, `fngr event title 2 "e.g. this is a note"`, which *does*
preserve `". "`.

**Recommendation:** document it plus the `event title` workaround rather than
adding abbreviation heuristics. A `--title`/`--body` pair on `add` is the
clean alternative if the friction proves real in daily use.

<a name="m11"></a>
### M11 — `fngr add` never creates a project-local `.fngr.db`

README documents resolution as `--db`/`$FNGR_DB` > `.fngr.db` in cwd >
`~/.fngr.db`, and separately says *"The database is created automatically on
the first `fngr add`."* Both are true in isolation and misleading together:
the cwd rung is consulted only if the file **already exists**, so the first
`fngr add` in a project directory silently writes to `~/.fngr.db`.

```
$ cd /tmp/fresh-project && fngr add "project note"   # no local .fngr.db
$ ls -a          # . ..    — nothing created locally
                 # the event went to ~/.fngr.db
```

`touch .fngr.db` first works (a 0-byte file is a valid empty SQLite DB and
migrates cleanly), so the capability exists — it's just undiscoverable.

**Two of the four reviewers polluted the real `~/.fngr.db` this way** despite
explicit instructions to work only in `/tmp`. Both cleaned up; the database was
verified clean afterward (17 events, max id 21, no orphaned meta, FTS in sync,
`integrity_check` = `ok`). That two independent agents tripped the same trap in
one afternoon is the strongest possible evidence that the failure is too
silent.

With `$HOME` unreachable it doesn't fall back or explain — it leaks SQLite:
`error: cannot set pragma foreign_keys: unable to open database file (14)`.

**Fix:** document the `touch .fngr.db` idiom in the "Database location"
section — one line. Optionally add `fngr init` or a `--here` flag, though that
is new surface area.

<a name="m12"></a>
### M12 — `--from`/`--to` reject relative forms *and* fngr's own timestamps

**`internal/timefmt/timefmt.go:255`** (`ParseDate`)

```
$ fngr --from yesterday
fngr: error: --from: unrecognized date "yesterday" (expected YYYY-MM-DD)
$ fngr --from 2026-07-27T00:00
fngr: error: --from: unrecognized date "2026-07-27T00:00" (expected YYYY-MM-DD)
```

Not just relative forms — the range flags also reject the ISO datetime the
tool itself emits in `--format=csv`/`json`. You cannot paste a `created_at`
value from fngr's own output back into `--from`. An inverted range
(`--from 2026-07-27 --to 2026-01-01`) returns nothing at exit 0 with no
warning.

Same class, `cmd/fngr/add_json.go:151`: JSON `created_at` is parsed with strict
`time.Parse(time.RFC3339)`, so `{"created_at":"2026-01-02"}` errors even though
`--time` accepts it. *(This half is fixed — see [C3](#c3); `--from`/`--to`
remain.)*

**Fix:** route both through `timefmt.ParsePartial`, flooring `--from` to 00:00
and ceilinging `--to` to 23:59:59 when the input is date-only. Add an
inverted-range warning on stderr.

**Resolved** as proposed, with the upper bound expressed as an exclusive
instant rather than `23:59:59`. `parseBound` in `cmd/fngr/list.go` delegates to
`timefmt.ParsePartial`, so both flags now accept everything `--time` does —
relative forms, bare dates, bare clocks, and the RFC 3339 stamp `--format=json`
emits. `--from` floors a date-only input to 00:00 via `startOfDay`.

`--to` is the interesting half. `ListOpts.To` is an *exclusive* upper bound
compared against stored text, so `--to` becomes the first instant past what the
user named: the next midnight for a bare date, the next second for a clock.
`23:59:59` would have been off by the sub-second remainder — a stamp of
`23:59:59.4` sorts after it and would drop out of a range the user wrote to
include the whole day. Rounding up instead of down makes `--to` mean "through
the end of what you named" at both granularities, which is what
`--to 2026-07-27` and `--to "2026-07-27 09:30"` are each read as.

The inverted-range warning is on stderr and fires on `From >= To`, equality
included: an equal pair is empty too, because the bound is exclusive, and a run
that returns nothing at exit 0 is the failure mode this warning exists for.

`ParseDate` is gone with its last caller. Keeping it would have left a second
accepted-input grammar in a package whose whole reason to exist is that there
is only one.

*Verified NOT broken while testing this area:* `--from`/`--to` timezone
handling is correct. With `TZ=Pacific/Auckland`, an 08:00-local event stored as
`2026-06-14 20:00:00` UTC and a 23:30-local event stored as
`2026-06-15 11:30:00` UTC are **both** returned by `--from 2026-06-15 --to
2026-06-15`. Same for `America/Los_Angeles`.

<a name="m13"></a>
### M13 — DST spring-forward silently shifts `event time`

**`internal/timefmt/timefmt.go:268,278`** — `SpliceTime`/`SpliceDate` build a
`time.Date` in the event's location with no DST-gap check:

```
SpliceTime(2026-03-08 12:00 America/New_York, 02:30) = 2026-03-08 01:30:00 -0500 EST
SpliceDate(2026-06-01 02:30 NY, date 2026-03-08)     = 2026-03-08 01:30:00 -0500 EST
```

`fngr event time N 2:30` on the spring-forward day stores **01:30** and reports
`Updated event N` as if it succeeded. Fall-back ambiguity at 01:30 resolves to
EDT without comment — acceptable, but undisclosed.

**Fix:** after `time.Date`, compare `.Hour()/.Minute()` to the requested clock
and warn on mismatch.

**Resolved** by that comparison, factored into `timefmt.localClock` and applied
at every door rather than only at the two this entry names.

*Scope.* `SpliceTime`/`SpliceDate` are two of the paths that turn a wall clock
the user typed into a stored timestamp; the others are `add --time`, `add`'s
`"9:30: had coffee"` title prefix, and the `--format=json` import (both its
shared `--time` default and each record's `created_at`). Fixing only the
splices would have left `fngr event time N 2:30` warning while
`fngr add "2:30: coffee"` stayed silent about the identical shift.
`ParsePartial` reports `exists` for everything that arrives as text; the
splices return it beside their result because they assemble a clock out of two
timestamps and there is no string to parse. `cmd/fngr/clock.go::warnSkippedClock`
is the single formatter. The import quotes only the first offending record and
appends a count for the rest, since a batch runs to 10 000 records.

*Detection.* Comparing against the requested clock needs the requested clock,
and after the fact it is gone — `time.Date` and `time.ParseInLocation` both
normalize a nonexistent wall clock silently and return an entirely ordinary
timestamp with no error. `parsePartial` therefore reads the absolute layouts in
UTC, which has no transitions and so yields the clock exactly as written, then
rebuilds those fields in `time.Local` through `localClock` and compares.
`layoutHasOffset` short-circuits RFC 3339 to true — an offset names an instant,
not a local clock — and `layoutHasTime` does the same for the date-only layout,
because a bare date names no clock and some zones shift DST at 00:00.

This started as a separate `ClockExists(s)` predicate, on the reasoning that
only the commands storing a typed clock care and everyone else would thread a
bool they ignore. That was wrong twice over: a predicate that re-parses the
text is a second parser to keep in step with the first, and it quietly missed
`yesterday at 2:30` — `parseRelative`'s `<day> at <time>` branch builds a wall
clock from a day the offset picked and an hour the user typed, and the
predicate treated every relative form as an instant. Folding `exists` into
`ParsePartial` puts `localClock` on the single path every wall clock in the
package is assembled through, which is what makes the answer exhaustive rather
than a list of cases someone remembered.

*A warning, not a refusal.* The shifted instant is a real one and almost
certainly the intended entry, so the event is still stored — refusing would
leave the user nothing useful to type instead. Fall-back ambiguity stays
silent, as the entry allows: a clock that happens twice resolves to the first
of the two offsets and the user gets the stamp they typed.

Tests swap `time.Local` for `America/New_York` in non-parallel tests with
`t.Cleanup` restore, and `_ "time/tzdata"` embeds the zone database so they run
without system zoneinfo. `time.FixedZone` has no transitions, so a real zone is
the only way to produce a skipped clock at all.

<a name="m14"></a>
### M14 — `-n N -r` returns the N oldest events

**`internal/event/event.go:862-869`** — `ORDER BY` is applied before `LIMIT`,
so `Ascending` changes *which* rows survive the limit:

```
$ fngr -n 2 --format=flat        -> 8 e3 / 7 e2       (newest 2)
$ fngr -n 2 -r --format=flat     -> 1 (oldest) / 2    (oldest 2, of 8 events)
```

Literally correct per each flag's documentation, but `-r` reads as a
display-order toggle, so `fngr -n 20 -r` — the natural "recent activity,
chronological" invocation — silently shows the 20 oldest entries in the
database.

**Fix:** select newest-N in a subquery and re-sort ascending in the outer
query, or document the interaction in `--limit`'s help.

**Resolved** with the subquery, not the documentation. `-r` is a display-order
toggle everywhere else it appears, and a help line explaining that it also
reselects the rows would be documenting a trap rather than removing one.

`buildListQuery` now emits `ORDER BY e.created_at DESC LIMIT ?` and, when
ascending, wraps it: `SELECT * FROM (<that>) ORDER BY created_at ASC`. `SELECT
*` rather than restating the column list, since the subquery already fixes both
the columns and their order and a second copy is a second place to update. The
unlimited path is unchanged — with no `LIMIT` there is nothing for the sort to
select, so it still sorts in place.

The rule is now stated on `ListOpts` itself: a limit always keeps the newest N,
and `Ascending` decides display order only. Two existing tests had pinned the
old behavior (`TestListCmd_Reverse` with `Limit: 1`, and `TestList_LimitAndSort`
expecting `evt 0` first) and were corrected to the new contract rather than the
fix being weakened around them. A new test drives `Filter` + `Limit` +
`Ascending` together through both `List` and `ListSeq`, since the wrapper has to
compose with the compiled `-S` condition and both entry points share
`buildListQuery`.

<a name="m15"></a>
### M15 — Unbuffered stdout: one `write(2)` per event

**`cmd/fngr/main.go:88-93`** binds `Out: os.Stdout` raw;
**`cmd/fngr/pager.go:26-46`** returns `io` unchanged when stdout isn't a TTY.
`render.FlatStream`'s `fmt.Fprintln(w, line)` is therefore one syscall per
event — 250,000 syscalls for a 250k list.

Measured in-process against the repo via a `replace` directive:

| path, 250k events | raw fd | `bufio` 64 KB | gain |
| --- | --- | --- | --- |
| `FlatStream` | 1.393 s | 1.240 s | **−11%** |
| `List` + `Tree` | 1.425 s | 1.276 s | −10% |
| `JSONStream` | 2.098 s | 1.707 s | **−19%** |

Corroborated externally: `flat 250k` shows 0.20 s sys out of 1.39 s.

**Fix:** wrap `Out` in a `bufio.Writer` inside `withPager`, which already owns
the wrapping and the closer, so the flush has a natural home. Caveat: TTFB for
`| head -3` would wait for the first buffer — use 16 KB rather than 64 KB to
keep that imperceptible.

**Resolved** — `withPager` now always buffers, 16 KiB. The pager decision moved
into a `pagerWriter` helper that returns the writer output should ultimately
reach plus a closer for it; `withPager` wraps *that* in the `bufio.Writer`.
Splitting them is the whole point: the old function early-returned `io`
unchanged on every non-TTY path, which is exactly the redirect-and-pipe path a
250k listing takes, so a buffer added inside the pager branch would have missed
the case that was measured.

The closer's flush error is now the command's error rather than a stderr
warning — with `Out` buffered the tail of a listing is written there and
nowhere else, so swallowing it would exit 0 over output the user never
received. `ListCmd.Run` takes it through a named return that does not mask an
error already on its way out. The pager's own exit status stays a warning: a
pager closed early is a user decision, not a lost write.

Two corrections to the finding as written. The per-line syscall cost applies to
tree, flat, markdown and JSON but *not* CSV — `csv.Writer` wraps its output in
a 4 KiB `bufio.Writer` of its own, so that format was already batching and now
pays one extra memcpy for a 4x syscall reduction. And the same one-write-per-row
shape is still there in `fngr meta` (measured 18.5 ms vs 2.0 ms for 10 001 rows)
and `fngr event N -t`; both are left alone here because handing them `withPager`
also switches paging on for them, which is a product decision rather than a
perf fix. Tracked as follow-up, not as part of M15.

<a name="m16"></a>
### M16 — Release workflow: broad privileges on mutable action tags

**`.github/workflows/release.yml:9-12,18-37`**

Workflow-level `contents: write` + `packages: write` + `id-token: write`, plus
`secrets.HOMEBREW_TAP_TOKEN`, combined with seven actions pinned only to
floating major tags (`actions/checkout@v6`, `setup-go@v6`,
`docker/setup-qemu-action@v4`, `setup-buildx-action@v4`, `login-action@v4`,
`sigstore/cosign-installer@v3`, `goreleaser/goreleaser-action@v7`). Tags are
mutable. A compromised upstream tag gets the repo's OIDC identity — meaning it
can cosign-sign malicious artifacts that verify correctly — plus ghcr push and
the brew tap token. `goreleaser-action` additionally runs `version: latest`, an
unpinned binary download.

**`.github/workflows/ci.yml`** has no `permissions:` block at all, inheriting
the repo/org default while running `make lint test` on PR code. GitHub
force-downgrades the token for fork PRs, so exposure is limited to same-repo
branches, but `permissions: contents: read` costs nothing.

**Fix:** pin all seven to full commit SHAs with `# vX.Y.Z` comments; pin
goreleaser to an exact version; move `permissions` from workflow scope into the
job; add `persist-credentials: false` to `checkout` (goreleaser's
`before: hooks: go mod tidy` runs with the token sitting in `.git/config`).

Smaller supply-chain items:

- **`Dockerfile:3`** — `gcr.io/distroless/static-debian13` defaults to
  `USER root` and is an unpinned tag. Use `:nonroot` (uid 65532) and pin by
  digest.
- **No SBOM** in `.goreleaser.yaml` (`sboms:` absent). `signs:` covers only
  `SHA256SUMS`, which transitively covers archives — acceptable.
- **`.goreleaser.yaml:5-7`** runs `go mod tidy` in the release `before` hook,
  letting `go.mod`/`go.sum` mutate during a release build.
- **`common-go.mk:28,35,42,49`** `go install …@latest` for staticcheck,
  golangci-lint, gosec, gocritic — four tools resolved at run time on every PR.
  `common-go.mk` is shared and off-limits here; pin in CI instead.
- **`govulncheck` is not in CI.** It runs clean today (0 called
  vulnerabilities; one un-called advisory, `GO-2026-5024` in
  `golang.org/x/sys@v0.43.0`, Windows-only, fixed in v0.44.0). Worth adding.

**Resolved**, all of it. `release.yml` declares `permissions: {}` at workflow
scope and grants per job (`contents: write` + `packages: write` +
`id-token: write` to `goreleaser`); `ci.yml` takes the simpler shape its jobs
call for, `contents: read` workflow-wide, since neither of them publishes
anything. Every action is pinned to a commit SHA with the version in a trailing
comment, `checkout` passes `persist-credentials: false`, and `goreleaser-action`
runs `version: v2.17.1` rather than `latest`. `sigstore/cosign-installer` is
pinned on the **v3** line (`v3.9.1`) — v4 installs cosign v3, which breaks the
`signs:` args, see `docs/PUBLISHING.md`.

Pinning an action is not enough on its own where the action then pulls an image:
`docker/setup-qemu-action` defaults to `tonistiigi/binfmt:latest`, which it runs
**privileged** on the runner, and `docker/setup-buildx-action` bootstraps
`moby/buildkit:buildx-stable-1`, which builds every layer we push — both mutable
tags, both inside the one job holding all three write scopes. `release.yml` now
passes each by digest.

The smaller items, in order: the `Dockerfile` is
`gcr.io/distroless/static-debian13:nonroot` pinned by digest; `.goreleaser.yaml`
gained a `sboms:` block writing one SPDX document per archive (verified in a
snapshot run to land in `SHA256SUMS`, so the existing cosign signature covers
them) and its `before` hook is now `go mod verify`, which writes nothing; the
four linter versions live in the `Makefile` behind `make lint-tools`, which
installs them into `GOPATH/bin` so `common-go.mk`'s `which` check finds them and
its `@latest` fallback never fires — the shared file itself is untouched, and CI
and a local `make lint` run the same versions because they read the same list;
and a separate `vuln` job runs `make vuln` (govulncheck), with
`golang.org/x/sys` bumped to v0.44.0 so the scan is clean of even un-called
advisories. That job declines setup-go's cache deliberately: the cache key is
OS + Go version + `go.sum` hash and nothing else, so it collides with
`test (ubuntu-latest)`'s, and the shorter job winning the reservation would
leave the longer one rebuilding all four linters from cold every run.

A pin regresses invisibly — a floating tag looks like a tidy-up in review and
breaks nothing until it is exploited — so `make lint-pins` fails on any `uses:`
without a `@<40-hex> # vX.Y.Z` suffix, any `FROM` without a digest, and any
action pinned to two different SHAs across the workflows (the likely outcome of
a half-finished refresh, since both workflows share four actions). It hangs off
`lint` as a prerequisite rather than being named separately by `make ci` and
`ci.yml`, so there is no second list to keep in step. What a grep cannot see —
GoReleaser's `version:`, the `Makefile`'s linter versions, the two `with:` image
digests — is stated as such in `CLAUDE.md` rather than implied to be covered.

Two things worth knowing. The `nonroot` base is a user-visible change, not just
a hardening: SQLite in WAL mode creates `-wal`/`-shm` siblings, and a
single-file bind mount leaves the directory around them root-owned inside the
container, so every documented `docker run` failed with `attempt to write a
readonly database` — reads included, since WAL needs `-shm` to open the
database at all — until the README switched to mounting the *directory* with
`--user "$(id -u):$(id -g)"`. Both forms were
tested against a locally built image. And the new `vuln` job is not yet in the
repo's required status checks — the playbook lists it for new repos, but adding
it to fngr's own branch protection is a repo-settings change, not a commit.

---

## Low

Grouped; each is small and independently actionable.

**Output and formatting**

- ~~Streaming JSON emits the separator `,` on its own line and a blank line
  before `]`~~ — **Done.** `JSONStream` writes the `[\n  ` / `,\n  ` lead itself
  and encodes each element at `SetIndent("  ", "  ")`, which is exactly how
  `MarshalIndent(slice, "", "  ")` renders one; a `json.Encoder` terminates every
  value with its own newline, which is where both artefacts came from. That
  layout is now pinned against `json.MarshalIndent` itself rather than against
  the other dispatcher, which cannot say anything: `JSON` is `JSONStream` over
  a slice-backed `slicedSeq`, so the two agree whatever they write. `Flat`,
  `CSV` and `Markdown` delegate the same way — every format but tree, which
  needs the whole topology before it can draw a line, is written in exactly one
  place. That also stopped `CSV` discarding the write errors `CSVStream`
  checked. Measured: delegating cost ~2.5% CPU for ~9x lower peak memory on a
  250k listing (470 MB of live heap for the whole serialized blob, against
  54 MB), and encoding each event through a hoisted pointer rather than a fresh
  interface box took allocations down a further third.
- ~~`event show --format=json` emits an **array** for a single event~~ —
  **Done.** New `render.JSONEvent` writes one object and `SingleEvent` dispatches
  to it. `jq '.title'` answers. The round trip is unaffected:
  `parseJSONAddInput` already dispatches on the leading `[`.
- ~~`--format=markdown` is rejected everywhere — only `md`~~ — **Done.**
  `render.Canonical` resolves a `formatAliases` table (`markdown` → `md`) for
  all three dispatchers, and `ListFormats` / `EventFormats` — the slices Kong
  takes as the `enum:` — are *derived* from it by `withAliases`, which adds an
  alias exactly when the vocabulary already has the format it resolves to.
  Listing them by hand is what lets an alias into a vocabulary that cannot
  render it, where it parses, resolves, misses every dispatcher case and falls
  through to the default at exit 0. `AddFormats` is derived too, and `AddCmd`
  dispatches through `Canonical`, so the input side cannot acquire that bug by
  gaining an alias later.
  The rest of the divergence stands: `tree`/`flat` need a set of events and
  `text` is the one-event view, so `list` keeps `tree|flat|json|csv|md` and
  `event show` keeps `text|json|csv|md`. Both are now stated in the README *and*
  generated into `--format`'s own help text from the same slices, rather than
  discovered at exit 80.
- ~~Empty results differ by format~~ — **Done**, both halves. The per-format
  renderings stay as they are — tree/flat/md write nothing, json writes `[]`,
  csv writes its header row, each what a consumer of that format expects to
  parse — and the README now says so. But `No events found.` was printed only in
  the tree branch, so `fngr --format=flat` over a filter that matched nothing was
  total silence at exit 0, the very thing the message exists to rule out. It is
  hoisted out of that branch and reaches every format; the streaming path learns
  it matched something through `noteAny` rather than materializing the result.
  `fngr meta`'s `No metadata found.` moved from stdout to stderr to match — it is
  a remark about the result, not a row of it — and both go through one
  `reportNone`, so the wording and the choice of stream are made once.
- ~~`fngr meta` column alignment flips with `-S`~~ — **Done.** The listing pads
  the joined `key=value` cell rather than the key and value separately. Padding
  the key made the `=` a column of its own, and its width came from whichever
  rows the query returned — the entry is one string; only its right edge is a
  column.
- ~~`render.Tree` on a limit-orphaned or cycle-broken set prints nothing at
  exit 0~~ — both halves fixed. Limit-orphaned events render with the marker
  ([M8](#m8)); cycle members are swept up as orphans rather than silently
  dropped ([M2](#m2)).

**Errors and exit codes**

- ~~Three exit-code classes with no documented contract: `1` runtime/domain
  error, `80` Kong parse error (with a full usage dump), `2` Go panic. `80` is
  a Kong implementation detail leaking into the scripting interface.~~
  **Fixed**, and the two halves are separate fixes. The contract is now three
  lines in the README — `0` success (`--help` and `--version` included), `1` an
  error fngr diagnosed and reported, `2` a command line it would not parse —
  the split being between a request that was refused and one that was wrong,
  which is what tells a script whether a retry can help. Writing it down was
  not enough on its own: `80` would then have been *documented* rather than
  *fixed*, and a number that means nothing outside Kong is exactly what a
  scripting interface should not be pinned to. The status now comes from `run`,
  which *returns* one of `exitOK` / `exitError` / `exitUsage` — the set is
  closed structurally rather than by a mapping. The two intermediate versions
  are worth recording because each was wrong in its own direction. Remapping
  *by elimination* (everything but 80 → `1`, so the unexported number need
  never be named) answered an `$EDITOR` that exited `3` with "the command line
  could not be parsed, nothing was attempted" — `FatalIfErrorf` runs every
  error through `kong.ExitCoder` first, so an `*exec.ExitError` carrying
  `ExitCode()` arrives intact, and elimination turned an undocumented leak into
  a documented lie. Naming 80 fixed that and was a shim, since the constant is
  unexported. Owning the parse removed the question: `reportParseError` asks
  `errors.As(err, &parseErr)` for `*kong.ParseError`, which *is* exported, and
  `exitCode` survives only as a clamp on the `--help` / `--version` hooks —
  the sole `kong.Exit` call sites left once `FatalIfErrorf` is gone, both
  passing 0. `2` rather than a fresh number because
  it is what argparse and clap answer with; the overlap with the Go runtime's
  own panic status is stated in the README rather than designed around, a panic
  being a bug and not a state the contract describes, and distinguishable by
  the `panic:` dump above it.
  `kongOptions` exists because the fix has to be testable: it is the one
  parser configuration `main` and the tests share, and `exit` is a parameter
  rather than an appended override so a test cannot step over the mapping it
  came to check.
- ~~Kong parse errors dump the entire command list before the message — on
  `fngr -n -1` the actual error is 25 lines below the fold.~~
  **Fixed:** the short usage in place of the full one, 32 lines down to 3 — a
  usage line, a pointer to `--help`, and the message. `fngr --help` is
  unaffected and still prints the full command list, which is the difference
  that matters: the long form is an answer when it was asked for and noise when
  it was not. The wart that survived the first pass was Kong's — the usage went
  to *stdout* while the error went to stderr, plus an unconditional blank line
  — and it is gone with the `main.go` restructure below. `reportParseError`
  writes both to stderr, still through Kong's own
  `kong.DefaultShortHelpPrinter` (`writeShortUsage` swaps the exported
  `parser.Stdout` for the duration) rather than a private copy of a format the
  library owns. `kong.ShortUsageOnError()` went with the `FatalIfErrorf` it
  configured.
- ~~`fngr -n -1` errors and *suggests* `--limit="-1"`; taking the suggestion
  silently means "no limit" at exit 0. The error's own remedy leads to an
  unvalidated path.~~ **Fixed.** `toListOpts` refuses a negative limit up
  front: `--limit: limit cannot be negative, got -1 (0 means no limit)`. Zero
  still means no limit, so the boundary the store tests (`Limit > 0`) is
  unchanged — what moved is that the one value Kong's own error hands the user
  no longer lands somewhere quieter than where it started. `buildListQuery`
  refuses it too, being the half a directly-built `ListOpts` cannot skip; the
  CLI keeps its own check because it must refuse before `withPager` spawns
  `$PAGER` and before the streaming renderer writes its opening bytes. One
  rule read from two altitudes is one exported function, `event.ValidateLimit`,
  next to `event.ValidateFilter` and for the same reason — two wordings of the
  same rule drift. Refused rather than clamped:
  clamping is the silent behaviour the bullet is about. Unlike `From`/`To`,
  which stay un-range-checked on purpose — a bad bound matches nothing or
  everything *as asked*, whereas a negative `Limit` silently **widens** the
  result to the whole journal.
- ~~Errors leak Go internals: `{"title":"t","meta":{"a":"b"}}` →
  `cannot unmarshal object into Go struct field jsonAddInput.meta of type
  [][2]string`, exposing a private type name. JSON `created_at` errors leak the
  Go reference-time layout.~~
  **Fixed**, both, and they are the same mistake at two altitudes: a Go
  spelling handed to someone who is holding a JSON file or typing a time.
  `add_json.go::wireTypeError` restates a `json.UnmarshalTypeError` in the
  wire's vocabulary — `field "meta": got object, want array (at byte 21)` —
  and passes every other decode error through untouched, `unknown field
  "ttile"` and `unexpected EOF` already being about the input. The three facts
  worth keeping were all already fields on the error; what had to go was
  `encoding/json`'s rendering of them. `wireTypeName` is kind-level on purpose:
  the exact shape of `meta` is the README's job, and "array of 2-element array
  of string" describes it no better than "array" and reads worse. The byte
  offset is included because it is the only locator the decoder keeps — a
  batch runs to 10 000 records and the field name alone cannot say which one.
  A top-level mismatch (`42`, or an array of scalars) has no field name at all,
  and is where the leak was worst — `cannot unmarshal number into Go value of
  type main.jsonAddInput` — so the field name is a prefix rather than a
  precondition: the first version returned early when it was missing and so
  passed through exactly the message it existed to replace.
  The `created_at` half was the hint listing `3:04PM` among `YYYY-MM-DD` and
  `HH:MM`: Go's reference clock passing as a placeholder, so a reader could not
  tell whether it was a pattern or a literal to type. Now `HH:MMpm` — and
  written once, not three times: fixing one token in three files was the tell,
  so the vocabulary is `timefmt.AbsoluteForms` / `RelativeForms`, built into
  the hint and threaded into the `--time` and `event time` help by `kongVars`,
  the way `render.ListFormats` already reached `--format`. The sites that
  gesture at the grammar without enumerating it (`event date`, list's
  `--from`/`--to`) stay prose: there is nothing there to drift.
- All `--db` failures surface as pragma errors — a directory, an unwritable
  path, and `--db ""` all give
  `cannot set pragma foreign_keys: unable to open database file (14)`; a
  non-SQLite file gives `cannot set pragma journal_mode: file is not a
  database (26)`. None say "is a directory" / "not a database file".
  *Done: [C1](#c1) moved the pragmas into the DSN, so the prefix is now
  `cannot open database <path>`. On top of that, `db.openHint` translates the
  two result codes that name the database whatever is at fault —
  SQLITE_CANTOPEN and, once the file exists, SQLITE_READONLY (which reports a
  read-only *directory* as "attempt to write a readonly database", the
  container single-file-bind-mount case) — into the filesystem object actually
  standing in the way: a directory, an unreadable file, or a parent that is
  missing, is not a directory, or is not writable (that message names the
  `-wal`/`-shm` siblings, since needing them is why a read fails too). The
  driver's own text survives wherever it is the better answer, as with
  `file is not a database (26)` — including for a corrupt file that also sits
  in a read-only directory. `--db ""` is not one of these: Kong's
  `type:"path"` expands it to the current directory, which then reports as
  the directory it is. Not covered, and out of scope for an open-time hint: a
  database file that is readable but not writable opens fine and fails at the
  first write with SQLite's own `attempt to write a readonly database (8)`,
  which is accurate there.*
- ~~Cycle errors double the sentinel text: `attaching event 3 to event 5 would
  form a cycle: would create a parent cycle`.~~
  *Done: the prefix's job is the two ids, so it states them and nothing else —
  `attaching event 3 to event 5: would create a parent cycle`. The self-parent
  message was reworded to match (`attaching event 3 to itself: …`) rather than
  left as the odd one out.*
- ~~`Renamed 1 occurrence(s)` / `Deleted 1 occurrence(s)` — pluralize
  properly.~~
  *Done: `cmd/fngr/plural.go` holds the one count+noun formatter, applied to
  both `meta` prompts, both `meta` result lines, the `--format=json` import
  count (which hand-pluralized) and its skipped-clock tail warning. Regular
  `-s` only — every noun fngr counts takes one, and the alternative was an
  irregular-plural argument no call site needs.*
- ~~`fngr help bogus` → blank line, then `unexpected argument bogus`, with no
  list of valid commands.~~
  *Done: `checkCommandPath` walks the path before handing it back to Kong and
  answers `fngr has no command "bogus"; try one of: add, delete, event, help,
  list, meta` (and `fngr meta has no command …` one level down). It stays
  quiet wherever the word might legitimately not be a command — a leaf like
  `add`, and a node that takes an argument, so `fngr help event 5` is still
  Kong's to explain. The old answer was not a Kong bug: `list`
  is `default:"withargs"`, so the word re-parsed as a stray argument to `list`
  and got list's entire usage block in reply. That is also why the check reads
  all three places Kong looks for an argument: `withargs` puts `event`'s `<id>`
  on the `show` child, not on `event`.*
- ~~The same defect is still live one path over: `fngr evnt 5` answers
  `unexpected argument evnt`, for exactly the `default:"withargs"` reason
  above, and that is the typo people actually make.~~ **Fixed** with the
  `main.go` restructure below: `run` owns the `kong.New`/`Parse` pair, so
  `reportParseError` has the failing context and runs the same
  `checkCommandPath` over it. `fngr evnt 5` now answers `fngr: error: fngr has
  no command "evnt"; try one of: add, delete, event, help, list, meta` at exit
  `2`. The node and the candidate words come from `unplaced`, which reads
  Kong's own trace (`kong.Path.Remainder()`) rather than re-scanning argv — a
  second scanner cannot know which flags take a value, since `list` is
  `default:"withargs"` and its `-S`/`-n`/`--format` are spliced onto the trace
  at parse time, so the first attempt read `fngr -S ops --bogus` as a mistyped
  command `ops` and swallowed the unknown flag that was the real complaint.
  `unplaced` also stops at the first `-`-prefixed word and backs a *default*
  command out to its parent when it consumed no token, which is what separates
  `fngr meat` (typo, diagnosed) from `fngr list extra` (stray positional, left
  to Kong).

**Behavior**

- ~~`delete -r` under-reports: deleting a 3-node subtree prints `Deleted event
  1` and the prompt names one event. State the count before asking and after
  acting.~~
  *Done: `deleteSubject` names the target once and both lines use it —
  `Delete event 1 and its subtree (3 events)? [y/N]` then `Deleted event 1 and
  its subtree (3 events)`, so the prompt cannot promise a different number
  from the result. The count comes from `CountSubtree`, added for this: the
  first cut called `GetSubtree` and so read every title, body and meta row of
  the subtree to print one integer — ~950 ms and ~110 MB of live heap at 100k
  descendants, paid before the prompt and thrown away on abort. A stored cycle
  stops either walk; any error from it degrades to the old un-counted `and all
  its children` rather than failing the command, because the delete is an FK
  cascade that walks nothing and `delete -r` is one of the ways out of a
  corrupt tree.*
- ~~`event detach` on a parentless event prints `Detached event 1` at exit 0
  with nothing done. Idempotent by design is fine; it reads as confirmation
  that work occurred.~~
  *Done: it reads the event first, so a parentless one reports `Event 1 has no
  parent; nothing to detach` (still exit 0, still idempotent) and a real
  detach names what it cleared — `Detached event 3 from event 2`. The read is
  a single-row `Get` and walks no ancestry, so the verb still works on the
  corrupt tree `withRepairHint` sends people to it for. `attach` had the
  mirror-image defect and got the mirror-image fix: re-parenting silently
  displaced whatever parent was there, so it now reads first too and reports
  `Attached event 3 to event 4 (was event 2)`, naming nothing when there was
  nothing to displace.*
- ~~`--meta 'k='` is accepted, creating an empty-valued entry rendered as
  `k=  (1)`. `parse.FlagMeta` rejects an empty key but accepts an empty value
  and whitespace *inside* a key (`-m 'a b=c d'` stores a key that indexes as
  two FTS tokens and can't be searched back as written).~~ **Fixed.** Both
  rules are stated once, in `parse.ValidateMeta`, and applied only where a
  tuple is *minted*: `parse.FlagMeta` (`--meta`), `internal/event`'s
  `requireStorableMeta` (called by `addInTx` and `AddTags`),
  `requireRenamableMeta`'s new value, and `cmd/fngr/add_json.go` per `meta`
  pair — replacing three separate copies of the empty-key check, one of them
  the entry point that takes 10 000 tuples at a time. The shared empty-key
  error is `parse.ErrEmptyKey`, since `KeyValue` and `ValidateMeta` reach that
  state from different directions and used to spell it twice.

  *Two altitudes on purpose. The parse-layer calls are pre-flights, for the
  message: `--meta` can name the flag and the JSON import can name the record
  and pair index, which the writer cannot. The guarantee itself lives at the
  writer, for the reason `requireOneAuthor` does — `parse.Meta` is an
  exported struct with exported fields, so a directly-built `AddInput`
  bypasses every argument parser. Safe to enforce there because
  `parse.BodyTags` is bounded by `metaNamePattern`, so no body-derived tuple
  and no migration back-fill can carry an empty value or a key that is not a
  single term.*

  *And only the minting paths. `KeyValue` and `MetaArg` stay permissive
  splitters, because the same functions serve the verbs that **name** an
  existing row — `event untag 'k='`, `meta delete 'k='`, `meta rename 'k='
  'k=v'`, the last two of which CLAUDE.md already documents as the only way
  to reach such a row. Gating those on the mint rule made exactly the rows
  this change stops creating into rows nothing could remove: every released
  build wrote them. `RemoveTags` is likewise unchecked at the writer.*

  *The key rule is "exactly one `-S` term", and it is `parse.IsFilterDelim` —
  the very predicate the tokenizer in `internal/event/filter.go` uses — that
  says which runes end one, rather than a list restated on the write side. The
  first draft of this fix restated it, and was wrong within the hour: it banned
  whitespace only, while `&` and `|` split a term just as hard (`-m 'a&b=c'`)
  and a leading `!` does something worse than fail — `-S '!k=v'` answers with
  every event **except** the tagged one. The predicate lives in `parse` because
  `internal/event` imports `parse` and not the reverse.*

  The rule is one *term*, deliberately not `MetaNameRe`:
  `-m ticket.id=PROJ-42` is a key that regex would refuse and `-S` finds
  perfectly, and narrowing it would route a storable key to a filter column
  that cannot answer for it. That was not hypothetical —
  `cmd/fngr/meta.go::parseMetaFilter` did test `MetaNameRe` for its bare-key
  form, so `fngr meta -S ticket.id` was refused by the one command that lists
  metadata. That form is now unvalidated outright: it reaches `ListMeta`'s
  plain `WHERE key = ?`, where the `-S` *expression* tokenizer is nowhere on
  the path, so nothing a key can contain makes it unmatchable there — and
  `fngr meta -S 'a b'` listing a legacy row is how an operator learns the row
  is there at all. Applying the mint rule to it (the first draft did) hid
  exactly the rows the change cannot create, behind a message untrue of that
  path. Whitespace and operators inside a *value* stay legal —
  `author=Ada Lovelace` is what people mean to write and `-S author=Ada`
  reaches the row, a term ending at the space being no obstacle when the key
  survives whole.
- ~~`$PAGER` is tokenized with `strings.Fields` (`cmd/fngr/pager.go:71`) but
  `$EDITOR` is not (`cmd/fngr/body.go:126`), so `PAGER="less -R"` works while
  `EDITOR="code -w"` fails with `fork/exec …/code -w: no such file or
  directory`. The roadmap lists `$EDITOR` tokenization under "considered, not
  pursued" — but the *asymmetry* with `$PAGER` is new information.~~
  **Fixed.** The asymmetry is what settles it: the roadmap deferred this as a
  feature nobody had asked for, but there was never a decision that the two
  variables should behave differently — one caller split and the other did
  not. So the fix is not `strings.Fields` copied into a second place, it is
  one `envCommand` (`cmd/fngr/envcmd.go`) that both call, which is also the
  only form of the fix that cannot drift back apart. Splitting is on
  whitespace only, deliberately: honouring `EDITOR='emacsclient -a ""'`
  means running a shell, and handing a shell an environment variable is a
  larger decision than this one.
- ~~Editor temp file leaks on signal (`cmd/fngr/body.go:114`) — `defer
  os.Remove` doesn't run when fngr is killed mid-edit (Ctrl-C is the common
  case). Mitigated: mode `0600` in the per-user `$TMPDIR`.~~ **Fixed**, but
  not the way the bullet implies. A signal handler that removes the temp file
  is the wrong shape: Ctrl-C is delivered to the whole foreground process
  group, and vim — like most editors — handles SIGINT itself and carries on,
  so removing the file would yank the buffer out from under a live editor.
  What actually went wrong is that *fngr* died: `ignoreTerminalSignals` now
  brackets the temp file, suspending fngr's response to SIGINT/SIGQUIT for its
  lifetime so the deferred removal always runs. That is what git does around
  its own editor launch, and it fixes the second half of the symptom too — the
  editor used to be left owning the terminal with nothing waiting to read its
  file. SIGKILL is still out of reach and always will be; the
  0600-in-`$TMPDIR` mitigation stands for that case.

  Worth recording because the obvious spelling is wrong twice over: the
  bracket is `signal.Notify` onto a buffered channel nobody reads plus
  `signal.Stop`, not `signal.Ignore`/`signal.Reset`. `Reset` only lifts a
  `Notify`, so after an `Ignore` the restore is a silent no-op and the process
  stays deaf to Ctrl-C for good — under the first draft `make test` itself
  became un-interruptible. And `exec` preserves an *ignored* disposition where
  it resets a caught one, so under `Ignore` the editor inherits `SIG_IGN` and
  cannot be interrupted either, the exact opposite of the intent and worst on
  a wrapper script — which is what `EDITOR="code -w"` is. Both were measured,
  and both now have a test that fails against the `Ignore` form. The first of
  those has to re-exec the test binary: in-process, a working restore is
  indistinguishable from a broken one except by the test binary dying.
- Search results never show why they matched. `-S kubernetes` on an event whose
  body mentions it displays only the title. FTS5 has `snippet()` built in.
  *Real, and new surface rather than a fix — moved to the roadmap's "Proposed
  (not yet scheduled)" section with the open question it carries: a snippet
  column is a listing affordance, and `json` / `csv` already carry the body.*
- `fngr meta` has **no machine-readable output** (`--format` is rejected) — the
  one surface where you'd script tag audits or bulk renames.
  *Same: moved to "Proposed (not yet scheduled)", together with the footnote it
  earns on the bulk-operations rejection, whose `-S … --format=json | jq |
  xargs` composition covers events and not metadata.*
- ~~Filtering by author works via `-S 'author=nico'` but `-S '@nico'` returns
  nothing, because `--author` writes key `author` while `@x` writes `people`.
  Every user will try `@` first; it isn't documented.~~
  **Documented**, not changed. The two keys answer different questions — who
  wrote it against who is named in it — and an event is routinely one without
  the other, so making `@nico` match both would remove the only way to ask
  either. What was missing was that nothing said so: the README's filter
  section now spells out both spellings side by side.

**Tooling**

- **`make bench` runs no benchmarks** — the repo has zero `Benchmark*`
  functions. All six packages report `PASS` with only tests. Worth knowing if
  the target is assumed to be doing something.
  *Done as a side effect of [H2](#h2): `BenchmarkTree_DeepChain` is the first
  one, and it guards the tree renderer's O(depth²) allocation regression.*

---

## Architecture and Go idioms

No package-boundary changes recommended. The `cmd` → `event.Store` →
`internal/*` layering holds up, the narrow `eventStore` interface is doing real
work, and the streaming/buffered split in `render` is well-judged. Items below
are local.

- **`internal/event/event.go` is 976 LoC** and has crossed the ~1,000-line
  threshold the last review set as the trigger to split. A read/write boundary
  is the natural seam: `event.go` (types, Add/AddMany/addInTx),
  `query.go` (List/ListSeq/Get/GetSubtree/buildListQuery/scanEvents),
  `mutate.go` (Update/Reparent/Delete), `meta.go` (AddTags/RemoveTags/
  ListMeta/CountMeta/UpdateMeta/DeleteMeta), `internal.go` (requireEventExists,
  rebuildEventFTS, loadMetaBatch/loadMetaChunk, deleteMetaTuples/
  insertMetaTuples).
- **Bare `tx.Commit()` at `event.go:351` and `:401`** breaks the wrapping
  convention every other commit site follows. Cosmetic but load-bearing for
  grep-ability.
- **`UpdateMeta` and `DeleteMeta` are 32-of-40 identical lines.** The last
  review's Won't-Fix on the `MetaRenameCmd`/`MetaDeleteCmd` *command* shapes
  said to revisit "if a third mutate-by-`(key,value)` verb lands." The
  duplication in the *store* layer is a separate and more compelling case —
  and [H3](#h3)'s fix will touch `UpdateMeta` anyway.
- ~~**`main.go:85` — `defer database.Close()` never runs.** `ctx.FatalIfErrorf`
  at `:94` calls `os.Exit`, which skips deferred functions.~~ **Fixed:**
  `main` is now the standard `os.Exit(run(...))` shape, and `run` returns a
  status from every path instead of exiting on one, so the deferred `Close`
  runs and a WAL database gets its checkpoint.
- ~~**Behavioral logic encoded as command-name string prefixes** —
  `strings.HasPrefix(ctx.Command(), "help")` at `main.go:75` and
  `strings.HasPrefix(ctx.Command(), "add")` at `:80` decide whether a DB is
  needed and whether to create it.~~ **Fixed,** and the two halves came out
  differently. "Needs a database" is no longer declared at all: `run` binds the
  store with `ctx.BindToProvider`, so the answer is read off each command's own
  `Run` signature — `HelpCmd.Run` takes no `eventStore`, so nothing resolves
  one, no path is resolved and no file is opened, which is what keeps
  `fngr help` answerable with a `--db` that is missing or unreadable. "May
  create it" stays declarative but is now a method, `AddCmd.createsDB` behind
  the `dbCreator` interface. Both prefixes were true of any command whose name
  merely started that way: `fngr addendum` would have started a second journal,
  `fngr helpers` would have run with no store bound. `main.go` is no longer in
  `.covignore` either — `run`, `reportParseError`, `unplaced`,
  `writeShortUsage`, `createsDB` and `exitCode` are all directly tested.
- ~~**`wrapFilterErr` string-matches driver error text** from the cmd layer
  (`list.go:64-68`, matching `"fts5"`, `"SQL logic error"`, `"unterminated"`).~~
  Resolved with [C5](#c5): the parser returns `event.ErrFilter` and the cmd
  layer is a one-line `errors.Is` (`withGrammarHint`).
- ~~**`list.go:30` writes the pager warning to `os.Stderr` directly**, bypassing
  the injected `io.Err`.~~ **Not a defect** — re-checked against the source.
  Both pager warnings go to `errOut`, which *is* the injected `io.Err`
  (`pager.go:44`, `:66`); the one `os.Stderr` in that file is `:88`, where the
  pager subprocess inherits the real stderr, which is the point of a pager.
- ~~**Inconsistent empty-result messaging in `list.go`**~~ — **Done** with the
  "Output and formatting" batch above: one `reportNone`, both branches, and
  `fngr meta` too.
- **The 16 KiB output buffer is bolted into `withPager`, so only `list` has
  one.** `fngr event N -t --format=json` writes straight to `os.Stdout` — 2N+1
  syscalls, measured at +19% wall clock over a buffered run of the same 50k
  rows. Buffering is an `ioStreams` concern, not a pager one; hoisting it to
  where `ioStreams` is built means no future command has to remember, at the
  cost of every command needing `list`'s flush-error promotion. Deferred to the
  `main.go` restructure below, which is already opening that constructor.
- **`internal/db/db.go` has no per-connection init hook**, which is the root of
  [C1](#c1). Moving to a DSN removes the need for one.

---

## Performance

**Fast enough everywhere for realistic data.** At 50k events every command is
≤0.29 s and ≤65 MB. The two real defects are [H2](#h2) (tree O(n²)) and
[M15](#m15) (unbuffered stdout). Everything below is measured, not estimated —
macOS, `/usr/bin/time -l`, best-of-3, synthetic DBs at 1k/10k/50k/250k
(250k = 217 MB, 700,440 meta rows).

**Streaming genuinely holds flat; the tree cliff is memory, not throughput:**

| events | tree RSS | flat RSS | tree TTFB | flat TTFB |
| --- | --- | --- | --- | --- |
| empty | 13.0 MB | 13.0 MB | — | — |
| 1k | 15.9 MB | 15.9 MB | — | — |
| 10k | 29.6 MB | 23.4 MB | 0.055 s | 0.011 s |
| 50k | 65.4 MB | 27.2 MB | 0.229 s | 0.012 s |
| 250k | **244.3 MB** | **27.7 MB** | 1.126 s | **0.011 s** |

Above the 13 MB baseline, tree grows linearly at ~0.93 KB/event; flat is
asymptotically flat, bounded by SQLite's page cache. Flat/csv/json/md TTFB is a
constant ~11 ms at every size. Wall-clock is nearly identical (tree 1.450 s vs
flat 1.390 s at 250k) — the cost is RSS and latency-to-first-line. `--limit 20`
makes both instant at every size (0.010 s / 13 MB). Extrapolated, tree at 1M
events ≈ 950 MB.

**Startup floor is 7.8 ms, and 3.0 ms of it parses `/etc/services`.**
`GODEBUG=inittrace=1` shows `modernc.org/libc/honnef.co/go/netdb` at
**3.0 ms clock, 3.66 MB, 44,055 allocs** — an `init()` that reads and parses
`/etc/protocols` and `/etc/services` (13,926 lines / 678 KB on macOS) on
*every* invocation, including `--version`, which never opens a database. fngr
never resolves a service name.

| binary | ms/run | delta |
| --- | --- | --- |
| `/usr/bin/true` | 1.93 | fork+exec floor |
| Go noop | 2.87 | +0.94 runtime |
| Go + `kong.Must` | 3.27 | +0.40 kong |
| Go + `import _ modernc/sqlite` | 7.14 | **+4.27** (3.0 netdb) |
| Go + open + 4 PRAGMA + `user_version` | 7.57 | +0.43 |
| `fngr --version` | 7.78 | |
| `fngr` bare list, empty DB | 8.80 | +1.02 |

That is ~39% of `--version` and ~28% of a `fngr add` (10.7 ms total; ~3-4 ms is
actual insert work). **It is not the PRAGMA round-trips (+0.43 ms), not the
`embed.FS` migration load, not Kong (+0.40 ms).** Not fixable in fngr's own
code — the options are (a) an upstream issue at gitlab.com/cznic/libc proposing
`sync.OnceValue` lazy init of `Protocols`/`Services` (only touched by
`getservbyname`/`getprotobyname`, which SQLite never calls), or (b) accept it.
Worth filing: it would cut the floor from 7.8 ms to ~4.8 ms.

**No full table scans.** Every `EXPLAIN QUERY PLAN` on the 50k DB is
index-driven: default list uses `idx_events_created_at`; `--from`/`--to` is a
range `SEARCH` on it; `ListMeta`'s `GROUP BY key,value`, `CountMeta`,
`HasChildren`, and `loadMetaChunk` all resolve as **covering** index
scans/searches. `GetSubtree`'s CTE searches `idx_events_parent_id` per step.
The only temp b-tree is `USE TEMP B-TREE FOR ORDER BY` on the FTS join, which
costs TTFB but stays small (`-S coffee`, 34,593 hits at 50k: 0.041 s vs
0.012 s unfiltered). Not worth touching.

**Migration 2's prefix-lookup claim holds.** Dropping `idx_event_meta_key_value`
for `UNIQUE (key,value,event_id)` costs nothing — bare-`key` lookups,
`key+value` lookups, and the `GROUP BY` all resolve as covering index
operations with no temp b-tree for grouping. Claim verified.

**The "SQLite is in-process, don't batch" rule is confirmed with numbers.**
Collapsing the events scan + meta load into one `LEFT JOIN` — the classic
"one query" move — is **2.7× slower**:

| meta-loading strategy, 250k events | time |
| --- | --- |
| chunked IN, size=100 | 0.585 s |
| **chunked IN, size=500 (current)** | **0.603 s** |
| chunked IN, size=900 | 0.652 s |
| chunked IN, size=2000 | 0.774 s |
| chunked IN, size=10000 | 1.521 s |
| single `LEFT JOIN` | 1.640 s |

This independently confirms the existing Won't-Fix on `loadMetaBatch` chunk
size: 500 is within 3% of optimal and every direction away from it is worse.

**`mmap_size` / `cache_size` give zero gain.** The CPU profile showed 81.8% of
samples in `syscall.rawsyscalln`, dominated by `pread`, which looked like a
tiny-page-cache problem. It isn't — the preads hit a warm OS page cache.
Baseline 1.122 s; `cache_size=-64000` 1.174 s; `mmap_size=256MB` 1.108 s; both
1.114 s. All within noise. Killing this speculative fix with data.

**No other O(n²), no per-call regex compilation, no missing capacity hints.**
All four `regexp.MustCompile` calls are package-level. Every hot-path slice has
a cap hint. `render.Tree`'s topology build is O(n) maps. No string `+=` in
loops outside [H2](#h2).

Cost breakdown at 250k: raw `rows.Scan` 0.446 s (32%), `+ loadMetaBatch`
1.121 s (+48%), `+ FlatStream` to raw fd 1.393 s (+20%). For reference the C
`sqlite3` CLI does the same scan in 0.21 s — the gap is inherent to pure-Go
`modernc.org/sqlite`, not to fngr's code. 10k-record JSON import: 0.26 s,
41 MB RSS (12× input amplification, bounded by `maxJSONBatchSize`). `fngr meta`
is cheap at every size: 0.07 s / 16.8 MB at 250k.

---

## Security posture

Threat model: operator trusted; event **content** often untrusted (piped
stdin, `--format=json` import, scraped text, captured command output).

Content-reachable issues are [C2](#c2) (brick the DB), [C4](#c4) (corrupt
metadata), [M4](#m4) (bogus tags), [M6](#m6) (forged rows, terminal escapes),
[M7](#m7) (forged tag matches), [M9](#m9) (output amplification). Operator-side
issues are [H5](#h5) (EOF as consent) and [M16](#m16) (supply chain).

**Substantial areas came back clean, with evidence:**

- **Command execution.** Both `$PAGER` (`pager.go:52`) and `$VISUAL`/`$EDITOR`
  (`body.go:126`) use `exec.Command` directly — **never `sh -c`**. Verified:
  `EDITOR='/path/ed.sh; touch /tmp/PWNED'` produced a `fork/exec` failure and
  no `PWNED` file. A `$PAGER` of `sh -c "echo PWNED"` is looked up as a literal
  binary of that name and fails. `pagerCommand()` cannot return an empty slice.
  Temp file is `os.CreateTemp` — random name, `O_EXCL`, mode `0600` (verified
  with `stat`), passed as a single argv element. No TOCTOU, no argument
  injection, no symlink window.
- **Parser fuzzing — nothing new.** `go test -fuzz` across two independent
  runs: `internal/parse` (BodyTags, KeyValue, MetaArg, FlagMeta,
  SplitTitleBody, FTSContent, MetaNameRe) 17.3M + 6.8M execs, **PASS**;
  `internal/timefmt` (Parse, ParsePartial, SplitTimePrefix, ParseDate,
  Splice*, FormatRelative, parseRelative) 7.1M + 3.2M execs, **PASS**;
  `internal/event.preprocessFilter` 4.9M + 1.8M execs — only the known bare-`!`
  crash, and no distinct second panic after patching it locally.
- **Catastrophic regex backtracking — structurally impossible.** Go's `regexp`
  is RE2 (linear time, no backtracking), and `metaNamePattern` has no nested
  quantifiers anyway. This sub-item can be closed permanently.
- **SQL injection — none.** Every user value is a bound `?`. All three
  `#nosec G202` concatenations are genuinely safe: `event.go:272` builds `SET`
  from a fixed three-branch allow-list (and `Update` returns early at `:225`
  when all three are nil, so the empty-`SET` syntax error is unreachable);
  `:719` appends only the literals `"key = ?"`/`"value = ?"`; `:958` repeats
  `"?,"`. `buildListQuery` passes `preprocessFilter`'s output as a parameter.
  28 hostile `-S` values probed — FTS5 expression syntax does pass through
  (`NEAR(a b)`, `^hello`, `hello*`, `content:hello`), but that is
  operator-supplied input reaching an operator-facing query language, the table
  has one column so column filters leak nothing, and malformed cases surface as
  clean wrapped errors. Not a finding.
- **Resource limits hold.** The 16 MiB stdin cap **is** enforced on the
  `--format=json` path (both go through `resolveBody`) — 17 MiB on either gives
  `stdin exceeds 16777216-byte limit`. Deep JSON hits Go's scanner depth limit
  (`[`×100,000 and `[`×5,000,000 both rejected, no stack exhaustion).
  `maxJSONBatchSize` (10,000) cannot be bypassed by nesting; `metaBatchSize`
  (500) holds.
- **Symlink handling — no exploitable primitive.** `.fngr.db` symlinked to a
  nonexistent path: `os.Stat` fails on the dangling link and `ResolvePath`
  falls through to `~/.fngr.db` (no arbitrary file creation). Symlinked to an
  existing non-DB file: clean error, target unmodified.
- **Concurrent `Reparent` cycle race — none.** 60 iterations of simultaneous
  `attach 1 2` / `attach 2 1` produced no cycle; SQLite write serialization
  holds.
- **Migrations** are static, applied inside a transaction with the
  `user_version` bump; the only interpolation is an internal int constant.
- **Escaping is correct in CSV, JSON, and Markdown** — commas, quotes, embedded
  newlines, and continuation lines all handled. Only `flat`/`tree` are exposed
  ([M6](#m6)).
- **`govulncheck` clean** — 0 called vulnerabilities.

---

## Feature recommendations

The project stance is **"dogfood and harden, not features."** That stance is
vindicated by this review: essentially every high-value item below is a bug
fix, not a feature. `docs/superpowers/roadmap.md`'s "Considered (not pursued)"
list was re-examined against real usage and **no rejection needs reversing**.

Ranked, with new-surface-area items explicitly flagged:

| # | Item | Type | Cost |
| --- | --- | --- | --- |
| 1 | [C1](#c1) DSN pragmas — stops silent write loss | harden | ~5 lines |
| 2 | [C4](#c4) Unicode `metaNamePattern` — stops silent name truncation | harden | 1 line + tests |
| 3 | [C2](#c2) Bound relative offsets + defensive year check | harden | ~10 lines |
| 4 | [C5](#c5) Real `-S` parser | harden | ~200 lines |
| 5 | [C3](#c3) JSON round-trip id remapping | harden | ~60 lines |
| 6 | [H3](#h3) `meta rename` merge semantics | harden | ~15 lines |
| 7 | [H4](#h4)/[H5](#h5) stdin peek + EOF-as-consent | harden | ~15 lines |
| 8 | [H2](#h2) Tree prefix buffer | harden | ~20 lines |
| 9 | **`-e` on `event body` / `event text`** | *new surface* | ~30 lines |
| 10 | [M12](#m12) `--from`/`--to` via `timefmt` | harden | few lines |
| 11 | [M8](#m8) Honest tree under `--limit`; `delete -r` count | harden | ~25 lines |
| 12 | **`meta --format=json`** | *new surface* | ~40 lines |
| 13 | **`snippet()` in search results** | *new surface* | ~30 lines |

Detail on the three genuinely new capabilities:

**9. `-e` on `event body` / `event text`** — the clearest real gap. `add -e`
exists precisely because multi-line bodies don't belong on a command line, but
the moment an event exists the only way to change its body is to retype the
whole thing as a shell argument:

```
$ fngr event body 2 -e
fngr: error: unknown flag -e, did you mean "-h"?
```

`launchEditor` is already a package-level `var` and `errCancel` already models
empty-save, so the machinery exists — the verbs just don't use it. Add
`Edit bool \`short:"e"\`` to `EventBodyCmd`/`EventTextCmd`, seed the temp file
with the current value, make the positional optional when `-e` is present.
Flagged as new surface, but it completes verbs that already exist rather than
adding a command, and it is the single most-implied missing capability.

**12. `meta --format=json`** — the only output surface with no
machine-readable form, which blocks scripted tag audits and bulk renames.
`ListMeta` already returns structured rows; add a `--format` flag dispatching
to a `render.Meta(w, format, rows)`. Flagged as new surface — but note this
weakens an existing rationale: the roadmap's rejection of bulk operations rests
on `fngr -S … --format json | jq | xargs`, and that composition works for
events but not for metadata. Either add the format or footnote the rationale.

**13. `snippet()` in search results** — `-S kubernetes` on an event whose body
mentions it shows only the title, with no indication of why it matched.
Combined with "list/flat show titles only," a body-heavy journal is effectively
unsearchable-in-place: every hit needs a follow-up `fngr event N`. FTS5 has
`snippet()` built in, so this is a query change rather than new machinery.

*12 and 13 are both tracked in the roadmap's "Proposed (not yet scheduled)"
section as of the low-severity sweep — accepted as real gaps, unscheduled
because each is new CLI surface and the project is at a feature plateau.*

Explicitly **not** proposed, having found no new evidence against the existing
rejections: config file, workspaces, soft delete/undo, stats command, shell
completion, backup, vacuum, filtered delete, `add -`.

---

## Documentation gaps

**`README.md`**

- Troubleshooting `database is locked` — the claim that WAL + `busy_timeout`
  makes transient locks self-heal is **false today** ([C1](#c1)). It currently
  sends users hunting a nonexistent stuck holder.
- "JSON is the only round-trip format", the quick-start pipe recipe, and the
  container round-trip recipe are all **false** ([C3](#c3)). Three separate
  occurrences.
- Filter syntax section describes intent, not behavior: `!` first in a
  conjunction discards the rest of the expression, `!` alone panics, hyphenated
  terms and stray quotes error. Needs a known-limitations note until
  [C5](#c5) lands.
- Filter table has no `author=` row, and doesn't say `@name` matches key
  `people` while `--author` writes key `author`.
- "Database location" — add that the cwd rung requires an *existing*
  `.fngr.db`, and that `touch .fngr.db` starts a project-local journal
  ([M11](#m11)).
- Nothing documents the ASCII-only restriction on `@person`/`#tag`
  ([C4](#c4)) — and once [C4](#c4) is fixed, nothing needs to.
- Quick start doesn't mention that `". "` splitting eats abbreviations
  (`Dr.`, `e.g.`) or that `fngr event title` is the fix ([M10](#m10)).
- Date ranges section doesn't state that `--from`/`--to` accept *only*
  `YYYY-MM-DD`, unlike `--time` — and it sits directly below the relative-time
  section, so readers will assume parity.
- Bulk-import examples don't state that JSON `created_at` is RFC3339-only.
- `--format=markdown` is rejected; only `md`. Not stated.
- No mention that `list`/`flat` show titles only, nor that `event show`'s
  `--format` vocabulary differs from `list`'s.
- `fngr event body 1 ""` requires the explicit empty string; omitting the arg
  is a usage error, not a clear.

**`--help`**

- `fngr add --help` prints `--author="nicolasm"` — the machine's `$USER`, not
  the effective default. With `FNGR_AUTHOR=zed` exported, help still shows
  `nicolasm` while events are authored `zed`.
- `event time --help` ("Replace clock time (or full timestamp)") gives no way
  to predict that `"3 hours ago"` is a full timestamp and **moves the date** by
  months.
- `list --help`'s `-S` text says "no grouping parentheses" but doesn't warn
  that `!` cannot lead a conjunction.
- `-f` means `--format` on `add` but `--force` on `delete` / `meta rename` /
  `meta delete`.

**`CLAUDE.md`** — stale, verified against source:

- The `internal/db/migrate.go` bullet says "Ordered list of migrations." They
  are embedded `.sql` files: `//go:embed migrations/*.sql` + `loadMigrations()`
  over `migrations/{1,2,3}.sql`. The `loadMigrations` doc-comment in the code
  already says this correctly, so CLAUDE.md contradicts the source.
- Describes migration 2 as newest; `3.sql` exists and does the
  `text` → `title`+`body` split.
- "Schema changes go in a new entry at the bottom of `migrations` in
  `internal/db/migrate.go`" → should be "drop a new `<N>.sql` into
  `internal/db/migrations/`".
- The `internal/db/db.go` bullet says "connection setup (FK + WAL +
  busy_timeout + synchronous=NORMAL)" as if these are pool-wide. They are
  per-connection `Exec`s on a pool and are **not reliably in effect**
  ([C1](#c1)). This bullet is what future work will trust.
- The `cmd/fngr/body.go` bullet says bare non-interactive `add` errors
  `event text cannot be empty`; the actual string is `event title cannot be
  empty`.
- The `internal/render/render.go` bullet says markdown bullets are
  `- <time> — <body>`; they are `- <time> — <title>`, with the body on
  indented continuation lines.
- The `internal/parse/parse.go` bullet doesn't mention `SplitTitleBody`, which
  `3.sql`'s comment explicitly references as the behavior it mirrors, nor that
  `metaNamePattern` is ASCII-only.

**`docs/superpowers/roadmap.md`**

- The "Markdown output" entry repeats the `- <time> — <body>` error.
- ~~The "Considered (not pursued)" entry for bulk operations rests on
  `fngr -S … --format json | jq | xargs`, which doesn't work for metadata
  (`fngr meta` has no `--format`). Footnote it or fix the gap.~~
  *Footnoted, and the gap it depends on is now tracked: `fngr meta --format`
  sits in the new "Proposed (not yet scheduled)" section, which the
  bulk-operations entry points at.*

---

## Won't Fix / Out of Scope

Carried forward from prior reviews. **One entry is retired this round** (see
below the table). Each entry states why so we don't re-propose it.

| Topic | Reason |
| --- | --- |
| CSV "formula injection" sanitization | Intentional. `TestCSV_SpecialChars` asserts the raw `=formula` is preserved. Local single-user export; user owns downstream pasting. |
| Path traversal via `--db` | Not exploitable. The user already controls the process and the filesystem; `--db /etc/passwd` simply fails on open. |
| Tighter default file permissions on the SQLite DB | Low value for a single-user tool. Users who care can `chmod 0600 ~/.fngr.db`. Auto-chmod inside `db.Open` would be a surprising side effect. |
| Two `testDB` helpers (in `internal/db` and `internal/event`) | Different scopes (raw connection vs. `db.Open`-wrapped). Sharing them would couple test packages without removing real duplication. |
| FTS triggers for INSERT/UPDATE on `events` | FTS content combines event text _and_ meta tokens, so triggers can't see the full picture. `event.Add`/`Update`/`AddTags`/`RemoveTags` keep FTS in sync — **and this review verified they do**, across every mutation verb. |
| Splitting `cmd/fngr/event.go` per verb | Spec deliberately put all eight verbs in one file (single cohesive responsibility). |
| Extra exit-code signaling on not-found | Kong's `ctx.FatalIfErrorf` already propagates non-zero on every returned error. |
| Tune `loadMetaBatch` chunk size | **Re-confirmed with measurements this round.** 500 is within 3% of optimal; 100 is 0.585 s, 500 is 0.603 s, 2000 is 0.774 s, and a single `LEFT JOIN` is 2.7× slower. Every direction away from 500 is worse. |
| Defer pager spawn until first output line | `less -F` already quits-if-fits-on-screen. Spawn cost is sub-100ms on local exec; not worth refactoring. |
| Unify confirm-prompt defaults across delete/meta verbs | Deliberate asymmetry: destructive verbs default `[y/N]`; rename defaults `[Y/n]`. **Note:** [H5](#h5) is a *different* issue — EOF being read as consent — and is not covered by this entry. |
| Show before/after diff before `event text` commits | `event N` is the canonical inspection tool. The user explicitly chose "no prompts on event verbs" during the S2 brainstorm. |
| `deleteMetaTuples` / `insertMetaTuples` vs `RemoveTags`/`AddTags` | The private helpers run inside an existing `tx`; the public functions own the tx + existence check + FTS rebuild. Sharing them would leak `*sql.Tx` into the public API. |
| Recursive-CTE rewrite of `event.Reparent`'s ancestry loop | SQLite is in-process; per-row `SELECT parent_id` calls are microseconds. The loop is clearer for the cycle-detection semantics. **Note:** [M2](#m2) asked for a *bound* on that loop, not a rewrite, and got one — a `seen` set. Still no CTE. |
| Comment-strip `git commit`-style editor template | Deliberately rejected during brainstorming (Q4 of body-input modes). Adds parsing surface for marginal gain. |
| Hardcoded editor fallback (`vi`/`nano`) | Minimal containers / CI may lack the chosen fallback; better to fail loudly than wedge the user into an unfamiliar editor. |
| Drop `t.Parallel()` from add-editor dispatch case | Race detector clean across 10+ iterations; the swap window is narrow and `TestResolveBody` inner subtests are sequential. |
| `withTx(ctx, db, fn)` helper around the six `BeginTx` blocks | The closure indirection costs more clarity than the literal four-line pattern saves. |
| Move `MetaKeyAuthor` / `MetaKeyPeople` / `MetaKeyTag` to `parse` | Costs: parse would need to import event, or the constants move into parse (forcing every caller to update). The test surface catches typos. |
| `--format` flag on `fngr delete` | Destructive verb; a "preview" format would compound with the confirmation UX. `fngr event N --format=json` already shows what would be deleted. |
| Tree format on bare `fngr event N` | Single events have no topology; `--tree` explicitly opts into the subtree view. |
| Single-line fast path in `renderMarkdownEvent` | Saves one slice allocation per event but doubles branching. Dominated by `Fprintf` overhead; clarity wins. |
| `sync.Once`-memoize `loadMigrations()` | Three migrations today; `embed.FS.ReadDir` + sort is sub-millisecond — **measured**: the entire migration load is inside the +0.43 ms "open + PRAGMA + user_version" bucket. Becomes interesting at ~10 migrations. |
| CSV header row dedup between `CSV` and `CSVStream` | Five-element string slice repeated twice. Not worth the import scope. |
| Cache `pagerCommand()` via `sync.OnceValue` | Memoization breaks `t.Setenv` isolation in pager tests. Sub-microsecond per one-shot invocation. |
| Org-level Actions secret with `--visibility selected` for `HOMEBREW_TAP_TOKEN` | Arrived **empty** in the runner despite passing every visibility check. Workaround: repo-level secret. Root cause unknown; documented in `docs/PUBLISHING.md`. |
| REST API for ghcr.io package visibility flip | None exists — the toggle is UI-only. `gh api -X PATCH …` returns 404. Documented in `docs/PUBLISHING.md`. |

**Retired this round:**

- ~~Recursive CTE depth cap in `GetSubtree`~~ — the prior reason was "FK
  constraints + Reparent's cycle check prevent write-path cycles; no untrusted
  input path." There **is** an untrusted input path: `db.ResolvePath` silently
  adopts `.fngr.db` from the current directory. And the failure mode is a
  **non-terminating loop**, not deep recursion, so a depth cap alone was never
  the right fix. Superseded by [M2](#m2).

---

## Next Review Pointers

- **Verify the C1 fix under real concurrency.** The regression test is the
  10-parallel-`add` loop from [C1](#c1), asserting 10 rows. Also assert
  `PRAGMA foreign_keys` on a *second* pooled connection, since that is the half
  a naive fix will miss.
- **`internal/event/event.go` is 976 LoC** — past the ~1,000-line trigger the
  last review set. The read/write split sketched in the Architecture section is
  the recommended seam. [H3](#h3) and [C3](#c3) both touch this file; splitting
  first would make both diffs legible.
- **`internal/event/filter.go`** — after [C5](#c5)'s rewrite this becomes the
  most test-worthy file in the repo. Table-driven cases must cover: leading
  `!`, `!` with `|`, bare `!`, `!!!`, hyphenated terms, embedded quotes, empty
  input, and every shorthand. Delete `wrapFilterErr` (`cmd/fngr/list.go`) as
  part of that change and replace it with a typed error + `errors.As`.
- **`internal/db/migrate.go`** — `loadMigrations` asserts contiguity from 1;
  tests assert against `migrations[len(migrations)-1].version`. Keep that
  invariant when migration 4 lands. **Migration 4 is now needed anyway** for
  [H1](#h1), so ride the long-deferred **`ANALYZE event_meta;`** along with it
  — migration 2 rebuilt the index without refreshing planner stats. Use
  `IF NOT EXISTS` / `IF EXISTS` clauses. Never edit `3.sql`.
  *(Done in [H1](#h1); a migration can now also carry a Go step via
  `goMigrations[N]`, run in the same transaction as its SQL.)*
- **`internal/parse/parse.go:17`** — `metaNamePattern` is load-bearing for the
  body-tag extractor, `MetaArg`, and the exported `MetaNameRe`. [C4](#c4) and
  [M4](#m4) both edit it; do them together and test the boundary cases as one
  table (unicode names, emails, URL fragments, hex colors, issue numbers).
- **`internal/timefmt`** — [C2](#c2), [M5](#m5), [M12](#m12), and [M13](#m13)
  all live here. It has 100% coverage and still shipped four arithmetic and
  range defects, which is worth internalizing: **line coverage measured the
  wrong thing.** The gap is value coverage — extremes, month-ends, DST
  boundaries, overflow. Property-based or fuzz tests over `Parse`/`Splice*`
  with a round-trip invariant would have caught all four.
- **`cmd/fngr/body.go`** — `resolveBody`'s dispatch table is the load-bearing
  UX contract for `fngr add`. [H4](#h4)'s fix changes when the peek happens;
  re-verify all eight rows, and add a ninth case for "stdin is an open pipe
  with no data" (the shape that hangs today).
- **`cmd/fngr/prompt.go`** — [H5](#h5) changes `confirm`'s contract. Every
  call site (`delete`, `meta rename`, `meta delete`) needs a non-interactive
  test asserting the new EOF behavior.
- **`internal/render/render.go`** — [H2](#h2), [M6](#m6), and [M8](#m8) all
  land here. The tree prefix refactor should come with a deep-chain benchmark
  (there are currently **zero** `Benchmark*` functions in the repo, so
  `make bench` is a no-op — worth fixing while touching this).
  *Done: `BenchmarkTree_DeepChain` is the repo's first benchmark, so `make
  bench` now does something. M6 grew its own file, `internal/render/sanitize.go`.*
- **`cmd/fngr/dispatch_test.go`** — every new verb or flag needs an entry.
  `-e` on `event body`/`event text` ([recommendation 9](#feature-recommendations))
  would need the per-case `launchEditor` swap pattern currently scoped to
  `add-editor`.
- **`.github/workflows/`** — [M16](#m16). When pinning to SHAs, note that
  `sigstore/cosign-installer` stays on `@v3` deliberately (v4 has a real
  behavior break — see roadmap).
  *Done: everything network-facing is pinned. The pins go stale silently, so
  `docs/PUBLISHING.md` grew a "Refreshing the pins" section with the resolve
  commands; a bulk refresh must not walk cosign-installer past v3.*
- **`docs/PUBLISHING.md`** — the "Gotchas" section is the institutional memory
  of the v0.0.1 rollout. Add to it when shipping a sibling repo.
