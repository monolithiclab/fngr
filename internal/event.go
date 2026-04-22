package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("not found")

type Event struct {
	ID        int64
	ParentID  *int64
	Text      string
	CreatedAt time.Time
	Meta      []Meta
}

type MetaCount struct {
	Key   string
	Value string
	Count int
}

func AddEvent(ctx context.Context, db *sql.DB, text string, parentID *int64, meta []Meta, createdAt *time.Time) (int64, error) {
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
			parentID, text, createdAt.Format("2006-01-02 15:04:05"),
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

	ftsContent := BuildFTSContent(text, meta)
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

func GetEvent(ctx context.Context, db *sql.DB, id int64) (*Event, error) {
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

func DeleteEvent(ctx context.Context, db *sql.DB, id int64) error {
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

func ListEvents(ctx context.Context, db *sql.DB, opts ListOpts) ([]Event, error) {
	var query string
	var args []any

	if opts.Filter != "" {
		matchExpr := PreprocessFilter(opts.Filter)
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
		var m Meta
		if err := rows.Scan(&eventID, &m.Key, &m.Value); err != nil {
			return fmt.Errorf("scan meta: %w", err)
		}
		if idx, ok := idIdx[eventID]; ok {
			events[idx].Meta = append(events[idx].Meta, m)
		}
	}
	return rows.Err()
}
