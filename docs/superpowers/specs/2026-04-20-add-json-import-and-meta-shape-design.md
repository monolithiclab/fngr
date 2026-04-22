# `add --format=json` import + meta JSON shape flip

Sub-project of the [roadmap](../roadmap.md). Closes the last
"Add command ergonomics" item (`--format=json` import) plus pulls forward
the "JSON tag shape" item from "Output format polish" so the input shape
matches the output shape on day one — no transient state where
`fngr list --format=json | fngr add --format=json` fails to round-trip.

The tool is pre-public; the meta JSON shape flip is a breaking change for
any external script that parses `fngr list --format=json` and assumes
`meta` is an object. No compat shims.

## Goals

- `fngr list --format=json` and `fngr event N --format=json` output meta
  as `[[key, value], ...]` (sorted by `(key, value)` for determinism)
  rather than `{key: [values]}`. Each `(event_id, key, value)` row from
  `event_meta` becomes one two-element JSON array, matching the DB
  schema directly.
- `fngr add --format=json` accepts a single event object or an array
  of objects on stdin or as args. The shape mirrors the output:
  ```json
  {
    "text": "deploy v1.2",
    "parent_id": 42,
    "created_at": "2026-04-20T15:30:00Z",
    "meta": [["tag", "ops"], ["author", "alice"]]
  }
  ```
- `text` is the only required field. Other fields default per the chain
  *JSON value supersedes CLI flag supersedes built-in default*.
- Bulk import is atomic: all events commit or none do. Output is
  `Imported N event(s)` once, after the batch commits.
- Body-tag extraction (`@person` → `meta.people`, `#tag` → `meta.tag`)
  still runs on each record's `text` and merges with explicit `meta`
  via `INSERT ... ON CONFLICT DO NOTHING` — same behavior as today's
  text-mode add.
- The `event.Add` data-layer signature stays unchanged; bulk inserts
  go through a new `event.AddMany([]AddInput) ([]int64, error)`. Both
  call a private `addInTx` helper, mirroring the existing
  `deleteMetaTuples` / `insertMetaTuples` pattern.

## Non-goals

- No editor mode under `--format=json`. The user gets no schema help in
  vim and the workflow doesn't compose with batch input.
- No bare-`fngr add --format=json` in TTY auto-launching anything.
  Errors with `"--format=json requires JSON via args or piped stdin"`.
- No partial-commit semantics. A single bad record rolls the whole
  batch back. The user can `jq` to filter problem records and retry.
- No streaming JSON parser. Inputs are read fully into memory and
  unmarshalled in one shot. CLI add is not an ETL pipeline; for
  truly large batches the user can split the file and run `fngr add`
  in a loop.
- No JSON schema document or validation library. Required-field
  checking is hand-coded against the small known shape.
- No alternate input shapes. The tuple-based meta is the one true
  shape; we don't accept `{key: [values]}` for backward-compat.
- No `--format=json` for any other command. `add` is the only command
  that takes structured input today; future commands that need it
  will add the flag separately.

## Architecture

### Step 0: meta JSON shape flip (lands first)

`internal/render/render.go` — `jsonEvent.Meta` becomes `[][2]string`:

```go
type jsonEvent struct {
    ID        int64       `json:"id"`
    ParentID  *int64      `json:"parent_id,omitempty"`
    Text      string      `json:"text"`
    CreatedAt string      `json:"created_at"`
    Meta      [][2]string `json:"meta,omitempty"`
}

func toJSONEvent(ev event.Event) jsonEvent {
    pairs := make([][2]string, 0, len(ev.Meta))
    for _, m := range ev.Meta {
        pairs = append(pairs, [2]string{m.Key, m.Value})
    }
    sort.Slice(pairs, func(i, j int) bool {
        if pairs[i][0] != pairs[j][0] {
            return pairs[i][0] < pairs[j][0]
        }
        return pairs[i][1] < pairs[j][1]
    })
    out := jsonEvent{
        ID:        ev.ID,
        ParentID:  ev.ParentID,
        Text:      ev.Text,
        CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
    }
    if len(pairs) > 0 {
        out.Meta = pairs
    }
    return out
}
```

