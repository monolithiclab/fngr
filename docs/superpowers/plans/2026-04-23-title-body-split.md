# Title + Body Data-Model Split — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single `events.text` column with `title` + `body`, split on the literal `". "` (dot+space) at the CLI boundary, with structured outputs (JSON/CSV/MD/event detail) carrying both fields and list views showing only the title.

**Architecture:** Pure split helper in `internal/parse`; pure-SQL migration 3 with `INSTR`/`SUBSTR` two-pass + FTS rebuild; event package schema/struct/queries change in one coupled commit; CLI gains `event title` / `event body` alongside re-splitting `event text`; renderers updated per-format.

**Tech Stack:** Go 1.26, Kong CLI parsing, modernc.org/sqlite (pure-Go), SQLite FTS5.

---

## Conventions

- Every task ends with `make ci` running clean (codefix + format + lint + test).
- Run `make test` after each step that touches Go code (uses `-race -cover`).
- Commit messages follow the existing repo style (Conventional Commits prefix + Co-Authored-By trailer is automatic).
- Table-driven tests with `t.Parallel()` and `t.Run` subtests where applicable.
- Tests use `newTestStore(t)` (cmd/fngr) or `testDB(t)` / `testDBWithSchema(t)` (internal/db).

---

## Task 1: `parse.SplitTitleBody` helper

Pure addition; nothing else depends on it yet.

**Files:**
- Modify: `internal/parse/parse.go` — add `SplitTitleBody`
- Modify: `internal/parse/parse_test.go` — add `TestSplitTitleBody`

- [ ] **Step 1: Write the failing test**

Append to `internal/parse/parse_test.go`:

```go
func TestSplitTitleBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		title string
		body  string
	}{
		{"", "", ""},
		{"   ", "", ""},
		{"hello", "hello", ""},
		{"hello.", "hello.", ""},
		{"hello. world", "hello", "world"},
		{"  hello  .  world  ", "hello", "world"},
		{"v1.2 done. Hotfix", "v1.2 done", "Hotfix"},
		{"v1.2.3 released", "v1.2.3 released", ""},
		{"first. second. third", "first", "second. third"},
		{". hello", "", "hello"},
		{"hello. ", "hello", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			gotTitle, gotBody := SplitTitleBody(tt.input)
			if gotTitle != tt.title || gotBody != tt.body {
				t.Errorf("SplitTitleBody(%q) = (%q, %q), want (%q, %q)",
					tt.input, gotTitle, gotBody, tt.title, tt.body)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/parse/ -run TestSplitTitleBody -v`
Expected: FAIL with "undefined: SplitTitleBody".

- [ ] **Step 3: Implement `SplitTitleBody`**

Append to `internal/parse/parse.go`:

```go
// SplitTitleBody splits text on the first occurrence of ". " (dot
// followed by a space). The dot+space sequence is dropped; both sides
// are trimmed of surrounding whitespace. When no ". " appears the
// whole input becomes the title and the body is empty.
func SplitTitleBody(text string) (title, body string) {
	if i := strings.Index(text, ". "); i >= 0 {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+2:])
	}
	return strings.TrimSpace(text), ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/parse/ -run TestSplitTitleBody -v`
Expected: PASS for every case.

