# `event` namespace + subcommands Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement S2 of the roadmap — replace `fngr show` and `fngr edit` with one `fngr event <id> [<verb>]` namespace. Bare `fngr event N` reads (today's `show` semantics); seven verbs mutate: `text`, `time`, `date`, `attach`, `detach`, `tag`, `untag`. `text` syncs body-derived tags to the new text; `attach` rejects cycles; `tag`/`untag` accept n args (`@person` / `#tag` / `key=value`); none of the verbs prompt.

**Architecture:** Tag dedup is moved to the database via a UNIQUE index on `event_meta(key, value, event_id)` and `INSERT … ON CONFLICT DO NOTHING`. Body-tag sync inside `event.Update` deletes the tags parsed from the previous text, updates the row, then re-inserts the tags parsed from the new text. New typed parsers (`timefmt.ParsePartial`, `parse.MetaArg`) own the input shape decisions; CLI verbs are thin.

**Tech Stack:** Go 1.26 + Kong CLI (parent-context binding via `AfterApply`), modernc.org/sqlite, `iter.Seq2` (already in use from S1).

**Spec:** [`docs/superpowers/specs/2026-04-18-event-namespace-design.md`](../specs/2026-04-18-event-namespace-design.md)

**Project conventions** (from `CLAUDE.md`):
- Always use `make ci -j8` for the full check; `go test ./pkg/...` while iterating.
- Tests parallel-safe (`t.Parallel()`), table-driven where useful. Tests using `t.Setenv` cannot use `t.Parallel()` (Go enforces this).
- Tests use per-test SQLite files (not bare `:memory:` — each pool connection sees its own empty in-memory DB; this matters for streaming queries that hold one connection while issuing a follow-up on another).
- Commits are lowercase imperative (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`) with a `Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>` trailer.
- Never commit `README.md`, `CLAUDE.md`, or `REVIEW.md`.
- Schema changes go in a NEW entry at the bottom of `migrations` in `internal/db/migrate.go`. Never edit a published migration.

---

## File map

**Created:**
- `cmd/fngr/event.go` — `EventCmd` plus eight sub-verb structs (Show, Text, Time, Date, Attach, Detach, Tag, Untag).
- `cmd/fngr/event_test.go` — per-verb tests against in-memory store.

**Modified:**
- `internal/db/migrate.go` — append migration `{version: 2, up: …}`.
- `internal/db/db_test.go` — `TestMigrate_V2DedupesAndAddsUnique`.
- `internal/timefmt/timefmt.go` — add `ParsePartial`; `Parse` becomes a thin wrapper.
- `internal/timefmt/timefmt_test.go` — `TestParsePartial`.
- `internal/parse/parse.go` — add `MetaArg`.
- `internal/parse/parse_test.go` — `TestMetaArg`.
- `internal/event/event.go` — extract `rebuildEventFTS`; extend `Update` with body-tag sync; add `Reparent` (+ `ErrCycle`), `AddTags`, `RemoveTags`.
- `internal/event/event_test.go` — update existing `Update` tests for the new sync semantics; add `TestReparent_*`, `TestAddTags_*`, `TestRemoveTags_*`, `TestUpdate_TextSyncsBodyTags`, `TestUpdate_TextDedupsRepeatedBodyTags`.
- `internal/event/store.go` — `Store.Reparent`, `Store.AddTags`, `Store.RemoveTags`.
- `internal/event/store_test.go` — direct tests for the three new wrappers.
- `cmd/fngr/store.go` — extend `eventStore` interface with `Reparent`, `AddTags`, `RemoveTags`.
- `cmd/fngr/main.go` — drop `Show`, `Edit` fields; add `Event EventCmd`.
- `cmd/fngr/dispatch_test.go` — drop `show`/`edit` cases; add `event` cases.

**Deleted:**
- `cmd/fngr/show.go`, `cmd/fngr/show_test.go`
- `cmd/fngr/edit.go`, `cmd/fngr/edit_test.go`

**Not committed (per project policy):** `README.md`, `CLAUDE.md`, `REVIEW.md`.

---

### Task 1: Migration 2 — dedupe and UNIQUE index on `event_meta`

**Files:**
- Modify: `internal/db/migrate.go`
- Modify: `internal/db/db_test.go`

- [ ] **Step 1: Add the failing test.** Append to `internal/db/db_test.go`:

```go
func TestMigrate_V2DedupesAndAddsUnique(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	// Bring the schema up to v1 only.
	if _, err := db.Exec(migrations[0].up); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if err := setUserVersion(db, 1); err != nil {
		t.Fatalf("set v1: %v", err)
	}

	// One event with three duplicate (event_id, key, value) rows in event_meta.
	if _, err := db.Exec("INSERT INTO events (text) VALUES (?)", "x"); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	for range 3 {
		if _, err := db.Exec(
			"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'tag', 'ops')",
		); err != nil {
			t.Fatalf("insert duplicate: %v", err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM event_meta WHERE event_id=1 AND key='tag' AND value='ops'",
	).Scan(&count); err != nil {
		t.Fatalf("count after dedupe: %v", err)
	}
	if count != 1 {
		t.Errorf("got %d rows after dedupe, want 1", count)
	}

	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'tag', 'ops')",
	); err == nil {
		t.Error("expected UNIQUE constraint error on duplicate insert")
	}

	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'tag', 'ops') ON CONFLICT DO NOTHING",
	); err != nil {
		t.Errorf("ON CONFLICT DO NOTHING raised error: %v", err)
	}

	v, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./internal/db/ -run TestMigrate_V2 -v`
Expected: FAIL because migration 2 doesn't exist yet (test asserts `user_version == 2`).

- [ ] **Step 3: Add migration 2.** In `internal/db/migrate.go`, find the `migrations` slice. After the existing `{version: 1, up: …}` entry add a comma and a second entry:

```go
	{
		version: 2,
		up: `
			-- Pre-emptive dedupe (no-op when Add already deduped via
			-- parse.CollectMeta, which is the only known insert path).
			DELETE FROM event_meta
			 WHERE rowid NOT IN (
			   SELECT MIN(rowid) FROM event_meta
			    GROUP BY event_id, key, value
			 );

			-- Replace the non-unique (key, value) index with a UNIQUE
			-- index on (key, value, event_id). Same prefix-lookup
			-- performance for ListMeta / CountMeta plus DB-level
			-- uniqueness so INSERT ... ON CONFLICT DO NOTHING works.
			DROP INDEX IF EXISTS idx_event_meta_key_value;
			CREATE UNIQUE INDEX idx_event_meta_key_value_event_id
			    ON event_meta(key, value, event_id);
		`,
	},