`omitempty` on the `Meta` field plus the explicit nil-when-empty
assignment keeps the field absent for events with no meta (matches
current behavior).

`JSON` and `JSONStream` writers are unchanged — they call
`toJSONEvent` and let the encoder do the rest.

### Schema (revised, post step 0)

Input record:

| Field | Type | Required | Notes |
|---|---|---|---|
| `text` | string | yes | Must be non-empty after `strings.TrimSpace`. |
| `parent_id` | int64 (nullable) | no | Omitted → `--parent` flag fallback → NULL. |
| `created_at` | RFC3339 string | no | Omitted → `--time` flag fallback → `time.Now()`. |
| `meta` | `[[k, v], ...]` | no | Omitted → `--meta` flag fallback → empty. |

Author resolution: if no `["author", ...]` tuple is present in `meta`,
inject from the existing chain (`--author` flag → `$FNGR_AUTHOR` →
`$USER`). If all are empty, return the existing
`"author is required: use --author, FNGR_AUTHOR, or ensure $USER is set"`
error.

Body-tag extraction always runs on `text` and merges into the
record's meta with the same `ON CONFLICT DO NOTHING` semantics as
today's `event.CollectMeta`.

### Detection: array vs single

```go
var batch []jsonAddInput
if err := json.Unmarshal(raw, &batch); err == nil {
    return batch, nil
}
var one jsonAddInput
if err := json.Unmarshal(raw, &one); err != nil {
    return nil, fmt.Errorf("--format=json: %w", err)
}
return []jsonAddInput{one}, nil
```

If the array unmarshal fails (e.g. input is `{...}`), the single-object
unmarshal runs as a fallback. If both fail, the single-object error is
returned (more informative for the common single-event mistake).

### Body source dispatch under `--format=json`

The existing `resolveBody` dispatch returns the raw text from one of
{joined args, piped stdin}. Under `--format=json`, two pre-flight
guards run before `resolveBody`:

```go
if c.Format == "json" {
    if c.Edit {
        return fmt.Errorf("--edit conflicts with --format=json")
    }
    if io.IsTTY && len(c.Args) == 0 {
        return fmt.Errorf("--format=json requires JSON via args or piped stdin")
    }
}
```

Otherwise `resolveBody` runs as today and returns the raw text.
`AddCmd.Run` then branches on `c.Format == "json"` and routes to
`runJSON` (in `add_json.go`); the text path is unchanged.

The args+stdin "ambiguous" error from the body-input modes spec applies
unchanged — text or JSON, both branches still see the same conflict.

### Data layer: `event.Add` + `event.AddMany` + `addInTx`

`internal/event/event.go`:

