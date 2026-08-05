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

// ErrNotFound is returned when an operation targets an event ID (or a
// (key, value) meta tuple) that does not exist.
var ErrNotFound = errors.New("not found")

// ErrCycle is returned when Reparent would introduce a cycle (including
// the self-parent case).
var ErrCycle = errors.New("would create a parent cycle")

// ErrTimeRange is returned when a timestamp cannot be represented in the
// storage format.
var ErrTimeRange = errors.New("timestamp out of range")

// Event is a single journal entry as stored, complete with its parsed
// metadata. CreatedAt is in UTC at the SQL boundary; renderers convert
// to local time for display.
type Event struct {
	ID        int64
	ParentID  *int64
	Title     string
	Body      string
	CreatedAt time.Time
	Meta      []parse.Meta
}

// MetaCount is one row of a `fngr meta` summary: a (key, value) tuple
// and how many events it appears on.
type MetaCount struct {
	Key   string
	Value string
	Count int
}

// AddInput holds the fields needed to insert one event. Used by both
// AddMany and the single-event Add.
type AddInput struct {
	Title string
	Body  string
	// ParentID names the parent by its id in this database.
	ParentID *int64
	// ParentIndex names the parent by its position in the same AddMany
	// batch, for callers importing a tree whose ids don't exist here yet
	// (see cmd/fngr/add_json.go). The two are mutually exclusive, and the
	// referenced record may appear later in the batch — `fngr --format=json`
	// emits newest-first, so children routinely precede their parents.
	ParentIndex *int
	Meta        []parse.Meta
	CreatedAt   *time.Time
}

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

// AddMany inserts the given events in a single transaction. Empty input
// is a no-op returning (nil, nil). Any per-record error rolls the entire
// batch back. Returns generated IDs in input order.
func AddMany(ctx context.Context, db *sql.DB, inputs []AddInput) ([]int64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	ids, err := addInTx(ctx, tx, inputs)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return ids, nil
}

// addInTx inserts events using the given tx. Caller owns commit/rollback.
// Each input becomes one INSERT into events, zero-or-more INSERTs into
// event_meta, and one INSERT into events_fts. Per-record errors abort
// the loop with a wrapped error; the caller's deferred Rollback fires.
func addInTx(ctx context.Context, tx *sql.Tx, inputs []AddInput) ([]int64, error) {
	if err := validateParentIndexes(inputs); err != nil {
		return nil, err
	}

	insertMeta, err := tx.PrepareContext(ctx,
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (?, ?, ?, ?)",
	)
	if err != nil {
		return nil, fmt.Errorf("prepare meta insert: %w", err)
	}
	defer insertMeta.Close()

	insertFTS, err := tx.PrepareContext(ctx,
		"INSERT INTO events_fts (rowid, content) VALUES (?, ?)",
	)
	if err != nil {
		return nil, fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer insertFTS.Close()

	ids := make([]int64, 0, len(inputs))
	for _, in := range inputs {
		if in.ParentID != nil {
			var exists int
			err := tx.QueryRowContext(ctx, "SELECT 1 FROM events WHERE id = ?", *in.ParentID).Scan(&exists)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("parent event %d: %w", *in.ParentID, ErrNotFound)
				}
				return nil, fmt.Errorf("query parent event: %w", err)
			}
		}

		if in.Title == "" {
			return nil, fmt.Errorf("title cannot be empty")
		}
		if err := requireOneAuthor(in.Meta); err != nil {
			return nil, err
		}

		var res sql.Result
		if in.CreatedAt != nil {
			createdAt, ferr := formatTimestamp(*in.CreatedAt)
			if ferr != nil {
				return nil, ferr
			}
			res, err = tx.ExecContext(ctx,
				"INSERT INTO events (parent_id, title, body, created_at) VALUES (?, ?, ?, ?)",
				in.ParentID, in.Title, in.Body, createdAt,
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

		// in.Meta arrives already merged, so provenance is recovered by
		// re-deriving the body tags rather than carried alongside it. A tuple
		// the text yields is recorded as body-derived even when `--meta` named
		// it too: the two sources agreed, and the live one is the text. That
		// is the same tie-break migration 5's back-fill makes, and it keeps a
		// `fngr --format=json | fngr add -f json` round trip from freezing
		// every body tag as explicit — `event tag` promotes any tuple whose
		// claim should outlive the mention.
		fromBody := metaSet(parse.BodyTags(parse.EventText(in.Title, in.Body)))
		for _, m := range in.Meta {
			source := metaSourceExplicit
			if _, ok := fromBody[m]; ok {
				source = metaSourceBody
			}
			if _, err := insertMeta.ExecContext(ctx, id, m.Key, m.Value, source); err != nil {
				return nil, fmt.Errorf("insert meta: %w", err)
			}
		}

		if _, err := insertFTS.ExecContext(ctx, id, parse.FTSContent(in.Title, in.Body, in.Meta)); err != nil {
			return nil, fmt.Errorf("insert FTS content: %w", err)
		}

		ids = append(ids, id)
	}

	// Batch-relative parents are wired up only now that every row exists,
	// because a child may be inserted before its parent.
	for i, in := range inputs {
		if in.ParentIndex == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE events SET parent_id = ? WHERE id = ?", ids[*in.ParentIndex], ids[i],
		); err != nil {
			return nil, fmt.Errorf("link event %d to its batch parent: %w", ids[i], err)
		}
	}
	return ids, nil
}

