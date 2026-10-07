# fngr — CLI Event Logger Design Spec

## Overview

fngr is a CLI tool for logging and tracking events, built in Go. Events are free-form timestamped text entries with optional tags/metadata, an author, and support for arbitrary-depth parent-child hierarchies.

## Data Model

Two tables in SQLite:

**`events`** — the core event log:

| Column       | Type                | Description                                   |
|--------------|---------------------|-----------------------------------------------|
| `id`         | INTEGER             | Primary key (autoincrement)                   |
| `parent_id`  | INTEGER (nullable)  | FK to `events.id` — null means top-level      |
| `text`       | TEXT                | Free-form entry body                          |
| `created_at` | DATETIME            | When the event was created                    |

**`event_meta`** — unified key-value metadata for all events:

| Column     | Type    | Description                          |
|------------|---------|--------------------------------------|
| `event_id` | INTEGER | FK to `events.id` (ON DELETE CASCADE)|
| `key`      | TEXT    | Metadata key (e.g., `author`, `tag`) |
| `value`    | TEXT    | Metadata value                       |

Index on `(key, value)` for fast filtering. Source of truth for structured metadata queries (e.g., listing all keys/values with counts).

**`events_fts`** — FTS5 virtual table for full-text search:

```sql
CREATE VIRTUAL TABLE events_fts USING fts5(
    content,
    tokenize = 'unicode61 tokenchars "=/"'
);
```

The `content` column stores the event body text concatenated with all metadata as `key=value` pairs:

```
Had a great meeting author=nicolas tag=planning people=team
```

With `tokenchars "=/"`, the `=` and `/` characters are preserved within tokens, so `tag=planning` is indexed as a single token. This means:
- Searching for `tag=planning` matches only the metadata, not the word "planning" in the body.
- Searching for `planning` (bare word) matches only the body text, not metadata.
- Hierarchical tags like `tag=work/project-x` are indexed as one token.

Kept in sync with `events` and `event_meta` via SQLite triggers on insert/delete.

### Metadata

All metadata — author, tags, people, and arbitrary key-value pairs — is stored uniformly in `event_meta`. There are no dedicated columns for author or tags.

**Body shorthand expansion:** Tags in the event text are parsed and stored as metadata:
- `#planning` → `tag=planning`
- `@sarah` → `people=sarah`
- Parsed with regex: `#[\w/]+` and `@[\w/]+`.
- Hierarchical namespacing is a tag convention (e.g., `#work/project-x`).
- Duplicates are deduplicated.

**CLI flag mapping:**
- `--author nicolas` → `author=nicolas`
- `--meta env=prod` → `env=prod`

**Author** is required metadata. Enforced at the application level (not as a DB constraint). Resolution order: `--author` flag > `FNGR_AUTHOR` env var > `$USER`.

### Tree Structure

- `parent_id` forms an arbitrary-depth parent-child tree.
- Tree queries use SQLite recursive CTEs.
- Deleting a parent cascades to all descendants via `ON DELETE CASCADE`.

## CLI Structure

