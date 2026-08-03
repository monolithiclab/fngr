# fngr

A command-line journal for logging and tracking events. Events support
parent-child trees, key-value metadata with `@person` and `#tag` shorthands,
and full-text search.

Data is stored in a single SQLite file (pure-Go, no CGo).

## Install

### Homebrew (macOS / Linux)

```
brew install monolithiclab/tap/fngr
```

### Go install

```
go install github.com/monolithiclab/fngr/cmd/fngr@latest
```

### Pre-built binaries

Download the right tarball for your OS/arch from the
[releases page](https://github.com/monolithiclab/fngr/releases).
SHA256 checksums and cosign signatures are attached to every release;
verify with:

```
cosign verify-blob \
  --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem \
  --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  SHA256SUMS
sha256sum -c SHA256SUMS
```

### Build from source

```
make build        # binary at build/fngr
make install      # installs to $GOBIN
```

## Container usage

A multi-arch (`linux/amd64` + `linux/arm64`) container image is
published on every release at `ghcr.io/monolithiclab/fngr:<version>`
and (for stable releases only) `ghcr.io/monolithiclab/fngr:latest`.
The image is ~6 MB, distroless-static-debian13 base.

### The database is on the host — you must mount it

`fngr` stores everything in a single SQLite file. The container has
no persistent storage of its own, so without a volume mount **every
run starts with an empty DB**. Mount your existing DB into the
container and point `FNGR_DB` at it:

```
docker run --rm \
  -v "$HOME/.fngr.db:/data/fngr.db" \
  -e FNGR_DB=/data/fngr.db \
  ghcr.io/monolithiclab/fngr:latest
```

The mount target (`/data/fngr.db` above) is arbitrary — pick anything
inside the container, just keep `$FNGR_DB` pointed at it. The
container runs as root by default so the file's host permissions
must allow root access (UID 0). For non-root host UIDs, pass
`--user "$(id -u):$(id -g)"`.

### Timezone

The container has `/usr/share/zoneinfo` baked in, so any IANA name
works via `TZ`. If unset, `fngr`'s local-time rendering (markdown
date headers, event detail) defaults to UTC.

```
docker run --rm \
  -v "$HOME/.fngr.db:/data/fngr.db" \
  -e FNGR_DB=/data/fngr.db \
  -e TZ=America/New_York \
  ghcr.io/monolithiclab/fngr:latest --format=md
```

### Common workflows

```
# Add an event
docker run --rm -v "$HOME/.fngr.db:/data/fngr.db" -e FNGR_DB=/data/fngr.db \
  ghcr.io/monolithiclab/fngr:latest add "deployed v1.2 to staging #ops"

# Bulk import from JSON (read from a host file)
cat events.json | docker run --rm -i \
  -v "$HOME/.fngr.db:/data/fngr.db" -e FNGR_DB=/data/fngr.db \
  ghcr.io/monolithiclab/fngr:latest add --format=json

# Round-trip between two databases via stdout pipe
docker run --rm -v "$HOME/src.db:/data/src.db" -e FNGR_DB=/data/src.db \
  ghcr.io/monolithiclab/fngr:latest --format=json \
  | docker run --rm -i -v "$HOME/dst.db:/data/dst.db" -e FNGR_DB=/data/dst.db \
    ghcr.io/monolithiclab/fngr:latest add --format=json

# Verify the image's signature with cosign
cosign verify ghcr.io/monolithiclab/fngr:0.0.1 \
  --certificate-identity-regexp 'https://github.com/monolithiclab/fngr' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```

### Limitations

The container is for scripted, non-interactive use. The following
don't work inside the image:

- `fngr add -e` (editor mode) — needs `$EDITOR` plus a binary in the
  image; neither exists in distroless-static.
- The pager on `fngr list` — needs `less` + a TTY; pass `--no-pager`
  or pipe to a host-side pager.
- Confirmation prompts on `delete` / `meta delete` — no TTY; pass
  `-f` to skip the prompt.

## Quick start

