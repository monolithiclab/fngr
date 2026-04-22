package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

var ErrNotFound = errors.New("not found")

// ErrCycle is returned when Reparent would introduce a cycle (including
// the self-parent case).
var ErrCycle = errors.New("would create a parent cycle")

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
			parentID, text, createdAt.UTC().Format(timefmt.DateTimeFormat),
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

	if err := requireEventExists(ctx, tx, id); err != nil {
		return err
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

	if err := requireEventExists(ctx, tx, id); err != nil {
		return err
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

	if err := requireEventExists(ctx, tx, id); err != nil {
		return 0, err
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

func readMetaTx(ctx context.Context, tx *sql.Tx, id int64) ([]parse.Meta, error) {
	rows, err := tx.QueryContext(ctx, "SELECT key, value FROM event_meta WHERE event_id = ? ORDER BY key, value", id)
	if err != nil {
		return nil, fmt.Errorf("query meta: %w", err)
	}
	defer rows.Close()

	var meta []parse.Meta
	for rows.Next() {
		var m parse.Meta
		if err := rows.Scan(&m.Key, &m.Value); err != nil {
			return nil, fmt.Errorf("scan meta: %w", err)
		}
		meta = append(meta, m)
	}
	return meta, rows.Err()
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

// requireEventExists returns ErrNotFound (wrapped) if id has no row in
// events. Used by every event-mutation function so missing rows surface
// the same sentinel before any other work begins.
func requireEventExists(ctx context.Context, tx *sql.Tx, id int64) error {
	var dummy int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", id).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("event %d: %w", id, ErrNotFound)
		}
		return fmt.Errorf("query event: %w", err)
	}
	return nil
}

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

func CountMeta(ctx context.Context, db *sql.DB, key, value string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count meta: %w", err)
	}
	return n, nil
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
	Filter    string
	From      *time.Time // inclusive lower bound
	To        *time.Time // exclusive upper bound (compute end-of-day in caller)
	Limit     int        // 0 means no limit
	Ascending bool       // oldest first when true; default is newest first
}

// ListSeq yields events matching opts one at a time, accumulating
// metaBatchSize rows from the events query, loading their metadata in one
// batched lookup, then yielding. Peak in-flight memory is one batch.
//
// The second yielded value is the first error encountered; iteration stops
// after an error is yielded.
func ListSeq(ctx context.Context, db *sql.DB, opts ListOpts) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		query, args := buildListQuery(opts)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			yield(Event{}, fmt.Errorf("query events: %w", err))
			return
		}
		defer rows.Close()

		batch := make([]Event, 0, metaBatchSize)
		flush := func() bool {
			if len(batch) == 0 {
				return true
			}
			if err := loadMetaBatch(ctx, db, batch); err != nil {
				yield(Event{}, err)
				return false
			}
			for _, ev := range batch {
				if !yield(ev, nil) {
					return false
				}
			}
			batch = batch[:0]
			return true
		}

		for rows.Next() {
			var e Event
			var parentID sql.NullInt64
			if err := rows.Scan(&e.ID, &parentID, &e.Text, &e.CreatedAt); err != nil {
				yield(Event{}, fmt.Errorf("scan event: %w", err))
				return
			}
			if parentID.Valid {
				e.ParentID = &parentID.Int64
			}
			batch = append(batch, e)
			if len(batch) >= metaBatchSize {
				if !flush() {
					return
				}
			}
		}
		if err := rows.Err(); err != nil {
			yield(Event{}, fmt.Errorf("iterate events: %w", err))
			return
		}
		flush()
	}
}

// List collects every event from ListSeq. Use ListSeq directly when you can
// stream (flat/csv/json renderers); use List when you genuinely need the
// full slice in memory (tree topology, GetSubtree).
func List(ctx context.Context, db *sql.DB, opts ListOpts) ([]Event, error) {
	var out []Event
	for ev, err := range ListSeq(ctx, db, opts) {
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func buildListQuery(opts ListOpts) (string, []any) {
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

	if opts.From != nil {
		query += " AND e.created_at >= ?"
		args = append(args, opts.From.UTC().Format(timefmt.DateTimeFormat))
	}
	if opts.To != nil {
		query += " AND e.created_at < ?"
		args = append(args, opts.To.UTC().Format(timefmt.DateTimeFormat))
	}

	if opts.Ascending {
		query += " ORDER BY e.created_at ASC"
	} else {
		query += " ORDER BY e.created_at DESC"
	}
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	return query, args
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

// metaBatchSize keeps the IN clause well under SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER (historically 999) so that loading metadata for
// large result sets cannot fail at runtime.
const metaBatchSize = 500

func loadMetaBatch(ctx context.Context, db *sql.DB, events []Event) error {
	idIdx := make(map[int64]int, len(events))
	for i, e := range events {
		idIdx[e.ID] = i
	}

	for start := 0; start < len(events); start += metaBatchSize {
		end := min(start+metaBatchSize, len(events))
		if err := loadMetaChunk(ctx, db, events, idIdx, start, end); err != nil {
			return err
		}
	}
	return nil
}

func loadMetaChunk(ctx context.Context, db *sql.DB, events []Event, idIdx map[int64]int, start, end int) error {
	ids := make([]any, end-start)
	for i, e := range events[start:end] {
		ids[i] = e.ID
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