```go
// AddInput holds the fields needed to insert one event. Used by the
// bulk path; the single-event Add keeps its positional signature.
type AddInput struct {
    Text      string
    ParentID  *int64
    Meta      []parse.Meta
    CreatedAt *time.Time
}

// addInTx inserts the given events using tx. Caller owns commit/rollback.
// Returns generated IDs in input order. Per-record error aborts the loop;
// the caller (Add or AddMany) sees the error and rolls back via defer.
func addInTx(ctx context.Context, tx *sql.Tx, inputs []AddInput) ([]int64, error) {
    ids := make([]int64, 0, len(inputs))
    insertEvent, _ := tx.PrepareContext(ctx, "INSERT INTO events ...")
    defer insertEvent.Close()
    insertMeta, _ := tx.PrepareContext(ctx, "INSERT INTO event_meta ... ON CONFLICT DO NOTHING")
    defer insertMeta.Close()
    insertFTS, _ := tx.PrepareContext(ctx, "INSERT INTO events_fts ...")
    defer insertFTS.Close()
    for _, in := range inputs {
        // existing Add body, parameterised by `in`
        // ...
        ids = append(ids, lastID)
    }
    return ids, nil
}

// Add inserts a single event in its own transaction. Signature unchanged
// from pre-batch: existing callers and tests do not migrate.
func Add(ctx context.Context, db *sql.DB, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error) {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return 0, fmt.Errorf("begin transaction: %w", err) }
    defer func() { _ = tx.Rollback() }()
    ids, err := addInTx(ctx, tx, []AddInput{{Text: text, ParentID: parentID, Meta: meta, CreatedAt: createdAt}})
    if err != nil { return 0, err }
    if err := tx.Commit(); err != nil { return 0, fmt.Errorf("commit: %w", err) }
    return ids[0], nil
}

// AddMany inserts the events in a single transaction. Empty input is a
// no-op returning (nil, nil). Any per-record error rolls back the batch.
func AddMany(ctx context.Context, db *sql.DB, inputs []AddInput) ([]int64, error) {
    if len(inputs) == 0 { return nil, nil }
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return nil, fmt.Errorf("begin transaction: %w", err) }
    defer func() { _ = tx.Rollback() }()
    ids, err := addInTx(ctx, tx, inputs)
    if err != nil { return nil, err }
    if err := tx.Commit(); err != nil { return nil, fmt.Errorf("commit: %w", err) }
    return ids, nil
}
```

`Store.AddMany` mirrors the function. The `eventStore` interface in
`cmd/fngr/store.go` gains `AddMany(ctx, []event.AddInput) ([]int64, error)`.
`Add` and `Store.Add` keep their existing signatures — zero migration
churn for the ~20+ test sites that call `s.Add(...)` directly.

The existing `event.Add` body becomes a thin wrapper that builds the
one-element slice and delegates to `addInTx`. The per-record body
(parse FTS content via `parse.FTSContent`, insert event row, insert
meta tuples, insert FTS row) is what `addInTx` runs in a loop.

### `cmd/fngr/add.go` — flag and dispatch

```go
type AddCmd struct {
    Args   []string `arg:"" optional:"" help:"Event text (joined with spaces). Omit and pipe to stdin, or use -e."`
    Edit   bool     `short:"e" help:"Open $VISUAL or $EDITOR for the body."`
    Format string   `short:"f" help:"Input format: text (default) or json." enum:"${ADD_FORMATS}" default:"${ADD_FORMAT_DEFAULT}"`
    Author string   `help:"Event author (used as default if JSON record omits meta.author)." env:"FNGR_AUTHOR" default:"${USER}"`
    Parent *int64   `help:"Parent event ID (used as default if JSON record omits parent_id)."`
    Meta   []string `help:"Metadata key=value pairs (used as defaults if JSON record omits meta)." short:"m"`
    Time   string   `help:"Override event timestamp (used as default if JSON record omits created_at)." short:"t"`
}
```

Kong vars in `main.go::kongVars`:
```go
"ADD_FORMATS":         strings.Join([]string{render.FormatText, render.FormatJSON}, ","),
"ADD_FORMAT_DEFAULT":  render.FormatText,
```

`AddCmd.Run`:

```go
func (c *AddCmd) Run(s eventStore, io ioStreams) error {
    if c.Author == "" {
        return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
    }
    if c.Format == render.FormatJSON {
        if c.Edit {
            return fmt.Errorf("--edit conflicts with --format=json")
        }
        if io.IsTTY && len(c.Args) == 0 {
            return fmt.Errorf("--format=json requires JSON via args or piped stdin")
        }
    }

    text, err := resolveBody(c.Args, c.Edit, io)
    if errors.Is(err, errCancel) {
        fmt.Fprintln(io.Err, "cancelled (empty body)")
        return nil
    }
    if err != nil {
        return err
    }

    if c.Format == render.FormatJSON {
        return runJSON(c, s, io, text)
    }
    return runText(c, s, io, text) // existing single-event flow
}
```