```
# Add events — multi-arg body, quote tags so the shell doesn't treat # as a comment
fngr add deployed v1.2 to staging '#ops' @alice
fngr add "fixed login bug #bugfix" --meta env=prod

# From a pipe
echo "build broken on main" | fngr add

# From a file
fngr add < notes.md

# Open $VISUAL or $EDITOR (also auto-launches on bare `fngr add` in a TTY)
fngr add -e

# Add a child event
fngr add "rollback needed" --parent 1

# Override the author (defaults to $FNGR_AUTHOR or $USER)
fngr add "ad-hoc note" --author sarah

# Backdate an event (date, datetime, or time-of-day with today's date)
fngr add "yesterday's standup" --time 2026-04-16
fngr add "earlier this morning"  --time 09:30
fngr add "after lunch sync"      --time 2:15PM

# Relative times work everywhere a timestamp is accepted: now, today,
# yesterday, "N {minutes,hours,days,weeks,months} ago", and "<day> at <time>".
# A bare relative day keeps the current time of day.
fngr add "deployed"  --time "2 days ago"
fngr add "incident"  --time "yesterday at 9am"
fngr add "quick fix" --time "15 minutes ago"

# Or write the time inline as a "<time>: <note>" prefix — parsed and stripped
# from the title (skipped when --time is given). The delimiter is colon+space,
# so times like 9:30 are not mistaken for it.
fngr add "9:30: had coffee"              # title "had coffee" at 09:30 today
fngr add "3pm: lunch with @sam"          # title "lunch with @sam" at 15:00 today
fngr add "2026-04-15: trip"              # title "trip" on that date
fngr add "yesterday at 9am: backfill"    # title "backfill", yesterday 09:00

# Bulk import a single event from JSON (title + optional body)
echo '{"title":"hi","body":"","meta":[["tag","ops"]]}' | fngr add --format=json

# Bulk import an array of events (atomic; any error rolls back the batch)
fngr add --format=json < events.json

# Round-trip via stdout pipe (e.g. copy events between databases). The target
# assigns its own ids; parent/child links are rewritten to match, so the tree
# survives even though the numbers change.
fngr --db src.db --format=json | fngr --db dst.db add --format=json

# Default command — list everything (newest first, tree view, paginated on TTY)
fngr

# Explicit subcommand (same behaviour)
fngr list

# Filter with tags, people, bare words (use -S, like git log)
fngr -S '#ops'
fngr -S '@sarah & #ops'
fngr -S 'deploy | rollback'
fngr -S '!#bugfix'

# Date ranges
fngr --from 2026-04-01 --to 2026-04-15

# Pagination and sort order
fngr -n 20             # at most 20 events
fngr -r                # oldest first (default is newest first)
fngr --no-pager        # don't pipe through $PAGER even on a TTY

# Output formats. JSON is the only round-trip format
# (`fngr --format=json | fngr add --format=json`); flat / csv / md
# are output-only and lossy.
fngr --format flat
fngr --format json
fngr --format csv

# Markdown digest — local-date sections + bullet entries; multi-line
# bodies and meta render as 2-space-indented continuation lines.
# Designed for paste-into-wiki workflows.
fngr --format=md
fngr --from 2026-04-15 --to 2026-04-22 --format=md > week.md

# Show a single event (bare form is shorthand for `event show N`)
fngr event 1
fngr event 1 --tree         # with children
fngr event 1 --format json

# Edit text — re-splits on '. ' into title + body
# (@person/#tag tags are synced — old ones removed, new ones added)
fngr event text 1 "fixed wording. for @sarah #urgent"

# Or set just the title (body untouched)
fngr event title 1 "fixed wording"

# Or set just the body (title untouched; empty arg clears body)
fngr event body 1 "for @sarah #urgent"

# Edit clock time (date preserved) or full timestamp (replaces both)
fngr event time 1 "09:30"
fngr event time 1 "2026-04-15T09:30"

# Edit date (clock preserved) or full timestamp (replaces both)
fngr event date 1 "2026-05-01"

# Re-parent / detach
fngr event attach 2 1
fngr event detach 2

# Add or remove tags (n args; @person, #tag, or key=value)
fngr event tag 1 "@sarah" "#urgent" "env=prod"
fngr event untag 1 "#urgent"

# Delete (prompts for confirmation)
fngr delete 3
fngr delete 1 -r            # recursive: delete with children
fngr delete 3 -f            # skip confirmation

# Metadata
fngr meta                   # list every key=value pair with counts
fngr meta -S tag            # filter: every tag=*
fngr meta -S tag=wip        # filter: exact key=value
fngr meta -S '@sarah'       # filter: shorthand for people=sarah
fngr meta -S '#ops'         # filter: shorthand for tag=ops
fngr meta rename tag=wip tag=done   # or '#wip' '#done'; merges if the target exists
fngr meta delete tag=obsolete       # or '#obsolete'

# Show help (alias for --help; takes a command path, including verb trees)
fngr help
fngr help add
fngr help event show

# Print version
fngr --version
```

## Filter syntax

Event filters are passed via the `-S` / `--search` flag (`fngr -S '#ops'`):

| Syntax      | Meaning                     |
| ----------- | --------------------------- |
| `word`      | Full-text search            |
| `#tag`      | Events with `tag=tag`       |
| `@person`   | Events with `people=person` |
| `key=value` | Events with exact metadata  |
| `word*`     | Prefix search               |
| `a & b`     | Both conditions (AND)       |
| `a b`       | Both conditions (AND)       |
| `a \| b`    | Either condition (OR)       |
| `!a`        | Exclude condition (NOT)     |

`!` binds tightest, then `&`, then `|` — so `#home | #bugfix & #work` means
`#home` or (`#bugfix` and `#work`). Parentheses for grouping are not supported.
`!` applies to the one term that follows it, and operand order does not change
the meaning: `!#bugfix & #work` and `#work & !#bugfix` are the same query.

Terms are matched literally, so punctuation needs no escaping —
`fngr -S session-handler` and `fngr -S 'v1.2'` search for themselves. `&`, `|`
and a leading `!` are the only reserved characters; a `!` anywhere else is part
of the term (`fngr -S 'wow!'`). There is no escape hatch, so a word that
genuinely starts with `!` cannot be searched for: `-S '!important'` excludes
"important" rather than looking for it.

