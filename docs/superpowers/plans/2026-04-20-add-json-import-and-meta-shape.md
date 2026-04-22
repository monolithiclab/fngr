# `add --format=json` + meta JSON shape flip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the `add --format=json` import (closing the last "Add command ergonomics" item) bundled with the meta JSON shape flip from `{key: [values]}` to `[[k, v], ...]` so input and output round-trip cleanly on day one.

**Architecture:** Output side flips first (`internal/render/render.go::jsonEvent.Meta` → `[][2]string`, sorted). Data layer keeps `event.Add` unchanged and adds `event.AddMany([]AddInput) ([]int64, error)`; both delegate to a private `addInTx(ctx, tx, []AddInput)` helper that owns the per-record INSERT/FTS-rebuild loop. CLI gains an `AddCmd.Format` flag and a new `cmd/fngr/add_json.go` that parses the JSON, applies CLI defaults per record, then calls `s.AddMany`.

**Tech Stack:** Go 1.22+, Kong (CLI), `encoding/json`, modernc.org/sqlite. Per the user's standing rule: invoke `/simplify` before every commit.

---

## File Structure

- **Create**:
  - `cmd/fngr/add_json.go` — `jsonAddInput`, `runJSON`, `parseJSONAddInput`, `jsonInputToAddInput`, `cliDefaults`, `metaToFlagStrings`.
  - `cmd/fngr/add_json_test.go` — table-driven coverage of parsing, default merging, error cases.
- **Modify**:
  - `internal/render/render.go` — `jsonEvent.Meta` becomes `[][2]string`; `toJSONEvent` rebuilds and sorts the slice.
  - `internal/render/render_test.go` — JSON assertion updates from `"meta":{"k":["v"]}` to `"meta":[["k","v"]]`; new multi-value-per-key test.
  - `internal/event/event.go` — new `AddInput` type, new `AddMany`, new private `addInTx`, `Add` becomes a thin wrapper that calls `addInTx` with a one-element slice.
  - `internal/event/event_test.go` — new `TestAddMany_*` cases.
  - `internal/event/store.go` — new `Store.AddMany` method.
  - `internal/event/store_test.go` — new `TestStore_AddMany`.
  - `cmd/fngr/store.go` — `eventStore` interface gains `AddMany`.
  - `cmd/fngr/add.go` — `Format string` field with `enum:"${ADD_FORMATS}"`; `Run` branches on Format.
  - `cmd/fngr/add_test.go` — new `TestAddCmd_FormatJSON_*` cases.
  - `cmd/fngr/main.go` — `kongVars` gains `ADD_FORMATS` and `ADD_FORMAT_DEFAULT`.
  - `cmd/fngr/dispatch_test.go` — one new `add-json` entry.
  - `CLAUDE.md` — bullets for the new `add_json.go`, the meta JSON shape, and `AddInput`/`AddMany`/`addInTx` in `event.go`.
  - `README.md` — Quick start gains JSON examples (single, array, round-trip).
  - `docs/superpowers/roadmap.md` — close `--format=json import` (was the lone remaining Add ergonomics item) and `JSON tag shape` (under Output format polish).

---

## Task 1: Meta JSON shape flip

**Files:**
- Modify: `internal/render/render.go`
- Modify: `internal/render/render_test.go`

This task is independent of the JSON import work — flips the output shape of `meta` in `fngr list --format=json` and `fngr event N --format=json` from `{key: [values]}` to `[[key, value], ...]`, sorted by `(key, value)` for determinism. Pre-public, no compat shim.

- [ ] **Step 1.1: Update existing render_test.go assertions to fail loudly first**

Run a quick grep to find every test that pins the old meta shape:

```
grep -n '"meta":{' internal/render/render_test.go
```

For each match, change the assertion substring from the old map shape to the new tuple shape. Example pairs:

| Before | After |
|---|---|
| `"meta":{"author":["nicolas"]}` | `"meta":[["author","nicolas"]]` |
| `"meta":{"tag":["ops"],"author":["alice"]}` | `"meta":[["author","alice"],["tag","ops"]]` (sorted by key, then value) |

Apply the substring edits literally. The tests will fail after this step (the implementation still emits the old shape) — that's expected.

Add one new test case in the same file to lock in the multi-value-per-key behavior (a single key appearing with multiple distinct values produces multiple tuples, sorted):

```go
func TestJSON_MultiValuePerKey(t *testing.T) {
	t.Parallel()
	pinNow(t, time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC))

	ev := event.Event{
		ID:        1,
		Text:      "x",
		CreatedAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
			{Key: "tag", Value: "deploy"},
			{Key: "author", Value: "alice"},
		},
	}
	var buf bytes.Buffer
	if err := JSON(&buf, []event.Event{ev}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := buf.String()
	want := `"meta":[["author","alice"],["tag","deploy"],["tag","ops"]]`
	if !strings.Contains(got, want) {
		t.Errorf("got %s\nwant substring %s", got, want)
	}
}
```

