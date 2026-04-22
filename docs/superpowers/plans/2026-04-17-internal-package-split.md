# Internal Package Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split the flat `internal/` package into `internal/db`, `internal/event`, `internal/parse`, and `internal/render` sub-packages with idiomatic Go naming.

**Architecture:** Build leaf packages first (`parse`, `db`), then dependent packages (`event`, `render`), then update the CLI layer. Each task creates one package, moves code, updates references, and verifies tests pass. The old `internal/` files are deleted last.

**Tech Stack:** Go 1.26, modernc.org/sqlite, make targets for test/lint/build.

---

### Task 1: Create `internal/parse` package

**Files:**
- Create: `internal/parse/parse.go`
- Create: `internal/parse/parse_test.go`

This is the leaf package with no internal dependencies. It owns the `Meta` type, body-tag parsing, flag-meta parsing, FTS content building, and date format constants.

- [ ] **Step 1: Create `internal/parse/parse.go`**

```go
package parse

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	DateFormat     = "2006-01-02"
	DateTimeFormat = "2006-01-02 15:04:05"
)

type Meta struct {
	Key   string
	Value string
}

var tagPatterns = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(`@([\w][\w/\-]*)`), "people"},
	{regexp.MustCompile(`#([\w][\w/\-]*)`), "tag"},
}

func BodyTags(text string) []Meta {
	seen := make(map[Meta]struct{})
	var result []Meta

	for _, p := range tagPatterns {
		for _, m := range p.re.FindAllStringSubmatch(text, -1) {
			meta := Meta{Key: p.key, Value: m[1]}
			if _, ok := seen[meta]; !ok {
				seen[meta] = struct{}{}
				result = append(result, meta)
			}
		}
	}

	return result
}

func FlagMeta(flags []string) ([]Meta, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	result := make([]Meta, 0, len(flags))
	for _, f := range flags {
		key, value, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid meta flag %q: missing '='", f)
		}
		result = append(result, Meta{Key: key, Value: value})
	}
	return result, nil
}

func FTSContent(text string, meta []Meta) string {
	parts := make([]string, 0, 1+len(meta))
	if text != "" {
		parts = append(parts, text)
	}
	for _, m := range meta {
		parts = append(parts, m.Key+"="+m.Value)
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 2: Create `internal/parse/parse_test.go`**

Move the tests from `internal/meta_test.go` that test `ParseBodyTags`, `ParseFlagMeta`, and `BuildFTSContent`. Update function names and package. Note: `CollectMeta` tests stay behind — they'll move to `internal/event/` in Task 3.

```go
package parse

import (
	"testing"
)

func assertMetaEqual(t *testing.T, got, want []Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestBodyTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []Meta
	}{
		{
			name: "hash tags",
			text: "Meeting about #planning and #budget",
			want: []Meta{
				{Key: "tag", Value: "planning"},
				{Key: "tag", Value: "budget"},
			},
		},
		{
			name: "at tags",
			text: "Talked with @sarah and @bob",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "people", Value: "bob"},
			},
		},
		{
			name: "mixed tags with people first",
			text: "Discussed #planning with @sarah",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "hierarchical tags",
			text: "Working on #work/project-x and #infra/deploy-v2",
			want: []Meta{
				{Key: "tag", Value: "work/project-x"},
				{Key: "tag", Value: "infra/deploy-v2"},
			},
		},
		{
			name: "no tags",
			text: "Just a plain text entry",
			want: nil,
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
		{
			name: "duplicate tags are deduplicated",
			text: "#planning and #planning again #planning",
			want: []Meta{
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "duplicate at tags are deduplicated",
			text: "@sarah and @sarah again",
			want: []Meta{
				{Key: "people", Value: "sarah"},
			},
		},
		{
			name: "mixed duplicates across types",
			text: "@sarah #ops @sarah #ops",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertMetaEqual(t, BodyTags(tt.text), tt.want)
		})
	}
}

func TestFlagMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   []string
		want    []Meta
		wantErr bool
	}{
		{
			name:  "single flag",
			flags: []string{"env=prod"},
			want:  []Meta{{Key: "env", Value: "prod"}},
		},
		{
			name:  "multiple flags",
			flags: []string{"env=prod", "region=us-east-1"},
			want: []Meta{
				{Key: "env", Value: "prod"},
				{Key: "region", Value: "us-east-1"},
			},
		},
		{
			name:    "missing equals sign",
			flags:   []string{"invalidflag"},
			wantErr: true,
		},
		{
			name:  "value with equals signs",
			flags: []string{"note=a=b=c"},
			want:  []Meta{{Key: "note", Value: "a=b=c"}},
		},
		{
			name:  "empty flags",
			flags: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FlagMeta(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FlagMeta(%v) expected error, got nil", tt.flags)
				}
				return
			}
			if err != nil {
				t.Fatalf("FlagMeta(%v) unexpected error: %v", tt.flags, err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

func TestFTSContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		meta []Meta
		want string
	}{
		{
			name: "text with metadata",
			text: "Deploy done",
			meta: []Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "tag", Value: "ops"},
			},
			want: "Deploy done author=nicolas tag=ops",
		},
		{
			name: "text only",
			text: "Just some text",
			meta: nil,
			want: "Just some text",
		},
		{
			name: "empty text with metadata",
			text: "",
			meta: []Meta{
				{Key: "author", Value: "nicolas"},
			},
			want: "author=nicolas",
		},
		{
			name: "empty text and no metadata",
			text: "",
			meta: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FTSContent(tt.text, tt.meta)
			if got != tt.want {
				t.Errorf("FTSContent(%q, %v) = %q, want %q", tt.text, tt.meta, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run parse package tests**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && go test -race ./internal/parse/...`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/parse/parse.go internal/parse/parse_test.go
git commit -m "refactor: create internal/parse package with Meta type and text parsing"
```

---

### Task 2: Create `internal/db` package

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`

Standalone package with no internal dependencies. Rename `ResolveDBPath` → `ResolvePath`, `OpenDB` → `Open`.

- [ ] **Step 1: Create `internal/db/db.go`**

```go
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func ResolvePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if _, err := os.Stat(".fngr.db"); err == nil {
		return ".fngr.db", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".fngr.db"), nil
}

func Open(path string, create bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("database not found: %s (use 'fngr add' to create one)", path)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot enable foreign keys: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot enable WAL mode: %w", err)
	}

	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot set busy timeout: %w", err)
	}

	if _, err := db.Exec("PRAGMA synchronous = NORMAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot set synchronous mode: %w", err)
	}

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot initialize schema: %w", err)
	}

	return db, nil
}

func initSchema(db *sql.DB) error {
	schema := `
		CREATE TABLE IF NOT EXISTS events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_id  INTEGER REFERENCES events(id) ON DELETE CASCADE,
			text       TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT (datetime('now'))
		);

		CREATE TABLE IF NOT EXISTS event_meta (
			event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
			key      TEXT NOT NULL,
			value    TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_events_parent_id ON events(parent_id);
		CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);

		CREATE INDEX IF NOT EXISTS idx_event_meta_key_value ON event_meta(key, value);
		CREATE INDEX IF NOT EXISTS idx_event_meta_event_id ON event_meta(event_id, key, value);

		CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
			content,
			tokenize = "unicode61 tokenchars '=/'"
		);

		CREATE TRIGGER IF NOT EXISTS trg_events_fts_delete
		AFTER DELETE ON events
		BEGIN
			DELETE FROM events_fts WHERE rowid = OLD.id;
		END;
	`
	_, err := db.Exec(schema)
	return err
}
```

