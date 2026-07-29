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
| [C3](#c3) | **Critical** | json | Documented JSON round-trip silently corrupts the event tree | open |
| [C4](#c4) | **Critical** | parse | `\w` is ASCII-only → `@josé` silently stored as `people=jos` | ✅ fixed |
| [C5](#c5) | **Critical** | filter | Leading `!` discards the rest of the expression; bare `!` panics; hyphens error | open |
| [H1](#h1) | High | migrate | Migration 3's SQL `TRIM()` ≠ `strings.TrimSpace` → corrupted legacy titles/bodies | open |
| [H2](#h2) | High | render | O(n²) prefix concatenation in `Tree` — 50k-deep chain: 67.85 s / 7.0 GB | open |
| [H3](#h3) | High | event | `meta rename` fails with a raw UNIQUE error on its primary use case | open |
| [H4](#h4) | High | cmd | `fngr add` hangs forever when stdin is an open, idle pipe | open |
| [H5](#h5) | High | cmd | `confirm` treats EOF as consent → non-interactive `meta rename` acts without `-f` | open |
| [M1](#m1) | Medium | event | Body-tag sync silently deletes operator-added meta | open |
| [M2](#m2) | Medium | event | Parent cycle → two non-terminating loops + a silent total data blackout | open |
| [M3](#m3) | Medium | event | Nothing enforces a single `author`; display picks whichever sorts first | open |
| [M4](#m4) | Medium | parse | Email addresses mint bogus `people` tags | ✅ fixed |
| [M5](#m5) | Medium | timefmt | `"1 month ago"` on the 31st lands in the wrong month; int64 overflow yields a *future* time | ✅ fixed |
| [M6](#m6) | Medium | render | Newlines and ANSI/OSC escapes in titles forge output rows | open |
| [M7](#m7) | Medium | event | FTS conflates content with metadata → body text forges tag matches | open |
| [M8](#m8) | Medium | render | `--limit` on tree format promotes orphaned children to roots, unmarked | open |
| [M9](#m9) | Medium | cmd | `fngr meta` output amplification: 1 MB stored → 202 MB printed | open |
| [M10](#m10) | Medium | parse | `". "` split eats abbreviations — `Dr. Smith` → title `Dr` | open |
| [M11](#m11) | Medium | db | `fngr add` never creates a project-local `.fngr.db`; first add lands in `~/.fngr.db` | open |
| [M12](#m12) | Medium | timefmt | `--from`/`--to` reject both relative forms and fngr's own emitted timestamps | open |
| [M13](#m13) | Medium | timefmt | DST spring-forward silently shifts `event time` to the prior hour | open |
| [M14](#m14) | Medium | event | `-n N -r` returns the N **oldest** events | open |
| [M15](#m15) | Medium | perf | Unbuffered stdout — one `write(2)` per event; 11-19% on large lists | open |
| [M16](#m16) | Medium | supply-chain | Release workflow: broad privileges on seven mutable-tag actions | open |

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
  [C3](#c3), which is already rewriting that file.

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
`--time` accepts it.

**Fix:** route both through `timefmt.ParsePartial`, flooring `--from` to 00:00
and ceilinging `--to` to 23:59:59 when the input is date-only. Add an
inverted-range warning on stderr.

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

---

## Low

Grouped; each is small and independently actionable.

**Output and formatting**

- Streaming JSON emits the separator `,` on its own line and a blank line
  before `]`. Valid JSON (`jq` parses it), but it looks broken and is
  un-diffable against the non-streaming form. `internal/render/render.go`.
- `event show --format=json` emits an **array** for a single event, so
  `jq '.title'` returns null and `jq '.[0].title'` is required. Consistent with
  round-trip intent, but nothing signals it.
- `--format` vocabularies diverge: `list` takes `tree|flat|json|csv|md`,
  `event show` takes `text|json|csv|md`. `fngr --format=text` and
  `fngr event 2 --format=flat` both fail with a full usage dump at exit 80.
  `--format=markdown` is rejected everywhere — only `md`.
- Empty results differ by format: tree and flat print nothing (tree sends
  `No events found.` to stderr — correct), json prints `[]`, csv prints a
  header row, md prints nothing. Fine for scripting; currently undiscoverable.
- `fngr meta` column alignment flips with `-S`: unfiltered pads the key
  (`author  =nico`), filtered doesn't (`tag=bugfix`). Same renderer, two looks.
- `render.Tree` on a limit-orphaned or cycle-broken set prints nothing at exit
  0 — see [M2](#m2) and [M8](#m8) for the underlying causes.

**Errors and exit codes**

- Three exit-code classes with no documented contract: `1` runtime/domain
  error, `80` Kong parse error (with a full usage dump), `2` Go panic. `80` is
  a Kong implementation detail leaking into the scripting interface.
- Kong parse errors dump the entire command list before the message — on
  `fngr -n -1` the actual error is 25 lines below the fold.
- `fngr -n -1` errors and *suggests* `--limit="-1"`; taking the suggestion
  silently means "no limit" at exit 0. The error's own remedy leads to an
  unvalidated path.
- Errors leak Go internals: `{"title":"t","meta":{"a":"b"}}` →
  `cannot unmarshal object into Go struct field jsonAddInput.meta of type
  [][2]string`, exposing a private type name. JSON `created_at` errors leak the
  Go reference-time layout.
- All `--db` failures surface as pragma errors — a directory, an unwritable
  path, and `--db ""` all give
  `cannot set pragma foreign_keys: unable to open database file (14)`; a
  non-SQLite file gives `cannot set pragma journal_mode: file is not a
  database (26)`. None say "is a directory" / "not a database file".
- Cycle errors double the sentinel text: `attaching event 3 to event 5 would
  form a cycle: would create a parent cycle`.
- `Renamed 1 occurrence(s)` / `Deleted 1 occurrence(s)` — pluralize properly.
- `fngr help bogus` → blank line, then `unexpected argument bogus`, with no
  list of valid commands.

**Behavior**

- `delete -r` under-reports: deleting a 3-node subtree prints `Deleted event 1`
  and the prompt names one event. State the count before asking and after
  acting.
- `event detach` on a parentless event prints `Detached event 1` at exit 0 with
  nothing done. Idempotent by design is fine; it reads as confirmation that
  work occurred.
- `--meta 'k='` is accepted, creating an empty-valued entry rendered as
  `k=  (1)`. `parse.FlagMeta` rejects an empty key but accepts an empty value
  and whitespace *inside* a key (`-m 'a b=c d'` stores a key that indexes as
  two FTS tokens and can't be searched back as written).
- `$PAGER` is tokenized with `strings.Fields` (`cmd/fngr/pager.go:71`) but
  `$EDITOR` is not (`cmd/fngr/body.go:126`), so `PAGER="less -R"` works while
  `EDITOR="code -w"` fails with `fork/exec …/code -w: no such file or
  directory`. The roadmap lists `$EDITOR` tokenization under "considered, not
  pursued" — but the *asymmetry* with `$PAGER` is new information.
- Editor temp file leaks on signal (`cmd/fngr/body.go:114`) — `defer
  os.Remove` doesn't run when fngr is killed mid-edit (Ctrl-C is the common
  case). Mitigated: mode `0600` in the per-user `$TMPDIR`.
- Search results never show why they matched. `-S kubernetes` on an event whose
  body mentions it displays only the title. FTS5 has `snippet()` built in.
- `fngr meta` has **no machine-readable output** (`--format` is rejected) — the
  one surface where you'd script tag audits or bulk renames.
- Filtering by author works via `-S 'author=nico'` but `-S '@nico'` returns
  nothing, because `--author` writes key `author` while `@x` writes `people`.
  Every user will try `@` first; it isn't documented.

**Tooling**

- **`make bench` runs no benchmarks** — the repo has zero `Benchmark*`
  functions. All six packages report `PASS` with only tests. Worth knowing if
  the target is assumed to be doing something.

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
- **`main.go:85` — `defer database.Close()` never runs.** `ctx.FatalIfErrorf`
  at `:94` calls `os.Exit`, which skips deferred functions. Harmless today
  (process exit closes the fd, and WAL checkpoints on clean close are
  best-effort anyway), but it's a latent trap if cleanup ever grows teeth. The
  fix is the standard `func main() { os.Exit(run()) }` shape.
- **Behavioral logic encoded as command-name string prefixes** —
  `strings.HasPrefix(ctx.Command(), "help")` at `main.go:75` and
  `strings.HasPrefix(ctx.Command(), "add")` at `:80` decide whether a DB is
  needed and whether to create it. Renaming a command silently changes DB
  semantics, and `main.go` is in `.covignore`, so nothing tests it. A
  `dbPolicy` method on the command structs (or a Kong tag) would make this
  declarative and testable.
- **`wrapFilterErr` string-matches driver error text** from the cmd layer
  (`list.go:64-68`, matching `"fts5"`, `"SQL logic error"`, `"unterminated"`).
  This was the right call when shipped, but [C5](#c5)'s parser rewrite makes it
  obsolete: a real parser returns a typed error and the cmd layer can
  `errors.As` it. Delete `wrapFilterErr` as part of that change.
- **`list.go:30` writes the pager warning to `os.Stderr` directly**, bypassing
  the injected `io.Err` and defeating the `ioStreams` abstraction in tests.
- **Inconsistent empty-result messaging in `list.go`** — the tree branch prints
  `No events found.` to `io.Err` (`:45`); the streaming branch prints nothing.
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
- The "Considered (not pursued)" entry for bulk operations rests on
  `fngr -S … --format json | jq | xargs`, which doesn't work for metadata
  (`fngr meta` has no `--format`). Footnote it or fix the gap.

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
| Recursive-CTE rewrite of `event.Reparent`'s ancestry loop | SQLite is in-process; per-row `SELECT parent_id` calls are microseconds. The loop is clearer for the cycle-detection semantics. **Note:** [M2](#m2) asks for a *bound* on that loop, not a rewrite. |
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
- **`cmd/fngr/dispatch_test.go`** — every new verb or flag needs an entry.
  `-e` on `event body`/`event text` ([recommendation 9](#feature-recommendations))
  would need the per-case `launchEditor` swap pattern currently scoped to
  `add-editor`.
- **`.github/workflows/`** — [M16](#m16). When pinning to SHAs, note that
  `sigstore/cosign-installer` stays on `@v3` deliberately (v4 has a real
  behavior break — see roadmap).
- **`docs/PUBLISHING.md`** — the "Gotchas" section is the institutional memory
  of the v0.0.1 rollout. Add to it when shipping a sibling repo.