If the existing test file's import block lacks `bytes`, `strings`, `time`, `event`, or `parse`, add them. Most are likely already present.

- [ ] **Step 1.2: Run tests to confirm failures**

```
go test ./internal/render/ -run 'TestJSON' -v
```

Expected: existing JSON tests FAIL (assertion mismatch — old shape vs. new substring); `TestJSON_MultiValuePerKey` also FAILS (same root cause).

- [ ] **Step 1.3: Update `jsonEvent` and `toJSONEvent` in render.go**

In `internal/render/render.go`, replace the `jsonEvent` struct and `toJSONEvent` function:

```go
type jsonEvent struct {
	ID        int64       `json:"id"`
	ParentID  *int64      `json:"parent_id,omitempty"`
	Text      string      `json:"text"`
	CreatedAt string      `json:"created_at"`
	Meta      [][2]string `json:"meta,omitempty"`
}

func toJSONEvent(ev event.Event) jsonEvent {
	out := jsonEvent{
		ID:        ev.ID,
		ParentID:  ev.ParentID,
		Text:      ev.Text,
		CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(ev.Meta) == 0 {
		return out
	}
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
	out.Meta = pairs
	return out
}
```

Add `"sort"` to the import block if not already present.

- [ ] **Step 1.4: Run tests; expect pass**

```
go test ./internal/render/ -run 'TestJSON' -v
```

Expected: PASS for all subtests.

- [ ] **Step 1.5: Run full CI**

```
make ci
```

Expected: green; no other tests should reference the old meta shape (the `cmd/fngr/dispatch_test.go` JSON cases assert wiring contract only, not output shape — verify by inspection if any FAIL appears).

- [ ] **Step 1.6: Run `/simplify` against the diff**

The user's standing rule: invoke the `/simplify` slash command (or run the equivalent three-lens review inline if delegation is unavailable) against the diff. Apply any actionable findings. Skip only for docs-only or single-line typo fixes.

- [ ] **Step 1.7: Commit**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "$(cat <<'EOF'
feat(render): meta JSON shape becomes [[k,v],...] tuples

Flip fngr list --format=json and fngr event N --format=json output
from meta: {key: [values]} to meta: [[key, value], ...], sorted by
(key, value) for determinism. Each (event_id, key, value) row from
event_meta becomes one two-element JSON array, matching the DB
schema directly. Pre-public; breaking for any external script that
parses the old shape, no compat shim.

Step 0 of the add --format=json import spec — the input shape
mirrors the output, so this lands first to avoid a transient state
where list --format=json | add --format=json fails to round-trip.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `event.AddInput` + `AddMany` + `addInTx`

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/store.go`
- Modify: `internal/event/event_test.go`
- Modify: `internal/event/store_test.go`
- Modify: `cmd/fngr/store.go`

Refactor: extract the per-record insertion logic from `Add` into a private `addInTx` helper. `Add` keeps its existing positional signature and becomes a thin wrapper around `addInTx`. New `AddMany` accepts a `[]AddInput` and inserts everything in a single transaction.

- [ ] **Step 2.1: Write the failing test for `AddMany`**

Append to `internal/event/event_test.go`:

```go
func TestAddMany_Empty(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	ids, err := AddMany(ctx, database, nil)
	if err != nil {
		t.Fatalf("AddMany(nil): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil for empty input", ids)
	}

	ids, err = AddMany(ctx, database, []AddInput{})
	if err != nil {
		t.Fatalf("AddMany([]): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil for empty input", ids)
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("created %d rows, want 0", count)
	}
}

func TestAddMany_HappyPath(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	inputs := []AddInput{
		{Text: "a", Meta: []parse.Meta{{Key: "tag", Value: "x"}}},
		{Text: "b"},
		{Text: "c", Meta: []parse.Meta{{Key: "tag", Value: "y"}, {Key: "people", Value: "alice"}}},
	}
	ids, err := AddMany(ctx, database, inputs)
	if err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("ids not strictly increasing: %v", ids)
		}
	}

	for i, id := range ids {
		ev, err := Get(ctx, database, id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if ev.Text != inputs[i].Text {
			t.Errorf("event %d text = %q, want %q", id, ev.Text, inputs[i].Text)
		}
		if len(ev.Meta) != len(inputs[i].Meta) {
			t.Errorf("event %d meta count = %d, want %d", id, len(ev.Meta), len(inputs[i].Meta))
		}
	}
}