- [ ] **Step 2: Create `internal/db/db_test.go`**

Move tests from `internal/db_test.go`. Rename function calls: `ResolveDBPath` → `ResolvePath`, `OpenDB` → `Open`. The `testDB` and `testDBWithSchema` helpers stay as unexported helpers within the db package.

```go
package db

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	return db
}

func testDBWithSchema(t *testing.T) *sql.DB {
	t.Helper()
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	return db
}

func TestInitSchema_CreatesAllTables(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	tables := []string{"events", "event_meta", "events_fts"}
	for _, table := range tables {
		var name string
		err := db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestInitSchema_Idempotent(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema call: %v", err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("second initSchema call: %v", err)
	}
}

func TestCascadeDelete_RemovesChildrenAndMeta(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)

	res, err := db.Exec("INSERT INTO events (text) VALUES (?)", "parent event")
	if err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	parentID, _ := res.LastInsertId()

	res, err = db.Exec("INSERT INTO events (parent_id, text) VALUES (?, ?)", parentID, "child event")
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}
	childID, _ := res.LastInsertId()

	if _, err := db.Exec("INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)", parentID, "author", "alice"); err != nil {
		t.Fatalf("insert parent meta: %v", err)
	}
	if _, err := db.Exec("INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)", childID, "tag", "important"); err != nil {
		t.Fatalf("insert child meta: %v", err)
	}

	if _, err := db.Exec("DELETE FROM events WHERE id = ?", parentID); err != nil {
		t.Fatalf("delete parent: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", childID).Scan(&count); err != nil {
		t.Fatalf("count child: %v", err)
	}
	if count != 0 {
		t.Errorf("expected child event to be deleted, got count=%d", count)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM event_meta").Scan(&count); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if count != 0 {
		t.Errorf("expected all metadata to be deleted, got count=%d", count)
	}
}

func TestFTSDeleteTrigger(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)

	res, err := db.Exec("INSERT INTO events (text) VALUES (?)", "searchable event")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, _ := res.LastInsertId()

	if _, err := db.Exec("INSERT INTO events_fts (rowid, content) VALUES (?, ?)", eventID, "searchable event"); err != nil {
		t.Fatalf("insert FTS content: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events_fts WHERE rowid = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("count FTS before delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 FTS row before delete, got %d", count)
	}

	if _, err := db.Exec("DELETE FROM events WHERE id = ?", eventID); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM events_fts WHERE rowid = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("count FTS after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected FTS row to be deleted, got count=%d", count)
	}
}

func TestResolvePath_ExplicitPath(t *testing.T) {
	t.Parallel()
	got, err := ResolvePath("/tmp/custom.db")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != "/tmp/custom.db" {
		t.Errorf("got %q, want %q", got, "/tmp/custom.db")
	}
}

func TestResolvePath_LocalFile(t *testing.T) {
	dir := t.TempDir()

	localDB := filepath.Join(dir, ".fngr.db")
	if err := os.WriteFile(localDB, nil, 0o644); err != nil {
		t.Fatalf("create local db: %v", err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}
	if got != ".fngr.db" {
		t.Errorf("got %q, want %q", got, ".fngr.db")
	}
}

func TestResolvePath_FallbackHome(t *testing.T) {
	dir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath: %v", err)
	}

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".fngr.db")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOpen_CreateTrue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open create=true: %v", err)
	}
	defer database.Close()

	if _, err := os.Stat(dbPath); errors.Is(err, fs.ErrNotExist) {
		t.Fatal("database file was not created")
	}

	var name string
	err = database.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='events'").Scan(&name)
	if err != nil {
		t.Errorf("events table not found: %v", err)
	}
}

func TestOpen_CreateFalseNotExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent.db")

	_, err := Open(dbPath, false)
	if err == nil {
		t.Fatal("expected error for nonexistent db with create=false")
	}
}

func TestOpen_ForeignKeysEnabled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fk.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var fkEnabled int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fkEnabled != 1 {
		t.Errorf("foreign_keys = %d, want 1", fkEnabled)
	}
}

func TestOpen_WALMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wal.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}
}

func TestOpen_BusyTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "busy.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var timeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}
}

func TestOpen_SynchronousNormal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sync.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	var syncMode int
	if err := database.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if syncMode != 1 {
		t.Errorf("synchronous = %d, want 1 (NORMAL)", syncMode)
	}
}
```

- [ ] **Step 3: Run db package tests**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && go test -race ./internal/db/...`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/db/db.go internal/db/db_test.go
git commit -m "refactor: create internal/db package with ResolvePath and Open"
```

---

### Task 3: Create `internal/event` package

**Files:**
- Create: `internal/event/event.go`
- Create: `internal/event/meta.go`
- Create: `internal/event/filter.go`
- Create: `internal/event/event_test.go`
- Create: `internal/event/meta_test.go`
- Create: `internal/event/filter_test.go`

This package imports `internal/parse` for the `Meta` type and text parsing functions. Rename exported functions to drop the redundant prefix.

- [ ] **Step 1: Create `internal/event/meta.go`**

This file holds domain-level meta constants and `CollectMeta`. The `Meta` type is imported from `parse`.

```go
package event

import (
	"github.com/monolithiclab/fngr/internal/parse"
)

const (
	MetaKeyAuthor = "author"
	MetaKeyPeople = "people"
	MetaKeyTag    = "tag"
)

func CollectMeta(text string, flags []string, author string) ([]parse.Meta, error) {
	seen := make(map[parse.Meta]struct{})
	var result []parse.Meta

	add := func(m parse.Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	add(parse.Meta{Key: MetaKeyAuthor, Value: author})

	for _, m := range parse.BodyTags(text) {
		add(m)
	}

	flagMeta, err := parse.FlagMeta(flags)
	if err != nil {
		return nil, err
	}
	for _, m := range flagMeta {
		add(m)
	}

	return result, nil
}
```

- [ ] **Step 2: Create `internal/event/filter.go`**