Built with [Kong](https://github.com/alecthomas/kong) (struct-based parsing). Most flags (like `--db`, `--author`) are also bindable via environment variables.

### Commands

```
fngr "Deploy achieved"                    # implicit add (default subcommand)
fngr add "Had a great meeting @team #planning" [--author NAME] [--parent ID] [--meta key=value ...]
fngr list [FILTER_EXPR] [--from DATE] [--to DATE] [--format table|json|csv] [--tree|--no-tree]
fngr show <id> [--tree]
fngr delete <id>
fngr meta
```

**`add`** (default) — Creates an event. If no subcommand is given, `add` is assumed (e.g., `fngr "Deploy achieved"`). Metadata is collected from three sources: body shorthands (`#tag`, `@person`), `--meta key=value` flags (repeatable), and `--author`. `--parent` attaches it as a child of an existing event.

**`list`** — Lists events with filtering via a filter expression (see below). Default output is a human-readable table. `--format json` for machine consumption. Supports `--tree` (default) / `--no-tree`. In tree mode, child events are shown indented under their parents using ASCII graph characters (like `git log --graph`). In no-tree mode, events are listed flat chronologically.

### Filter Expressions

The `list` command accepts an optional filter expression as a positional argument. Expressions combine terms with logical operators, evaluated left-to-right (no precedence, no parentheses).

**Terms:**
- `key=value` — metadata match (e.g., `tag=deploy`, `author=nicolas`)
- `#value` — shorthand for `tag=value`
- `@value` — shorthand for `people=value`
- bare word — full-text search on the event body (e.g., `project`, `daily`)

**Operators:**
- `&` — AND
- `|` — OR
- `!` — NOT (prefix, applies to the next term)

**Examples:**
- `"tag=deploy & project"` — events with metadata `tag=deploy` AND body containing `project`
- `"@user & !daily"` — events with metadata `people=user` AND body NOT containing `daily`
- `"#ops | #deploy"` — events tagged `ops` OR `deploy`
- `"author=nicolas & #work & !meeting"` — events by nicolas, tagged `work`, body not containing `meeting`

**Implementation:** Filter expressions are preprocessed (expand `#` → `tag=`, `@` → `people=`, `&` → `AND`, `|` → `OR`, `!` → `NOT`) and forwarded to FTS5 as a `MATCH` query. The FTS5 tokenizer preserves `=` and `/` within tokens, so metadata and body terms are naturally isolated.

**`show <id>`** — Displays a single event with its metadata. `--tree` renders the full subtree of child events beneath it.

**`delete <id>`** — Removes an event. Deleting a parent also deletes its children (cascade via `ON DELETE CASCADE` on both `events.parent_id` and `event_meta.event_id`).

**`meta`** — Lists all unique metadata keys and their values, with counts.

### Tree Output Example

```
1   2026-04-10  nicolas  Sprint 12 #work
├─ 2   2026-04-10  nicolas  Planning meeting @team #planning
│  └─ 4   2026-04-10  nicolas  Decided on architecture #planning
└─ 3   2026-04-11  nicolas  Deploy v2.0 #ops
5   2026-04-12  nicolas  Lunch with Sarah @social
```

### Database Location

Resolution order:
1. `--db` flag or `FNGR_DB` env var (explicit)
2. `.fngr.db` in the current directory (project-local)
3. `~/.fngr.db` (user-global fallback)

If no database exists, `add` creates it at the resolved path (explicit path if provided, otherwise `~/.fngr.db`). Other commands error with a clear message if no database is found.

### Environment Variables

Most CLI parameters can be set via environment variables. Convention: `FNGR_<PARAM>` (e.g., `FNGR_AUTHOR`, `FNGR_DB`).

## Project Structure

```
fngr/
├── cmd/
│   └── fngr/
│       └── main.go          # Entry point, Kong wiring, version var
├── internal/
│   ├── cli.go               # Kong command structs and flag definitions
│   ├── db.go                # SQLite connection, schema init, migrations
│   ├── event.go             # Event CRUD operations
│   ├── meta.go              # Metadata parsing (body shorthands, flags) and queries
│   ├── filter.go            # Filter expression preprocessing → FTS5 MATCH queries
│   └── tree.go              # Tree rendering (ASCII graph output)
├── common-go.mk             # Shared Go Makefile targets (lint, format, codefix, etc.)
├── Makefile                  # Project Makefile (build, test, bench, ci, etc.)
├── .covignore                # Patterns to exclude from coverage reports
├── go.mod
└── go.sum
```

## Dependencies

- `github.com/alecthomas/kong` — CLI parsing
- `modernc.org/sqlite` — pure Go SQLite driver (no CGO)

## Error Handling

- **Invalid parent ID**: `add --parent 999` errors if the parent event doesn't exist.
- **Empty text**: `add` requires non-empty text.
- **Missing DB**: Commands other than `add` error with a clear message if no database is found.

## Build & Quality

The project uses a Makefile (with shared `common-go.mk` targets) for all build, test, and quality operations.

| Command | Purpose |
|---|---|
| `make build` | Build binary to `build/fngr` (version injected via ldflags) |
| `make install` | Install to `$GOBIN` |
| `make test` | Run tests with race detection, coverage profile, and `.covignore` filtering |
| `make lint` | Run all linters: gofmt, vet, staticcheck, golangci-lint, gosec, gocritic |
| `make format` | Auto-format source code |
| `make codefix` | Apply `go fix` for latest Go best practices |
| `make ci` | Full quality gate: codefix + format + lint + test |
| `make bench` | Run benchmarks with memory stats |
| `make clean` | Remove build artifacts and coverage files |

**Version**: `main.version` is injected at build time via `-ldflags` from `git describe --tags --always --dirty`.

## Testing

- **Unit tests** for metadata parsing (body shorthands, flag mapping, deduplication).
- **Unit tests** for filter expression preprocessing (shorthand expansion, operator mapping, edge cases).
- **Unit tests** for tree rendering (flat list, nested tree, deep nesting, ASCII output).
- **Integration tests** for the DB layer using in-memory SQLite (`:memory:`). Cover CRUD operations, cascade deletes, recursive CTE queries, FTS5 search and sync triggers.
- No CLI-level end-to-end tests for now — test the internals directly.
- Run via `make test` (includes race detection and coverage profiling).
- **Target code coverage: 90%.** Coverage is reported by `make test` with `.covignore` exclusions applied.
- **Quality gate**: `make ci` must pass (codefix + format + lint + test).

## Future Work (Not In Scope)

- Timeline UI served over HTTP/HTML.
- Webhooks — configured in a `webhooks` table in the schema. A webhook has a `name`, a `matched_event` (only `add` initially), a `filter` (using the same filter expression syntax as `list` / FTS5 MATCH), and a `url` to POST the event to when triggered.