// validateParentIndexes rejects batch-relative parents that are out of range,
// combined with an explicit ParentID, or part of a cycle. It runs before any
// INSERT because the UPDATE pass that applies them cannot fail on a cycle the
// way an INSERT would — SQLite's foreign key only checks that the parent row
// exists, and in a cycle every row does. An unchecked cycle would commit a
// clump of events unreachable from any root.
func validateParentIndexes(inputs []AddInput) error {
	for i, in := range inputs {
		switch {
		case in.ParentIndex == nil:
		case in.ParentID != nil:
			return fmt.Errorf("record %d: ParentID and ParentIndex are mutually exclusive", i)
		case *in.ParentIndex < 0 || *in.ParentIndex >= len(inputs):
			return fmt.Errorf("record %d: parent index %d out of range", i, *in.ParentIndex)
		}
	}

	// Each record has at most one parent, so the graph is a forest plus
	// possible cycles. Walk from every node, marking nodes already known to
	// terminate, which keeps the whole scan linear.
	const (
		unvisited = iota
		onPath
		safe
	)
	state := make([]int8, len(inputs))
	var path []int
	for start := range inputs {
		path = path[:0]
		for i := start; state[i] == unvisited; {
			state[i] = onPath
			path = append(path, i)
			if inputs[i].ParentIndex == nil {
				break
			}
			i = *inputs[i].ParentIndex
			if state[i] == onPath {
				return fmt.Errorf("record %d: parent index cycle (the record is its own ancestor)", i)
			}
		}
		for _, i := range path {
			state[i] = safe
		}
	}
	return nil
}

