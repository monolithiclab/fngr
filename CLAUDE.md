# CLAUDE.md

## Project

fngr is a CLI tool for logging and tracking events, written in Go. It uses Kong for CLI parsing and
modernc.org/sqlite (pure-Go, CGo-free) for storage. Events support parent-child trees, key-value
metadata, and FTS5 full-text search.

## Commands

```bash
make build          # Build binary to build/fngr
make test           # Run tests with -race, -cover, coverage report
make lint           # Run all linters (gofmt, vet, staticcheck, golangci-lint, gosec, gocritic)
make format         # Format source code
make bench          # Run benchmarks
make ci             # codefix + format + lint + test
```

> Always use make targets for linting, testing, building, etc.

## Architecture

- `cmd/fngr/main.go` — Entrypoint. Wires Kong CLI parsing, resolves DB path, opens DB, dispatches to
  command handlers.
- `cmd/fngr/{add,list,show,delete,meta}.go` — Kong command structs with `Run(*sql.DB)` methods, one
  file per command. Explicit `fngr add` required (no default command).
- `internal/db/db.go` — DB path resolution (explicit > `.fngr.db` in cwd > `~/.fngr.db`), connection
  setup (FK + WAL + busy_timeout + synchronous=NORMAL), schema initialization (events, event_meta,
  events_fts, triggers, indexes).
- `internal/parse/parse.go` — `Meta` type (Key/Value struct), body-tag extraction (`@person` →
  people, `#tag` → tag), flag metadata parsing (`--meta key=value`), FTS content building, date
  format constants.
- `internal/event/meta.go` — Domain meta key constants (`MetaKeyAuthor`, etc.), `CollectMeta` merges
  all meta sources (author, body tags, flags) with dedup.
- `internal/event/event.go` — Data access: `Add` (transactional insert of event + meta + FTS), `Get`,
  `Delete`, `HasChildren`, `ListMeta`, `UpdateMeta`, `DeleteMeta`, `List` (FTS5 filtering + date
  range), `GetSubtree` (recursive CTE). All functions accept `context.Context`. `ErrNotFound`
  sentinel error for programmatic not-found checks.
- `internal/event/filter.go` — Filter expression preprocessor: expands `#`/`@` shorthands and
  `&`/`|`/`!` operators into FTS5 MATCH syntax. Escapes embedded double quotes in FTS5 phrases.
- `internal/render/render.go` — Output rendering to `io.Writer`: `Tree` (ASCII tree), `Flat`, `JSON`,
  `CSV`, `Event` (single event detail). Human-readable formats display local time; machine formats
  (JSON, CSV) use UTC.

## Conventions

- Tests use in-memory SQLite via `testDB(t)` / `testDBWithSchema(t)` helpers in `internal/db/db_test.go`
  and `internal/event/event_test.go`. No test fixtures on disk.
- Tests should be parallelized
- Table-driven tests with `t.Run` subtests.
- Use modern Go idioms and features
- Try hard to prevent duplicated code
- Version injected via `-ldflags` at build time from git tags.
- `.covignore` excludes `cmd/fngr/main.go` from coverage.
- `common-go.mk` is shared across repos — don't modify it here.