```

- [ ] **Step 4: Run test to verify it passes.**

Run: `go test ./internal/db/ -run TestMigrate_V2 -v`
Expected: PASS.

- [ ] **Step 5: Run the full migrate suite to confirm v1→v2 fresh-DB path also passes.**

Run: `go test ./internal/db/...`
Expected: PASS (all `TestMigrate_*` and the cascade/FTS-trigger tests).

- [ ] **Step 6: Run the full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/db/migrate.go internal/db/db_test.go
git commit -m "$(cat <<'EOF'
feat(db): migration 2 — dedupe event_meta + UNIQUE index

Adds the prerequisite for INSERT ... ON CONFLICT DO NOTHING in S2's
tag flow: dedupe any stray (event_id, key, value) duplicates (no-op
in practice because Add already dedups), then replace the non-unique
(key, value) index with a UNIQUE (key, value, event_id) index. Same
prefix-lookup behaviour for ListMeta / CountMeta; idx_event_meta_event_id
keeps serving loadMetaBatch unchanged.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: `timefmt.ParsePartial`

**Files:**
- Modify: `internal/timefmt/timefmt.go`
- Modify: `internal/timefmt/timefmt_test.go`

- [ ] **Step 1: Add the failing test.** Append to `internal/timefmt/timefmt_test.go`:

```go
func TestParsePartial(t *testing.T) {
	t.Parallel()
	now := time.Now()
	tests := []struct {
		name        string
		input       string
		wantHasDate bool
		wantHasTime bool
		check       func(t *testing.T, got time.Time)
	}{
		{
			name:        "date only",
			input:       "2026-04-15",
			wantHasDate: true,
			wantHasTime: false,
			check: func(t *testing.T, got time.Time) {
				want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:        "datetime",
			input:       "2026-04-15T14:30",
			wantHasDate: true,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				want := time.Date(2026, 4, 15, 14, 30, 0, 0, time.Local)
				if !got.Equal(want) {
					t.Errorf("got %v, want %v", got, want)
				}
			},
		},
		{
			name:        "RFC3339",
			input:       now.UTC().Format(time.RFC3339),
			wantHasDate: true,
			wantHasTime: true,
			check:       nil, // value-tested by the parse round-trip
		},
		{
			name:        "24h time only",
			input:       "09:30",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 9 || got.Minute() != 30 {
					t.Errorf("got h=%d m=%d, want 9:30", got.Hour(), got.Minute())
				}
				// Time-only inputs fill today's local date.
				today := time.Now()
				if got.Year() != today.Year() || got.Month() != today.Month() || got.Day() != today.Day() {
					t.Errorf("got date %v, want today", got)
				}
			},
		},
		{
			name:        "12h pm",
			input:       "2:15PM",
			wantHasDate: false,
			wantHasTime: true,
			check: func(t *testing.T, got time.Time) {
				if got.Hour() != 14 || got.Minute() != 15 {
					t.Errorf("got h=%d m=%d, want 14:15", got.Hour(), got.Minute())
				}
			},
		},
		{
			name:        "garbage",
			input:       "not a time",
			wantHasDate: false,
			wantHasTime: false,
			check:       nil, // err path
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, hasDate, hasTime, err := ParsePartial(tt.input)
			if tt.input == "not a time" {
				if err == nil {
					t.Fatalf("ParsePartial(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePartial(%q) err = %v", tt.input, err)
			}
			if hasDate != tt.wantHasDate || hasTime != tt.wantHasTime {
				t.Errorf("ParsePartial(%q) hasDate=%v hasTime=%v, want %v/%v",
					tt.input, hasDate, hasTime, tt.wantHasDate, tt.wantHasTime)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./internal/timefmt/ -run TestParsePartial -v`
Expected: FAIL with `undefined: ParsePartial`.

- [ ] **Step 3: Implement `ParsePartial`; refactor `Parse`.** Replace the existing `Parse` function in `internal/timefmt/timefmt.go` with:

```go
// ParsePartial parses s using the same layouts as Parse but reports which
// components were present in the input. Time-only inputs (e.g. "9:30",
// "3:04PM") return hasDate=false; date-only inputs ("2026-04-15") return
// hasTime=false; full timestamps return both true.
//
// When hasDate is false, the returned t carries today's local date so the
// caller can either use it as-is or splice into another date.
func ParsePartial(s string) (t time.Time, hasDate, hasTime bool, err error) {
	for _, layout := range fullFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, true, layoutHasTime(layout), nil
		}
	}
	for _, layout := range timeOnlyFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			now := time.Now()
			t = time.Date(
				now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), t.Nanosecond(),
				t.Location(),
			)
			return t, false, true, nil
		}
	}
	return time.Time{}, false, false, fmt.Errorf("unrecognized time %q (try YYYY-MM-DD, YYYY-MM-DDTHH:MM, RFC3339, HH:MM, or 3:04PM)", s)
}

// layoutHasTime reports whether layout (one of fullFormats) carries a time
// component. The only date-only layout in fullFormats is DateFormat.
func layoutHasTime(layout string) bool { return layout != DateFormat }