// Get returns the event with the given id, including its meta tuples.
// Returns ErrNotFound when no such event exists.
func Get(ctx context.Context, db *sql.DB, id int64) (*Event, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, parent_id, title, body, created_at FROM events WHERE id = ?", id,
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

// Delete removes the event with the given id. Cascades through the
// schema's foreign keys to event_meta and events_fts, and to any child
// events whose parent_id points to id. Returns ErrNotFound when no such
// event exists.
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

// Update mutates an existing event's title, body, and/or createdAt
// timestamp. Any field with a nil pointer is left untouched. When
// either title or body changes, body-derived tags (`@person`,
// `#tag`) are synced — tags present in the old text but not the new
// are removed, the new set is inserted via ON CONFLICT DO NOTHING —
// and the FTS row is rebuilt. Empty title is rejected;
// empty body clears it. Returns ErrNotFound when no such event
// exists.
func Update(ctx context.Context, db *sql.DB, id int64, title, body *string, createdAt *time.Time) error {
	if title == nil && body == nil && createdAt == nil {
		return nil
	}
	if title != nil && *title == "" {
		return fmt.Errorf("title cannot be empty")
	}
	var stamp string
	if createdAt != nil {
		var err error
		if stamp, err = formatTimestamp(*createdAt); err != nil {
			return err
		}
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

	// The source = 'body' filter on the delete is what carries the semantics:
	// it stops `event tag @bob` from being collateral damage the first time an
	// edit drops an inline @bob. Deleting only the delta against the old text
	// is an optimisation on top — end state is the same either way, since a
	// surviving tuple would just be re-inserted — worth it because most edits
	// touch none of the tags and would otherwise rewrite every row.
	var removedTags, newBodyTags []parse.Meta
	if textChanged {
		var oldTitle, oldBody string
		if err := tx.QueryRowContext(ctx,
			"SELECT title, body FROM events WHERE id = ?", id,
		).Scan(&oldTitle, &oldBody); err != nil {
			return fmt.Errorf("query event title/body: %w", err)
		}
		newTitle, newBody := oldTitle, oldBody
		if title != nil {
			newTitle = *title
		}
		if body != nil {
			newBody = *body
		}
		newBodyTags = parse.BodyTags(parse.EventText(newTitle, newBody))
		removedTags = subtractMeta(parse.BodyTags(parse.EventText(oldTitle, oldBody)), newBodyTags)
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
		args = append(args, stamp)
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, "UPDATE events SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil { // #nosec G202 -- sets is built from a fixed allow-list
		return fmt.Errorf("update event: %w", err)
	}

	if textChanged {
		if err := execBodyMetaTuples(ctx, tx, deleteBodyMetaSQL, "delete", id, removedTags); err != nil {
			return err
		}
		if err := execBodyMetaTuples(ctx, tx, insertBodyMetaSQL, "insert", id, newBodyTags); err != nil {
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

// AddTags inserts the given meta entries for event id as explicit metadata:
// a tag named on the command line is the operator's, and a later body edit
// that happens to drop the same `@name` must not retract it, so a row already
// present as body-derived is promoted rather than left alone. Returns the
// number of rows that did not already exist, so callers can report "M added,
// K already present" — counted from a pre-read rather than RowsAffected,
// which cannot tell an insert from that promotion. FTS is rebuilt in the same
// transaction. Returns ErrNotFound if the event is missing. Empty `tags` is a
// no-op returning (0, nil). Protected keys (`author`) are refused outright —
// see protectedMetaKeys.
func AddTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	if err := requireUnprotectedTags("add", tags); err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireEventExists(ctx, tx, id); err != nil {
		return 0, err
	}

	existing, err := readMetaTx(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	present := metaSet(existing)

	stmt, err := tx.PrepareContext(ctx,
		// The DO UPDATE is guarded so it fires only for the promotion it
		// exists for; an already-explicit row is left untouched rather than
		// rewritten to the value it already holds.
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (?, ?, ?, ?)"+
			" ON CONFLICT DO UPDATE SET source = excluded.source"+
			" WHERE event_meta.source <> excluded.source",
	)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	var added int64
	for _, m := range tags {
		if _, dup := present[m]; !dup {
			present[m] = struct{}{}
			added++
		}
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value, metaSourceExplicit); err != nil {
			return 0, fmt.Errorf("insert tag: %w", err)
		}
	}

	if err := rebuildEventFTS(ctx, tx, id); err != nil {
		return 0, err
	}

	return added, tx.Commit()
}

// RemoveTags deletes (event_id, key, value) rows matching tags. Returns
// the number of rows removed. FTS rebuilt in the same transaction.
// Returns ErrNotFound if the event is missing; (0, nil) is a valid
// outcome when none of the tags were present. Empty tags is a no-op.
// Protected keys (`author`) are refused outright, matching DeleteMeta —
// an event with no author renders a blank column everywhere.
func RemoveTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	if err := requireUnprotectedTags("remove", tags); err != nil {
		return 0, err
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

// The two halves of Update's body-tag sync. Both are scoped to
// metaSourceBody, which is the point: the delete leaves an operator-added
// tuple of the same key and value standing, and the insert's DO NOTHING
// leaves it explicit rather than demoting it to a mention.
const (
	deleteBodyMetaSQL = "DELETE FROM event_meta" +
		" WHERE event_id = ? AND key = ? AND value = ? AND source = ?"
	insertBodyMetaSQL = "INSERT INTO event_meta (event_id, key, value, source)" +
		" VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING"
)

// execBodyMetaTuples runs query once per tag with (id, key, value, 'body')
// bound. verb names the operation in any error. Empty tags is a no-op.
func execBodyMetaTuples(ctx context.Context, tx *sql.Tx, query, verb string, id int64, tags []parse.Meta) error {
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", verb, err)
	}
	defer stmt.Close()
	for _, m := range tags {
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value, metaSourceBody); err != nil {
			return fmt.Errorf("%s meta: %w", verb, err)
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

// HasChildren reports whether any other event has the given id as its
// parent_id. Used by `fngr delete` to gate the recursive-delete prompt.
func HasChildren(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE parent_id = ?", id).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("query children: %w", err)
	}
	return count > 0, nil
}

// protectedMetaKeys are single-valued and fixed at insert time. Every meta
// verb refuses them as a *target* — adding a second one, removing the only
// one, or renaming another key onto them all leave the event in a state no
// insert path can produce, and renderers that look the key up by name
// (event.AuthorOf) then pick whichever tuple sorts first or print a blank
// column. `Add` is the sole writer, and requireOneAuthor is what keeps it to
// one — the data layer refuses to repair only an invariant it also enforces.
var protectedMetaKeys = map[string]bool{
	MetaKeyAuthor: true,
}

// requireUnprotectedMeta rejects a protected key as the target of verb.
func requireUnprotectedMeta(verb, key string) error {
	if protectedMetaKeys[key] {
		return fmt.Errorf("cannot %s meta key %q: it is single-valued and set when the event is created", verb, key)
	}
	return nil
}

// requireUnprotectedTags applies requireUnprotectedMeta across a tag slice.
func requireUnprotectedTags(verb string, tags []parse.Meta) error {
	for _, m := range tags {
		if err := requireUnprotectedMeta(verb, m.Key); err != nil {
			return err
		}
	}
	return nil
}

// requireRenamableMeta is the protected-key gate for UpdateMeta, which is
// looser than the other verbs' by one case. What protection buys is the count:
// exactly one `author` row per event. A rename that changes the key breaks it
// in one direction or the other — `author=x` → `k=v` strips the author off
// every event that had it, `k=v` → `author=evil` mints a second one — and is
// refused at both ends. A rename that keeps the key only rewrites the value,
// leaving every event with the single row it already had, so a mistyped
// `--author` stays correctable: refusing it too made the one field no verb can
// touch also the one field no verb can fix, and the workaround was re-adding
// the event under a new id. An empty new value is still refused, since that
// blank is exactly the unrepairable state.
func requireRenamableMeta(oldKey, newKey, newValue string) error {
	if oldKey == newKey {
		if protectedMetaKeys[newKey] && newValue == "" {
			return fmt.Errorf("cannot rename meta key %q to an empty value: it is single-valued and every event must carry one", newKey)
		}
		return nil
	}
	if err := requireUnprotectedMeta("rename", oldKey); err != nil {
		return err
	}
	// The target matters as much as the source: without this, `meta rename
	// k=v author=evil` minted a second author on every event carrying k=v.
	return requireUnprotectedMeta("rename onto", newKey)
}

// UpdateMeta renames every (oldKey, oldValue) tuple across all events to
// (newKey, newValue) and returns the number of tuples it renamed. A rename
// onto a tuple that already exists is a merge, not an error: an event
// carrying both ends up with one, and the row it absorbed is gone (so the
// count is rows renamed, not rows the database gained). Refuses a protected
// key (currently `author`) on either end of a rename that *changes* the key,
// so no rename can break the renderers that look it up by name; correcting a
// protected key's value in place is allowed — see requireRenamableMeta.
func UpdateMeta(ctx context.Context, db *sql.DB, oldKey, oldValue, newKey, newValue string) (int64, error) {
	if err := requireRenamableMeta(oldKey, newKey, newValue); err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Capture affected events before the rename so we can resync their FTS
	// content (the indexed string includes key=value meta tokens).
	ids, err := metaEventIDs(ctx, tx, oldKey, oldValue)
	if err != nil {
		return 0, err
	}

	// OR REPLACE is what makes consolidating two tags (`#wip` → `#done`)
	// work, and consolidating is the main reason to run this verb. An event
	// carrying both collides with the UNIQUE(key, value, event_id) index
	// migration 2 added; a plain UPDATE aborts on the first such row and
	// rolls the whole rename back, so nothing moves at all. OR REPLACE drops
	// the row in the way and completes the update, which is exactly the
	// merge. Renaming a tuple to itself needs no special case: SQLite checks
	// uniqueness against the *other* rows, so the row is simply rewritten
	// with the values it already had.
	//
	// The renamed row becomes explicit whatever it was before: the new value
	// is one the operator chose and no event's text yields it, so leaving it
	// body-derived would let the next edit of any renamed event delete the
	// rename along with the mention it no longer matches.
	res, err := tx.ExecContext(ctx,
		"UPDATE OR REPLACE event_meta SET key = ?, value = ?, source = ? WHERE key = ? AND value = ?",
		newKey, newValue, metaSourceExplicit, oldKey, oldValue,
	)
	if err != nil {
		return 0, fmt.Errorf("update meta: %w", err)
	}
	// Rows replaced away are not counted here, only rows updated — which is
	// the honest number for "renamed".
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	for _, id := range ids {
		if err := rebuildEventFTS(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return n, nil
}

// DeleteMeta removes every (key, value) tuple across all events and
// returns the number of rows deleted. Refuses protected keys for the same
// reason as UpdateMeta.
func DeleteMeta(ctx context.Context, db *sql.DB, key, value string) (int64, error) {
	if err := requireUnprotectedMeta("delete", key); err != nil {
		return 0, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Capture affected events before the delete so we can resync their FTS
	// content (the indexed string includes key=value meta tokens).
	ids, err := metaEventIDs(ctx, tx, key, value)
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx,
		"DELETE FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	)
	if err != nil {
		return 0, fmt.Errorf("delete meta: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	for _, id := range ids {
		if err := rebuildEventFTS(ctx, tx, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return n, nil
}

// metaEventIDs returns the distinct event IDs carrying the given (key, value)
// meta tuple. Used by UpdateMeta/DeleteMeta to know which events' FTS content
// needs rebuilding after the tuple is renamed or removed.
func metaEventIDs(ctx context.Context, tx *sql.Tx, key, value string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT event_id FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	)
	if err != nil {
		return nil, fmt.Errorf("query meta event ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan meta event id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta event ids: %w", err)
	}
	return ids, nil
}

// CountMeta returns the number of events carrying the given (key, value)
// meta tuple.
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

// ListMetaOpts narrows the result of ListMeta. Both fields are optional;
// zero values mean "no filter on that field". When both are set, the
// query is an exact (key, value) match.
type ListMetaOpts struct {
	Key   string
	Value string
}

// ListMeta returns one MetaCount row per (key, value) tuple matching opts,
// sorted by key then value.
func ListMeta(ctx context.Context, db *sql.DB, opts ListMetaOpts) ([]MetaCount, error) {
	query := "SELECT key, value, COUNT(*) AS count FROM event_meta"
	var args []any
	var conds []string
	if opts.Key != "" {
		conds = append(conds, "key = ?")
		args = append(args, opts.Key)
	}
	if opts.Value != "" {
		conds = append(conds, "value = ?")
		args = append(args, opts.Value)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ") // #nosec G202 -- conds are "key = ?" and "value = ?" only, not user input
	}
	query += " GROUP BY key, value ORDER BY key, value"

	rows, err := db.QueryContext(ctx, query, args...)
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

// ListOpts narrows the result of List / ListSeq. All fields are optional;
// the zero value matches every event in newest-first order.
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
		query, args, err := buildListQuery(opts)
		if err != nil {
			yield(Event{}, err)
			return
		}
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
			e, err := scanEventRow(rows)
			if err != nil {
				yield(Event{}, err)
				return
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

func buildListQuery(opts ListOpts) (string, []any, error) {
	query := `SELECT e.id, e.parent_id, e.title, e.body, e.created_at
		FROM events e
		WHERE 1=1`
	var args []any

	if opts.Filter != "" {
		cond, filterArgs, err := compileFilter(opts.Filter)
		if err != nil {
			return "", nil, err
		}
		query += " AND " + cond
		args = append(args, filterArgs...)
	}

	// Bounds are compared lexically against the stored TEXT, so they have to
	// use the same encoder as the write path. No range check: an out-of-range
	// bound only ever matches nothing or everything, which is what the user
	// asked for.
	if opts.From != nil {
		query += " AND e.created_at >= ?"
		args = append(args, timefmt.FormatStorage(*opts.From))
	}
	if opts.To != nil {
		query += " AND e.created_at < ?"
		args = append(args, timefmt.FormatStorage(*opts.To))
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

	return query, args, nil
}

// GetSubtree returns the event with id == rootID plus every transitive
// descendant via parent_id, sorted by created_at ascending. Returns
// ErrNotFound when no event has id == rootID.
func GetSubtree(ctx context.Context, db *sql.DB, rootID int64) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, parent_id, title, body, created_at FROM events WHERE id = ?
			UNION ALL
			SELECT e.id, e.parent_id, e.title, e.body, e.created_at
			FROM events e JOIN subtree s ON e.parent_id = s.id
		)
		SELECT id, parent_id, title, body, created_at FROM subtree ORDER BY created_at ASC
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

// formatTimestamp renders t for the created_at column.
//
// The range check is the last line of defence, not the first: timefmt rejects
// out-of-range input at parse time. It is here because a timestamp can also
// arrive from --format=json or from a caller setting AddInput.CreatedAt
// directly — see timeScanner for what an unstorable year does to reads.
func formatTimestamp(t time.Time) (string, error) {
	u := t.UTC()
	if !timefmt.InRange(u) {
		return "", fmt.Errorf("%w: year %d", ErrTimeRange, u.Year())
	}
	return timefmt.FormatStorage(u), nil
}

// scanEventRow reads one (id, parent_id, title, body, created_at) row.
func scanEventRow(rows *sql.Rows) (Event, error) {
	var e Event
	var parentID sql.NullInt64
	if err := rows.Scan(&e.ID, &parentID, &e.Title, &e.Body, timeScanner{&e.CreatedAt}); err != nil {
		return Event{}, fmt.Errorf("scan event: %w", err)
	}
	if parentID.Valid {
		e.ParentID = &parentID.Int64
	}
	return e, nil
}

// timeScanner reads created_at without ever failing.
//
// Scanning straight into a *time.Time is what we want but cannot have: the
// driver converts a well-formed TEXT timestamp itself and hands back the raw
// string when it cannot, and the default conversion then errors with
// "unsupported Scan, storing driver.Value type string into type *time.Time".
// That error aborted the whole result set, so a single row written by an older
// build with an out-of-range year made every read command fail — including the
// Get that `delete` runs first, leaving no CLI path to remove it.
//
// Such a row now yields the zero time and stays listable and deletable. New
// rows cannot reach that state: formatTimestamp rejects them on write.
type timeScanner struct{ dst *time.Time }

func (s timeScanner) Scan(v any) error {
	*s.dst = timeFromDriverValue(v)
	return nil
}

// timeFromDriverValue converts whatever the driver produced for created_at
// into a time.Time, returning the zero time when the value is unusable.
//
// A value the driver could not parse arrives here as a string, and this cannot
// parse it either — that is the recovery path, and it yields the zero time.
// The parse is here for the other case: a driver that stops auto-converting
// would otherwise silently zero every timestamp in the database.
func timeFromDriverValue(v any) time.Time {
	var s string
	switch v := v.(type) {
	case time.Time:
		return v
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return time.Time{}
	}
	t, _ := timefmt.ParseStorage(s)
	return t
}

func scanEvents(ctx context.Context, db *sql.DB, rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		e, err := scanEventRow(rows)
		if err != nil {
			return nil, err
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