Metadata from `@person` and `#tag` in an event's title or body is extracted
automatically and stored separately from the text, so `#deploy` only matches the
tag, not the word "deploy" in the body.

Names may contain any Unicode letter or digit plus `_`, `/` and `-`, so `@josé`,
`@田中` and `#déploiement` are stored whole. A sigil only opens a tag at the
start of the text or after a non-name character, so `bob@example.com` and
`https://example.com/guide#installation` do not mint metadata.

`fngr meta -S` is a different, narrower filter: exactly one `#tag`, `@person`,
`key=value` or bare key, with no operators and no full-text search. It selects
which metadata rows to *list*, not which events to match.

## JSON import format

`fngr add --format=json` reads a single object or an array of them. Only
`title` is required; unknown fields are an error, so a typo surfaces instead of
being silently dropped.

```json
{
  "id": 3,
  "parent_id": 2,
  "title": "rollback needed",
  "body": "reverted the deploy",
  "created_at": "2026-04-01T12:00:00Z",
  "meta": [["author", "nico"], ["tag", "ops"]]
}
```

`id` is never inserted — the target database assigns its own. It exists so that
`fngr --format=json` output pipes back in unchanged, and so `parent_id` can
point at another record in the same batch. **A `parent_id` matching an `id` in
the batch is resolved to that record's new id**, in either direction: the
default newest-first export lists children before their parents. A `parent_id`
matching nothing in the batch is an id in the *target* database, which is how
you graft an import onto an existing tree; if no such event exists, the whole
batch is rejected.

`created_at` accepts everything `--time` does, not just the RFC 3339 stamps
that `--format=json` emits. Omitted fields fall back to the corresponding CLI
flag (`--parent`, `--time`, `--meta`, `--author`) and then to the built-in
default. An explicit `meta` array replaces the `--meta` flags rather than
adding to them, and an `author` entry there overrides `--author` for that
record. Every record must end up with an author from some source. Batches are
capped at 10 000 records and are atomic — any error rolls back the whole thing.

## Database location

Resolved in order:

1. `--db` flag or `$FNGR_DB`
2. `.fngr.db` in the current directory
3. `~/.fngr.db`

The database is created automatically on the first `fngr add`.

## Troubleshooting

**`database is locked`** — another process has the file open with a
write lock (a long-running `fngr add -e` editor session, a
manually-opened SQLite shell, a desktop file-sync agent, etc.). Close
the other holder and retry. fngr opens the DB with WAL +
`busy_timeout=5000ms`, so transient locks self-heal; only a stuck
holder produces this error.

**`database disk image is malformed` / corruption** — restore from
backup. The DB is a single SQLite file with no auxiliary state; a
backup is just a copy:

```
# Take a backup before any risky operation
cp ~/.fngr.db ~/.fngr.db.bak

# Restore
mv ~/.fngr.db.bak ~/.fngr.db
```

If you have no backup, the canonical SQLite recovery dance applies:

```
sqlite3 ~/.fngr.db ".recover" | sqlite3 ~/.fngr.db.recovered
mv ~/.fngr.db.recovered ~/.fngr.db
```

If recovery fails too, delete the file (`rm ~/.fngr.db`) and start
fresh — `fngr add` will re-create the schema.

**An event dated `Jan 01 0001`** — its stored timestamp is unreadable.
Versions before v0.0.3 accepted absurd relative offsets (`fngr add x -t
'1000000 days ago'`) and wrote a negative-year timestamp; such a row used
to make *every* read command fail. It now shows as the zero date instead,
so the row stays usable: repair it with `fngr event date <id> <date>`
followed by `fngr event time <id> <time>` (the date verb keeps the old
time-of-day, which is also meaningless here), or just delete it. New
input is rejected at parse time.

**`event title cannot be empty`** — `fngr add` (no args, no piped
stdin) launches `$VISUAL` / `$EDITOR`; saving an empty buffer is
treated as a cancel. To force an empty event, that's not supported
by design.

**`set $EDITOR or $VISUAL` from `fngr add -e`** — the editor mode
needs an editor binary on `PATH`. `export EDITOR=vim` (or whichever)
in your shell rc.

**`invalid filter syntax: ... (see --help for the -S grammar)`** —
your `-S` expression is malformed, and the message names the rune
position. It is almost always a dangling operator: `-S '#ops &'` or
`-S '!'`. Punctuation inside a term is not a cause — terms are
matched literally. The full grammar is in the
[Filter syntax](#filter-syntax) section above.

**Pager misbehavior on `fngr list`** — pass `--no-pager` to bypass.
The default pager is `$PAGER` (fallback `less -FRX`); `less -F`
quits-if-fits-on-screen, so very short result sets don't trigger
the pager at all.

**`fngr --version` shows `dev-<sha>`** — you built from source
without a tag. Either install a tagged release (`brew install
monolithiclab/tap/fngr`, `go install ...@v0.0.1`) or build from a
checkout that has tags reachable.

**Container fails to find the database** — the container has no
persistent storage of its own. See [Container usage](#container-usage)
above for the volume-mount pattern; without it, every `docker run`
starts fresh.