func TestAddMany_AtomicOnError(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	bogusParent := int64(9999)
	inputs := []AddInput{
		{Text: "good 1"},
		{Text: "good 2"},
		{Text: "bad", ParentID: &bogusParent}, // parent does not exist
		{Text: "good 3"},
	}
	_, err := AddMany(ctx, database, inputs)
	if err == nil {
		t.Fatal("AddMany returned nil err, want parent-not-found")
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("created %d rows, want 0 (batch should roll back atomically)", count)
	}
}

func TestAddMany_FTSPopulatedPerRecord(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	inputs := []AddInput{
		{Text: "deploy ops", Meta: []parse.Meta{{Key: "tag", Value: "ops"}}},
		{Text: "lunch chat", Meta: []parse.Meta{{Key: "tag", Value: "personal"}}},
	}
	if _, err := AddMany(ctx, database, inputs); err != nil {
		t.Fatalf("AddMany: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "deploy"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 1 || matches[0].Text != "deploy ops" {
		t.Errorf("FTS not populated: matches = %v", matches)
	}
}
```

- [ ] **Step 2.2: Run the test; expect failure**

```
go test ./internal/event/ -run TestAddMany -v
```

Expected: FAIL with `undefined: AddMany` and `undefined: AddInput`.

- [ ] **Step 2.3: Add `AddInput`, `addInTx`, `AddMany`; refactor `Add` to delegate**

Edit `internal/event/event.go`. Add the new type after the existing `Event`/`MetaCount` types:

```go
// AddInput holds the fields needed to insert one event. Used by AddMany;
// the single-event Add keeps its positional signature so existing call
// sites don't need to construct a struct literal.
type AddInput struct {
	Text      string
	ParentID  *int64
	Meta      []parse.Meta
	CreatedAt *time.Time
}
```

Replace the existing `Add` function with the refactored trio:

```go
func Add(ctx context.Context, db *sql.DB, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := addInTx(ctx, tx, []AddInput{{Text: text, ParentID: parentID, Meta: meta, CreatedAt: createdAt}})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return ids[0], nil
}

// AddMany inserts the given events in a single transaction. Empty input
// is a no-op returning (nil, nil). Any per-record error rolls the entire
// batch back. Returns generated IDs in input order.
func AddMany(ctx context.Context, db *sql.DB, inputs []AddInput) ([]int64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := addInTx(ctx, tx, inputs)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return ids, nil
}

// addInTx inserts events using the given tx. Caller owns commit/rollback.
// Each input becomes one INSERT into events, zero-or-more INSERTs into
// event_meta, and one INSERT into events_fts. Per-record errors abort
// the loop with a wrapped error; the caller's deferred Rollback fires.
func addInTx(ctx context.Context, tx *sql.Tx, inputs []AddInput) ([]int64, error) {
	insertMeta, err := tx.PrepareContext(ctx,
		"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)",
	)
	if err != nil {
		return nil, fmt.Errorf("prepare meta insert: %w", err)
	}
	defer insertMeta.Close()

	insertFTS, err := tx.PrepareContext(ctx,
		"INSERT INTO events_fts (rowid, content) VALUES (?, ?)",
	)
	if err != nil {
		return nil, fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer insertFTS.Close()

	ids := make([]int64, 0, len(inputs))
	for _, in := range inputs {
		if in.ParentID != nil {
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", *in.ParentID).Scan(&exists)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("parent event %d: %w", *in.ParentID, ErrNotFound)
				}
				return nil, fmt.Errorf("query parent event: %w", err)
			}
		}

		var res sql.Result
		if in.CreatedAt != nil {
			res, err = tx.ExecContext(ctx,
				"INSERT INTO events (parent_id, text, created_at) VALUES (?, ?, ?)",
				in.ParentID, in.Text, in.CreatedAt.UTC().Format(timefmt.DateTimeFormat),
			)
		} else {
			res, err = tx.ExecContext(ctx,
				"INSERT INTO events (parent_id, text) VALUES (?, ?)",
				in.ParentID, in.Text,
			)
		}
		if err != nil {
			return nil, fmt.Errorf("insert event: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("last insert id: %w", err)
		}

		for _, m := range in.Meta {
			if _, err := insertMeta.ExecContext(ctx, id, m.Key, m.Value); err != nil {
				return nil, fmt.Errorf("insert meta: %w", err)
			}
		}

		if _, err := insertFTS.ExecContext(ctx, id, parse.FTSContent(in.Text, in.Meta)); err != nil {
			return nil, fmt.Errorf("insert FTS content: %w", err)
		}

		ids = append(ids, id)
	}
	return ids, nil
}
```

Note: the per-record meta insert no longer uses `ON CONFLICT DO NOTHING` — the existing `Add` body didn't either (the only paths that need ON CONFLICT are `AddTags` and `Update`'s body-tag sync, which run after the row exists). Keep parity with the existing Add behavior.

Verify the import block at the top of `event.go` still includes everything: `context`, `database/sql`, `errors`, `fmt`, `iter`, `strings`, `time`, plus `parse` and `timefmt`. No new imports needed.

- [ ] **Step 2.4: Add `Store.AddMany` and `TestStore_AddMany`**

Append to `internal/event/store.go`:

```go
func (s *Store) AddMany(ctx context.Context, inputs []AddInput) ([]int64, error) {
	return AddMany(ctx, s.DB, inputs)
}
```

Append to `internal/event/store_test.go`:

```go
func TestStore_AddMany(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	ids, err := s.AddMany(ctx, []AddInput{
		{Text: "a"},
		{Text: "b"},
	})
	if err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("got %d ids, want 2", len(ids))
	}
}
```

- [ ] **Step 2.5: Add `AddMany` to the `eventStore` interface**

Edit `cmd/fngr/store.go`. Insert the new method right after `Add`:

```go
type eventStore interface {
	Add(ctx context.Context, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error)
	AddMany(ctx context.Context, inputs []event.AddInput) ([]int64, error)
	AddTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error)
	// ... rest unchanged
}
```

- [ ] **Step 2.6: Run the new tests; expect pass**

```
go test ./internal/event/ -run 'TestAddMany|TestStore_AddMany' -v -race
```

Expected: PASS for all subtests.

- [ ] **Step 2.7: Run full CI**

```
make ci
```

Expected: green. The existing `Add` callers (`Store.Add`, ~20+ test sites that call `s.Add(...)` directly) compile unchanged because `Add`'s signature is preserved.

- [ ] **Step 2.8: Run `/simplify` against the diff**

Three-lens review (reuse, quality, efficiency). Apply actionable findings. Particular attention: the `addInTx` body shares structure with the existing `Update` flow — verify there's no new opportunity to extract a shared helper that didn't already exist.

- [ ] **Step 2.9: Commit**

```bash
git add internal/event/event.go internal/event/event_test.go internal/event/store.go internal/event/store_test.go cmd/fngr/store.go
git commit -m "$(cat <<'EOF'
feat(event): AddMany for atomic batch insert