// Parse accepts a timestamp in one of several layouts. Time-only inputs
// (e.g. "15:04", "3:04PM") are completed with today's local date.
func Parse(s string) (time.Time, error) {
	t, _, _, err := ParsePartial(s)
	return t, err
}
```

- [ ] **Step 4: Run test to verify it passes.**

Run: `go test ./internal/timefmt/ -run TestParsePartial -v`
Expected: PASS for all six subtests.

- [ ] **Step 5: Run the package's existing tests to confirm `Parse` still works through the wrapper.**

Run: `go test ./internal/timefmt/...`
Expected: PASS (the existing `TestParse_*` calls go through the same code path).

- [ ] **Step 6: Run the full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/timefmt/timefmt.go internal/timefmt/timefmt_test.go
git commit -m "$(cat <<'EOF'
feat(timefmt): ParsePartial reports which components were in input

Same layouts as Parse, but exposes hasDate/hasTime so the caller can
decide whether to splice into an existing timestamp or replace it
outright. Parse becomes a one-line wrapper. Used by the upcoming
event time/date verbs to distinguish partial from full input.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `parse.MetaArg`

**Files:**
- Modify: `internal/parse/parse.go`
- Modify: `internal/parse/parse_test.go`

- [ ] **Step 1: Add the failing test.** Append to `internal/parse/parse_test.go`:

```go
func TestMetaArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{name: "people", input: "@Sarah", wantKey: "people", wantVal: "Sarah"},
		{name: "tag", input: "#ops", wantKey: "tag", wantVal: "ops"},
		{name: "key=value", input: "env=prod", wantKey: "env", wantVal: "prod"},
		{name: "value with =", input: "note=a=b", wantKey: "note", wantVal: "a=b"},
		{name: "hierarchical tag", input: "#work/project-x", wantKey: "tag", wantVal: "work/project-x"},
		{name: "empty value", input: "k=", wantKey: "k", wantVal: ""},

		{name: "bare word", input: "urgent", wantErr: true},
		{name: "lone @", input: "@", wantErr: true},
		{name: "lone #", input: "#", wantErr: true},
		{name: "@ with space", input: "@ Sarah", wantErr: true},
		{name: "missing key", input: "=value", wantErr: true},
		{name: "empty input", input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MetaArg(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("MetaArg(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("MetaArg(%q) err = %v", tt.input, err)
			}
			if got.Key != tt.wantKey || got.Value != tt.wantVal {
				t.Errorf("MetaArg(%q) = (%q, %q), want (%q, %q)",
					tt.input, got.Key, got.Value, tt.wantKey, tt.wantVal)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails.**

Run: `go test ./internal/parse/ -run TestMetaArg -v`
Expected: FAIL with `undefined: MetaArg`.

- [ ] **Step 3: Implement `MetaArg`.** In `internal/parse/parse.go`, add this function after the existing `KeyValue` function (or wherever feels natural; keep package alphabetical-ish):

```go
// metaArgRe matches the body of an @person or #tag arg. Same character
// class as the body-tag patterns: word chars plus '/' and '-', starting
// with a word char.
var metaArgRe = regexp.MustCompile(`^[\w][\w/\-]*$`)

// MetaArg parses a single CLI argument into a Meta entry. Supported forms:
//
//	"@name"      -> {people, name}
//	"#name"      -> {tag, name}
//	"key=value"  -> {key, value}      (delegates to KeyValue)
//
// Names following @ or # must match the body-tag regex [\w][\w/\-]*. Any
// other shape is rejected with the message "expected @person, #tag, or
// key=value".
func MetaArg(s string) (Meta, error) {
	if len(s) == 0 {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got empty arg")
	}
	switch s[0] {
	case '@':
		name := s[1:]
		if !metaArgRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid @person arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "people", Value: name}, nil
	case '#':
		name := s[1:]
		if !metaArgRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid #tag arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "tag", Value: name}, nil
	}
	if !strings.Contains(s, "=") {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got %q", s)
	}
	key, value, err := KeyValue(s)
	if err != nil {
		return Meta{}, err
	}
	if key == "" {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got %q (empty key)", s)
	}
	return Meta{Key: key, Value: value}, nil
}
```

- [ ] **Step 4: Run test to verify it passes.**

Run: `go test ./internal/parse/ -run TestMetaArg -v`
Expected: PASS for all twelve subtests.

- [ ] **Step 5: Run full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/parse/parse.go internal/parse/parse_test.go
git commit -m "$(cat <<'EOF'
feat(parse): MetaArg parses one CLI tag arg

Accepts @person, #tag, or key=value (delegates to KeyValue for the
last form). Names after @/# must match the existing body-tag regex
[\w][\w/\-]*. Used by the upcoming event tag/untag verbs.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Extract `rebuildEventFTS` helper (refactor; no behavior change)

**Files:**
- Modify: `internal/event/event.go`

- [ ] **Step 1: Identify the existing FTS update inside `Update`.** Open `internal/event/event.go` and find `Update`. The block that rebuilds FTS today is:

```go
		if text != nil {
			meta, err := readMetaTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE events_fts SET content = ? WHERE rowid = ?",
				parse.FTSContent(*text, meta), id,
			); err != nil {
				return fmt.Errorf("update FTS: %w", err)
			}
		}
```

- [ ] **Step 2: Add a private helper.** Add this new function next to `readMetaTx` in `internal/event/event.go`:

```go
// rebuildEventFTS reads the event's current text + meta inside tx and
// writes parse.FTSContent into events_fts.
func rebuildEventFTS(ctx context.Context, tx *sql.Tx, id int64) error {
	var text string
	if err := tx.QueryRowContext(ctx, "SELECT text FROM events WHERE id = ?", id).Scan(&text); err != nil {
		return fmt.Errorf("read event text for FTS: %w", err)
	}
	meta, err := readMetaTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE events_fts SET content = ? WHERE rowid = ?",
		parse.FTSContent(text, meta), id,
	); err != nil {
		return fmt.Errorf("update FTS: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Replace the inline block in `Update` with a call to the helper.** In `Update`, replace the `if text != nil { meta, err := readMetaTx(...); ... }` block with:

```go
	if text != nil {
		if err := rebuildEventFTS(ctx, tx, id); err != nil {
			return err
		}
	}
```

- [ ] **Step 4: Run all event tests to confirm no behavior change.**

Run: `go test ./internal/event/...`
Expected: PASS.

- [ ] **Step 5: Run full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/event/event.go
git commit -m "$(cat <<'EOF'
refactor(event): extract rebuildEventFTS helper

Pulls the FTS-rebuild block out of Update so the upcoming AddTags,
RemoveTags, and body-tag-sync paths share one implementation. Pure
refactor: same SQL, same call site behaviour.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: `event.Reparent` + `ErrCycle` + `Store.Reparent`

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`

- [ ] **Step 1: Write the failing tests.** Append to `internal/event/event_test.go`:

```go
func TestReparent_DetachClearsParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parent, err := Add(ctx, database, "parent", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	child, err := Add(ctx, database, "child", &parent, nil, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	if err := Reparent(ctx, database, child, nil); err != nil {
		t.Fatalf("Reparent detach: %v", err)
	}

	ev, err := Get(ctx, database, child)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.ParentID != nil {
		t.Errorf("ParentID = %d, want nil", *ev.ParentID)
	}
}

func TestReparent_AllowsValidMove(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	a, _ := Add(ctx, database, "a", nil, nil, nil)
	b, _ := Add(ctx, database, "b", nil, nil, nil)
	c, _ := Add(ctx, database, "c", &a, nil, nil)

	if err := Reparent(ctx, database, c, &b); err != nil {
		t.Fatalf("Reparent: %v", err)
	}
	ev, _ := Get(ctx, database, c)
	if ev.ParentID == nil || *ev.ParentID != b {
		t.Errorf("ParentID = %v, want %d", ev.ParentID, b)
	}
}

func TestReparent_RejectsSelf(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x", nil, nil, nil)

	err := Reparent(ctx, database, id, &id)
	if !errors.Is(err, ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestReparent_RejectsAncestryCycle(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// 1 -> 2 -> 3   (3 has parent 2 has parent 1)
	a, _ := Add(ctx, database, "a", nil, nil, nil)
	b, _ := Add(ctx, database, "b", &a, nil, nil)
	c, _ := Add(ctx, database, "c", &b, nil, nil)

	// Attaching a (top) to c (descendant) would form a cycle.
	err := Reparent(ctx, database, a, &c)
	if !errors.Is(err, ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestReparent_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	err := Reparent(ctx, database, 9999, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestReparent_NewParentNotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, "x", nil, nil, nil)

	missing := int64(9999)
	err := Reparent(ctx, database, id, &missing)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/event/ -run TestReparent -v`
Expected: FAIL with `undefined: Reparent` and `undefined: ErrCycle`.

- [ ] **Step 3: Implement.** Add to `internal/event/event.go` (place after `Update` is fine):

```go
// ErrCycle is returned when Reparent would introduce a cycle (including
// the self-parent case).
var ErrCycle = errors.New("would create a parent cycle")

// Reparent sets event id's parent to newParent, or clears it when
// newParent is nil. Walks the candidate parent's ancestry chain and
// returns ErrCycle if id appears in it (including newParent == &id).
// Returns ErrNotFound if id or *newParent does not exist.
func Reparent(ctx context.Context, db *sql.DB, id int64, newParent *int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Confirm the event exists.
	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("event %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("query event: %w", err)
	}

	if newParent != nil {
		if *newParent == id {
			return fmt.Errorf("self-parent on event %d: %w", id, ErrCycle)
		}

		// Walk ancestry from *newParent upward; reject if we hit id.
		cursor := *newParent
		for {
			var parent sql.NullInt64
			err := tx.QueryRowContext(ctx, "SELECT parent_id FROM events WHERE id = ?", cursor).Scan(&parent)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("parent event %d: %w", cursor, ErrNotFound)
				}
				return fmt.Errorf("walk ancestry: %w", err)
			}
			if !parent.Valid {
				break
			}
			if parent.Int64 == id {
				return fmt.Errorf("attaching event %d to event %d would form a cycle: %w", id, *newParent, ErrCycle)
			}
			cursor = parent.Int64
		}

		if _, err := tx.ExecContext(ctx,
			"UPDATE events SET parent_id = ? WHERE id = ?", *newParent, id,
		); err != nil {
			return fmt.Errorf("set parent: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			"UPDATE events SET parent_id = NULL WHERE id = ?", id,
		); err != nil {
			return fmt.Errorf("clear parent: %w", err)
		}
	}

	return tx.Commit()
}
```

- [ ] **Step 4: Add the `Store.Reparent` wrapper.** Append to `internal/event/store.go` (next to other Store methods):

```go
func (s *Store) Reparent(ctx context.Context, id int64, newParent *int64) error {
	return Reparent(ctx, s.DB, id, newParent)
}
```

- [ ] **Step 5: Add a direct Store test.** Append to `internal/event/store_test.go`:

```go
func TestStore_Reparent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	a, _ := s.Add(ctx, "a", nil, nil, nil)
	b, _ := s.Add(ctx, "b", nil, nil, nil)
	c, _ := s.Add(ctx, "c", &a, nil, nil)

	if err := s.Reparent(ctx, c, &b); err != nil {
		t.Fatalf("Reparent: %v", err)
	}
	ev, err := s.Get(ctx, c)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.ParentID == nil || *ev.ParentID != b {
		t.Errorf("ParentID = %v, want %d", ev.ParentID, b)
	}
}
```

- [ ] **Step 6: Run all event tests.**

Run: `go test ./internal/event/ -run "Reparent|TestStore_Reparent" -v`
Expected: PASS for all subtests.

- [ ] **Step 7: Run full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go internal/event/store.go internal/event/store_test.go
git commit -m "$(cat <<'EOF'
feat(event): Reparent + ErrCycle for attach/detach

Walks the candidate parent's ancestry chain to reject self-parent and
any cycle. Detach clears parent_id when newParent is nil. Wrapper +
direct Store test included so the wrapper isn't covered transitively.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: `event.AddTags` + `Store.AddTags`

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`

- [ ] **Step 1: Write the failing tests.** Append to `internal/event/event_test.go`:

```go
func TestAddTags_DedupsAtDB(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	tags := []parse.Meta{
		{Key: "tag", Value: "ops"},  // already there
		{Key: "tag", Value: "deploy"},
		{Key: "people", Value: "alice"},
	}
	if err := AddTags(ctx, database, id, tags); err != nil {
		t.Fatalf("AddTags: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:       true,
		{Key: "tag", Value: "deploy"}:    true,
		{Key: "people", Value: "alice"}:  true,
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta entries, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestAddTags_RebuildsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "no tags here", nil, nil, nil)
	if err := AddTags(ctx, database, id, []parse.Meta{{Key: "tag", Value: "ops"}}); err != nil {
		t.Fatalf("AddTags: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("FTS not refreshed: %d matches for #ops, want 1", len(matches))
	}
}

func TestAddTags_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	err := AddTags(ctx, database, 9999, []parse.Meta{{Key: "tag", Value: "x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAddTags_EmptyIsNoOp(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, "x", nil, nil, nil)
	if err := AddTags(ctx, database, id, nil); err != nil {
		t.Errorf("empty AddTags returned err: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/event/ -run TestAddTags -v`
Expected: FAIL with `undefined: AddTags`.

- [ ] **Step 3: Implement.** Add to `internal/event/event.go`:

```go
// AddTags inserts the given meta entries for event id. Duplicates are
// dropped at the database via INSERT ... ON CONFLICT DO NOTHING (the
// UNIQUE index on (key, value, event_id) added in migration 2). FTS is
// rebuilt in the same transaction. Returns ErrNotFound if the event is
// missing. Empty `tags` is a no-op.
func AddTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) error {
	if len(tags) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("event %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("query event: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, m := range tags {
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value); err != nil {
			return fmt.Errorf("insert tag: %w", err)
		}
	}

	if err := rebuildEventFTS(ctx, tx, id); err != nil {
		return err
	}

	return tx.Commit()
}
```

- [ ] **Step 4: Add `Store.AddTags`.** Append to `internal/event/store.go`:

```go
func (s *Store) AddTags(ctx context.Context, id int64, tags []parse.Meta) error {
	return AddTags(ctx, s.DB, id, tags)
}
```

- [ ] **Step 5: Add direct Store test.** Append to `internal/event/store_test.go`:

```go
func TestStore_AddTags(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.Add(ctx, "x", nil, nil, nil)
	if err := s.AddTags(ctx, id, []parse.Meta{{Key: "tag", Value: "ops"}}); err != nil {
		t.Fatalf("AddTags: %v", err)
	}
	n, err := s.CountMeta(ctx, "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}
```

- [ ] **Step 6: Run tests.**

Run: `go test ./internal/event/ -run "AddTags" -v`
Expected: PASS.

- [ ] **Step 7: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go internal/event/store.go internal/event/store_test.go
git commit -m "$(cat <<'EOF'
feat(event): AddTags inserts meta with DB-level dedup + FTS rebuild

Uses INSERT ... ON CONFLICT DO NOTHING against the UNIQUE index
added in migration 2. Wrapped by Store.AddTags. Empty tags is a
no-op. FTS rebuilt inside the same transaction.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: `event.RemoveTags` + `Store.RemoveTags`

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`

- [ ] **Step 1: Write the failing tests.** Append to `internal/event/event_test.go`:

```go
func TestRemoveTags_ReturnsCount(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "deploy"},
		{Key: "people", Value: "alice"},
	}, nil)

	n, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "missing"},
	})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}

	ev, _ := Get(ctx, database, id)
	for _, m := range ev.Meta {
		if m.Key == "tag" && m.Value == "ops" {
			t.Error("tag=ops not removed")
		}
	}
}

func TestRemoveTags_RebuildsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x #ops", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	if _, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}); err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("FTS not refreshed: %d matches for #ops, want 0", len(matches))
	}
}

