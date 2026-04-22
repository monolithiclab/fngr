package internal

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// testDB opens an in-memory SQLite database with foreign keys enabled
// and registers cleanup to close it when the test finishes.
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

func TestInitSchema_CreatesAllTables(t *testing.T) {
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
	db := testDB(t)

	if err := initSchema(db); err != nil {
		t.Fatalf("first initSchema call: %v", err)
	}
	if err := initSchema(db); err != nil {
		t.Fatalf("second initSchema call: %v", err)
	}
}

func TestCascadeDelete_RemovesChildrenAndMeta(t *testing.T) {
	db := testDB(t)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	// Insert parent event.
	res, err := db.Exec("INSERT INTO events (text) VALUES (?)", "parent event")
	if err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	parentID, _ := res.LastInsertId()

	// Insert child event referencing parent.
	res, err = db.Exec("INSERT INTO events (parent_id, text) VALUES (?, ?)", parentID, "child event")
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}
	childID, _ := res.LastInsertId()

	// Insert metadata for both parent and child.
	if _, err := db.Exec("INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)", parentID, "author", "alice"); err != nil {
		t.Fatalf("insert parent meta: %v", err)
	}
	if _, err := db.Exec("INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)", childID, "tag", "important"); err != nil {
		t.Fatalf("insert child meta: %v", err)
	}

	// Delete parent; cascade should remove child and all metadata.
	if _, err := db.Exec("DELETE FROM events WHERE id = ?", parentID); err != nil {
		t.Fatalf("delete parent: %v", err)
	}

	// Verify child event is gone.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", childID).Scan(&count); err != nil {
		t.Fatalf("count child: %v", err)
	}
	if count != 0 {
		t.Errorf("expected child event to be deleted, got count=%d", count)
	}

	// Verify all metadata is gone.
	if err := db.QueryRow("SELECT COUNT(*) FROM event_meta").Scan(&count); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if count != 0 {
		t.Errorf("expected all metadata to be deleted, got count=%d", count)
	}
}

func TestFTSDeleteTrigger(t *testing.T) {
	db := testDB(t)

	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	// Insert an event.
	res, err := db.Exec("INSERT INTO events (text) VALUES (?)", "searchable event")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	eventID, _ := res.LastInsertId()

	// Manually add FTS content with matching rowid.
	if _, err := db.Exec("INSERT INTO events_fts (rowid, content) VALUES (?, ?)", eventID, "searchable event"); err != nil {
		t.Fatalf("insert FTS content: %v", err)
	}

	// Verify FTS content exists.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM events_fts WHERE rowid = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("count FTS before delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 FTS row before delete, got %d", count)
	}

	// Delete the event; trigger should remove FTS row.
	if _, err := db.Exec("DELETE FROM events WHERE id = ?", eventID); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	// Verify FTS content is gone.
	if err := db.QueryRow("SELECT COUNT(*) FROM events_fts WHERE rowid = ?", eventID).Scan(&count); err != nil {
		t.Fatalf("count FTS after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected FTS row to be deleted, got count=%d", count)
	}
}