Refactor Add to delegate to a private addInTx helper that owns the
per-record INSERT events / INSERT event_meta / INSERT events_fts
loop. New AddMany accepts a []AddInput and runs the loop inside a
single transaction — atomic, returns IDs in input order, empty input
returns (nil, nil).

Add's positional signature stays unchanged so existing callers and
test sites don't migrate. Pattern mirrors deleteMetaTuples /
insertMetaTuples (private tx-taking helpers reused across paths).

Prep for cmd/fngr/add --format=json import (next task).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `AddCmd.Format` flag + `add_json.go` JSON path

**Files:**
- Create: `cmd/fngr/add_json.go`
- Create: `cmd/fngr/add_json_test.go`
- Modify: `cmd/fngr/add.go`
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/add_test.go`
- Modify: `cmd/fngr/dispatch_test.go`

This task wires the `Format` flag through Kong, adds the JSON branch in `AddCmd.Run`, and implements the JSON parsing + per-record default merging + `s.AddMany` call in a new file.

- [ ] **Step 3.1: Add Kong vars for the new flag**

Edit `cmd/fngr/main.go`. In the `kongVars(version, username string) kong.Vars` map, append two entries (preserve alphabetical-ish order):

```go
return kong.Vars{
	"version":              version,
	"USER":                 username,
	"ADD_FORMATS":          strings.Join([]string{render.FormatText, render.FormatJSON}, ","),
	"ADD_FORMAT_DEFAULT":   render.FormatText,
	"LIST_FORMATS":         strings.Join(render.ListFormats, ","),
	"LIST_FORMAT_DEFAULT":  render.FormatTree,
	"EVENT_FORMATS":        strings.Join(render.EventFormats, ","),
	"EVENT_FORMAT_DEFAULT": render.FormatText,
}
```

`render.FormatText` and `render.FormatJSON` already exist (Task F4 added them).

- [ ] **Step 3.2: Add `Format` field to `AddCmd` and pre-flight guards in `Run`**

Edit `cmd/fngr/add.go`. Replace the struct and `Run` with:

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
		return c.runJSON(s, io, text)
	}
	return c.runText(s, io, text)
}

func (c *AddCmd) runText(s eventStore, io ioStreams, text string) error {
	meta, err := event.CollectMeta(text, c.Meta, c.Author)
	if err != nil {
		return err
	}

	var createdAt *time.Time
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value: %w", err)
		}
		createdAt = &t
	}

	id, err := s.Add(context.Background(), text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
```

Add `"github.com/monolithiclab/fngr/internal/render"` to the import block.