`runText` is the existing meta-collect / time-parse / `s.Add` /
`Added event N` block, factored out of the prior Run for clarity.

### `cmd/fngr/add_json.go` — JSON path

```go
type jsonAddInput struct {
    Text      string      `json:"text"`
    ParentID  *int64      `json:"parent_id"`
    CreatedAt *string     `json:"created_at"`
    Meta      [][2]string `json:"meta"`
}

func runJSON(c *AddCmd, s eventStore, io ioStreams, raw string) error {
    inputs, err := parseJSONAddInput(raw)
    if err != nil {
        return err
    }
    cliDefaults, err := buildCLIDefaults(c) // parses --time once, validates --meta, etc.
    if err != nil {
        return err
    }
    addInputs := make([]event.AddInput, 0, len(inputs))
    for i, in := range inputs {
        ai, err := jsonInputToAddInput(in, cliDefaults, c.Author, i)
        if err != nil {
            return err
        }
        addInputs = append(addInputs, ai)
    }

    ctx := context.Background()
    ids, err := s.AddMany(ctx, addInputs)
    if err != nil {
        return err
    }
    if len(ids) == 1 {
        fmt.Fprintf(io.Out, "Imported 1 event\n")
    } else {
        fmt.Fprintf(io.Out, "Imported %d events\n", len(ids))
    }
    return nil
}

func parseJSONAddInput(raw string) ([]jsonAddInput, error) {
    data := []byte(raw)
    var batch []jsonAddInput
    if err := json.Unmarshal(data, &batch); err == nil {
        return batch, nil
    }
    var one jsonAddInput
    if err := json.Unmarshal(data, &one); err != nil {
        return nil, fmt.Errorf("--format=json: %w", err)
    }
    return []jsonAddInput{one}, nil
}

func jsonInputToAddInput(in jsonAddInput, cli cliDefaults, defaultAuthor string, index int) (event.AddInput, error) {
    text := strings.TrimSpace(in.Text)
    if text == "" {
        return event.AddInput{}, fmt.Errorf("--format=json: record %d: text is required", index)
    }

    parent := in.ParentID
    if parent == nil { parent = cli.parent }

    var createdAt *time.Time
    if in.CreatedAt != nil {
        t, err := time.Parse(time.RFC3339, *in.CreatedAt)
        if err != nil { return event.AddInput{}, fmt.Errorf("--format=json: record %d: created_at: %w", index, err) }
        createdAt = &t
    } else {
        createdAt = cli.time
    }

    // Meta resolution: JSON wins if present (even if empty); otherwise CLI flags.
    var explicitMeta []parse.Meta
    if in.Meta != nil {
        explicitMeta = make([]parse.Meta, 0, len(in.Meta))
        for j, pair := range in.Meta {
            if pair[0] == "" {
                return event.AddInput{}, fmt.Errorf("--format=json: record %d: meta[%d]: empty key", index, j)
            }
            explicitMeta = append(explicitMeta, parse.Meta{Key: pair[0], Value: pair[1]})
        }
    } else {
        explicitMeta = cli.meta
    }

    // Apply same author-injection + body-tag-merge as event.CollectMeta.
    author := defaultAuthor
    for _, m := range explicitMeta {
        if m.Key == event.MetaKeyAuthor { author = "" } // already present, don't inject
    }
    meta, err := event.CollectMeta(text, metaToFlagStrings(explicitMeta), author)
    if err != nil { return event.AddInput{}, fmt.Errorf("--format=json: record %d: %w", index, err) }

    return event.AddInput{Text: text, ParentID: parent, Meta: meta, CreatedAt: createdAt}, nil
}
```

`metaToFlagStrings` converts `[]parse.Meta` back to the `[]string`
`key=value` form `event.CollectMeta` already accepts. Slightly
awkward but reuses the existing dedup/extraction code.

