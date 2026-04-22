package db

import (
	"database/sql"
	"fmt"
)

// migration is a single forward step from version N-1 to N.
type migration struct {
	version int
	up      string
}

// migrations are applied in order; never edit a published migration. Add new
// versions at the bottom.
var migrations = []migration{
	{
		version: 1,
		up: `
			CREATE TABLE events (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				parent_id  INTEGER REFERENCES events(id) ON DELETE CASCADE,
				text       TEXT NOT NULL,
				created_at DATETIME NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE event_meta (
				event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
				key      TEXT NOT NULL,
				value    TEXT NOT NULL
			);

			CREATE INDEX idx_events_parent_id ON events(parent_id);
			CREATE INDEX idx_events_created_at ON events(created_at);

			CREATE INDEX idx_event_meta_key_value ON event_meta(key, value);
			CREATE INDEX idx_event_meta_event_id ON event_meta(event_id, key, value);

			CREATE VIRTUAL TABLE events_fts USING fts5(
				content,
				tokenize = "unicode61 tokenchars '=/'"
			);

			CREATE TRIGGER trg_events_fts_delete
			AFTER DELETE ON events
			BEGIN
				DELETE FROM events_fts WHERE rowid = OLD.id;
			END;
		`,
	},
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
}

// migrate applies any pending migrations. Pre-migration databases (created
// before this code shipped) carry the v1 schema with user_version still at 0;
// detect that case and bump the version so we don't try to recreate tables
// that already exist.
func migrate(db *sql.DB) error {
	current, err := userVersion(db)
	if err != nil {
		return err
	}

	if current == 0 {
		legacy, err := hasLegacyV1Schema(db)
		if err != nil {
			return err
		}
		if legacy {
			if err := setUserVersion(db, 1); err != nil {
				return err
			}
			current = 1
		}
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
	}
	return nil
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

func setUserVersion(db *sql.DB, v int) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil { // #nosec G201 -- v is an internal int constant
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

func hasLegacyV1Schema(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'",
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("probe legacy schema: %w", err)
	}
	return n == 1, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(m.up); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil { // #nosec G201 -- m.version is an internal int constant
		return fmt.Errorf("bump user_version: %w", err)
	}
	return tx.Commit()
}