```go
package event

import (
	"strings"
)

func preprocessFilter(expr string) string {
	tokens := tokenizeFilter(expr)
	if len(tokens) == 0 {
		return ""
	}

	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		switch tok {
		case "&":
			out = append(out, "AND")
		case "|":
			out = append(out, "OR")
		default:
			out = append(out, convertTerm(tok))
		}
	}

	result := strings.Join(out, " ")
	result = strings.ReplaceAll(result, " AND NOT ", " NOT ")
	return result
}

var shorthandKeys = map[byte]string{
	'#': MetaKeyTag,
	'@': MetaKeyPeople,
}

func convertTerm(tok string) string {
	if strings.HasPrefix(tok, "!") {
		return "NOT " + convertTerm(tok[1:])
	}

	if key, ok := shorthandKeys[tok[0]]; ok {
		return ftsQuote(key + "=" + tok[1:])
	}

	if strings.Contains(tok, "=") {
		return ftsQuote(tok)
	}

	return tok
}

func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func tokenizeFilter(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}

	var tokens []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for _, ch := range expr {
		switch ch {
		case ' ':
			flush()
		case '&', '|':
			flush()
			tokens = append(tokens, string(ch))
		default:
			current.WriteRune(ch)
		}
	}
	flush()

	return tokens
}
```

Note: `preprocessFilter` is now unexported since it's only called by `List` within this package.

- [ ] **Step 3: Create `internal/event/event.go`**

```go
package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

var ErrNotFound = errors.New("not found")

type Event struct {
	ID        int64
	ParentID  *int64
	Text      string
	CreatedAt time.Time
	Meta      []parse.Meta
}

type MetaCount struct {
	Key   string
	Value string
	Count int
}

func Add(ctx context.Context, db *sql.DB, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if parentID != nil {
		var exists int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", *parentID).Scan(&exists)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("parent event %d: %w", *parentID, ErrNotFound)
			}
			return 0, fmt.Errorf("query parent event: %w", err)
		}
	}

	var res sql.Result
	if createdAt != nil {
		res, err = tx.ExecContext(ctx,
			"INSERT INTO events (parent_id, text, created_at) VALUES (?, ?, ?)",
			parentID, text, createdAt.UTC().Format(parse.DateTimeFormat),
		)
	} else {
		res, err = tx.ExecContext(ctx,
			"INSERT INTO events (parent_id, text) VALUES (?, ?)",
			parentID, text,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	if len(meta) > 0 {
		stmt, err := tx.PrepareContext(ctx, "INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)")
		if err != nil {
			return 0, fmt.Errorf("prepare meta insert: %w", err)
		}
		defer stmt.Close()
		for _, m := range meta {
			if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value); err != nil {
				return 0, fmt.Errorf("insert meta: %w", err)
			}
		}
	}

	ftsContent := parse.FTSContent(text, meta)
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO events_fts (rowid, content) VALUES (?, ?)",
		id, ftsContent,
	); err != nil {
		return 0, fmt.Errorf("insert FTS content: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return id, nil
}

func Get(ctx context.Context, db *sql.DB, id int64) (*Event, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, parent_id, text, created_at FROM events WHERE id = ?", id,
	)
	if err != nil {
		return nil, fmt.Errorf("query event: %w", err)
	}
	defer rows.Close()

	events, err := scanEvents(ctx, db, rows)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("event %d: %w", id, ErrNotFound)
	}
	return &events[0], nil
}

func Delete(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, "DELETE FROM events WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete event: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("event %d: %w", id, ErrNotFound)
	}

	return nil
}

func HasChildren(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE parent_id = ?", id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("query children: %w", err)
	}
	return count > 0, nil
}

var wellKnownMetaKeys = map[string]bool{
	MetaKeyAuthor: true,
}

func UpdateMeta(ctx context.Context, db *sql.DB, oldKey, oldValue, newKey, newValue string) (int64, error) {
	if wellKnownMetaKeys[oldKey] {
		return 0, fmt.Errorf("cannot rename well-known meta key %q", oldKey)
	}
	res, err := db.ExecContext(ctx,
		"UPDATE event_meta SET key = ?, value = ? WHERE key = ? AND value = ?",
		newKey, newValue, oldKey, oldValue,
	)
	if err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}
	return res.RowsAffected()
}

func DeleteMeta(ctx context.Context, db *sql.DB, key, value string) (int64, error) {
	if wellKnownMetaKeys[key] {
		return 0, fmt.Errorf("cannot delete well-known meta key %q", key)
	}
	res, err := db.ExecContext(ctx,
		"DELETE FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	)
	if err != nil {
		return 0, fmt.Errorf("delete meta: %w", err)
	}
	return res.RowsAffected()
}

func ListMeta(ctx context.Context, db *sql.DB) ([]MetaCount, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT key, value, COUNT(*) AS count FROM event_meta GROUP BY key, value ORDER BY key, value",
	)
	if err != nil {
		return nil, fmt.Errorf("query meta counts: %w", err)
	}
	defer rows.Close()

	var result []MetaCount
	for rows.Next() {
		var mc MetaCount
		if err := rows.Scan(&mc.Key, &mc.Value, &mc.Count); err != nil {
			return nil, fmt.Errorf("scan meta count: %w", err)
		}
		result = append(result, mc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta counts: %w", err)
	}

	return result, nil
}

type ListOpts struct {
	Filter string
	From   string
	To     string
}

func List(ctx context.Context, db *sql.DB, opts ListOpts) ([]Event, error) {
	var query string
	var args []any

	if opts.Filter != "" {
		matchExpr := preprocessFilter(opts.Filter)
		if positiveExpr, ok := strings.CutPrefix(matchExpr, "NOT "); ok {
			query = `SELECT e.id, e.parent_id, e.text, e.created_at
				FROM events e
				WHERE e.id NOT IN (
					SELECT rowid FROM events_fts WHERE events_fts MATCH ?
				)`
			args = append(args, positiveExpr)
		} else {
			query = `SELECT e.id, e.parent_id, e.text, e.created_at
				FROM events e
				JOIN events_fts f ON f.rowid = e.id
				WHERE events_fts MATCH ?`
			args = append(args, matchExpr)
		}
	} else {
		query = `SELECT e.id, e.parent_id, e.text, e.created_at
			FROM events e
			WHERE 1=1`
	}

	if opts.From != "" {
		query += " AND e.created_at >= ?"
		args = append(args, opts.From)
	}
	if opts.To != "" {
		query += " AND e.created_at <= datetime(?, '+1 day')"
		args = append(args, opts.To)
	}

	query += " ORDER BY e.created_at ASC"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	return scanEvents(ctx, db, rows)
}

func GetSubtree(ctx context.Context, db *sql.DB, rootID int64) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, parent_id, text, created_at FROM events WHERE id = ?
			UNION ALL
			SELECT e.id, e.parent_id, e.text, e.created_at
			FROM events e JOIN subtree s ON e.parent_id = s.id
		)
		SELECT id, parent_id, text, created_at FROM subtree ORDER BY created_at ASC
	`, rootID)
	if err != nil {
		return nil, fmt.Errorf("query subtree: %w", err)
	}
	defer rows.Close()

	events, err := scanEvents(ctx, db, rows)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("event %d: %w", rootID, ErrNotFound)
	}
	return events, nil
}