Alternative: extend `event.CollectMeta` to accept `[]parse.Meta`
directly. Defer that refactor — call site is contained.

`cliDefaults` is a small struct holding the parsed `--time`,
`--parent`, and the converted `--meta` flag values, computed once
before the per-record loop.

## Testing

### `internal/render/render_test.go` — meta shape

JSON assertion updates:

```go
// Before
want: `"meta":{"tag":["ops"]}`
// After
want: `"meta":[["tag","ops"]]`
```

Apply to every JSON / JSONStream test that exercises events with
meta. Add a new case for an event with multiple meta values for the
same key (`{tag: ops, tag: deploy}`) to lock in the tuple-per-row
shape: expects `[["tag","deploy"],["tag","ops"]]` (sorted).

### `internal/event/event_test.go` — AddMany

New tests:
- `TestAddMany_Empty`: nil and zero-length inputs both return
  `(nil, nil)` and create no rows.
- `TestAddMany_HappyPath`: 3 distinct events committed in order;
  IDs returned in order; FTS rows present for each.
- `TestAddMany_AtomicOnError`: 3 valid inputs + 1 with a
  `parent_id` that doesn't exist → all rows rolled back; row count
  unchanged.
- `TestAddMany_OrderPreservedAcrossMeta`: 2 events each with multiple
  meta tuples; both events' meta correctly attributed via per-event
  `INSERT event_meta` (no cross-contamination).

Existing `TestAdd_*` tests are unchanged — `Add` keeps its signature.

### `cmd/fngr/add_json_test.go` — new

Table-driven coverage of `parseJSONAddInput` and
`jsonInputToAddInput`:
- single object happy path
- array of N objects
- empty array → `(nil, nil)` from runJSON; reports "Imported 0 events"
- malformed JSON → error message contains `--format=json`
- missing `text` → error message mentions record index
- whitespace-only `text` after trim → same error
- `created_at` present → overrides `--time`
- `created_at` absent + `--time` set → uses `--time`
- `created_at` absent + `--time` unset → uses `time.Now()` (within tolerance)
- `parent_id` present → overrides `--parent`
- `meta` present (even if empty `[]`) → CLI `--meta` ignored
- `meta` absent → CLI `--meta` applied
- `meta.author` present → no auto-injection
- `meta.author` absent → injects from `--author` chain
- body-tag merge: `text: "deploy #ops"` + `meta: [["tag","release"]]` → both `tag=ops` and `tag=release` end up in DB
- empty meta key (`[["", "v"]]`) → error mentions tuple index

### `cmd/fngr/add_test.go` — JSON path integration

- `TestAddCmd_FormatJSON_Single`: stdin `{"text":"hi"}`, `IsTTY:false` → 1 event with text "hi", output "Imported 1 event".
- `TestAddCmd_FormatJSON_Array`: stdin `[{"text":"a"},{"text":"b"}]` → 2 events, output "Imported 2 events".
- `TestAddCmd_FormatJSON_EmptyArray`: stdin `[]` → 0 events, output "Imported 0 events", no error.
- `TestAddCmd_FormatJSON_AtomicRollback`: stdin with one bad record → no events, error message mentions index.
- `TestAddCmd_FormatJSON_EditConflicts`: `--format=json -e` → error contains `"--edit conflicts"`.
- `TestAddCmd_FormatJSON_BareTTYRejects`: `--format=json` with no args + IsTTY:true → error contains `"requires JSON via args or piped stdin"`.
- `TestAddCmd_FormatJSON_FromArgs`: `Args: []string{"{\"text\":\"hi\"}"}` → 1 event.
- `TestAddCmd_FormatJSON_ArgsAndStdinError`: args + piped stdin both present → existing "ambiguous" error fires before JSON parsing.
- `TestAddCmd_FormatJSON_TimeFlagFallback`: stdin `{"text":"hi"}` + `--time 2026-04-01` → event has the flag's timestamp.
- `TestAddCmd_FormatJSON_MetaFlagFallback`: stdin `{"text":"hi"}` + `--meta env=prod` → meta has `env=prod`.

