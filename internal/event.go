package internal

import (
	"database/sql"
	"fmt"
	"time"
)

// Event represents a journal entry with optional parent linkage and metadata.
type Event struct {
	ID        int64
	ParentID  *int64
	Text      string
	CreatedAt time.Time
	Meta      []Meta
}

// MetaCount represents a metadata key-value pair with its occurrence count.
type MetaCount struct {
	Key   string
	Value string
	Count int
}

// AddEvent inserts a new event with metadata and FTS content within a transaction.
// If parentID is non-nil, it validates the parent exists before inserting.
func AddEvent(db *sql.DB, text string, parentID *int64, meta []Meta) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Validate parent exists if specified.
	if parentID != nil {
		var exists int
		err := tx.QueryRow("SELECT 1 FROM events WHERE id = ?", *parentID).Scan(&exists)
		if err != nil {
			return 0, fmt.Errorf("parent event %d not found", *parentID)
		}
	}

	// Insert the event.
	res, err := tx.Exec(
		"INSERT INTO events (parent_id, text) VALUES (?, ?)",
		parentID, text,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}

	// Insert metadata rows.
	for _, m := range meta {
		if _, err := tx.Exec(
			"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)",
			id, m.Key, m.Value,
		); err != nil {
			return 0, fmt.Errorf("insert meta: %w", err)
		}
	}

	// Insert FTS content.
	ftsContent := BuildFTSContent(text, meta)
	if _, err := tx.Exec(
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

// GetEvent retrieves an event by ID including its metadata.
func GetEvent(db *sql.DB, id int64) (*Event, error) {
	e := &Event{}
	var parentID sql.NullInt64

	err := db.QueryRow(
		"SELECT id, parent_id, text, created_at FROM events WHERE id = ?", id,
	).Scan(&e.ID, &parentID, &e.Text, &e.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("event %d not found", id)
		}
		return nil, fmt.Errorf("query event: %w", err)
	}

	if parentID.Valid {
		e.ParentID = &parentID.Int64
	}

	meta, err := loadMeta(db, id)
	if err != nil {
		return nil, err
	}
	e.Meta = meta

	return e, nil
}

// DeleteEvent removes an event by ID. Returns an error if the event does not exist.
// Child events and metadata are cascade-deleted via FK constraints.
// FTS cleanup is handled by the database trigger.
func DeleteEvent(db *sql.DB, id int64) error {
	res, err := db.Exec("DELETE FROM events WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete event: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("event %d not found", id)
	}

	return nil
}

// ListMeta returns all distinct metadata key-value pairs with their occurrence
// counts, ordered by key then value.
func ListMeta(db *sql.DB) ([]MetaCount, error) {
	rows, err := db.Query(
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

// loadMeta queries all metadata for a given event ID, ordered by key then value.
func loadMeta(db *sql.DB, eventID int64) ([]Meta, error) {
	rows, err := db.Query(
		"SELECT key, value FROM event_meta WHERE event_id = ? ORDER BY key, value",
		eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("query meta: %w", err)
	}
	defer rows.Close()

	var result []Meta
	for rows.Next() {
		var m Meta
		if err := rows.Scan(&m.Key, &m.Value); err != nil {
			return nil, fmt.Errorf("scan meta: %w", err)
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta: %w", err)
	}

	return result, nil
}
