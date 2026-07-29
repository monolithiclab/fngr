package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
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
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// migrationSQL returns the SQL body for the migration at the given 1-based
// index. It re-invokes loadMigrations each call so the io.Reader is fresh.
func migrationSQL(t *testing.T, index int) string {
	t.Helper()
	migrations := loadMigrations()
	body, err := io.ReadAll(migrations[index].up)
	if err != nil {
		t.Fatalf("load migration %d: %v", index, err)
	}
	return string(body)
}

func TestMigrate_CreatesAllTables(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
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

func TestMigrate_Idempotent(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	for i := range 3 {
		if err := migrate(db); err != nil {
			t.Fatalf("migrate call %d: %v", i, err)
		}
	}
}

func TestMigrate_BumpsUserVersion(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	v, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	migrations := loadMigrations()
	want := migrations[len(migrations)-1].version
	if v != want {
		t.Errorf("user_version = %d, want %d", v, want)
	}
}

func TestMigrate_DetectsLegacyV1(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	// Simulate a database created before the migration framework: schema
	// matches v1 but PRAGMA user_version is still 0.
	if _, err := db.Exec(migrationSQL(t, 0)); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := setUserVersion(db, 0); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	v, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	migrations := loadMigrations()
	want := migrations[len(migrations)-1].version
	if v != want {
		t.Errorf("legacy db user_version = %d, want %d", v, want)
	}
}

func TestCascadeDelete_RemovesChildrenAndMeta(t *testing.T) {
	t.Parallel()
	db := testDBWithSchema(t)

	res, err := db.Exec("INSERT INTO events (title) VALUES (?)", "parent event")
	if err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	parentID, _ := res.LastInsertId()

	res, err = db.Exec("INSERT INTO events (parent_id, title) VALUES (?, ?)", parentID, "child event")
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

	res, err := db.Exec("INSERT INTO events (title) VALUES (?)", "searchable event")
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

func TestMigrate_V2DedupesAndAddsUnique(t *testing.T) {
	t.Parallel()
	db := testDB(t)

	// Bring the schema up to v1 only.
	if _, err := db.Exec(migrationSQL(t, 0)); err != nil {
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
	migrations := loadMigrations()
	want := migrations[len(migrations)-1].version
	if v != want {
		t.Errorf("user_version = %d, want %d", v, want)
	}
}

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
		{"   . body", "", "body"},           // leading whitespace + leading separator → empty title, trimmed body
		{"hello.  world", "hello", "world"}, // dot + double space → trimmed body (no leading space)
		{"   hello   ", "hello", ""},        // no separator, surrounding whitespace → trimmed title, empty body
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

// TestOpen_PragmasApplyToEveryPooledConnection is the regression test for the
// pool-scope half of the DSN fix: a post-open db.Exec configures only the one
// connection it lands on. Holding connections open forces the pool to
// establish fresh ones. See dsn() in db.go.
func TestOpen_PragmasApplyToEveryPooledConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	database, err := Open(filepath.Join(t.TempDir(), "pool.db"), true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	want := []struct{ pragma, value string }{
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
		{"journal_mode", "wal"},
		{"synchronous", "1"},
	}

	// Each iteration keeps the previous connections open, so the pool cannot
	// hand back the one Open already configured.
	for i := range 4 {
		c, err := database.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		defer c.Close()

		for _, w := range want {
			var got string
			if err := c.QueryRowContext(ctx, "PRAGMA "+w.pragma).Scan(&got); err != nil {
				t.Fatalf("conn %d: PRAGMA %s: %v", i, w.pragma, err)
			}
			if got != w.value {
				t.Errorf("conn %d: %s = %q, want %q", i, w.pragma, got, w.value)
			}
		}
	}
}

// TestOpen_ConcurrentWritersAllSucceed guards the user-visible symptom: with
// busy_timeout missing on the pool's later connections, parallel writers fail
// instantly with SQLITE_BUSY and their events are lost without a trace.
func TestOpen_ConcurrentWritersAllSucceed(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")

	database, err := Open(dbPath, true)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	const writers = 10
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			_, err := database.Exec(
				"INSERT INTO events (title, body, created_at) VALUES (?, '', '2026-01-01 00:00:00')",
				fmt.Sprintf("event %d", i),
			)
			if err != nil {
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent insert: %v", err)
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != writers {
		t.Errorf("stored %d events, want %d", count, writers)
	}
}

// TestOpen_PathWithURISpecialChars covers the escaping half of dsn(): the
// driver opens with SQLITE_OPEN_URI, so an unescaped '?' or '#' would
// truncate the filename and '%' would start an escape sequence.
func TestOpen_PathWithURISpecialChars(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	for _, name := range []string{"a?b.db", "c#d.db", "e%f.db", "g h.db"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(dir, name)

			database, err := Open(dbPath, true)
			if err != nil {
				t.Fatalf("Open(%q): %v", dbPath, err)
			}
			defer database.Close()

			if _, err := database.Exec("INSERT INTO events (title) VALUES ('x')"); err != nil {
				t.Fatalf("insert: %v", err)
			}
			if _, err := os.Stat(dbPath); err != nil {
				t.Errorf("expected database at %q: %v", dbPath, err)
			}
		})
	}
}