### `cmd/fngr/dispatch_test.go` — wiring

One new entry:
```go
{name: "add-json", argv: []string{"add", "--format=json"}, stdin: `{"text":"hi"}`, isTTY: false, want: ""},
```

## Code organization

- **Create**: `cmd/fngr/add_json.go`, `cmd/fngr/add_json_test.go`.
- **Modify**:
  - `internal/render/render.go` — `jsonEvent.Meta` type change, `toJSONEvent` rebuild + sort.
  - `internal/render/render_test.go` — JSON assertion updates, new multi-value-per-key test case.
  - `internal/event/event.go` — `AddInput` type, private `addInTx`, refactored `Add` (signature unchanged), new `AddMany`.
  - `internal/event/store.go` — new `Store.AddMany` method.
  - `internal/event/event_test.go` — new `TestAddMany_*` cases.
  - `cmd/fngr/store.go` — `eventStore` interface gains `AddMany`.
  - `cmd/fngr/add.go` — `Format` flag, `Run` branches, factor existing flow into `runText`.
  - `cmd/fngr/add_test.go` — new `TestAddCmd_FormatJSON_*` cases.
  - `cmd/fngr/main.go` — `kongVars` gains `ADD_FORMATS` and `ADD_FORMAT_DEFAULT`.
  - `cmd/fngr/dispatch_test.go` — `add-json` entry.
- **No change**: existing `Add` callers (Store.Add, all `*_test.go`
  sites that call `s.Add(...)`). Keeping `Add`'s signature is the
  whole point of the `Add` + `AddMany` split.

## Migration & breaking changes

- **Output meta JSON shape change is breaking** for any external
  script that parses `fngr list --format=json` and indexes `meta[key]`.
  Tool is pre-public; no compat shims. Worth a one-line note in the
  README "Output formats" section if the README documents JSON output
  (today it shows examples but doesn't pin the schema).
- **`event.AddMany` is additive** — no existing callers change.
- **`--format=json` on `add` is additive** — `--format=text` (default)
  preserves all existing behavior.
- **`AddCmd.Format` field name is new** — no test sites construct
  `&AddCmd{}` literals that would conflict.

## Documentation

- `CLAUDE.md`:
  - `cmd/fngr/add.go` bullet — extend with the new `--format=json`
    branch and the JSON-supersedes-CLI-defaults rule.
  - New `cmd/fngr/add_json.go` bullet — `jsonAddInput` + `runJSON` +
    `parseJSONAddInput` + `jsonInputToAddInput` + `cliDefaults`.
  - `internal/event/event.go` bullet — mention `AddInput` + `AddMany`
    + the private `addInTx` helper that both `Add` and `AddMany` call.
  - `internal/render/render.go` bullet — note meta JSON shape is
    `[[key, value], ...]` sorted by `(key, value)`.
- `README.md` — Quick start gains JSON examples:
  - single: `echo '{"text":"hi"}' | fngr add --format=json`
  - array: `fngr add --format=json < events.json`
  - export-then-import round-trip: `fngr --format=json | fngr add --format=json`
- `roadmap.md` — once shipped, mark `--format=json import` done under
  the (now empty) Add command ergonomics section AND mark "JSON tag
  shape" done under Output format polish, leaving only Markdown
  format under that epic.

## Roadmap impact

- Closes "Add command ergonomics" entirely (the `--format=json` import
  was the lone remaining item after the body-input modes epic).
- Closes "JSON tag shape" under "Output format polish", leaving only
  "Markdown format" under that epic.
- The "CLI surface alignment" epic items (compact help, `-S`
  everywhere, `help` alias) are unaffected.