`runJSON` is defined in `add_json.go` (next step). The Go file-level visibility makes it accessible as a method in the same package.

- [ ] **Step 3.3: Write the failing test for `parseJSONAddInput`**

Create `cmd/fngr/add_json_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestParseJSONAddInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantLen int
		wantErr string
	}{
		{name: "single-object", input: `{"text":"hi"}`, wantLen: 1},
		{name: "array-of-one", input: `[{"text":"hi"}]`, wantLen: 1},
		{name: "array-of-three", input: `[{"text":"a"},{"text":"b"},{"text":"c"}]`, wantLen: 3},
		{name: "empty-array", input: `[]`, wantLen: 0},
		{name: "malformed-json", input: `{"text":`, wantErr: "--format=json"},
		{name: "scalar-string", input: `"hello"`, wantErr: "--format=json"},
		{name: "scalar-number", input: `42`, wantErr: "--format=json"},
		{name: "with-meta", input: `{"text":"hi","meta":[["tag","ops"]]}`, wantLen: 1},
		{name: "with-parent-and-time", input: `{"text":"hi","parent_id":3,"created_at":"2026-04-01T12:00:00Z"}`, wantLen: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseJSONAddInput(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJSONAddInput: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("got %d records, want %d", len(got), tc.wantLen)
			}
		})
	}
}
```

- [ ] **Step 3.4: Run the test; expect failure**

```
go test ./cmd/fngr/ -run TestParseJSONAddInput -v
```

Expected: FAIL with `undefined: parseJSONAddInput`.

- [ ] **Step 3.5: Implement `add_json.go`**

Create `cmd/fngr/add_json.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// jsonAddInput is the wire shape for one event in --format=json input.
// Pointer types distinguish "field omitted" (apply CLI/built-in default)
// from "field present" (JSON value wins, even if zero/empty).
type jsonAddInput struct {
	Text      string      `json:"text"`
	ParentID  *int64      `json:"parent_id"`
	CreatedAt *string     `json:"created_at"`
	Meta      [][2]string `json:"meta"`
}

// cliDefaults bundles the parsed CLI flag values that may be applied
// when a JSON record omits the corresponding field. Computed once
// before the per-record loop.
type cliDefaults struct {
	parent *int64
	time   *time.Time
	meta   []parse.Meta // already parsed from --meta key=value flags
}

// parseJSONAddInput tries to unmarshal raw as an array first; falls back
// to a single object if that fails. Returns the single-object error if
// both attempts fail (more informative for the common single-event case).
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

func (c *AddCmd) runJSON(s eventStore, io ioStreams, raw string) error {
	inputs, err := parseJSONAddInput(raw)
	if err != nil {
		return err
	}

	defaults, err := buildCLIDefaults(c)
	if err != nil {
		return err
	}

	addInputs := make([]event.AddInput, 0, len(inputs))
	for i, in := range inputs {
		ai, err := jsonInputToAddInput(in, defaults, c.Author, i)
		if err != nil {
			return err
		}
		addInputs = append(addInputs, ai)
	}

	ids, err := s.AddMany(context.Background(), addInputs)
	if err != nil {
		return err
	}
	if len(ids) == 1 {
		fmt.Fprintln(io.Out, "Imported 1 event")
	} else {
		fmt.Fprintf(io.Out, "Imported %d events\n", len(ids))
	}
	return nil
}

func buildCLIDefaults(c *AddCmd) (cliDefaults, error) {
	d := cliDefaults{parent: c.Parent}
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return cliDefaults{}, fmt.Errorf("invalid --time value: %w", err)
		}
		d.time = &t
	}
	if len(c.Meta) > 0 {
		parsed, err := parse.FlagMeta(c.Meta)
		if err != nil {
			return cliDefaults{}, err
		}
		d.meta = parsed
	}
	return d, nil
}

func jsonInputToAddInput(in jsonAddInput, defaults cliDefaults, defaultAuthor string, index int) (event.AddInput, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: text is required", index)
	}

	parent := in.ParentID
	if parent == nil {
		parent = defaults.parent
	}

	var createdAt *time.Time
	if in.CreatedAt != nil {
		t, err := time.Parse(time.RFC3339, *in.CreatedAt)
		if err != nil {
			return event.AddInput{}, fmt.Errorf("--format=json: record %d: created_at: %w", index, err)
		}
		createdAt = &t
	} else {
		createdAt = defaults.time
	}

	// Meta resolution: JSON wins if present (including explicit empty);
	// otherwise CLI flag defaults apply.
	var explicit []parse.Meta
	if in.Meta != nil {
		explicit = make([]parse.Meta, 0, len(in.Meta))
		for j, pair := range in.Meta {
			if pair[0] == "" {
				return event.AddInput{}, fmt.Errorf("--format=json: record %d: meta[%d]: empty key", index, j)
			}
			explicit = append(explicit, parse.Meta{Key: pair[0], Value: pair[1]})
		}
	} else {
		explicit = defaults.meta
	}

	// If the explicit set already has an author entry, suppress injection.
	author := defaultAuthor
	for _, m := range explicit {
		if m.Key == event.MetaKeyAuthor {
			author = ""
			break
		}
	}

	// Reuse CollectMeta for body-tag merge + dedup. Round-trip through
	// the []string flag form to match its existing signature.
	merged, err := event.CollectMeta(text, metaToFlagStrings(explicit), author)
	if err != nil {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: %w", index, err)
	}

	return event.AddInput{
		Text:      text,
		ParentID:  parent,
		Meta:      merged,
		CreatedAt: createdAt,
	}, nil
}

// metaToFlagStrings converts []parse.Meta back to the []string key=value
// form CollectMeta accepts. Awkward but contained — the alternative is
// extending CollectMeta to accept []parse.Meta directly.
func metaToFlagStrings(meta []parse.Meta) []string {
	if len(meta) == 0 {
		return nil
	}
	out := make([]string, len(meta))
	for i, m := range meta {
		out[i] = m.Key + "=" + m.Value
	}
	return out
}
```

- [ ] **Step 3.6: Run the JSON test; expect pass**

```
go test ./cmd/fngr/ -run TestParseJSONAddInput -v
```

Expected: PASS for all 9 subtests.

- [ ] **Step 3.7: Add table-driven tests for `jsonInputToAddInput`**

Append to `cmd/fngr/add_json_test.go`:

```go
func TestJSONInputToAddInput(t *testing.T) {
	t.Parallel()

	mkPtr := func(s string) *string { return &s }
	mkInt64 := func(n int64) *int64 { return &n }

	cases := []struct {
		name     string
		in       jsonAddInput
		defaults cliDefaults
		author   string
		wantText string
		wantErr  string
	}{
		{
			name:     "happy-single",
			in:       jsonAddInput{Text: "hi"},
			author:   "alice",
			wantText: "hi",
		},
		{
			name:    "missing-text",
			in:      jsonAddInput{},
			author:  "alice",
			wantErr: "text is required",
		},
		{
			name:    "whitespace-only-text",
			in:      jsonAddInput{Text: "   "},
			author:  "alice",
			wantErr: "text is required",
		},
		{
			name:     "json-meta-overrides-cli",
			in:       jsonAddInput{Text: "x", Meta: [][2]string{{"env", "prod"}}},
			defaults: cliDefaults{meta: []parse.Meta{{Key: "env", Value: "dev"}}},
			author:   "alice",
			wantText: "x",
		},
		{
			name:    "empty-meta-key",
			in:      jsonAddInput{Text: "x", Meta: [][2]string{{"", "v"}}},
			author:  "alice",
			wantErr: "meta[0]: empty key",
		},
		{
			name:    "bad-created-at",
			in:      jsonAddInput{Text: "x", CreatedAt: mkPtr("not-a-time")},
			author:  "alice",
			wantErr: "created_at",
		},
		{
			name:     "json-parent-id-overrides-cli",
			in:       jsonAddInput{Text: "x", ParentID: mkInt64(7)},
			defaults: cliDefaults{parent: mkInt64(3)},
			author:   "alice",
			wantText: "x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := jsonInputToAddInput(tc.in, tc.defaults, tc.author, 0)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("jsonInputToAddInput: %v", err)
			}
			if got.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tc.wantText)
			}
		})
	}
}
```

Add `parse` to the import block: `"github.com/monolithiclab/fngr/internal/parse"`.

- [ ] **Step 3.8: Add CLI integration tests for `--format=json`**

Append to `cmd/fngr/add_test.go`:

```go
func TestAddCmd_FormatJSON_Single(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`{"text":"hi"}`, false) // piped stdin

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 event") {
		t.Errorf("output = %q, want 'Imported 1 event'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "hi" {
		t.Errorf("text = %q, want 'hi'", ev.Text)
	}
}

func TestAddCmd_FormatJSON_Array(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`[{"text":"a"},{"text":"b"},{"text":"c"}]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 3 events") {
		t.Errorf("output = %q, want 'Imported 3 events'", out.String())
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 3 {
		t.Errorf("created %d events, want 3", len(events))
	}
}

