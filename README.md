# fngr

A command-line journal for logging and tracking events. Events support
parent-child trees, key-value metadata with `@person` and `#tag` shorthands,
and full-text search.

Data is stored in a single SQLite file (pure-Go, no CGo).

## Install

```
go install github.com/monolithiclab/fngr/cmd/fngr@latest
```

Or build from source:

```
make build        # binary at build/fngr
make install      # installs to $GOBIN
```

## Quick start

```
# Add events
fngr add "deployed v2.3 to prod #ops @sarah"
fngr add "fixed login bug #bugfix" --meta env=prod

# Add a child event
fngr add "rollback needed" --parent 1

# Override the author (defaults to $FNGR_AUTHOR or $USER)
fngr add "ad-hoc note" --author sarah

# Backdate an event (date, datetime, or time-of-day with today's date)
fngr add "yesterday's standup" --time 2026-04-16
fngr add "earlier this morning"  --time 09:30
fngr add "after lunch sync"      --time 2:15PM

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

# Output formats
fngr --format flat
fngr --format json
fngr --format csv

# Show a single event (bare form is shorthand for `event show N`)
fngr event 1
fngr event 1 --tree         # with children
fngr event 1 --format json

# Edit text (body @person/#tag tags are synced — old ones removed, new ones added)
fngr event text 1 "fixed wording for @sarah #urgent"

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
fngr meta rename tag=wip tag=done   # or '#wip' '#done'
fngr meta delete tag=obsolete       # or '#obsolete'

# Print version
fngr --version
```

## Filter syntax

Filters are passed via the `-S` / `--search` flag (`fngr -S '#ops'`,
`fngr meta -S tag=wip`). The same syntax applies wherever a `-S` flag is
accepted:

| Syntax      | Meaning                     |
| ----------- | --------------------------- |
| `word`      | Full-text search            |
| `#tag`      | Events with `tag=tag`       |
| `@person`   | Events with `people=person` |
| `key=value` | Events with exact metadata  |
| `a & b`     | Both conditions (AND)       |
| `a \| b`    | Either condition (OR)       |
| `!a`        | Exclude condition (NOT)     |

`!` binds to the immediately following term (`!#bugfix` excludes events tagged
`bugfix`); parentheses for grouping are not supported.

Metadata from `@person` and `#tag` in event text is extracted automatically and
stored separately from body text, so `#deploy` only matches the tag, not the
word "deploy" in the body.

## Database location

Resolved in order:

1. `--db` flag or `$FNGR_DB`
2. `.fngr.db` in the current directory
3. `~/.fngr.db`

The database is created automatically on the first `fngr add`.
