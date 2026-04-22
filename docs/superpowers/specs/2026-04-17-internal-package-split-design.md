# Internal Package Split Design

Split the flat `internal/` package into focused sub-packages with idiomatic Go naming.

## Motivation

The current `internal` package bundles database setup, domain logic, text parsing, and output
rendering in a single namespace. This produces verbose call sites (`internal.AddEvent`,
`internal.RenderTree`) and makes it harder to reason about dependency boundaries. Splitting into
sub-packages gives cleaner naming (`event.Add`, `render.Tree`) and explicit dependency direction.

## Package Layout

```
internal/
├── db/
│   └── db.go
├── event/
│   ├── event.go
│   ├── meta.go
│   └── filter.go
├── parse/
│   └── parse.go
└── render/
    └── render.go
```

Each package gets corresponding `_test.go` files.

## Package Responsibilities

### `internal/db`

Database path resolution, connection setup, and schema initialization.

**Exports:**

| Symbol | Old Name |
|--------|----------|
| `ResolvePath(explicit string) (string, error)` | `ResolveDBPath` |
| `Open(path string, create bool) (*sql.DB, error)` | `OpenDB` |

`initSchema` remains unexported — called by `Open`.

**Dependencies:** None (leaf package).

### `internal/event`

Domain types and all data access operations. Also contains filter preprocessing since it is only
used internally by `List`.

**Types:**

| Symbol | Notes |
|--------|-------|
| `Event` | Struct: ID, ParentID, Text, CreatedAt, Meta (where Meta is `parse.Meta`) |
| `MetaCount` | Struct: Key, Value, Count |
| `ListOpts` | Struct: Filter, From, To |
| `ErrNotFound` | Sentinel error variable |
| `MetaKeyAuthor`, `MetaKeyPeople`, `MetaKeyTag` | String constants (domain semantics over `parse.Meta` keys) |

**Functions:**

| Symbol | Old Name |
|--------|----------|
| `Add(ctx, db, text, parentID, meta, createdAt)` | `AddEvent` |
| `Get(ctx, db, id)` | `GetEvent` |
| `Delete(ctx, db, id)` | `DeleteEvent` |
| `List(ctx, db, opts)` | `ListEvents` |
| `GetSubtree(ctx, db, rootID)` | `GetSubtree` |
| `HasChildren(ctx, db, id)` | `HasChildren` |
| `CollectMeta(text, flags, author)` | `CollectMeta` |
| `UpdateMeta(ctx, db, oldKey, oldValue, newKey, newValue)` | `UpdateMeta` |
| `DeleteMeta(ctx, db, key, value)` | `DeleteMeta` |
| `ListMeta(ctx, db)` | `ListMeta` |

`PreprocessFilter`, `scanEvents`, `loadMetaBatch` remain unexported within the package.

**Dependencies:** `internal/parse`.

### `internal/parse`

Pure text parsing utilities, format constants, and the `Meta` data type. No database dependencies.

**Types:**

| Symbol | Notes |
|--------|-------|
| `Meta` | Struct: Key, Value. Plain data struct, no domain logic. |

**Functions:**

| Symbol | Old Name |
|--------|----------|
| `BodyTags(text string) []Meta` | `ParseBodyTags` |
| `FlagMeta(flags []string) ([]Meta, error)` | `ParseFlagMeta` |
| `FTSContent(text string, meta []Meta) string` | `BuildFTSContent` |
| `DateFormat` | `dateFormat` (was unexported) |
| `DateTimeFormat` | `dateTimeFormat` (was unexported) |

**Dependencies:** None (leaf package).

### `internal/render`

Output rendering to `io.Writer`. Handles tree, flat, JSON, CSV, and single-event detail formats.

**Exports:**

| Symbol | Old Name |
|--------|----------|
| `Tree(w, events)` | `RenderTree` |
| `Flat(w, events)` | `RenderFlat` |
| `JSON(w, events)` | `RenderJSON` |
| `CSV(w, events)` | `RenderCSV` |
| `Event(w, event)` | `RenderEvent` |

`renderNode`, `formatLocalDate`, `formatLocalDateTime`, `metaValue`, `eventAuthor`, `jsonEvent`
remain unexported.

**Dependencies:** `internal/event` (for `Event` and `Meta` types), `internal/parse` (for format
constants).

## Dependency Graph

```
cmd/fngr  →  db, event, render
render    →  event, parse
event     →  parse
db        →  (nothing internal)
parse     →  (nothing internal)
```

No circular dependencies. `Meta` is defined in `parse` (plain data struct with no domain logic).
`event` imports `parse.Meta` and adds domain semantics via the `MetaKey*` constants. `render`
imports both `event` (for `Event` type) and `parse` (for format constants and `Meta` type).

## Test Strategy

- Each package has its own `_test.go` files with table-driven subtests.
- `db/` tests use in-memory SQLite (`:memory:`).
- `event/` tests need a database with schema. Options:
  - Export a test helper from `db/` (e.g., `db.OpenTest(t)`) that returns an in-memory DB with
    schema already initialized.
  - Inline setup in event tests calling `db.Open(":memory:", true)`.
  - Prefer the first approach to avoid duplication.
- `parse/` tests are pure — no database needed.
- `render/` tests create events in-memory and verify output formatting.
- All tests remain parallelized.

## CLI Layer Changes

`cmd/fngr/` files update their imports:

```go
// Before
"github.com/monolithiclab/fngr/internal"

// After
"github.com/monolithiclab/fngr/internal/db"
"github.com/monolithiclab/fngr/internal/event"
"github.com/monolithiclab/fngr/internal/render"
```

Call sites update accordingly (e.g., `internal.AddEvent(...)` → `event.Add(...)`).

## Out of Scope

- No behavioral changes — this is a pure structural refactor.
- No new features or API changes beyond renaming.
- `common-go.mk` is not modified.