func TestAddCmd_FormatJSON_EmptyArray(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`[]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run returned err = %v, want nil for empty array", err)
	}
	if !strings.Contains(out.String(), "Imported 0 events") {
		t.Errorf("output = %q, want 'Imported 0 events'", out.String())
	}
}

func TestAddCmd_FormatJSON_AtomicRollback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// Second record references parent_id=9999 which doesn't exist → rollback.
	io, _, _ := newTestIOFull(`[{"text":"good"},{"text":"bad","parent_id":9999}]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("Run returned nil err, want parent-not-found")
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0 (atomic rollback)", len(events))
	}
}

func TestAddCmd_FormatJSON_EditConflicts(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true)

	cmd := &AddCmd{Format: "json", Edit: true, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--edit conflicts with --format=json") {
		t.Errorf("err = %v, want '--edit conflicts with --format=json'", err)
	}
}

func TestAddCmd_FormatJSON_BareTTYRejects(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true) // TTY, no args

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "requires JSON via args or piped stdin") {
		t.Errorf("err = %v, want 'requires JSON via args or piped stdin'", err)
	}
}

func TestAddCmd_FormatJSON_FromArgs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Format: "json", Args: []string{`{"text":"from arg"}`}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 event") {
		t.Errorf("output = %q, want 'Imported 1 event'", out.String())
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "from arg" {
		t.Errorf("text = %q, want 'from arg'", ev.Text)
	}
}

