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