- [ ] **Step 5: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/parse/parse.go internal/parse/parse_test.go
git commit -m "feat(parse): SplitTitleBody helper for '. ' split rule"
```

---

## Task 2: `parse.FTSContent` signature change to `(title, body, meta)`

Refactor the FTS content builder to accept title + body separately. Bridge the two existing call sites in the event package by passing `(text, "", meta)` so behavior is preserved until Task 4 wires real title+body.

**Files:**
- Modify: `internal/parse/parse.go` — change `FTSContent` signature
- Modify: `internal/parse/parse_test.go` — update / extend FTSContent tests
- Modify: `internal/event/event.go` — bridge two call sites

- [ ] **Step 1: Update parse_test.go for new FTSContent signature**

Find the existing `TestFTSContent` (or any test calling `FTSContent`) and replace with:

```go
func TestFTSContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		title string
		body  string
		meta  []Meta
		want  string
	}{
		{"empty", "", "", nil, ""},
		{"title only", "hello", "", nil, "hello"},
		{"body only", "", "world", nil, "world"},
		{"title + body", "hello", "world", nil, "hello world"},
		{
			"title + body + meta",
			"hello", "world",
			[]Meta{{Key: "tag", Value: "ops"}, {Key: "people", Value: "sarah"}},
			"hello world tag=ops people=sarah",
		},
		{
			"empty title with meta",
			"", "world",
			[]Meta{{Key: "tag", Value: "ops"}},
			"world tag=ops",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FTSContent(tt.title, tt.body, tt.meta); got != tt.want {
				t.Errorf("FTSContent(%q, %q, %v) = %q, want %q",
					tt.title, tt.body, tt.meta, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails (compile error)**

Run: `go test ./internal/parse/ -run TestFTSContent -v`
Expected: FAIL with "too many arguments in call to FTSContent".

- [ ] **Step 3: Update `parse.FTSContent` signature and body**

Replace the existing `FTSContent` function in `internal/parse/parse.go` with:

```go
// FTSContent builds the searchable string indexed in events_fts. Empty
// title or body contribute nothing (no leading/trailing or doubled
// spaces). Meta entries render as "key=value" tokens — the FTS
// tokenizer treats '=' as a token char, so a -S '#ops' filter matches
// the literal "tag=ops" emitted here.
//
// The SQL rebuild query in internal/db/migrations/3.sql must match
// this formula. If you change one, change the other.
func FTSContent(title, body string, meta []Meta) string {
	parts := make([]string, 0, 2+len(meta))
	if title != "" {
		parts = append(parts, title)
	}
	if body != "" {
		parts = append(parts, body)
	}
	for _, m := range meta {
		parts = append(parts, m.Key+"="+m.Value)
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Bridge `internal/event/event.go` callers**

In `addInTx`, change the FTS insert (currently around line 159):

```go
		if _, err := insertFTS.ExecContext(ctx, id, parse.FTSContent(in.Text, "", in.Meta)); err != nil {
			return nil, fmt.Errorf("insert FTS content: %w", err)
		}
```

In `rebuildEventFTS` (currently around line 518), change the UPDATE:

```go
	if _, err := tx.ExecContext(ctx,
		"UPDATE events_fts SET content = ? WHERE rowid = ?",
		parse.FTSContent(text, "", meta), id,
	); err != nil {
		return fmt.Errorf("update FTS: %w", err)
	}
```

(Both changes pass `text` as the title and `""` as the body — Task 4 swaps to real `title`/`body`.)

- [ ] **Step 5: Run tests**

Run: `make test`
Expected: PASS — FTS content stays semantically the same (`"text key=value"`).

- [ ] **Step 6: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/parse/parse.go internal/parse/parse_test.go internal/event/event.go
git commit -m "refactor(parse): FTSContent takes title+body+meta"
```

---

## Task 3: Migration 3 — split text into title + body, rebuild FTS

Pure-SQL migration; no Go code changes besides the test.

**Files:**
- Create: `internal/db/migrations/3.sql`
- Modify: `internal/db/db_test.go` — add `TestMigrate_V3SplitsTitleBody`

- [ ] **Step 1: Write the failing test**

Append to `internal/db/db_test.go`:

```go
func TestMigrate_V3SplitsTitleBody(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	// Bring schema up to v2 (run migrations 1 and 2).
	if _, err := db.Exec(migrationSQL(t, 0)); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if _, err := db.Exec(migrationSQL(t, 1)); err != nil {
		t.Fatalf("apply migration 2: %v", err)
	}
	if err := setUserVersion(db, 2); err != nil {
		t.Fatalf("set v2: %v", err)
	}

	rows := []struct {
		text      string
		wantTitle string
		wantBody  string
	}{
		{"hello", "hello", ""},
		{"v1.2 done. Hotfix", "v1.2 done", "Hotfix"},
		{"no separator here", "no separator here", ""},
		{". body only", "", "body only"},
		{"v1.2.3 released", "v1.2.3 released", ""},
	}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO events (text) VALUES (?)", r.text); err != nil {
			t.Fatalf("insert %q: %v", r.text, err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (2, 'tag', 'ops')",
	); err != nil {
		t.Fatalf("insert meta: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO events_fts (rowid, content) VALUES (2, 'v1.2 done. Hotfix tag=ops')",
	); err != nil {
		t.Fatalf("insert fts: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	v, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if v != 3 {
		t.Errorf("user_version = %d, want 3", v)
	}

	for i, r := range rows {
		var title, body string
		err := db.QueryRow("SELECT title, body FROM events WHERE id = ?", i+1).Scan(&title, &body)
		if err != nil {
			t.Fatalf("select id %d: %v", i+1, err)
		}
		if title != r.wantTitle || body != r.wantBody {
			t.Errorf("id %d: got (title=%q, body=%q), want (%q, %q)",
				i+1, title, body, r.wantTitle, r.wantBody)
		}
	}

	var ftsContent string
	if err := db.QueryRow("SELECT content FROM events_fts WHERE rowid = 2").Scan(&ftsContent); err != nil {
		t.Fatalf("select fts row 2: %v", err)
	}
	want := "v1.2 done Hotfix tag=ops"
	if ftsContent != want {
		t.Errorf("FTS content for row 2 = %q, want %q", ftsContent, want)
	}

	var emptyFTS string
	if err := db.QueryRow("SELECT content FROM events_fts WHERE rowid = 1").Scan(&emptyFTS); err != nil {
		t.Fatalf("select fts row 1: %v", err)
	}
	if emptyFTS != "hello" {
		t.Errorf("FTS content for row 1 = %q, want %q", emptyFTS, "hello")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestMigrate_V3SplitsTitleBody -v`
Expected: FAIL — migration 3 file doesn't exist (the test will fail at `db.QueryRow("SELECT title, body...")` because `title` column doesn't exist).

- [ ] **Step 3: Create migration 3**

Create `internal/db/migrations/3.sql`:

```sql
ALTER TABLE events RENAME COLUMN text TO title;
ALTER TABLE events ADD COLUMN body TEXT NOT NULL DEFAULT '';

-- Two-pass split on first '. ' separator. Order matters: populate
-- body first, then truncate title — pass 2 destroys the INSTR
-- landmark.
UPDATE events
   SET body = SUBSTR(title, INSTR(title, '. ') + 2)
 WHERE INSTR(title, '. ') > 0;

UPDATE events
   SET title = SUBSTR(title, 1, INSTR(title, '. ') - 1)
 WHERE INSTR(title, '. ') > 0;

-- Rebuild events_fts content from new columns + meta key=value tokens.
-- Formula must match internal/parse/parse.go::FTSContent.
DELETE FROM events_fts;
INSERT INTO events_fts(rowid, content)
SELECT e.id,
       TRIM(
         CASE WHEN e.title <> '' THEN e.title || ' ' ELSE '' END ||
         CASE WHEN e.body  <> '' THEN e.body  || ' ' ELSE '' END ||
         COALESCE(GROUP_CONCAT(em.key || '=' || em.value, ' '), '')
       )
  FROM events e
  LEFT JOIN event_meta em ON em.event_id = e.id
 GROUP BY e.id;
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestMigrate_V3SplitsTitleBody -v`
Expected: PASS — migrations 1+2+3 applied, columns split correctly, FTS rebuilt with `key=value` tokens.

- [ ] **Step 5: Run full lint + test**

Run: `make ci`

Note: Existing event package tests will likely fail because they SELECT/INSERT `text` from the events table (which no longer exists post-migration 3). This is expected — Task 4 fixes them. Verify only the db package passes:

Run: `go test ./internal/db/ -v`
Expected: PASS.
Run: `go test ./internal/parse/ -v`
Expected: PASS.

If the larger `make ci` fails only inside `internal/event` and `cmd/fngr` packages with errors mentioning the `text` column, proceed; Task 4 unblocks them. If any other package fails, stop and investigate.

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/3.sql internal/db/db_test.go
git commit -m "feat(db): migration 3 splits text into title+body"
```

---

## Task 4: Event package data layer migration

The big coupled refactor. Renames `events.text` → `title` everywhere in the event package, adds `body`, switches `FTSContent` calls to use real title+body, changes `Add` to take an `AddInput` value, changes `Update` to take `title` and `body` pointers. Also patches `cmd/fngr/store.go` interface, `cmd/fngr/add.go`, `cmd/fngr/add_json.go`, `cmd/fngr/event.go` to call sites that compile (placeholder behavior; full integration in later tasks).

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`
- Modify: `cmd/fngr/store.go`
- Modify: `cmd/fngr/add.go`
- Modify: `cmd/fngr/add_json.go`
- Modify: `cmd/fngr/event.go`
- Modify: `cmd/fngr/add_test.go`, `cmd/fngr/add_json_test.go`, `cmd/fngr/event_test.go`, `cmd/fngr/dispatch_test.go`, `cmd/fngr/list_test.go`, `cmd/fngr/delete_test.go`, `cmd/fngr/meta_test.go`, `cmd/fngr/help_test.go`, `cmd/fngr/pager_test.go`, `cmd/fngr/body_test.go` — only fields named `Text` on `event.Event` / `event.AddInput` need swapping; renderer-coupled tests stay until Task 8/9.

### Step plan

This task has many small steps. Each step must keep the file syntactically valid, but the project as a whole only re-compiles after Step 14. Run `go build ./...` after Step 14, not before.

- [ ] **Step 1: Update `event.Event` struct in `internal/event/event.go`**

Find:

```go
type Event struct {
	ID        int64
	ParentID  *int64
	Text      string
	CreatedAt time.Time
	Meta      []parse.Meta
}
```

Replace with:

```go
type Event struct {
	ID        int64
	ParentID  *int64
	Title     string
	Body      string
	CreatedAt time.Time
	Meta      []parse.Meta
}
```

- [ ] **Step 2: Update `event.AddInput` struct**

Find:

```go
type AddInput struct {
	Text      string
	ParentID  *int64
	Meta      []parse.Meta
	CreatedAt *time.Time
}
```

Replace with:

```go
type AddInput struct {
	Title     string
	Body      string
	ParentID  *int64
	Meta      []parse.Meta
	CreatedAt *time.Time
}
```

- [ ] **Step 3: Replace `event.Add` with the AddInput-taking signature**

Find:

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
```

Replace with:

```go
// Add inserts a single event with its meta tuples and FTS row inside one
// transaction, returning the new event ID. A nil ParentID creates a root
// event; a non-nil ParentID must reference an existing event or
// ErrNotFound is returned. A nil CreatedAt defaults to the SQL
// CURRENT_TIMESTAMP. Title is required; Body may be empty.
func Add(ctx context.Context, db *sql.DB, in AddInput) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := addInTx(ctx, tx, []AddInput{in})
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return ids[0], nil
}
```

- [ ] **Step 4: Update `addInTx` INSERT statements and FTS call**

In `addInTx`, find the `if in.CreatedAt != nil { ... } else { ... }` block and the FTS insert below it. Replace the entire per-record body of the loop with this code (preserving the surrounding loop and the parent-existence check above it):

```go
		if in.Title == "" {
			return nil, fmt.Errorf("title cannot be empty")
		}

		var res sql.Result
		if in.CreatedAt != nil {
			res, err = tx.ExecContext(ctx,
				"INSERT INTO events (parent_id, title, body, created_at) VALUES (?, ?, ?, ?)",
				in.ParentID, in.Title, in.Body, in.CreatedAt.UTC().Format(timefmt.DateTimeFormat),
			)
		} else {
			res, err = tx.ExecContext(ctx,
				"INSERT INTO events (parent_id, title, body) VALUES (?, ?, ?)",
				in.ParentID, in.Title, in.Body,
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

		if _, err := insertFTS.ExecContext(ctx, id, parse.FTSContent(in.Title, in.Body, in.Meta)); err != nil {
			return nil, fmt.Errorf("insert FTS content: %w", err)
		}

		ids = append(ids, id)
```

- [ ] **Step 5: Update `Get`, `scanEvents`, `ListSeq`, `GetSubtree`, `buildListQuery` to use title + body**

In `internal/event/event.go`:

In `Get`, change:

```go
	rows, err := db.QueryContext(ctx,
		"SELECT id, parent_id, title, body, created_at FROM events WHERE id = ?", id,
	)
```

In `scanEvents`, change:

```go
		if err := rows.Scan(&e.ID, &parentID, &e.Title, &e.Body, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
```

In `ListSeq`, change the row-scan:

```go
			if err := rows.Scan(&e.ID, &parentID, &e.Title, &e.Body, &e.CreatedAt); err != nil {
				yield(Event{}, fmt.Errorf("scan event: %w", err))
				return
			}
```

In `buildListQuery`, replace every `e.text` with `e.title, e.body`. The three SELECT clauses become:

```go
		query = `SELECT e.id, e.parent_id, e.title, e.body, e.created_at
			FROM events e
			WHERE e.id NOT IN (
				SELECT rowid FROM events_fts WHERE events_fts MATCH ?
			)`
```

```go
		query = `SELECT e.id, e.parent_id, e.title, e.body, e.created_at
			FROM events e
			JOIN events_fts f ON f.rowid = e.id
			WHERE events_fts MATCH ?`
```

```go
		query = `SELECT e.id, e.parent_id, e.title, e.body, e.created_at
			FROM events e
			WHERE 1=1`
```

In `GetSubtree`, change the recursive CTE:

```go
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, parent_id, title, body, created_at FROM events WHERE id = ?
			UNION ALL
			SELECT e.id, e.parent_id, e.title, e.body, e.created_at
			FROM events e JOIN subtree s ON e.parent_id = s.id
		)
		SELECT id, parent_id, title, body, created_at FROM subtree ORDER BY created_at ASC
	`, rootID)
```

- [ ] **Step 6: Replace `Update` with the title+body+createdAt signature**

In `internal/event/event.go`, replace the entire `Update` function (currently `func Update(ctx context.Context, db *sql.DB, id int64, text *string, createdAt *time.Time) error`) with:

```go
// Update mutates an existing event's title, body, and/or createdAt
// timestamp. Any field with a nil pointer is left untouched. When
// either title or body changes, body-derived tags (`@person`,
// `#tag`) are synced — the old set (extracted from title+body
// joined) is removed, the new set is inserted via ON CONFLICT DO
// NOTHING — and the FTS row is rebuilt. Empty title is rejected;
// empty body clears it. Returns ErrNotFound when no such event
// exists.
func Update(ctx context.Context, db *sql.DB, id int64, title, body *string, createdAt *time.Time) error {
	if title == nil && body == nil && createdAt == nil {
		return nil
	}
	if title != nil && *title == "" {
		return fmt.Errorf("title cannot be empty")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireEventExists(ctx, tx, id); err != nil {
		return err
	}

	textChanged := title != nil || body != nil

	if textChanged {
		var oldTitle, oldBody string
		if err := tx.QueryRowContext(ctx,
			"SELECT title, body FROM events WHERE id = ?", id,
		).Scan(&oldTitle, &oldBody); err != nil {
			return fmt.Errorf("query event title/body: %w", err)
		}
		oldBodyTags := parse.BodyTags(oldTitle + " " + oldBody)
		if err := deleteMetaTuples(ctx, tx, id, oldBodyTags); err != nil {
			return err
		}
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *title)
	}
	if body != nil {
		sets = append(sets, "body = ?")
		args = append(args, *body)
	}
	if createdAt != nil {
		sets = append(sets, "created_at = ?")
		args = append(args, createdAt.UTC().Format(timefmt.DateTimeFormat))
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, "UPDATE events SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil { // #nosec G202 -- sets is built from a fixed allow-list
		return fmt.Errorf("update event: %w", err)
	}

	if textChanged {
		var newTitle, newBody string
		if err := tx.QueryRowContext(ctx,
			"SELECT title, body FROM events WHERE id = ?", id,
		).Scan(&newTitle, &newBody); err != nil {
			return fmt.Errorf("query event title/body after update: %w", err)
		}
		newBodyTags := parse.BodyTags(newTitle + " " + newBody)
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
```

- [ ] **Step 7: Update `rebuildEventFTS` to read title + body**

Replace the entire `rebuildEventFTS` function with:

```go
// rebuildEventFTS reads the event's current title + body + meta inside
// tx and writes parse.FTSContent into events_fts.
func rebuildEventFTS(ctx context.Context, tx *sql.Tx, id int64) error {
	var title, body string
	if err := tx.QueryRowContext(ctx,
		"SELECT title, body FROM events WHERE id = ?", id,
	).Scan(&title, &body); err != nil {
		return fmt.Errorf("read event title/body for FTS: %w", err)
	}
	meta, err := readMetaTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE events_fts SET content = ? WHERE rowid = ?",
		parse.FTSContent(title, body, meta), id,
	); err != nil {
		return fmt.Errorf("update FTS: %w", err)
	}
	return nil
}
```

- [ ] **Step 8: Update `internal/event/store.go`**

Replace the `Add` and `Update` methods on `*Store`:

```go
func (s *Store) Add(ctx context.Context, in AddInput) (int64, error) {
	return Add(ctx, s.DB, in)
}
```

```go
func (s *Store) Update(ctx context.Context, id int64, title, body *string, createdAt *time.Time) error {
	return Update(ctx, s.DB, id, title, body, createdAt)
}
```

(Other methods on `*Store` are unchanged.)

The unused imports (`parse`, `time`) might shift; if `goimports` complains, drop unused ones. After this step, `parse` is no longer imported by store.go (Add no longer takes `[]parse.Meta` directly; the AddInput value carries it). Drop it:

```go
import (
	"context"
	"database/sql"
	"iter"
	"time"
)
```

- [ ] **Step 9: Update `cmd/fngr/store.go` `eventStore` interface**

Replace the `Add` and `Update` lines in the interface:

```go
	Add(ctx context.Context, in event.AddInput) (int64, error)
```

```go
	Update(ctx context.Context, id int64, title, body *string, createdAt *time.Time) error
```

The `parse` import is no longer needed for the `Add` line. If still needed elsewhere keep it; if `goimports` removes it that's fine.

- [ ] **Step 10: Patch `cmd/fngr/add.go::runText` to construct AddInput**

Replace the body of `runText` with:

```go
func (c *AddCmd) runText(s eventStore, io ioStreams, text string) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
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

	id, err := s.Add(context.Background(), event.AddInput{
		Title:     text,
		Body:      "",
		ParentID:  c.Parent,
		Meta:      meta,
		CreatedAt: createdAt,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
```

(Title=text, Body="" is a placeholder — Task 5 wires the real split.)

- [ ] **Step 11: Patch `cmd/fngr/add_json.go::jsonInputToAddInput`**

The function returns `event.AddInput`. Find the final `return event.AddInput{...}` near the end of the function and replace with:

```go
	return event.AddInput{
		Title:     text,
		Body:      "",
		ParentID:  parent,
		Meta:      merged,
		CreatedAt: createdAt,
	}, nil
```

(Same Title=text placeholder — Task 6 wires the title/body wire shape.)

- [ ] **Step 12: Patch `cmd/fngr/event.go::EventTextCmd.Run`**

Replace the `EventTextCmd.Run` function with:

```go
func (c *EventTextCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Body == "" {
		return fmt.Errorf("event text cannot be empty")
	}
	title := c.Body
	body := ""
	if err := s.Update(ctx, c.ID, &title, &body, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}
```

(Placeholder — Task 7 introduces real splitting + the `title`/`body` verbs.)

In `EventTimeCmd.Run`, find the call `s.Update(ctx, c.ID, nil, &when)` and change to:

```go
	if err := s.Update(ctx, c.ID, nil, nil, &when); err != nil {
```

In `EventDateCmd.Run`, do the same:

```go
	if err := s.Update(ctx, c.ID, nil, nil, &when); err != nil {
```

- [ ] **Step 13: Update event package tests for new struct/signatures**

Edit `internal/event/event_test.go`. Two mechanical replacements throughout the file:

1. Every reference to `event.Event{...Text: "X"...}` or `ev.Text` → `Title: "X"` / `ev.Title`.
2. Every call like `Add(ctx, db, "text", parentPtr, meta, ts)` → `Add(ctx, db, AddInput{Title: "text", ParentID: parentPtr, Meta: meta, CreatedAt: ts})`.
3. Every call like `Update(ctx, db, id, &newText, nil)` → `Update(ctx, db, id, &newText, nil, nil)`.
4. Every call like `Update(ctx, db, id, nil, &when)` → `Update(ctx, db, id, nil, nil, &when)`.

Add at least one new test asserting the empty-title rejection. Append to `internal/event/event_test.go`:

```go
func TestAdd_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	if _, err := Add(context.Background(), db, AddInput{Title: ""}); err == nil {
		t.Error("Add with empty title: expected error")
	}
}

func TestUpdate_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	id, err := Add(context.Background(), db, AddInput{Title: "starter"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	empty := ""
	if err := Update(context.Background(), db, id, &empty, nil, nil); err == nil {
		t.Error("Update with empty title: expected error")
	}
}

func TestUpdate_BodyOnly(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	id, err := Add(context.Background(), db, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newBody := "newbody"
	if err := Update(context.Background(), db, id, nil, &newBody, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "title" || ev.Body != "newbody" {
		t.Errorf("got (title=%q, body=%q), want (title, newbody)", ev.Title, ev.Body)
	}
}

func TestUpdate_TitleOnly(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	id, err := Add(context.Background(), db, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newTitle := "newtitle"
	if err := Update(context.Background(), db, id, &newTitle, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "newtitle" || ev.Body != "body" {
		t.Errorf("got (title=%q, body=%q), want (newtitle, body)", ev.Title, ev.Body)
	}
}

func TestUpdate_ClearsBody(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	id, err := Add(context.Background(), db, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	empty := ""
	if err := Update(context.Background(), db, id, nil, &empty, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "title" || ev.Body != "" {
		t.Errorf("got (title=%q, body=%q), want (title, '')", ev.Title, ev.Body)
	}
}

func TestUpdate_BodyTagSyncAcrossFields(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)
	id, err := Add(context.Background(), db, AddInput{
		Title: "deploy #ops",
		Body:  "@sarah",
		Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
			{Key: "people", Value: "sarah"},
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Move tags from title→body and body→title; expected meta set is unchanged.
	newTitle := "@sarah deploy"
	newBody := "#ops"
	if err := Update(context.Background(), db, id, &newTitle, &newBody, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), db, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantSet := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:      true,
		{Key: "people", Value: "sarah"}: true,
	}
	for _, m := range ev.Meta {
		if wantSet[m] {
			delete(wantSet, m)
		}
	}
	if len(wantSet) != 0 {
		t.Errorf("missing meta after move: %v", wantSet)
	}
}
```

- [ ] **Step 14: Update event package store_test for new Add/Update**

Edit `internal/event/store_test.go`. Replace any `s.Add(ctx, "text", ...)` calls with `s.Add(ctx, AddInput{Title: "text", ...})`. Replace any `s.Update(ctx, id, &text, ts)` calls with `s.Update(ctx, id, &text, nil, ts)`.

- [ ] **Step 15: Update cmd/fngr tests for new Add/Update fields**

Across `cmd/fngr/*_test.go`, the same mechanical sweep:

- `s.Add(ctx, "x", parentPtr, meta, ts)` → `s.Add(ctx, event.AddInput{Title: "x", ParentID: parentPtr, Meta: meta, CreatedAt: ts})`
- Any direct construction of `event.AddInput{Text: "x", ...}` → `event.AddInput{Title: "x", ...}`
- Any `ev.Text` access → `ev.Title` (these are usually in renderer-output assertions; placeholder Title=full text is the right read until renderer tasks)

Use `grep -rn "\.Text\b" cmd/fngr/ internal/event/` to find every site. Update one file at a time, run `go build ./cmd/fngr/...` between files to localize errors.

- [ ] **Step 16: Run the build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 17: Run all tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 18: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 19: Commit**

```bash
git add internal/event cmd/fngr
git commit -m "feat(event): title+body schema in data layer"
```

---

## Task 5: `fngr add` calls `parse.SplitTitleBody`

Wire the real split into the text-mode add path. JSON path stays placeholder until Task 6.

**Files:**
- Modify: `cmd/fngr/add.go`
- Modify: `cmd/fngr/add_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/fngr/add_test.go`:

```go
func TestAddCmd_SplitsTitleBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{
		Args:   []string{"deployed v1.2 to staging. needed a manual restart"},
		Author: "alice",
	}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Title != "deployed v1.2 to staging" || ev.Body != "needed a manual restart" {
		t.Errorf("got (title=%q, body=%q), want (deployed v1.2 to staging, needed a manual restart)",
			ev.Title, ev.Body)
	}
}

func TestAddCmd_NoSeparator(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"v1.2.3 released"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if events[0].Title != "v1.2.3 released" || events[0].Body != "" {
		t.Errorf("got (title=%q, body=%q), want (v1.2.3 released, '')", events[0].Title, events[0].Body)
	}
}

func TestAddCmd_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{". body only"}, Author: "alice"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/fngr/ -run "TestAddCmd_SplitsTitleBody|TestAddCmd_NoSeparator|TestAddCmd_RejectsEmptyTitle" -v`
Expected: FAIL on the split test (currently Title gets the full input, Body is "").

- [ ] **Step 3: Update `runText` to call `parse.SplitTitleBody`**

In `cmd/fngr/add.go`, add `"github.com/monolithiclab/fngr/internal/parse"` to the imports, then replace the body of `runText` with:

```go
func (c *AddCmd) runText(s eventStore, io ioStreams, text string) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
	title, body := parse.SplitTitleBody(text)
	if title == "" {
		return fmt.Errorf("event title cannot be empty")
	}
	meta, err := event.CollectMeta(title+" "+body, c.Meta, c.Author)
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

	id, err := s.Add(context.Background(), event.AddInput{
		Title:     title,
		Body:      body,
		ParentID:  c.Parent,
		Meta:      meta,
		CreatedAt: createdAt,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/fngr/ -run "TestAddCmd_SplitsTitleBody|TestAddCmd_NoSeparator|TestAddCmd_RejectsEmptyTitle" -v`
Expected: PASS.

- [ ] **Step 5: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/fngr/add.go cmd/fngr/add_test.go
git commit -m "feat(cmd/add): split body via parse.SplitTitleBody"
```

---

## Task 6: JSON wire shape — `text` → `title` + `body`

Replace the `text` field on `jsonAddInput` with `title` (required) + `body` (optional, default `""`). No backward compat shim. Output side is handled in Task 8 (renderer).

**Files:**
- Modify: `cmd/fngr/add_json.go`
- Modify: `cmd/fngr/add_json_test.go`

- [ ] **Step 1: Update / add failing tests**

Edit `cmd/fngr/add_json_test.go`. Update existing tests that use `"text":"..."` in their JSON literals to use `"title":"..."` + optionally `"body":"..."`. Then append:

```go
func TestAddJSON_TitleBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO(`{"title":"deploy","body":"hotfix #ops"}`)

	cmd := &AddCmd{Args: []string{`{"title":"deploy","body":"hotfix #ops"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if events[0].Title != "deploy" || events[0].Body != "hotfix #ops" {
		t.Errorf("got (title=%q, body=%q), want (deploy, hotfix #ops)", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_BodyOptional(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{`{"title":"hello"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, _ := s.List(context.Background(), event.ListOpts{})
	if events[0].Title != "hello" || events[0].Body != "" {
		t.Errorf("got (title=%q, body=%q), want (hello, '')", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"title":""}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}

func TestAddJSON_RejectsTextField(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"text":"old"}`}, Author: "alice", Format: "json"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown-field error on `text`, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/fngr/ -run "TestAddJSON_TitleBody|TestAddJSON_BodyOptional|TestAddJSON_RejectsEmptyTitle|TestAddJSON_RejectsTextField" -v`
Expected: FAIL — `jsonAddInput` still has `Text` field; `title`/`body` are unknown.

- [ ] **Step 3: Update `jsonAddInput` and related helpers**

In `cmd/fngr/add_json.go`, replace:

```go
type jsonAddInput struct {
	Text      string      `json:"text"`
	ParentID  *int64      `json:"parent_id"`
	CreatedAt *string     `json:"created_at"`
	Meta      [][2]string `json:"meta"`
}
```

with:

```go
type jsonAddInput struct {
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	ParentID  *int64      `json:"parent_id"`
	CreatedAt *string     `json:"created_at"`
	Meta      [][2]string `json:"meta"`
}
```

Replace the body of `jsonInputToAddInput` with:

```go
func jsonInputToAddInput(in jsonAddInput, defaults cliDefaults, defaultAuthor string, index int) (event.AddInput, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: title is required", index)
	}
	body := strings.TrimSpace(in.Body)

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

	merged := mergeMetaForJSON(title+" "+body, explicit, defaultAuthor)

	hasAuthor := false
	for _, m := range merged {
		if m.Key == event.MetaKeyAuthor {
			hasAuthor = true
			break
		}
	}
	if !hasAuthor {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: author is required (set meta.author, --author, FNGR_AUTHOR, or $USER)", index)
	}

	return event.AddInput{
		Title:     title,
		Body:      body,
		ParentID:  parent,
		Meta:      merged,
		CreatedAt: createdAt,
	}, nil
}
```

(Note: `mergeMetaForJSON` already takes a single text string for body-tag extraction; we pass `title+" "+body` so tags from either are picked up.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/fngr/ -run "TestAddJSON" -v`
Expected: PASS.

- [ ] **Step 5: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/fngr/add_json.go cmd/fngr/add_json_test.go
git commit -m "feat(cmd/add): JSON wire shape with title+body"
```

---

## Task 7: Three event verbs — `event title`, `event body`, `event text`

`text` re-splits via SplitTitleBody and updates both fields. `title` and `body` are pass-through field setters.

**Files:**
- Modify: `cmd/fngr/event.go`
- Modify: `cmd/fngr/event_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/fngr/event_test.go`:

```go
func TestEventCmd_TitleVerb(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, err := s.Add(context.Background(), event.AddInput{
		Title: "old", Body: "body stays",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &EventTitleCmd{ID: id, Title: "new title"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "new title" || ev.Body != "body stays" {
		t.Errorf("got (title=%q, body=%q), want (new title, body stays)", ev.Title, ev.Body)
	}
}

func TestEventCmd_TitleRejectsEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "x",
		Meta:  []parse.Meta{{Key: "author", Value: "alice"}},
	})
	cmd := &EventTitleCmd{ID: id, Title: ""}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}

func TestEventCmd_BodyVerb(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "title stays", Body: "old",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventBodyCmd{ID: id, Body: "new body"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "title stays" || ev.Body != "new body" {
		t.Errorf("got (title=%q, body=%q), want (title stays, new body)", ev.Title, ev.Body)
	}
}

func TestEventCmd_BodyAcceptsEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "stays", Body: "to clear",
		Meta:  []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventBodyCmd{ID: id, Body: ""}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "stays" || ev.Body != "" {
		t.Errorf("got (title=%q, body=%q), want (stays, '')", ev.Title, ev.Body)
	}
}

func TestEventCmd_TextVerbResplits(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "old", Body: "old body",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventTextCmd{ID: id, Text: "fresh title. fresh body"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "fresh title" || ev.Body != "fresh body" {
		t.Errorf("got (title=%q, body=%q), want (fresh title, fresh body)", ev.Title, ev.Body)
	}
}

func TestEventCmd_TextVerbRejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "x",
		Meta:  []parse.Meta{{Key: "author", Value: "alice"}},
	})
	cmd := &EventTextCmd{ID: id, Text: ". body only"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}
```

Also: locate the existing test `TestEventCmd_TextRequiresNonEmpty` and `TestEventCmd_TextSyncs` in `event_test.go`. Update them: `EventTextCmd{ID: id, Body: "..."}` becomes `EventTextCmd{ID: id, Text: "..."}` (the field name changes in Step 3).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/fngr/ -run "TestEventCmd_TitleVerb|TestEventCmd_BodyVerb|TestEventCmd_TextVerbResplits" -v`
Expected: FAIL — `EventTitleCmd` / `EventBodyCmd` undefined.

- [ ] **Step 3: Add `EventTitleCmd`, `EventBodyCmd`; rename `EventTextCmd.Body` → `Text`; rewrite `EventTextCmd.Run` to re-split**

In `cmd/fngr/event.go`, add `"github.com/monolithiclab/fngr/internal/parse"` to imports if not already present.

Update the parent `EventCmd` struct to add the two new verbs:

```go
type EventCmd struct {
	Show   EventShowCmd   `cmd:"" default:"withargs" help:"Show event detail (default)."`
	Text   EventTextCmd   `cmd:"" help:"Replace event text — re-splits on '. ' into title+body."`
	Title  EventTitleCmd  `cmd:"" help:"Replace event title (body untouched)."`
	Body   EventBodyCmd   `cmd:"" help:"Replace event body (title untouched; empty arg clears)."`
	Time   EventTimeCmd   `cmd:"" help:"Replace clock time (or full timestamp)."`
	Date   EventDateCmd   `cmd:"" help:"Replace date (or full timestamp)."`
	Attach EventAttachCmd `cmd:"" help:"Set parent event."`
	Detach EventDetachCmd `cmd:"" help:"Clear parent."`
	Tag    EventTagCmd    `cmd:"" help:"Add tags (one or more @person, #tag, or key=value)."`
	Untag  EventUntagCmd  `cmd:"" help:"Remove tags (one or more @person, #tag, or key=value)."`
}
```

Replace the existing `EventTextCmd` struct + `Run` with:

```go
// EventTextCmd replaces the event's text by re-splitting it into title +
// body via parse.SplitTitleBody. Body tags are synced.
type EventTextCmd struct {
	ID   int64  `arg:"" help:"Event ID."`
	Text string `arg:"" help:"New event text. Re-split on '. ' into title+body."`
}

func (c *EventTextCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	title, body := parse.SplitTitleBody(c.Text)
	if title == "" {
		return fmt.Errorf("event title cannot be empty")
	}
	if err := s.Update(ctx, c.ID, &title, &body, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}

// EventTitleCmd replaces only the title.
type EventTitleCmd struct {
	ID    int64  `arg:"" help:"Event ID."`
	Title string `arg:"" help:"New event title."`
}

func (c *EventTitleCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if c.Title == "" {
		return fmt.Errorf("event title cannot be empty")
	}
	if err := s.Update(ctx, c.ID, &c.Title, nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}

// EventBodyCmd replaces only the body. Empty arg clears the body.
type EventBodyCmd struct {
	ID   int64  `arg:"" help:"Event ID."`
	Body string `arg:"" help:"New event body (empty clears)."`
}

func (c *EventBodyCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	if err := s.Update(ctx, c.ID, nil, &c.Body, nil); err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Updated event %d\n", c.ID)
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/fngr/ -run "TestEventCmd" -v`
Expected: PASS for all event subtests (including the renamed `TestEventCmd_TextRequiresNonEmpty` and `TestEventCmd_TextSyncs`).

- [ ] **Step 5: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/fngr/event.go cmd/fngr/event_test.go
git commit -m "feat(cmd/event): title/body verbs alongside text"
```

---

## Task 8: Renderer — `Event` detail, `Tree`/`Flat`, JSON shape, CSV columns

Title-only on list views; labeled `Title:` line + raw body block in detail; JSON wire shape mirrors input; CSV gains a `body` column.

**Files:**
- Modify: `internal/render/render.go`
- Modify: `internal/render/render_test.go`

- [ ] **Step 1: Write / update failing tests**

In `internal/render/render_test.go`, find the existing tests for `JSON`, `CSV`, `Event`, `Tree`, `Flat`, `JSONStream`, `CSVStream`, `FlatStream`. Update them to construct events with `Title:`/`Body:` (instead of `Text:`) and assert the new output shapes. Then append:

```go
func TestEvent_BodyEmpty(t *testing.T) {
	t.Parallel()
	ev := &event.Event{
		ID:        1,
		Title:     "headline",
		Body:      "",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}
	var b bytes.Buffer
	if err := Event(&b, ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "Title:  headline") {
		t.Errorf("missing title line: %q", got)
	}
	if strings.Contains(got, "\n\n") && !strings.Contains(got, "Meta:") {
		t.Errorf("body section should be omitted when body is empty: %q", got)
	}
}

func TestEvent_BodyPresent(t *testing.T) {
	t.Parallel()
	ev := &event.Event{
		ID:        1,
		Title:     "headline",
		Body:      "the full story\nspans two lines",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}
	var b bytes.Buffer
	if err := Event(&b, ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "Title:  headline") {
		t.Errorf("missing title line: %q", got)
	}
	if !strings.Contains(got, "\n\nthe full story\nspans two lines\n") {
		t.Errorf("body block missing or mislaid: %q", got)
	}
}

func TestJSON_TitleBodyShape(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "the body",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
	}}
	var b bytes.Buffer
	if err := JSON(&b, events); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, `"title": "headline"`) || !strings.Contains(got, `"body": "the body"`) {
		t.Errorf("missing title/body in JSON: %q", got)
	}
	if strings.Contains(got, `"text"`) {
		t.Errorf("JSON should not have text field: %q", got)
	}
}

func TestCSV_TitleBodyColumns(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "body line",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}}
	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "id,parent_id,created_at,author,title,body\n") {
		t.Errorf("unexpected CSV header: %q", got)
	}
	if !strings.Contains(got, ",alice,headline,body line\n") {
		t.Errorf("missing title/body in CSV row: %q", got)
	}
}

func TestFlat_ShowsTitleNotBody(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "headline",
		Body:      "secret body",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}}
	var b bytes.Buffer
	if err := Flat(&b, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "headline") {
		t.Errorf("title not in flat: %q", got)
	}
	if strings.Contains(got, "secret body") {
		t.Errorf("body leaked into flat: %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/render/ -run "TestEvent_BodyEmpty|TestEvent_BodyPresent|TestJSON_TitleBodyShape|TestCSV_TitleBodyColumns|TestFlat_ShowsTitleNotBody" -v`
Expected: FAIL — current renderers still use `ev.Text`.

- [ ] **Step 3: Update `Event` detail (replace `Text:` with `Title:` + body block)**

In `internal/render/render.go`, replace the entire `Event` function with:

```go
// Event writes a single event in the human-readable detail layout used
// by `fngr event N` (ID / Parent / Date / Title / [body block] / Meta).
// When body is non-empty, a blank line and the raw body block follow
// the Title line. When body is empty, Meta follows directly after Title.
func Event(w io.Writer, ev *event.Event) error {
	if _, err := fmt.Fprintf(w, "ID:     %d\n", ev.ID); err != nil {
		return err
	}
	if ev.ParentID != nil {
		if _, err := fmt.Fprintf(w, "Parent: %d\n", *ev.ParentID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Date:   %s\n", formatLocalDateTime(ev.CreatedAt)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Title:  %s\n", ev.Title); err != nil {
		return err
	}
	if ev.Body != "" {
		if _, err := fmt.Fprintf(w, "\n%s\n", ev.Body); err != nil {
			return err
		}
	}

	if len(ev.Meta) > 0 {
		if _, err := fmt.Fprintln(w, "Meta:"); err != nil {
			return err
		}
		for _, m := range ev.Meta {
			if _, err := fmt.Fprintf(w, "  %s=%s\n", m.Key, m.Value); err != nil {
				return err
			}
		}
	}

	return nil
}
```

- [ ] **Step 4: Update `Tree`/`Flat` `formatEventLine` calls and `renderNode`/`Flat` to use Title**

In `renderNode`, change:

```go
	line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Title)
```

In `Flat`, change:

```go
	for _, ev := range events {
		line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Title)
```

In `FlatStream`, change:

```go
	line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Title)
```

(Three sites; mechanical `ev.Text` → `ev.Title`.)

- [ ] **Step 5: Update `jsonEvent` shape and `toJSONEvent`**

Replace:

```go
type jsonEvent struct {
	ID        int64       `json:"id"`
	ParentID  *int64      `json:"parent_id,omitempty"`
	Text      string      `json:"text"`
	CreatedAt string      `json:"created_at"`
	Meta      [][2]string `json:"meta,omitempty"`
}
```

with:

```go
type jsonEvent struct {
	ID        int64       `json:"id"`
	ParentID  *int64      `json:"parent_id,omitempty"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	CreatedAt string      `json:"created_at"`
	Meta      [][2]string `json:"meta,omitempty"`
}
```

In `toJSONEvent`, change:

```go
	out := jsonEvent{
		ID:        ev.ID,
		ParentID:  ev.ParentID,
		Title:     ev.Title,
		Body:      ev.Body,
		CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
	}
```

- [ ] **Step 6: Update CSV header + row in `CSV` and `CSVStream`**

In `CSV`, replace the body with:

```go
func CSV(w io.Writer, events []event.Event) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "parent_id", "created_at", "author", "title", "body"})
	for _, ev := range events {
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		_ = cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Title,
			ev.Body,
		})
	}
	cw.Flush()
	return cw.Error()
}
```

In `CSVStream`, change the header line and the per-row write:

```go
	if err := cw.Write([]string{"id", "parent_id", "created_at", "author", "title", "body"}); err != nil {
		return err
	}
```

```go
		if err := cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Title,
			ev.Body,
		}); err != nil {
			return err
		}
```

Update the doc comment on `CSV`:

```go
// CSV writes events as a CSV table with the columns
// `id, parent_id, created_at, author, title, body`. Meta tuples beyond
// `author` are not represented; for full meta, use JSON or Markdown.
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/render/ -v`
Expected: PASS.

- [ ] **Step 8: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "refactor(render): title+body in tree/flat/json/csv/event detail"
```

---

## Task 9: Markdown renderer — body as continuation lines

Markdown bullet shows title; body lines render as 2-space-indented continuation lines below; meta line stays after body.

**Files:**
- Modify: `internal/render/markdown.go`
- Modify: `internal/render/markdown_test.go`

- [ ] **Step 1: Write / update failing tests**

In `internal/render/markdown_test.go`, update existing tests that build `event.Event{Text: "..."}` to use `Title:`/`Body:`. Then append:

```go
func TestMarkdown_BodyContinuation(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "deploy v1.2",
		Body:      "needed a manual restart\nran into firewall issue first",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.Local),
		Meta:      []parse.Meta{{Key: "tag", Value: "ops"}},
	}}
	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "- 9.00am — deploy v1.2\n") {
		t.Errorf("missing title bullet: %q", got)
	}
	if !strings.Contains(got, "  needed a manual restart\n") {
		t.Errorf("missing first body continuation line: %q", got)
	}
	if !strings.Contains(got, "  ran into firewall issue first\n") {
		t.Errorf("missing second body continuation line: %q", got)
	}
	if !strings.Contains(got, "  tag=ops\n") {
		t.Errorf("missing meta continuation line: %q", got)
	}
}

func TestMarkdown_BodyEmpty(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		ID:        1,
		Title:     "title only",
		CreatedAt: time.Date(2026, 4, 22, 9, 0, 0, 0, time.Local),
		Meta:      []parse.Meta{{Key: "author", Value: "alice"}},
	}}
	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	got := b.String()
	if !strings.Contains(got, "- 9.00am — title only\n") {
		t.Errorf("missing title bullet: %q", got)
	}
	if !strings.Contains(got, "  author=alice\n") {
		t.Errorf("missing meta continuation line: %q", got)
	}
	// No body should mean the only continuation lines are the meta line.
	bodyMarker := "9.00am — title only\n  "
	pos := strings.Index(got, bodyMarker)
	if pos < 0 {
		t.Fatalf("could not locate marker: %q", got)
	}
	rest := got[pos+len(bodyMarker):]
	if !strings.HasPrefix(rest, "author=alice\n") {
		t.Errorf("expected meta line directly after bullet (no body); got %q", rest)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/render/ -run "TestMarkdown_BodyContinuation|TestMarkdown_BodyEmpty" -v`
Expected: FAIL — `renderMarkdownEvent` still uses `ev.Text`.

- [ ] **Step 3: Replace `renderMarkdownEvent`**

In `internal/render/markdown.go`, replace the entire `renderMarkdownEvent` function with:

```go
// renderMarkdownEvent writes one event's bullet (title) followed by
// body lines and meta line as 2-space-indented continuation lines.
// It updates *lastDate; when the local date of ev differs, it first
// writes a date header (with a leading blank line if *lastDate is
// non-empty).
func renderMarkdownEvent(w io.Writer, lastDate *string, ev event.Event) error {
	local := ev.CreatedAt.Local()
	date := local.Format(timefmt.DateFormat)
	if date != *lastDate {
		if *lastDate != "" {
			if _, err := fmt.Fprint(w, "\n"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "## %s\n\n", date); err != nil {
			return err
		}
		*lastDate = date
	}

	timeStr := local.Format(timefmt.LayoutToday)

	titleLines := strings.Split(ev.Title, "\n")
	for i, line := range titleLines {
		titleLines[i] = strings.TrimSuffix(line, "\r")
	}

	if _, err := fmt.Fprintf(w, "- %s — %s\n", timeStr, titleLines[0]); err != nil {
		return err
	}
	for _, line := range titleLines[1:] {
		if _, err := fmt.Fprintf(w, "  %s\n", line); err != nil {
			return err
		}
	}

	if ev.Body != "" {
		bodyLines := strings.Split(ev.Body, "\n")
		for _, line := range bodyLines {
			line = strings.TrimSuffix(line, "\r")
			if _, err := fmt.Fprintf(w, "  %s\n", line); err != nil {
				return err
			}
		}
	}

	if len(ev.Meta) > 0 {
		pairs := make([]string, len(ev.Meta))
		for i, m := range ev.Meta {
			pairs[i] = m.Key + "=" + m.Value
		}
		slices.Sort(pairs)
		if _, err := fmt.Fprintf(w, "  %s\n", strings.Join(pairs, " ")); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/render/ -run "TestMarkdown" -v`
Expected: PASS.

- [ ] **Step 5: Run full lint + test**

Run: `make ci`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/render/markdown.go internal/render/markdown_test.go
git commit -m "refactor(render): markdown shows body as continuation lines"
```

---

## Task 10: Documentation — README + roadmap update

Capture the new wire shape, the three event verbs, and move the roadmap entry from "Data model" to "Done".

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/roadmap.md`
- Modify: `CLAUDE.md` (if architecture description references `events.text`)

- [ ] **Step 1: Update `README.md` examples**

Find the JSON example block in README (currently shows `{"text":"hi","meta":...}`). Replace with:

```
# Bulk import a single event from JSON
echo '{"title":"hi","body":"","meta":[["tag","ops"]]}' | fngr add --format=json
```

Find the `event text` block (currently `fngr event text 1 "fixed wording for @sarah #urgent"`). Replace the surrounding section with:

```
# Edit text — re-splits via '. ' into title + body
fngr event text 1 "fixed wording. for @sarah #urgent"

# Or set just the title (body untouched)
fngr event title 1 "fixed wording"

# Or set just the body (title untouched; empty arg clears body)
fngr event body 1 "for @sarah #urgent"
```

- [ ] **Step 2: Move roadmap entry**

In `docs/superpowers/roadmap.md`, delete the `**Title + body split**` bullet from the "Data model" section, and append to the "Done" section a new bullet:

```
- **Title + body data-model split** — `events.text` replaced by
  separate `title` + `body` columns. Split rule on input is the
  literal `". "` (dot+space): everything before is the title, after
  is the body, both trimmed; no `". "` means title-only with empty
  body. JSON wire shape carries `title` + `body` directly. Three
  event verbs: `event text` (re-splits), `event title`, `event body`.
  Lists and tree show titles only; `fngr event N` and `--format=md`
  show body too. Pure-SQL migration 3 splits via INSTR/SUBSTR and
  rebuilds FTS.
```

- [ ] **Step 3: Update `CLAUDE.md` architecture description**

Find the bullet in CLAUDE.md describing `internal/event/event.go` that mentions `text`. Replace `text` references with `title, body`. Specifically, in the bullet starting with `internal/event/event.go`, swap:

- "the per-record INSERT loop" — unchanged structure, but mention title+body
- "Update (text and/or timestamp..." — change to "Update (title, body, and/or timestamp...; on title or body change body-derived tags are *synced* — `parse.BodyTags(oldTitle+\" \"+oldBody)` deleted then `parse.BodyTags(newTitle+\" \"+newBody)` inserted via `ON CONFLICT DO NOTHING`; FTS rebuilt)"

Keep the rest as-is.

- [ ] **Step 4: Run full lint + test (sanity)**

Run: `make ci`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add README.md docs/superpowers/roadmap.md CLAUDE.md
git commit -m "docs: README + roadmap for title+body split"
```

---

## Self-review checklist (controller, after all tasks)

After every task is complete and `make ci` is green, verify:

- [ ] `git log --oneline | head -12` shows the 10 commits in order.
- [ ] `grep -rn "events\.text\|ev\.Text\b\|in\.Text\b" internal/ cmd/` returns nothing (excluding `migrations/3.sql` which references `text` as the pre-migration column name).
- [ ] `grep -rn '"text"' cmd/fngr/add_json.go internal/render/render.go` returns nothing (JSON shape fully migrated).
- [ ] `fngr add "v1.2 done. Hotfix"` produces an event with `Title=v1.2 done, Body=Hotfix`.
- [ ] `fngr add "no separator"` produces an event with `Title=no separator, Body=""`.
- [ ] `fngr event title <id> "new"` updates only title.
- [ ] `fngr event body <id> ""` clears the body.
- [ ] `fngr --format=json | fngr add --format=json` round-trips.
- [ ] `fngr -S '#ops'` still matches events tagged `ops`.