func scanEvents(ctx context.Context, db *sql.DB, rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		var parentID sql.NullInt64
		if err := rows.Scan(&e.ID, &parentID, &e.Text, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if parentID.Valid {
			e.ParentID = &parentID.Int64
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}

	if len(events) > 0 {
		if err := loadMetaBatch(ctx, db, events); err != nil {
			return nil, err
		}
	}

	return events, nil
}

func loadMetaBatch(ctx context.Context, db *sql.DB, events []Event) error {
	ids := make([]any, len(events))
	idIdx := make(map[int64]int, len(events))
	for i, e := range events {
		ids[i] = e.ID
		idIdx[e.ID] = i
	}

	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]

	query := "SELECT event_id, key, value FROM event_meta WHERE event_id IN (" + placeholders + ") ORDER BY event_id, key, value" // #nosec G202 -- placeholders are "?" repeated, not user input
	rows, err := db.QueryContext(ctx, query, ids...)
	if err != nil {
		return fmt.Errorf("query meta batch: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var eventID int64
		var m parse.Meta
		if err := rows.Scan(&eventID, &m.Key, &m.Value); err != nil {
			return fmt.Errorf("scan meta: %w", err)
		}
		if idx, ok := idIdx[eventID]; ok {
			events[idx].Meta = append(events[idx].Meta, m)
		}
	}
	return rows.Err()
}
```

- [ ] **Step 4: Create `internal/event/filter_test.go`**

```go
package event

import (
	"testing"
)

func TestTokenizeFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty input", "", nil},
		{"bare word", "project", []string{"project"}},
		{"hash tag", "#ops", []string{"#ops"}},
		{"at tag", "@sarah", []string{"@sarah"}},
		{"key=value", "tag=deploy", []string{"tag=deploy"}},
		{"AND operator", "tag=deploy & project", []string{"tag=deploy", "&", "project"}},
		{"OR operator", "#ops | #deploy", []string{"#ops", "|", "#deploy"}},
		{"NOT prefix", "@user & !daily", []string{"@user", "&", "!daily"}},
		{"complex expression", "author=nicolas & #work & !meeting", []string{"author=nicolas", "&", "#work", "&", "!meeting"}},
		{"NOT with key=value", "!tag=deploy", []string{"!tag=deploy"}},
		{"hierarchical tag", "#work/project-x", []string{"#work/project-x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tokenizeFilter(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("tokenizeFilter(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenizeFilter(%q)[%d] = %q, want %q",
						tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPreprocessFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"hash tag shorthand", "#ops", `"tag=ops"`},
		{"at tag shorthand", "@sarah", `"people=sarah"`},
		{"key=value passthrough", "tag=deploy", `"tag=deploy"`},
		{"bare word passthrough", "project", "project"},
		{"AND operator", "tag=deploy & project", `"tag=deploy" AND project`},
		{"OR operator", "#ops | #deploy", `"tag=ops" OR "tag=deploy"`},
		{"NOT operator", "@user & !daily", `"people=user" NOT daily`},
		{"complex expression", "author=nicolas & #work & !meeting", `"author=nicolas" AND "tag=work" NOT meeting`},
		{"hierarchical tag", "#work/project-x", `"tag=work/project-x"`},
		{"empty input", "", ""},
		{"NOT with key=value", "!tag=deploy", `NOT "tag=deploy"`},
		{"embedded double quotes", `tag=val"ue`, `"tag=val""ue"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := preprocessFilter(tt.input)
			if got != tt.want {
				t.Errorf("preprocessFilter(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 5: Create `internal/event/meta_test.go`**

This contains the `CollectMeta` tests (the only meta test that stayed in the event package).

```go
package event

import (
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func assertMetaEqual(t *testing.T, got, want []parse.Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestCollectMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		flags   []string
		author  string
		want    []parse.Meta
		wantErr bool
	}{
		{
			name:   "combines all sources",
			text:   "Deploy done #ops @sarah",
			flags:  []string{"env=prod"},
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
				{Key: "env", Value: "prod"},
			},
		},
		{
			name:   "deduplicates across sources",
			text:   "#ops",
			flags:  []string{"tag=ops"},
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "tag", Value: "ops"},
			},
		},
		{
			name:    "propagates flag parse error",
			text:    "some text",
			flags:   []string{"noequalssign"},
			author:  "nicolas",
			wantErr: true,
		},
		{
			name:   "author only",
			text:   "plain text",
			flags:  nil,
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CollectMeta(tt.text, tt.flags, tt.author)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CollectMeta expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CollectMeta unexpected error: %v", err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}
```

- [ ] **Step 6: Create `internal/event/event_test.go`**

```go
package event

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/parse"
)

var ctx = context.Background()

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:", true)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func TestAdd(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	meta := []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "meeting"},
		{Key: "people", Value: "bob"},
	}

	id, err := Add(ctx, database, "standup with @bob #meeting", nil, meta, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id < 1 {
		t.Fatalf("expected positive event ID, got %d", id)
	}

	var text string
	err = database.QueryRow("SELECT text FROM events WHERE id = ?", id).Scan(&text)
	if err != nil {
		t.Fatalf("query event row: %v", err)
	}
	if text != "standup with @bob #meeting" {
		t.Errorf("event text = %q, want %q", text, "standup with @bob #meeting")
	}

	var metaCount int
	err = database.QueryRow("SELECT COUNT(*) FROM event_meta WHERE event_id = ?", id).Scan(&metaCount)
	if err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if metaCount != 3 {
		t.Errorf("meta count = %d, want 3", metaCount)
	}

	var ftsCount int
	err = database.QueryRow("SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?", "standup").Scan(&ftsCount)
	if err != nil {
		t.Fatalf("FTS query: %v", err)
	}
	if ftsCount != 1 {
		t.Errorf("FTS match count = %d, want 1", ftsCount)
	}
}

func TestAdd_WithParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parentID, err := Add(ctx, database, "parent event", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, "child event", &parentID, nil, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	var storedParentID int64
	err = database.QueryRow("SELECT parent_id FROM events WHERE id = ?", childID).Scan(&storedParentID)
	if err != nil {
		t.Fatalf("query child parent_id: %v", err)
	}
	if storedParentID != parentID {
		t.Errorf("parent_id = %d, want %d", storedParentID, parentID)
	}
}

func TestAdd_InvalidParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	invalidParent := int64(9999)
	_, err := Add(ctx, database, "orphan event", &invalidParent, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid parent, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestGet(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	meta := []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}

	id, err := Add(ctx, database, "get me", nil, meta, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if ev.Text != "get me" {
		t.Errorf("event.Text = %q, want %q", ev.Text, "get me")
	}
	if len(ev.Meta) != 2 {
		t.Errorf("len(event.Meta) = %d, want 2", len(ev.Meta))
	}
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Get(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, "to be deleted", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := Delete(ctx, database, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", id).Scan(&count)
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected event to be deleted, got count=%d", count)
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	err := Delete(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestDelete_CascadesChildren(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parentID, err := Add(ctx, database, "parent", nil, []parse.Meta{{Key: "author", Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, "child", &parentID, []parse.Meta{{Key: "tag", Value: "reply"}}, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	if err := Delete(ctx, database, parentID); err != nil {
		t.Fatalf("Delete parent: %v", err)
	}

	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", childID).Scan(&count)
	if err != nil {
		t.Fatalf("count child: %v", err)
	}
	if count != 0 {
		t.Errorf("expected child event to be cascade-deleted, got count=%d", count)
	}
}

func TestList_NoFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "first event #work", nil, []parse.Meta{{Key: "tag", Value: "work"}}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err = Add(ctx, database, "second event #personal", nil, []parse.Meta{{Key: "tag", Value: "personal"}}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	events, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Text != "first event #work" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "first event #work")
	}
	if events[1].Text != "second event #personal" {
		t.Errorf("events[1].Text = %q, want %q", events[1].Text, "second event #personal")
	}
}

func TestList_WithFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "deploy to prod #ops", nil, []parse.Meta{{Key: "tag", Value: "ops"}}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err = Add(ctx, database, "standup meeting #work", nil, []parse.Meta{{Key: "tag", Value: "work"}}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "deploy to prod #ops" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "deploy to prod #ops")
	}
}

func TestList_WithDateRange(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := database.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "old event", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (1, 'old event')")
	if err != nil {
		t.Fatalf("insert old FTS: %v", err)
	}

	_, err = database.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "new event", "2026-03-15 12:00:00")
	if err != nil {
		t.Fatalf("insert new event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (2, 'new event')")
	if err != nil {
		t.Fatalf("insert new FTS: %v", err)
	}

	events, err := List(ctx, database, ListOpts{From: "2026-03-01"})
	if err != nil {
		t.Fatalf("List with From: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}

	events, err = List(ctx, database, ListOpts{To: "2026-02-01"})
	if err != nil {
		t.Fatalf("List with To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "old event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "old event")
	}

	events, err = List(ctx, database, ListOpts{From: "2026-02-01", To: "2026-04-01"})
	if err != nil {
		t.Fatalf("List with From and To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}
}

func TestListMeta(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "event one", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	_, err = Add(ctx, database, "event two", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "personal"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	counts, err := ListMeta(ctx, database)
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}

	if len(counts) != 3 {
		t.Fatalf("len(counts) = %d, want 3", len(counts))
	}

	expected := []struct {
		key   string
		value string
		count int
	}{
		{"author", "alice", 2},
		{"tag", "personal", 1},
		{"tag", "work", 1},
	}

	for i, exp := range expected {
		if counts[i].Key != exp.key || counts[i].Value != exp.value || counts[i].Count != exp.count {
			t.Errorf("counts[%d] = {%q, %q, %d}, want {%q, %q, %d}",
				i, counts[i].Key, counts[i].Value, counts[i].Count,
				exp.key, exp.value, exp.count)
		}
	}
}

func TestGetSubtree(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	root, err := Add(ctx, database, "root", nil, []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add root: %v", err)
	}

	child, err := Add(ctx, database, "child", &root, []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	grandchild, err := Add(ctx, database, "grandchild", &child, []parse.Meta{{Key: MetaKeyAuthor, Value: "bob"}}, nil)
	if err != nil {
		t.Fatalf("Add grandchild: %v", err)
	}

	if _, err := Add(ctx, database, "unrelated", nil, nil, nil); err != nil {
		t.Fatalf("Add unrelated: %v", err)
	}

	events, err := GetSubtree(ctx, database, root)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}

	if ids[0] != root || ids[1] != child || ids[2] != grandchild {
		t.Errorf("subtree IDs = %v, want [%d %d %d]", ids, root, child, grandchild)
	}

	if len(events[2].Meta) != 1 || events[2].Meta[0].Value != "bob" {
		t.Errorf("grandchild meta = %v, want [{author bob}]", events[2].Meta)
	}
}

func TestGetSubtree_LeafNode(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, "leaf", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := GetSubtree(ctx, database, id)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "leaf" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "leaf")
	}
}

func TestGetSubtree_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := GetSubtree(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent root, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestFTSIsolation_MetaTokensNotMatchedByBareWords(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "pushed to production", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "deploy"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "deploy"})
	if err != nil {
		t.Fatalf("List bare word: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("bare word 'deploy' matched %d events, want 0 (FTS isolation broken)", len(events))
	}

	events, err = List(ctx, database, ListOpts{Filter: "#deploy"})
	if err != nil {
		t.Fatalf("List #deploy: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("#deploy matched %d events, want 1", len(events))
	}
}

func TestFTSIsolation_BodyWordsNotMatchedByMetaFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "heading to work early", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "#work"})
	if err != nil {
		t.Fatalf("List #work: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("#work matched %d events, want 0 (body word leaked into meta filter)", len(events))
	}

	events, err = List(ctx, database, ListOpts{Filter: "work"})
	if err != nil {
		t.Fatalf("List bare work: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("bare 'work' matched %d events, want 1", len(events))
	}
}

func TestList_ComplexFilters(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "deploy to prod", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyPeople, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	_, err = Add(ctx, database, "standup meeting", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "bob"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "bob"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	_, err = Add(ctx, database, "deploy standup", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 3: %v", err)
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"AND tags", "#ops & #work", 1},
		{"OR tags", "#ops | #work", 3},
		{"NOT tag", "!#work", 1},
		{"tag AND person", "#ops & @alice", 2},
		{"body AND tag", "deploy & #ops", 2},
		{"body NOT tag", "deploy & !#work", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := List(ctx, database, ListOpts{Filter: tt.filter})
			if err != nil {
				t.Fatalf("List(%q): %v", tt.filter, err)
			}
			if len(events) != tt.want {
				texts := make([]string, len(events))
				for i, e := range events {
					texts[i] = e.Text
				}
				t.Errorf("filter %q matched %d events %v, want %d", tt.filter, len(events), texts, tt.want)
			}
		})
	}
}
```

- [ ] **Step 7: Run event package tests**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && go test -race ./internal/event/...`
Expected: All tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/event/event.go internal/event/meta.go internal/event/filter.go internal/event/event_test.go internal/event/meta_test.go internal/event/filter_test.go
git commit -m "refactor: create internal/event package with domain types and data access"
```

---

### Task 4: Create `internal/render` package

**Files:**
- Create: `internal/render/render.go`
- Create: `internal/render/render_test.go`

Rename all `Render*` functions to drop the prefix. Import `event.Event`, `event.MetaKeyAuthor`, and `parse.Meta`/`parse.DateFormat`/`parse.DateTimeFormat`.

- [ ] **Step 1: Create `internal/render/render.go`**

```go
package render

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func formatLocalDate(t time.Time) string {
	return t.Local().Format(parse.DateFormat)
}

func formatLocalDateTime(t time.Time) string {
	return t.Local().Format(parse.DateTimeFormat)
}

func metaValue(meta []parse.Meta, key string) string {
	for _, m := range meta {
		if m.Key == key {
			return m.Value
		}
	}
	return ""
}

func eventAuthor(ev event.Event) string {
	return metaValue(ev.Meta, event.MetaKeyAuthor)
}

func Tree(w io.Writer, events []event.Event) error {
	if len(events) == 0 {
		return nil
	}

	byID := make(map[int64]int, len(events))
	children := make(map[int64][]int64)
	var roots []int64

	for i, ev := range events {
		byID[ev.ID] = i
		if ev.ParentID == nil {
			roots = append(roots, ev.ID)
		} else {
			children[*ev.ParentID] = append(children[*ev.ParentID], ev.ID)
		}
	}

	for _, id := range roots {
		if err := renderNode(w, events, byID, children, id, "", ""); err != nil {
			return err
		}
	}
	return nil
}

func renderNode(w io.Writer, events []event.Event, byID map[int64]int, children map[int64][]int64, id int64, linePrefix, childPrefix string) error {
	idx := byID[id]
	ev := events[idx]
	date := formatLocalDate(ev.CreatedAt)
	author := eventAuthor(ev)

	if _, err := fmt.Fprintf(w, "%s%-4d%s  %s  %s\n", linePrefix, ev.ID, date, author, ev.Text); err != nil {
		return err
	}

	kids := children[id]
	for i, kidID := range kids {
		isLast := i == len(kids)-1
		var connector string
		var continuation string
		if isLast {
			connector = "\u2514\u2500 "
			continuation = "   "
		} else {
			connector = "\u251c\u2500 "
			continuation = "\u2502  "
		}
		if err := renderNode(w, events, byID, children, kidID, childPrefix+connector, childPrefix+continuation); err != nil {
			return err
		}
	}
	return nil
}

func Flat(w io.Writer, events []event.Event) error {
	for _, ev := range events {
		date := formatLocalDate(ev.CreatedAt)
		author := eventAuthor(ev)
		if _, err := fmt.Fprintf(w, "%-4d%s  %s  %s\n", ev.ID, date, author, ev.Text); err != nil {
			return err
		}
	}
	return nil
}

type jsonEvent struct {
	ID        int64               `json:"id"`
	ParentID  *int64              `json:"parent_id,omitempty"`
	Text      string              `json:"text"`
	CreatedAt string              `json:"created_at"`
	Meta      map[string][]string `json:"meta,omitempty"`
}

func JSON(w io.Writer, events []event.Event) error {
	out := make([]jsonEvent, len(events))
	for i, ev := range events {
		meta := make(map[string][]string)
		for _, m := range ev.Meta {
			meta[m.Key] = append(meta[m.Key], m.Value)
		}
		out[i] = jsonEvent{
			ID:        ev.ID,
			ParentID:  ev.ParentID,
			Text:      ev.Text,
			CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
			Meta:      meta,
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func CSV(w io.Writer, events []event.Event) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "parent_id", "created_at", "author", "text"})
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
			ev.Text,
		})
	}
	cw.Flush()
	return cw.Error()
}

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
	if _, err := fmt.Fprintf(w, "Text:   %s\n", ev.Text); err != nil {
		return err
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

- [ ] **Step 2: Create `internal/render/render_test.go`**

```go
package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func makeEvent(id int64, parentID *int64, text string, date string, author string) event.Event {
	t, _ := time.Parse("2006-01-02", date)
	return event.Event{
		ID:        id,
		ParentID:  parentID,
		Text:      text,
		CreatedAt: t,
		Meta:      []parse.Meta{{Key: event.MetaKeyAuthor, Value: author}},
	}
}

func renderTreeString(t *testing.T, events []event.Event) string {
	t.Helper()
	var b bytes.Buffer
	if err := Tree(&b, events); err != nil {
		t.Fatalf("Tree: %v", err)
	}
	return b.String()
}

func TestTree_Empty(t *testing.T) {
	t.Parallel()
	if got := renderTreeString(t, nil); got != "" {
		t.Errorf("Tree(nil) = %q, want %q", got, "")
	}
	if got := renderTreeString(t, []event.Event{}); got != "" {
		t.Errorf("Tree([]) = %q, want %q", got, "")
	}
}

func TestTree_FlatList(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "First event", "2026-04-10", "nicolas"),
		makeEvent(2, nil, "Second event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  First event\n" +
		"2   2026-04-11  nicolas  Second event\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree flat list:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_NestedChildren(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "First child", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(1)), "Second child", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Parent event\n" +
		"\u251c\u2500 2   2026-04-10  nicolas  First child\n" +
		"\u2514\u2500 3   2026-04-11  nicolas  Second child\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree nested:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_DeepNesting(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Root", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Child", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(2)), "Grandchild", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Root\n" +
		"\u2514\u2500 2   2026-04-10  nicolas  Child\n" +
		"   \u2514\u2500 3   2026-04-11  nicolas  Grandchild\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree deep nesting:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestTree_MixedRootsAndChildren(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Sprint 12 #work", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Planning meeting", "2026-04-10", "nicolas"),
		makeEvent(4, new(int64(2)), "Decided on architecture", "2026-04-10", "nicolas"),
		makeEvent(3, new(int64(1)), "Deploy v2.0 #ops", "2026-04-11", "nicolas"),
		makeEvent(5, nil, "Lunch with Sarah", "2026-04-12", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Sprint 12 #work\n" +
		"\u251c\u2500 2   2026-04-10  nicolas  Planning meeting\n" +
		"\u2502  \u2514\u2500 4   2026-04-10  nicolas  Decided on architecture\n" +
		"\u2514\u2500 3   2026-04-11  nicolas  Deploy v2.0 #ops\n" +
		"5   2026-04-12  nicolas  Lunch with Sarah\n"

	got := renderTreeString(t, events)
	if got != want {
		t.Errorf("Tree mixed:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestFlat(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Parent event", "2026-04-10", "nicolas"),
		makeEvent(2, new(int64(1)), "Child event", "2026-04-11", "nicolas"),
	}

	want := "" +
		"1   2026-04-10  nicolas  Parent event\n" +
		"2   2026-04-11  nicolas  Child event\n"

	var b bytes.Buffer
	if err := Flat(&b, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	got := b.String()
	if got != want {
		t.Errorf("Flat:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMetaValue(t *testing.T) {
	t.Parallel()
	meta := []parse.Meta{
		{Key: "author", Value: "nicolas"},
		{Key: "tag", Value: "work"},
	}

	if got := metaValue(meta, "author"); got != "nicolas" {
		t.Errorf("metaValue(author) = %q, want %q", got, "nicolas")
	}
	if got := metaValue(meta, "tag"); got != "work" {
		t.Errorf("metaValue(tag) = %q, want %q", got, "work")
	}
	if got := metaValue(meta, "missing"); got != "" {
		t.Errorf("metaValue(missing) = %q, want %q", got, "")
	}
	if got := metaValue(nil, "author"); got != "" {
		t.Errorf("metaValue(nil, author) = %q, want %q", got, "")
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := JSON(&b, events); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got := b.String()

	var parsed []json.RawMessage
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("JSON produced invalid JSON: %v\noutput:\n%s", err, got)
	}

	if len(parsed) != 1 {
		t.Errorf("JSON produced %d items, want 1", len(parsed))
	}

	if !strings.HasSuffix(got, "\n") {
		t.Error("JSON output missing trailing newline")
	}
}

func TestCSV(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "Test event", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	if len(lines) != 2 {
		t.Errorf("CSV produced %d lines, want 2; output:\n%s", len(lines), got)
	}

	wantHeader := "id,parent_id,created_at,author,text"
	if lines[0] != wantHeader {
		t.Errorf("CSV header = %q, want %q", lines[0], wantHeader)
	}
}

func TestCSV_SpecialChars(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, `text with "quotes" and, commas`, "2026-04-10", "nicolas"),
		makeEvent(2, nil, "=formula", "2026-04-10", "nicolas"),
	}

	var b bytes.Buffer
	if err := CSV(&b, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	got := b.String()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")

	if len(lines) != 3 {
		t.Fatalf("CSV produced %d lines, want 3; output:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[1], `"text with ""quotes"" and, commas"`) {
		t.Errorf("csv.Writer should quote/escape special chars, got line: %s", lines[1])
	}
	if !strings.Contains(lines[2], "=formula") {
		t.Errorf("expected raw =formula (no sanitization prefix), got line: %s", lines[2])
	}
}
```

- [ ] **Step 3: Run render package tests**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && go test -race ./internal/render/...`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "refactor: create internal/render package with Tree, Flat, JSON, CSV, Event"
```

---

### Task 5: Update `cmd/fngr/` to use new packages

**Files:**
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/add.go`
- Modify: `cmd/fngr/list.go`
- Modify: `cmd/fngr/show.go`
- Modify: `cmd/fngr/delete.go`
- Modify: `cmd/fngr/meta.go`

Update all imports and call sites in the CLI layer.

- [ ] **Step 1: Update `cmd/fngr/main.go`**

Replace import and call sites:
- `internal.ResolveDBPath` → `db.ResolvePath`
- `internal.OpenDB` → `db.Open`

```go
// Change import from:
"github.com/monolithiclab/fngr/internal"
// To:
"github.com/monolithiclab/fngr/internal/db"

// Change line 49:
dbPath, err := db.ResolvePath(cli.DB)

// Change line 55:
database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))

// Change variable name from db to database to avoid shadowing the package name.
// Update defer and ctx.Run accordingly:
defer database.Close()
err = ctx.Run(database)
```

Full updated file:

```go
package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
)

var version = "dev"

type CLI struct {
	DB string `help:"Path to database file." env:"FNGR_DB" type:"path"`

	Add    AddCmd    `cmd:"" help:"Add an event."`
	List   ListCmd   `cmd:"" help:"List events."`
	Show   ShowCmd   `cmd:"" help:"Show a single event."`
	Delete DeleteCmd `cmd:"" help:"Delete an event."`
	Meta   MetaCmd   `cmd:"" help:"List all metadata keys and values."`
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

func main() {
	username := currentUser()

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kong.Vars{
			"version": version,
			"USER":    username,
		},
		kong.UsageOnError(),
	)

	dbPath, err := db.ResolvePath(cli.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	err = ctx.Run(database)
	ctx.FatalIfErrorf(err)
}
```

- [ ] **Step 2: Update `cmd/fngr/add.go`**

Replace import and call sites:
- `internal.CollectMeta` → `event.CollectMeta`
- `internal.AddEvent` → `event.Add`

```go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
)

type AddCmd struct {
	Text   string   `arg:"" help:"Event text. Use @person and #tag for inline metadata extraction."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
	Time   string   `help:"Override event timestamp (ISO 8601, e.g. 2026-04-15T14:30:00)." short:"t"`
}

func (c *AddCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}
	if c.Text == "" {
		return fmt.Errorf("event text cannot be empty")
	}

	meta, err := event.CollectMeta(c.Text, c.Meta, c.Author)
	if err != nil {
		return err
	}

	var createdAt *time.Time
	if c.Time != "" {
		t, err := parseTime(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value %q: %w", c.Time, err)
		}
		createdAt = &t
	}

	id, err := event.Add(ctx, db, c.Text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}

	fmt.Printf("Added event %d\n", id)
	return nil
}

var timeFormats = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range timeFormats {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("supported formats: YYYY-MM-DD, YYYY-MM-DDTHH:MM, YYYY-MM-DDTHH:MM:SS, RFC3339")
}
```

- [ ] **Step 3: Update `cmd/fngr/list.go`**

```go
package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
)

type ListCmd struct {
	Filter string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
}

func (c *ListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	events, err := event.List(ctx, db, event.ListOpts{Filter: c.Filter, From: c.From, To: c.To})
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return render.JSON(os.Stdout, events)
	case "csv":
		return render.CSV(os.Stdout, events)
	case "flat":
		return render.Flat(os.Stdout, events)
	default:
		return render.Tree(os.Stdout, events)
	}
}
```

- [ ] **Step 4: Update `cmd/fngr/show.go`**

```go
package main

import (
	"context"
	"database/sql"
	"os"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
)

type ShowCmd struct {
	ID     int64  `arg:"" help:"Event ID."`
	Tree   bool   `help:"Show subtree." default:"false"`
	Format string `help:"Output format: text (default), json, csv." enum:"text,json,csv" default:"text"`
}

func (c *ShowCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	if c.Tree {
		events, err := event.GetSubtree(ctx, db, c.ID)
		if err != nil {
			return err
		}
		switch c.Format {
		case "json":
			return render.JSON(os.Stdout, events)
		case "csv":
			return render.CSV(os.Stdout, events)
		default:
			return render.Tree(os.Stdout, events)
		}
	}

	ev, err := event.Get(ctx, db, c.ID)
	if err != nil {
		return err
	}

	switch c.Format {
	case "json":
		return render.JSON(os.Stdout, []event.Event{*ev})
	case "csv":
		return render.CSV(os.Stdout, []event.Event{*ev})
	default:
		return render.Event(os.Stdout, ev)
	}
}
```

- [ ] **Step 5: Update `cmd/fngr/delete.go`**

```go
package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
)

type DeleteCmd struct {
	ID        int64 `arg:"" help:"Event ID."`
	Force     bool  `help:"Skip confirmation prompt." short:"f"`
	Recursive bool  `help:"Delete event and all children." short:"r"`
}

func (c *DeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	ev, err := event.Get(ctx, db, c.ID)
	if err != nil {
		return err
	}

	hasChildren, err := event.HasChildren(ctx, db, c.ID)
	if err != nil {
		return err
	}

	if hasChildren && !c.Recursive {
		return fmt.Errorf("event %d has child events; use -r to delete recursively", c.ID)
	}

	if !c.Force {
		if hasChildren {
			fmt.Printf("Delete event %d and all its children? [Y/n] ", ev.ID)
		} else {
			fmt.Printf("Delete event %d? [Y/n] ", ev.ID)
		}
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	if err := event.Delete(ctx, db, c.ID); err != nil {
		return err
	}
	fmt.Printf("Deleted event %d\n", c.ID)
	return nil
}
```

- [ ] **Step 6: Update `cmd/fngr/meta.go`**

```go
package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/monolithiclab/fngr/internal/event"
)

type MetaCmd struct {
	List   MetaListCmd   `cmd:"" default:"withargs" help:"List all metadata keys and values."`
	Update MetaUpdateCmd `cmd:"" help:"Rename a metadata key=value pair."`
	Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata key=value pair."`
}

type MetaListCmd struct{}

func (c *MetaListCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	counts, err := event.ListMeta(ctx, db)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		fmt.Println("No metadata found.")
		return nil
	}

	maxKey, maxVal := 0, 0
	for _, mc := range counts {
		if len(mc.Key) > maxKey {
			maxKey = len(mc.Key)
		}
		if len(mc.Value) > maxVal {
			maxVal = len(mc.Value)
		}
	}

	for _, mc := range counts {
		fmt.Fprintf(os.Stdout, "%-*s=%-*s  (%d)\n", maxKey, mc.Key, maxVal, mc.Value, mc.Count)
	}

	return nil
}

type MetaUpdateCmd struct {
	Old   string `arg:"" help:"Old key=value pair."`
	New   string `arg:"" help:"New key=value pair."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaUpdateCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	oldKey, oldValue, ok := strings.Cut(c.Old, "=")
	if !ok {
		return fmt.Errorf("invalid old meta %q: expected key=value", c.Old)
	}
	newKey, newValue, ok := strings.Cut(c.New, "=")
	if !ok {
		return fmt.Errorf("invalid new meta %q: expected key=value", c.New)
	}

	affected, err := event.UpdateMeta(ctx, db, oldKey, oldValue, newKey, newValue)
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("no metadata matching %s=%s", oldKey, oldValue)
	}

	if !c.Force {
		fmt.Printf("Update %d occurrence(s) of %s=%s to %s=%s? [Y/n] ", affected, oldKey, oldValue, newKey, newValue)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	affected, err = event.UpdateMeta(ctx, db, oldKey, oldValue, newKey, newValue)
	if err != nil {
		return err
	}

	fmt.Printf("Updated %d occurrence(s)\n", affected)
	return nil
}

type MetaDeleteCmd struct {
	Meta  string `arg:"" help:"Metadata key=value to delete."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaDeleteCmd) Run(db *sql.DB) error {
	ctx := context.Background()

	key, value, ok := strings.Cut(c.Meta, "=")
	if !ok {
		return fmt.Errorf("invalid meta %q: expected key=value", c.Meta)
	}

	counts, err := event.ListMeta(ctx, db)
	if err != nil {
		return err
	}
	var affected int
	for _, mc := range counts {
		if mc.Key == key && mc.Value == value {
			affected = mc.Count
			break
		}
	}
	if affected == 0 {
		return fmt.Errorf("no metadata matching %s=%s", key, value)
	}

	if !c.Force {
		fmt.Printf("Delete %d occurrence(s) of %s=%s? [Y/n] ", affected, key, value)
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "" && answer != "y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	n, err := event.DeleteMeta(ctx, db, key, value)
	if err != nil {
		return err
	}

	fmt.Printf("Deleted %d occurrence(s)\n", n)
	return nil
}
```

- [ ] **Step 7: Run all new package tests**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && go test -race ./internal/db/... ./internal/parse/... ./internal/event/... ./internal/render/...`
Expected: All tests PASS.

- [ ] **Step 8: Build the binary**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && make build`
Expected: Binary builds successfully at `build/fngr`.

- [ ] **Step 9: Commit**

```bash
git add cmd/fngr/main.go cmd/fngr/add.go cmd/fngr/list.go cmd/fngr/show.go cmd/fngr/delete.go cmd/fngr/meta.go
git commit -m "refactor: update cmd/fngr to use new internal sub-packages"
```

---

### Task 6: Delete old `internal/` files

**Files:**
- Delete: `internal/db.go`
- Delete: `internal/meta.go`
- Delete: `internal/filter.go`
- Delete: `internal/renderer.go`
- Delete: `internal/event.go`
- Delete: `internal/db_test.go`
- Delete: `internal/meta_test.go`
- Delete: `internal/filter_test.go`
- Delete: `internal/renderer_test.go`
- Delete: `internal/event_test.go`

- [ ] **Step 1: Delete all old internal files**

```bash
git rm internal/db.go internal/meta.go internal/filter.go internal/renderer.go internal/event.go internal/db_test.go internal/meta_test.go internal/filter_test.go internal/renderer_test.go internal/event_test.go
```

- [ ] **Step 2: Run full test suite**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && make test`
Expected: All tests PASS with coverage report.

- [ ] **Step 3: Run linter**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && make lint`
Expected: No lint errors.

- [ ] **Step 4: Build**

Run: `cd /Users/nicolasm/Work/Monolithic/repositories/fngr && make build`
Expected: Binary builds successfully.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: remove old flat internal/ package files"
```

---

### Task 7: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

Update the Architecture section to reflect the new package structure.

- [ ] **Step 1: Update CLAUDE.md Architecture section**

Replace the Architecture section with:

```markdown
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
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md architecture for new package structure"
```