func TestAddCmd_FormatJSON_TimeFlagFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"text":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Time: "2026-04-01", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.CreatedAt.Year() != 2026 || ev.CreatedAt.Month() != 4 || ev.CreatedAt.Day() != 1 {
		t.Errorf("CreatedAt = %v, want 2026-04-01", ev.CreatedAt)
	}
}

func TestAddCmd_FormatJSON_MetaFlagFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"text":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Meta: []string{"env=prod"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), 1)
	hasEnv := false
	for _, m := range ev.Meta {
		if m.Key == "env" && m.Value == "prod" {
			hasEnv = true
		}
	}
	if !hasEnv {
		t.Errorf("Meta = %v, want env=prod from --meta fallback", ev.Meta)
	}
}
```

If `add_test.go`'s import block lacks `event`, add `"github.com/monolithiclab/fngr/internal/event"`.

- [ ] **Step 3.9: Add the dispatch test entry**

Edit `cmd/fngr/dispatch_test.go`. In the `TestKongDispatch_AllCommands` `cases` slice, add one entry next to the other `add-*` cases:

```go
{name: "add-json", argv: []string{"add", "--format=json"}, stdin: `{"text":"hi"}`, isTTY: false, want: ""},
```

- [ ] **Step 3.10: Run the new tests and full CI**

```
go test ./cmd/fngr/ -run 'TestParseJSONAddInput|TestJSONInputToAddInput|TestAddCmd_FormatJSON|TestKongDispatch' -v -race
```

Expected: PASS for all subtests.

```
make ci
```

Expected: green.

- [ ] **Step 3.11: Run `/simplify` against the diff**

Three-lens review (reuse, quality, efficiency). Particular attention:
- `metaToFlagStrings` is the awkward shim flagged in the spec — confirm the call site stays contained and doesn't proliferate.
- `runText` factored out of the prior `Run` — verify it's not subtly different from the pre-task behavior.
- Help text on the new `Format` flag — does it document the JSON-supersedes-CLI rule clearly enough?

- [ ] **Step 3.12: Commit**

```bash
git add cmd/fngr/add.go cmd/fngr/add_json.go cmd/fngr/add_json_test.go cmd/fngr/add_test.go cmd/fngr/main.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): add --format=json import for single events and arrays

Closes the last "Add command ergonomics" roadmap item. AddCmd gains a
Format flag (text default, json opt-in). Under --format=json:
  - input comes from args or piped stdin (editor and bare-TTY reject)
  - parser tries []jsonAddInput first, falls back to single object
  - per-record defaults flow JSON value > CLI flag > built-in
  - body-tag extraction still merges with explicit meta via existing
    CollectMeta path
  - batch is atomic: any per-record error rolls back via AddMany

Output: "Imported 1 event" or "Imported N events" once per batch.
The text path (existing behavior) is factored into runText with no
behavioral change. Kong vars register ADD_FORMATS / ADD_FORMAT_DEFAULT
mirroring the existing LIST_/EVENT_ pattern.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Documentation + roadmap

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Modify: `docs/superpowers/roadmap.md`

- [ ] **Step 4.1: Update CLAUDE.md**

Read `CLAUDE.md` first, then edit:

- Find the `cmd/fngr/{add,list,event,delete,meta}.go` bullet (post-body-input-modes wording about the dispatch). Extend it with one sentence about `--format=json`:

  > `cmd/fngr/add.go` accepts variadic positional `Args` (joined with spaces); body source resolved by `cmd/fngr/body.go::resolveBody`; `-e/--edit` forces the editor; bare `fngr add` in a TTY auto-launches `$VISUAL`/`$EDITOR`. With `--format=json` the body is parsed as a JSON event record (or array) by `cmd/fngr/add_json.go`; per-record defaults flow JSON value > CLI flag > built-in.