func TestRemoveTags_NoMatchReturnsZero(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x", nil, nil, nil)
	n, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ghost"},
	})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestRemoveTags_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	_, err := RemoveTags(ctx, database, 9999, []parse.Meta{{Key: "tag", Value: "x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRemoveTags_EmptyIsNoOp(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, "x", nil, nil, nil)
	n, err := RemoveTags(ctx, database, id, nil)
	if err != nil {
		t.Errorf("empty RemoveTags err: %v", err)
	}
	if n != 0 {
		t.Errorf("empty RemoveTags returned %d, want 0", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail.**

Run: `go test ./internal/event/ -run TestRemoveTags -v`
Expected: FAIL with `undefined: RemoveTags`.

- [ ] **Step 3: Implement.** Add to `internal/event/event.go`:

```go
// RemoveTags deletes (event_id, key, value) rows matching tags. Returns
// the number of rows removed. FTS rebuilt in the same transaction.
// Returns ErrNotFound if the event is missing; (0, nil) is a valid
// outcome when none of the tags were present. Empty tags is a no-op.
func RemoveTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var dummy int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("event %d: %w", id, ErrNotFound)
		}
		return 0, fmt.Errorf("query event: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		"DELETE FROM event_meta WHERE event_id = ? AND key = ? AND value = ?",
	)
	if err != nil {
		return 0, fmt.Errorf("prepare delete: %w", err)
	}
	defer stmt.Close()

	var total int64
	for _, m := range tags {
		res, err := stmt.ExecContext(ctx, id, m.Key, m.Value)
		if err != nil {
			return 0, fmt.Errorf("delete tag: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		total += n
	}

	if err := rebuildEventFTS(ctx, tx, id); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return total, nil
}
```

- [ ] **Step 4: Add `Store.RemoveTags`.** Append to `internal/event/store.go`:

```go
func (s *Store) RemoveTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error) {
	return RemoveTags(ctx, s.DB, id, tags)
}
```

- [ ] **Step 5: Add direct Store test.** Append to `internal/event/store_test.go`:

```go
func TestStore_RemoveTags(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.Add(ctx, "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	n, err := s.RemoveTags(ctx, id, []parse.Meta{{Key: "tag", Value: "ops"}})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
}
```

- [ ] **Step 6: Run tests.**

Run: `go test ./internal/event/ -run "RemoveTags" -v`
Expected: PASS.

- [ ] **Step 7: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go internal/event/store.go internal/event/store_test.go
git commit -m "$(cat <<'EOF'
feat(event): RemoveTags deletes meta entries + rebuilds FTS

Returns the count of rows removed; (0, nil) is valid (no match).
Wrapped by Store.RemoveTags. FTS rebuilt inside the same tx so the
index reflects the post-delete state.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Extend `event.Update` with body-tag sync semantics

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`

- [ ] **Step 1: Write the failing test.** Append to `internal/event/event_test.go`:

```go
func TestUpdate_TextSyncsBodyTags(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// Pre-existing meta: body-derived (alice, ops) plus non-body (env=prod, author).
	id, err := Add(ctx, database, "deploy with @alice #ops", nil, []parse.Meta{
		{Key: "author", Value: "nicolas"},
		{Key: "people", Value: "alice"},
		{Key: "tag", Value: "ops"},
		{Key: "env", Value: "prod"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Replace text: drop @alice, add @bob, keep #ops.
	newText := "rolled back with @bob #ops"
	if err := Update(ctx, database, id, &newText, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "author", Value: "nicolas"}: true, // non-body — preserved
		{Key: "env", Value: "prod"}:       true, // non-body — preserved
		{Key: "tag", Value: "ops"}:        true, // body in both → kept (exactly once)
		{Key: "people", Value: "bob"}:     true, // new body tag
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta entries, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v (alice should have been removed)", m)
		}
	}
}

func TestUpdate_TextDedupsRepeatedBodyTags(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, "x #ops", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	newText := "y #ops #ops"
	if err := Update(ctx, database, id, &newText, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	n, err := CountMeta(ctx, database, "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("tag=ops count = %d, want 1", n)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail.** The first probably fails on the alice-not-removed assertion; the second probably passes already (because dedup at the DB).

Run: `go test ./internal/event/ -run "TestUpdate_Text" -v`
Expected: At minimum `TestUpdate_TextSyncsBodyTags` FAILS.

- [ ] **Step 3: Extend `Update`.** In `internal/event/event.go`, locate `Update`. The current shape (after Task 4) is:

```go
func Update(ctx context.Context, db *sql.DB, id int64, text *string, createdAt *time.Time) error {
	if text == nil && createdAt == nil {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	...
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT text FROM events WHERE id = ?", id).Scan(&existing)
	...
	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if text != nil {
		sets = append(sets, "text = ?")
		args = append(args, *text)
	}
	if createdAt != nil {
		sets = append(sets, "created_at = ?")
		args = append(args, createdAt.UTC().Format(timefmt.DateTimeFormat))
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, "UPDATE events SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return fmt.Errorf("update event: %w", err)
	}

	if text != nil {
		if err := rebuildEventFTS(ctx, tx, id); err != nil {
			return err
		}
	}
	...
}
```

Insert two new steps in the `text != nil` path: untag old body tags BEFORE updating; tag new body tags AFTER updating. Replace the `if text != nil { ... }` blocks (both the SET-collection and the FTS rebuild) so the final body of `Update` is:

```go
func Update(ctx context.Context, db *sql.DB, id int64, text *string, createdAt *time.Time) error {
	if text == nil && createdAt == nil {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existing string
	err = tx.QueryRowContext(ctx, "SELECT text FROM events WHERE id = ?", id).Scan(&existing)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("event %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("query event: %w", err)
	}

	if text != nil {
		// Body-tag sync, step 1: remove tags parsed from the *previous* text.
		oldBodyTags := parse.BodyTags(existing)
		if err := deleteMetaTuples(ctx, tx, id, oldBodyTags); err != nil {
			return err
		}
	}

	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if text != nil {
		sets = append(sets, "text = ?")
		args = append(args, *text)
	}
	if createdAt != nil {
		sets = append(sets, "created_at = ?")
		args = append(args, createdAt.UTC().Format(timefmt.DateTimeFormat))
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, "UPDATE events SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil { // #nosec G202 -- sets is built from a fixed allow-list
		return fmt.Errorf("update event: %w", err)
	}

	if text != nil {
		// Body-tag sync, step 2: insert tags parsed from the *new* text.
		newBodyTags := parse.BodyTags(*text)
		if err := insertMetaTuples(ctx, tx, id, newBodyTags); err != nil {
			return err
		}
		if err := rebuildEventFTS(ctx, tx, id); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// deleteMetaTuples removes (id, key, value) rows for the given tags. Empty
// tags is a no-op.
func deleteMetaTuples(ctx context.Context, tx *sql.Tx, id int64, tags []parse.Meta) error {
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		"DELETE FROM event_meta WHERE event_id = ? AND key = ? AND value = ?",
	)
	if err != nil {
		return fmt.Errorf("prepare delete: %w", err)
	}
	defer stmt.Close()
	for _, m := range tags {
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value); err != nil {
			return fmt.Errorf("delete meta: %w", err)
		}
	}
	return nil
}

// insertMetaTuples inserts (id, key, value) rows for the given tags using
// ON CONFLICT DO NOTHING. Empty tags is a no-op.
func insertMetaTuples(ctx context.Context, tx *sql.Tx, id int64, tags []parse.Meta) error {
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
	)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()
	for _, m := range tags {
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value); err != nil {
			return fmt.Errorf("insert meta: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests.**

Run: `go test ./internal/event/ -run "TestUpdate" -v`
Expected: PASS for both new tests AND all existing `TestUpdate_*` tests (TestUpdate_TextOnly, TimeOnly, NoOp, NotFound).

- [ ] **Step 5: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go
git commit -m "$(cat <<'EOF'
feat(event): Update text syncs body-derived tags

When text changes, parse.BodyTags(oldText) entries are deleted and
parse.BodyTags(newText) entries are inserted via ON CONFLICT DO
NOTHING. Non-body meta (author, --meta key=value, manual tag) is
never touched. FTS rebuilt from the final state. Two new private
helpers (deleteMetaTuples, insertMetaTuples) are reused inside the
single transaction.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Extend `eventStore` interface

**Files:**
- Modify: `cmd/fngr/store.go`

- [ ] **Step 1: Find the `eventStore` interface.** In `cmd/fngr/store.go`, the interface currently has `Update`, `List`, `ListSeq`, `GetSubtree`, plus the meta methods. Add three new methods alphabetically near the existing event-mutation methods. Final interface:

```go
type eventStore interface {
	Add(ctx context.Context, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error)
	AddTags(ctx context.Context, id int64, tags []parse.Meta) error
	Get(ctx context.Context, id int64) (*event.Event, error)
	Delete(ctx context.Context, id int64) error
	Update(ctx context.Context, id int64, text *string, createdAt *time.Time) error
	Reparent(ctx context.Context, id int64, newParent *int64) error
	RemoveTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error)
	HasChildren(ctx context.Context, id int64) (bool, error)
	List(ctx context.Context, opts event.ListOpts) ([]event.Event, error)
	ListSeq(ctx context.Context, opts event.ListOpts) iter.Seq2[event.Event, error]
	GetSubtree(ctx context.Context, rootID int64) ([]event.Event, error)
	ListMeta(ctx context.Context) ([]event.MetaCount, error)
	CountMeta(ctx context.Context, key, value string) (int64, error)
	UpdateMeta(ctx context.Context, oldKey, oldValue, newKey, newValue string) (int64, error)
	DeleteMeta(ctx context.Context, key, value string) (int64, error)
}
```

- [ ] **Step 2: Build to confirm `*event.Store` satisfies the new interface.**

Run: `go build ./...`
Expected: PASS. (`event.Store` already has the three methods from Tasks 5-7.)

- [ ] **Step 3: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 4: Commit.**

```bash
git add cmd/fngr/store.go
git commit -m "$(cat <<'EOF'
refactor(cmd): eventStore gains Reparent / AddTags / RemoveTags

Mechanical addition; *event.Store already implements them.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Replace `cmd/fngr/show.go` with `cmd/fngr/event.go` (read path only)

**Files:**
- Delete: `cmd/fngr/show.go`, `cmd/fngr/show_test.go`
- Create: `cmd/fngr/event.go`
- Create: `cmd/fngr/event_test.go`
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 1: Delete the old show files.**

Run:
```bash
git rm cmd/fngr/show.go cmd/fngr/show_test.go
```
Expected: both removed from index.

- [ ] **Step 2: Create the new event command.** Write `cmd/fngr/event.go`:

```go
package main

import (
	"context"

	"github.com/alecthomas/kong"

	"github.com/monolithiclab/fngr/internal/render"
)

// EventCmd is the parent for all `fngr event N <verb>` invocations. The ID
// is bound into the Kong context so each verb's Run can read it via a
// *EventCmd parameter.
type EventCmd struct {
	ID int64 `arg:"" help:"Event ID."`

	Show EventShowCmd `cmd:"" default:"withargs" help:"Show event detail (default)."`
}

// AfterApply makes *EventCmd available to verb Run methods so they can
// reach the parsed ID.
func (c *EventCmd) AfterApply(kctx *kong.Context) error {
	kctx.Bind(c)
	return nil
}

// EventShowCmd reads the event. Honours --tree (subtree view) and --format.
type EventShowCmd struct {
	Tree   bool   `help:"Show subtree." short:"t"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *EventShowCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Tree {
		events, err := s.GetSubtree(ctx, parent.ID)
		if err != nil {
			return err
		}
		return render.Events(io.Out, c.Format, events)
	}

	ev, err := s.Get(ctx, parent.ID)
	if err != nil {
		return err
	}
	return render.SingleEvent(io.Out, c.Format, ev)
}
```

- [ ] **Step 3: Wire `EventCmd` into the CLI.** In `cmd/fngr/main.go`, find the `CLI` struct. Currently it has:

```go
	Show   ShowCmd   `cmd:"" help:"Show a single event."`
	Edit   EditCmd   `cmd:"" help:"Edit an event's text or timestamp."`
```

Replace with:

```go
	Event EventCmd `cmd:"" help:"Show or modify a single event."`
```

(The `default:"withargs"` on `List` from S1 stays put.)

- [ ] **Step 4: Update the dispatch test.** In `cmd/fngr/dispatch_test.go`, remove the entries for `show` and `edit`. Add an entry for the new event read path:

Find the `cases` slice and remove these lines:
```go
		{name: "show", argv: []string{"show", "1"}, want: ""},
		{name: "edit", argv: []string{"edit", "1", "--text", "x", "-f"}, want: ""},
```
Add at the appropriate spot in the slice:
```go
		{name: "event-show", argv: []string{"event", "1"}, want: ""},
		{name: "event-show-tree", argv: []string{"event", "1", "--tree"}, want: ""},
		{name: "event-show-json", argv: []string{"event", "1", "--format", "json"}, want: ""},
```

If there is also a `TestKongDispatch_AddThenListEndToEnd` in the file that mentions `show` or `edit`, leave it alone — it only references `add` and `list`.

- [ ] **Step 5: Add a focused event-show test.** Write `cmd/fngr/event_test.go`:

```go
package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestEventCmd_ShowText(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, err := s.Add(context.Background(), "show me", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	parent := &EventCmd{ID: id}
	cmd := &EventShowCmd{Format: "text"}
	if err := cmd.Run(parent, s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "show me") || !strings.Contains(got, "ID:") {
		t.Errorf("output = %q, want detail text", got)
	}
}

func TestEventCmd_ShowSubtree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	parent, err := s.Add(context.Background(), "parent", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(context.Background(), "child", &parent, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add child: %v", err)
	}

	cmd := &EventShowCmd{Tree: true, Format: "tree"}
	if err := cmd.Run(&EventCmd{ID: parent}, s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "parent") || !strings.Contains(got, "child") {
		t.Errorf("output = %q, want both parent and child", got)
	}
}

func TestEventCmd_ShowNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &EventShowCmd{}
	err := cmd.Run(&EventCmd{ID: 9999}, s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 6: Run cmd/fngr tests.**

Run: `go test ./cmd/fngr/ -v`
Expected: PASS for the new event tests, PASS for the dispatch test (with the bare-fngr + event entries), and the show tests should be gone.

- [ ] **Step 7: Smoke-test the binary.**

Run:
```bash
make build
./build/fngr --db /tmp/fngr-s2-show.db add "smoke test" --author tester
./build/fngr --db /tmp/fngr-s2-show.db event 1
./build/fngr --db /tmp/fngr-s2-show.db event 1 --tree --format json
rm /tmp/fngr-s2-show.db
```
Expected: `Added event 1`; event detail output containing `smoke test`; JSON output containing the event.

- [ ] **Step 8: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
git add cmd/fngr/event.go cmd/fngr/event_test.go cmd/fngr/main.go cmd/fngr/dispatch_test.go cmd/fngr/show.go cmd/fngr/show_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): replace fngr show with fngr event N (read path)

EventCmd holds the ID and binds itself via AfterApply so verbs can
reach it via a *EventCmd parameter. EventShowCmd covers today's show
behaviour (text/json/csv + --tree). cmd/fngr/show.go and its test
file are removed; dispatch test updated to exercise event N / event
N --tree / event N --format json.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Add `text`, `time`, `date` verbs (replaces `fngr edit`)

**Files:**
- Delete: `cmd/fngr/edit.go`, `cmd/fngr/edit_test.go`
- Modify: `cmd/fngr/event.go`
- Modify: `cmd/fngr/event_test.go`
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 1: Delete the old edit files.**

Run:
```bash
git rm cmd/fngr/edit.go cmd/fngr/edit_test.go
```

- [ ] **Step 2: Add the three verb structs to `EventCmd`.** In `cmd/fngr/event.go`, expand `EventCmd` with three new fields and add the three verb types and their `Run` methods. Final state of `EventCmd`:

```go
type EventCmd struct {
	ID int64 `arg:"" help:"Event ID."`

	Show EventShowCmd `cmd:"" default:"withargs" help:"Show event detail (default)."`
	Text EventTextCmd `cmd:"" help:"Replace event text."`
	Time EventTimeCmd `cmd:"" help:"Replace clock time (or full timestamp)."`
	Date EventDateCmd `cmd:"" help:"Replace date (or full timestamp)."`
}
```

Add the three verb types (any order; place after `EventShowCmd`):

```go
// EventTextCmd replaces the event's text. Body tags are synced.
type EventTextCmd struct {
	Body string `arg:"" help:"New event text."`
}

func (c *EventTextCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Body == "" {
		return fmt.Errorf("event text cannot be empty")
	}
	if err := s.Update(ctx, parent.ID, &c.Body, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", parent.ID)
	return nil
}

// EventTimeCmd replaces the clock time (or both date+time when given a
// full timestamp).
type EventTimeCmd struct {
	Value string `arg:"" help:"New time (HH:MM, 3:04PM, ...) or full timestamp."`
}

func (c *EventTimeCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	parsed, hasDate, hasTime, err := timefmt.ParsePartial(c.Value)
	if err != nil {
		return fmt.Errorf("--time: %w", err)
	}
	if !hasTime {
		return fmt.Errorf("event time: expected a time or full timestamp, got date-only %q", c.Value)
	}

	var when time.Time
	if hasDate {
		when = parsed
	} else {
		ev, err := s.Get(ctx, parent.ID)
		if err != nil {
			return err
		}
		orig := ev.CreatedAt.Local()
		when = time.Date(
			orig.Year(), orig.Month(), orig.Day(),
			parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(),
			orig.Location(),
		)
	}

	if err := s.Update(ctx, parent.ID, nil, &when); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", parent.ID)
	return nil
}

// EventDateCmd replaces the date (or both date+time when given a full
// timestamp).
type EventDateCmd struct {
	Value string `arg:"" help:"New date (YYYY-MM-DD) or full timestamp."`
}

func (c *EventDateCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	parsed, hasDate, hasTime, err := timefmt.ParsePartial(c.Value)
	if err != nil {
		return fmt.Errorf("--date: %w", err)
	}
	if !hasDate {
		return fmt.Errorf("event date: expected a date or full timestamp, got time-only %q", c.Value)
	}

	var when time.Time
	if hasTime {
		when = parsed
	} else {
		ev, err := s.Get(ctx, parent.ID)
		if err != nil {
			return err
		}
		orig := ev.CreatedAt.Local()
		when = time.Date(
			parsed.Year(), parsed.Month(), parsed.Day(),
			orig.Hour(), orig.Minute(), orig.Second(), orig.Nanosecond(),
			orig.Location(),
		)
	}

	if err := s.Update(ctx, parent.ID, nil, &when); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", parent.ID)
	return nil
}
```

Add `"fmt"`, `"time"`, and `"github.com/monolithiclab/fngr/internal/timefmt"` to the import block of `cmd/fngr/event.go`.

- [ ] **Step 3: Add tests.** Append to `cmd/fngr/event_test.go`:

```go
func TestEventCmd_TextRequiresNonEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTextCmd{Body: ""}
	err := cmd.Run(&EventCmd{ID: id}, s, io)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("err = %v, want empty-text error", err)
	}
}

func TestEventCmd_TextSyncs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), "first @alice", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "people", Value: "alice"},
	}, nil)

	cmd := &EventTextCmd{Body: "second @bob"}
	if err := cmd.Run(&EventCmd{ID: id}, s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Updated event") {
		t.Errorf("output = %q, want Updated event", out.String())
	}

	ev, _ := s.Get(context.Background(), id)
	want := map[parse.Meta]bool{
		{Key: "author", Value: "alice"}: true,
		{Key: "people", Value: "bob"}:   true,
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestEventCmd_TimePreservesDate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	orig := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	id, _ := s.Add(context.Background(), "x", nil, nil, &orig)

	cmd := &EventTimeCmd{Value: "09:30"}
	if err := cmd.Run(&EventCmd{ID: id}, s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != time.April || got.Day() != 15 {
		t.Errorf("date drifted: %v", got)
	}
	if got.Hour() != 9 || got.Minute() != 30 {
		t.Errorf("clock = %d:%02d, want 09:30", got.Hour(), got.Minute())
	}
}

func TestEventCmd_TimeRejectsDateOnly(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTimeCmd{Value: "2026-04-15"}
	err := cmd.Run(&EventCmd{ID: id}, s, io)
	if err == nil || !strings.Contains(err.Error(), "date-only") {
		t.Errorf("err = %v, want date-only rejection", err)
	}
}

func TestEventCmd_DatePreservesTime(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	orig := time.Date(2026, 4, 15, 14, 30, 0, 0, time.UTC)
	id, _ := s.Add(context.Background(), "x", nil, nil, &orig)

	cmd := &EventDateCmd{Value: "2026-05-01"}
	if err := cmd.Run(&EventCmd{ID: id}, s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != time.May || got.Day() != 1 {
		t.Errorf("date wrong: %v", got)
	}
	if got.Hour() != 14 || got.Minute() != 30 {
		t.Errorf("clock drifted: %d:%02d, want 14:30", got.Hour(), got.Minute())
	}
}

func TestEventCmd_DateRejectsTimeOnly(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventDateCmd{Value: "09:30"}
	err := cmd.Run(&EventCmd{ID: id}, s, io)
	if err == nil || !strings.Contains(err.Error(), "time-only") {
		t.Errorf("err = %v, want time-only rejection", err)
	}
}
```

Add `"time"` to the import block of `cmd/fngr/event_test.go` if not already there.

- [ ] **Step 4: Update the dispatch test.** Append three rows to the `cases` slice in `cmd/fngr/dispatch_test.go`:

```go
		{name: "event-text", argv: []string{"event", "1", "text", "x"}, want: ""},
		{name: "event-time", argv: []string{"event", "1", "time", "09:30"}, want: ""},
		{name: "event-date", argv: []string{"event", "1", "date", "2026-05-01"}, want: ""},
```

- [ ] **Step 5: Run tests.**

Run: `go test ./cmd/fngr/ -v`
Expected: PASS for new tests, dispatch test green, no remaining edit-related tests.

- [ ] **Step 6: Smoke test.**

Run:
```bash
make build
./build/fngr --db /tmp/fngr-s2-edit.db add "first @alice" --author nicolas
./build/fngr --db /tmp/fngr-s2-edit.db event 1 text "second @bob"
./build/fngr --db /tmp/fngr-s2-edit.db event 1 time "09:30"
./build/fngr --db /tmp/fngr-s2-edit.db event 1 date "2026-05-01"
./build/fngr --db /tmp/fngr-s2-edit.db event 1
rm /tmp/fngr-s2-edit.db
```
Expected: `Added event 1` then `Updated event 1` three times, then a detail view with text "second @bob" and timestamp 2026-05-01 09:30:00 (in local zone).

- [ ] **Step 7: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 8: Commit.**

```bash
git add cmd/fngr/event.go cmd/fngr/event_test.go cmd/fngr/dispatch_test.go cmd/fngr/edit.go cmd/fngr/edit_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): event N text/time/date verbs (replaces fngr edit)

text replaces the body and triggers the body-tag sync inside Update.
time and date use timefmt.ParsePartial: time-only or date-only inputs
splice into the event's existing timestamp; full inputs replace
both. Wrong-shape inputs (date passed to time, time passed to date)
are rejected with a clear message. cmd/fngr/edit.go and its test
file are removed. Dispatch test exercises each verb.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: Add `attach`, `detach`, `tag`, `untag` verbs

**Files:**
- Modify: `cmd/fngr/event.go`
- Modify: `cmd/fngr/event_test.go`
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 1: Add the four verb structs.** In `cmd/fngr/event.go`, expand `EventCmd` to:

```go
type EventCmd struct {
	ID int64 `arg:"" help:"Event ID."`

	Show   EventShowCmd   `cmd:"" default:"withargs" help:"Show event detail (default)."`
	Text   EventTextCmd   `cmd:"" help:"Replace event text."`
	Time   EventTimeCmd   `cmd:"" help:"Replace clock time (or full timestamp)."`
	Date   EventDateCmd   `cmd:"" help:"Replace date (or full timestamp)."`
	Attach EventAttachCmd `cmd:"" help:"Set parent event."`
	Detach EventDetachCmd `cmd:"" help:"Clear parent."`
	Tag    EventTagCmd    `cmd:"" help:"Add tags (one or more @person, #tag, or key=value)."`
	Untag  EventUntagCmd  `cmd:"" help:"Remove tags (one or more @person, #tag, or key=value)."`
}
```

Append the four verb types and their Runs at the bottom of the file:

```go
// EventAttachCmd sets parent_id.
type EventAttachCmd struct {
	Parent int64 `arg:"" help:"Parent event ID."`
}

func (c *EventAttachCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()
	if err := s.Reparent(ctx, parent.ID, &c.Parent); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Attached event %d to event %d\n", parent.ID, c.Parent)
	return nil
}

// EventDetachCmd clears parent_id.
type EventDetachCmd struct{}

func (c *EventDetachCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()
	if err := s.Reparent(ctx, parent.ID, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Detached event %d\n", parent.ID)
	return nil
}

// EventTagCmd adds one or more tags.
type EventTagCmd struct {
	Args []string `arg:"" help:"Tags to add: @person, #tag, or key=value (one or more)."`
}

func (c *EventTagCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	if len(c.Args) == 0 {
		return fmt.Errorf("at least one tag required")
	}
	tags, err := parseTagArgs(c.Args)
	if err != nil {
		return err
	}
	if err := s.AddTags(ctx, parent.ID, tags); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Tagged event %d with %d tag(s)\n", parent.ID, len(tags))
	return nil
}

// EventUntagCmd removes one or more tags. Reports the count removed.
type EventUntagCmd struct {
	Args []string `arg:"" help:"Tags to remove: @person, #tag, or key=value (one or more)."`
}

func (c *EventUntagCmd) Run(parent *EventCmd, s eventStore, io ioStreams) error {
	ctx := context.Background()

	if len(c.Args) == 0 {
		return fmt.Errorf("at least one tag required")
	}
	tags, err := parseTagArgs(c.Args)
	if err != nil {
		return err
	}
	n, err := s.RemoveTags(ctx, parent.ID, tags)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("nothing to untag: %s", strings.Join(c.Args, " "))
	}
	fmt.Fprintf(io.Out, "Untagged event %d (%d removed)\n", parent.ID, n)
	return nil
}

// parseTagArgs validates every arg up front so a malformed last arg never
// triggers a partial DB write.
func parseTagArgs(args []string) ([]parse.Meta, error) {
	out := make([]parse.Meta, 0, len(args))
	for _, a := range args {
		m, err := parse.MetaArg(a)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
```

Add to the import block of `cmd/fngr/event.go`:

```go
	"strings"

	"github.com/monolithiclab/fngr/internal/parse"
```

(`fmt`, `time`, `event`/`render` already there.)

- [ ] **Step 2: Add tests.** Append to `cmd/fngr/event_test.go`:

```go
func TestEventCmd_AttachAndDetach(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), "a", nil, nil, nil)
	b, _ := s.Add(context.Background(), "b", nil, nil, nil)

	if err := (&EventAttachCmd{Parent: a}).Run(&EventCmd{ID: b}, s, io); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ev, _ := s.Get(context.Background(), b)
	if ev.ParentID == nil || *ev.ParentID != a {
		t.Fatalf("ParentID = %v, want %d", ev.ParentID, a)
	}

	if err := (&EventDetachCmd{}).Run(&EventCmd{ID: b}, s, io); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	ev, _ = s.Get(context.Background(), b)
	if ev.ParentID != nil {
		t.Errorf("ParentID = %d, want nil", *ev.ParentID)
	}
}

func TestEventCmd_AttachRejectsCycle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), "a", nil, nil, nil)
	b, _ := s.Add(context.Background(), "b", &a, nil, nil)

	err := (&EventAttachCmd{Parent: b}).Run(&EventCmd{ID: a}, s, io)
	if !errors.Is(err, event.ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestEventCmd_TagAddsAndDedups(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	cmd := &EventTagCmd{Args: []string{"#ops", "@alice", "env=prod"}}
	if err := cmd.Run(&EventCmd{ID: id}, s, io); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:      true,
		{Key: "people", Value: "alice"}: true,
		{Key: "env", Value: "prod"}:     true,
	}
	ev, _ := s.Get(context.Background(), id)
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestEventCmd_TagInvalidArgErrors(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTagCmd{Args: []string{"#ops", "bare-word", "env=prod"}}
	err := cmd.Run(&EventCmd{ID: id}, s, io)
	if err == nil {
		t.Fatal("expected error for bare-word arg")
	}
	// Confirm no partial write happened.
	n, _ := s.CountMeta(context.Background(), "tag", "ops")
	if n != 0 {
		t.Errorf("partial write: tag=ops count = %d, want 0", n)
	}
}

func TestEventCmd_UntagRemovesAndReportsCount(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}, nil)

	cmd := &EventUntagCmd{Args: []string{"#ops", "@alice"}}
	if err := cmd.Run(&EventCmd{ID: id}, s, io); err != nil {
		t.Fatalf("Untag: %v", err)
	}
	if !strings.Contains(out.String(), "Untagged event") {
		t.Errorf("output = %q, want Untagged event", out.String())
	}
	ev, _ := s.Get(context.Background(), id)
	if len(ev.Meta) != 0 {
		t.Errorf("Meta = %v, want empty", ev.Meta)
	}
}

func TestEventCmd_UntagNothingMatches(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventUntagCmd{Args: []string{"#ghost"}}
	err := cmd.Run(&EventCmd{ID: id}, s, io)
	if err == nil || !strings.Contains(err.Error(), "nothing to untag") {
		t.Errorf("err = %v, want 'nothing to untag'", err)
	}
}
```

- [ ] **Step 3: Update the dispatch test.** Append four rows to the `cases` slice in `cmd/fngr/dispatch_test.go`:

```go
		{name: "event-attach", argv: []string{"event", "1", "attach", "2"}, want: ""},
		{name: "event-detach", argv: []string{"event", "1", "detach"}, want: ""},
		{name: "event-tag", argv: []string{"event", "1", "tag", "#ops"}, want: ""},
		{name: "event-untag", argv: []string{"event", "1", "untag", "#ops"}, want: ""},
```

(Some of these may surface non-binding errors from Reparent / RemoveTags depending on test seed state — that's fine, the dispatch test only guards "couldn't find binding" type wiring failures.)

- [ ] **Step 4: Run tests.**

Run: `go test ./cmd/fngr/ -v`
Expected: PASS for all event tests, dispatch matrix green.

- [ ] **Step 5: Smoke test.**

Run:
```bash
make build
./build/fngr --db /tmp/fngr-s2-tag.db add "first" --author nicolas
./build/fngr --db /tmp/fngr-s2-tag.db add "second" --author nicolas
./build/fngr --db /tmp/fngr-s2-tag.db event 2 attach 1
./build/fngr --db /tmp/fngr-s2-tag.db event 2 tag '#ops' '@alice' 'env=prod'
./build/fngr --db /tmp/fngr-s2-tag.db event 2
./build/fngr --db /tmp/fngr-s2-tag.db event 2 untag '#ops'
./build/fngr --db /tmp/fngr-s2-tag.db event 2 detach
./build/fngr --db /tmp/fngr-s2-tag.db event 2
rm /tmp/fngr-s2-tag.db
```
Expected (in order): `Added event 1`, `Added event 2`, `Attached event 2 to event 1`, `Tagged event 2 with 3 tag(s)`, detail view showing parent=1 and three tags, `Untagged event 2 (1 removed)`, `Detached event 2`, detail view showing no parent and two tags (people=alice, env=prod).

- [ ] **Step 6: Full ci.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add cmd/fngr/event.go cmd/fngr/event_test.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): event N attach/detach/tag/untag verbs

attach calls Reparent (which rejects self-parent and ancestry
cycles); detach passes nil. tag and untag take n positional args,
each validated up-front via parse.MetaArg so a bad arg can't trigger
a partial DB write. untag returns "nothing to untag" when no rows
matched. Dispatch test exercises each verb.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 13: README + CLAUDE.md updates (uncommitted)

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

> **Important:** Never commit `README.md` or `CLAUDE.md`. Update them in the working tree only.

- [ ] **Step 1: Update the Quick start in README.** In `README.md`, find the `# Show a single event` and `# Edit an event` blocks. Replace them with one event block:

```
# Show an event (default verb; honors --tree and --format)
fngr event 1
fngr event 1 --tree
fngr event 1 --format json

# Edit text (body @person/#tag tags are synced — old ones removed, new ones added)
fngr event 1 text "fixed wording for @sarah #urgent"

# Edit clock time (date preserved) or full timestamp (replaces both)
fngr event 1 time "09:30"
fngr event 1 time "2026-04-15T09:30"

# Edit date (clock preserved) or full timestamp (replaces both)
fngr event 1 date "2026-05-01"

# Re-parent / detach
fngr event 2 attach 1
fngr event 2 detach

# Add or remove tags (n args; @person, #tag, or key=value)
fngr event 1 tag "@sarah" "#urgent" "env=prod"
fngr event 1 untag "#urgent"
```

- [ ] **Step 2: Verify the file is in the working tree only.**

Run: `git status`
Expected: `README.md` shows as modified-uncommitted.

- [ ] **Step 3: Update CLAUDE.md architecture entries.** In `CLAUDE.md`:

Find the `cmd/fngr/{add,list,show,edit,delete,meta}.go` bullet and replace with:

```
- `cmd/fngr/{add,list,event,delete,meta}.go` — Kong command structs with
  `Run(...) error` methods, one file per top-level command. `list` is
  marked `default:"withargs"` so bare `fngr` dispatches to it. `event`
  hosts a sub-command tree: bare `fngr event N` shows; `text`, `time`,
  `date`, `attach`, `detach`, `tag`, `untag` mutate. None of the event
  verbs prompt.
```

Find the `internal/event/event.go` bullet and update the function list:

```
- `internal/event/event.go` — Data access functions: `Add` (transactional
  event + meta + FTS), `Get`, `Update` (text and/or timestamp; on text
  change, body tags are synced — parse.BodyTags(oldText) deleted then
  parse.BodyTags(newText) inserted via ON CONFLICT DO NOTHING; FTS
  rebuilt), `Reparent` (set/clear parent_id; rejects self and ancestry
  cycles via `ErrCycle`), `AddTags` / `RemoveTags` (event-scoped meta
  CRUD with FTS resync), `Delete`, `HasChildren`, `List` / `ListSeq`
  (FTS5 filter + date range + Limit + Ascending), `GetSubtree` (recursive
  CTE), `ListMeta`, `CountMeta`, `UpdateMeta`, `DeleteMeta`. All
  functions accept `context.Context`. `ErrNotFound` and `ErrCycle`
  sentinels. `loadMetaBatch` chunks the IN clause to stay under SQLite's
  parameter limit. `rebuildEventFTS` is a private helper used by Update,
  AddTags, and RemoveTags.
```

Find the `internal/parse/parse.go` bullet and update:

```
- `internal/parse/parse.go` — `Meta` type, body-tag extraction
  (`@person` → people, `#tag` → tag), `KeyValue` helper for `key=value`
  strings (used by both CLI parsing and `FlagMeta`), `MetaArg` parser
  for individual CLI args (`@person`, `#tag`, or `key=value`; used by
  `event tag` / `event untag`), FTS content building.
```

Find the `internal/timefmt/timefmt.go` bullet and update:

```
- `internal/timefmt/timefmt.go` — Single source of truth for accepted
  time inputs. `Parse` returns just the parsed timestamp; `ParsePartial`
  also reports whether the input had a date and/or time component, so
  `event time` / `event date` can splice into an existing timestamp
  instead of replacing it. Canonical `DateFormat` / `DateTimeFormat`
  layouts used for storage and display.
```

Find the `internal/db/migrate.go` bullet and update:

```
- `internal/db/migrate.go` — Ordered list of migrations gated by
  `PRAGMA user_version`. Pre-migration databases are detected via the
  legacy v1 `events` table and bumped to `user_version = 1`. Migration
  2 deduplicates `event_meta` and adds a UNIQUE index on
  `(key, value, event_id)` so `INSERT ... ON CONFLICT DO NOTHING`
  works in `AddTags` and the body-tag sync inside `Update`.
```

- [ ] **Step 4: Final smoke-check.**

Run: `make ci -j8`
Expected: PASS, total coverage in line with the prior baseline (≥ 80%).

(No commit — `README.md` and `CLAUDE.md` stay uncommitted by project policy.)

---

## Self-review

Spec coverage:

- Migration 2 (UNIQUE + dedupe) → Task 1.
- `timefmt.ParsePartial` → Task 2.
- `parse.MetaArg` → Task 3.
- `rebuildEventFTS` helper → Task 4.
- `event.Reparent` + `ErrCycle` → Task 5.
- `event.AddTags` (DB dedup, FTS resync) → Task 6.
- `event.RemoveTags` (count return, FTS resync) → Task 7.
- `event.Update` body-tag sync (untag old → update text → tag new → FTS) → Task 8.
- `eventStore` interface gains the three new methods → Task 9.
- Bare `fngr event N` read path (replaces `show`) → Task 10.
- `text`/`time`/`date` verbs (replace `edit`) → Task 11.
- `attach`/`detach`/`tag`/`untag` verbs → Task 12.
- README + CLAUDE.md updates → Task 13.
- `Store.Reparent`, `Store.AddTags`, `Store.RemoveTags` direct tests → embedded in Tasks 5/6/7.
- Wrong-shape rejection for `event time` (date-only) and `event date` (time-only) → Task 11 covers both with explicit tests.
- Cycle prevention test for `attach` → Task 12 (`TestEventCmd_AttachRejectsCycle`).
- Migration 2's UNIQUE constraint exercised end-to-end → Task 1's regression test asserts both the dedupe and the UNIQUE/ON-CONFLICT behaviour.

Placeholder scan: every step has either complete code or an exact command. No `TODO`, `TBD`, "similar to", or vague handwaving.

Type / name consistency:
- Errors: `ErrNotFound`, `ErrCycle`.
- Functions: `event.Reparent`, `event.AddTags`, `event.RemoveTags`, `event.Update`, `event.rebuildEventFTS`, `event.deleteMetaTuples`, `event.insertMetaTuples`, `parse.MetaArg`, `timefmt.ParsePartial`.
- Methods: `Store.Reparent`, `Store.AddTags`, `Store.RemoveTags`.
- CLI types: `EventCmd`, `EventShowCmd`, `EventTextCmd`, `EventTimeCmd`, `EventDateCmd`, `EventAttachCmd`, `EventDetachCmd`, `EventTagCmd`, `EventUntagCmd`.
- Helper in cmd/fngr: `parseTagArgs`.

All cross-task references match.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-18-event-namespace.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
