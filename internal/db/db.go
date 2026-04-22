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