- Add a new bullet for `cmd/fngr/add_json.go` (insert after the existing `cmd/fngr/body.go` bullet):

  > `cmd/fngr/add_json.go` — `--format=json` import path. `jsonAddInput` is the wire shape `{text, parent_id?, created_at?, meta?: [[k,v],...]}`; `parseJSONAddInput` tries array-then-single unmarshal; `jsonInputToAddInput` applies CLI defaults, runs body-tag extraction via existing `event.CollectMeta`, returns an `event.AddInput`. `runJSON` calls `s.AddMany` for atomic batch insert.

- Update the `internal/event/event.go` bullet to mention the new shape:

  > `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS), `AddMany` (batched same shape, atomic), `AddInput` value type. Both `Add` and `AddMany` delegate to a private `addInTx` that runs the per-record INSERT loop using a caller-owned `*sql.Tx`. ... (rest of the bullet unchanged)

- Update the `internal/render/render.go` bullet (or add a clause) noting the new meta JSON shape:

  > Meta in JSON output is `[[key, value], ...]`, sorted by `(key, value)`. Each `event_meta` row maps to one tuple — multiple values for the same key produce multiple tuples.

- [ ] **Step 4.2: Update README.md**

Read README.md, find the Quick start section, and add JSON examples after the existing add examples:

```markdown
# Bulk import a single event from JSON
echo '{"text":"hi","meta":[["tag","ops"]]}' | fngr add --format=json

# Bulk import an array of events (atomic; any error rolls back the batch)
fngr add --format=json < events.json

# Round-trip via stdout pipe (e.g. copy events between databases)
fngr --db src.db --format=json | fngr --db dst.db add --format=json
```

- [ ] **Step 4.3: Update roadmap**

Edit `docs/superpowers/roadmap.md`:

- Move the `--format=json import` bullet from `## Add command ergonomics` to `## Done` as part of a consolidated entry. Suggested wording for the new Done entry (placed in chronological order after the body-input-modes entry):

  > **`add --format=json` import + meta JSON shape** — `fngr add --format=json` accepts a single event object or an array on stdin or args; per-record defaults flow JSON value > CLI flag > built-in; batches are atomic. JSON meta shape across both input and `fngr list --format=json` output is now `[[key, value], ...]` sorted by `(key, value)` — replaces the prior `{key: [values]}` map.

- Remove the `## Add command ergonomics` section entirely (it's now empty).

- Under `## Output format polish`, remove the `JSON tag shape` bullet (it shipped with this work). The remaining bullet is just `Markdown format`.

- [ ] **Step 4.4: Run CI to verify docs don't break anything**

```
make ci
```

Expected: green (docs-only changes).

- [ ] **Step 4.5: Commit**

Docs-only — `/simplify` is skipped per the user's standing rule (single-line typo / docs-only fixes don't require it).

```bash
git add CLAUDE.md README.md docs/superpowers/roadmap.md
git commit -m "$(cat <<'EOF'
docs: README + CLAUDE.md + roadmap for add --format=json

CLAUDE.md gains the new add_json.go bullet, extends add.go's bullet
with the --format=json branch, mentions AddMany/addInTx in event.go,
and notes the new tuple-shaped meta in render.go.

README Quick start gains three JSON examples (single, array, round-
trip via stdout pipe).

Roadmap consolidates the shipped items into one Done entry. The Add
command ergonomics section is now empty (removed). Output format
polish is down to its lone Markdown format bullet.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Checklist (post-implementation)

After all tasks land:

1. **Per-function coverage** (`make test` should report):
   - `event.AddMany` ≥ 90%
   - `event.addInTx` ≥ 90%
   - `parseJSONAddInput` 100%
   - `jsonInputToAddInput` ≥ 90%
   - `AddCmd.runJSON` ≥ 85%
   - `AddCmd.runText` unchanged from prior coverage
2. **Manual smoke test** in a real terminal:
   - `echo '{"text":"hi"}' | fngr add --format=json` → "Imported 1 event"; `fngr event 1` shows it
   - `fngr --format=json | fngr --db /tmp/copy.db add --format=json` → round-trip succeeds
   - `fngr add --format=json '{"text":"x","meta":[["tag","y"]]}'` → 1 event with tag=y
   - `fngr add --format=json` (TTY, no args) → errors with "requires JSON via args or piped stdin"
   - `fngr add --format=json -e` → errors with "--edit conflicts with --format=json"
   - `echo '[{"text":"a"},{"parent_id":9999,"text":"b"}]' | fngr add --format=json` → errors, no rows created
3. **`fngr list --format=json`** output now shows `meta: [["k","v"]]` not `meta: {"k":["v"]}`.
4. **CLAUDE.md / README / roadmap** all reflect the new state.
