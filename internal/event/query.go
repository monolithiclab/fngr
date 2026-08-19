package event

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

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

// ListOpts narrows the result of List / ListSeq. All fields are optional;
// the zero value matches every event in newest-first order.
type ListOpts struct {
	Filter    string
	From      *time.Time // inclusive lower bound
	To        *time.Time // exclusive upper bound (caller rounds up past what it means to include)
	Limit     int        // 0 means no limit; a limit always keeps the newest N
	Ascending bool       // display order only: oldest first when true
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

// ValidateLimit reports whether n is a usable ListOpts.Limit. Zero means no
// limit; a negative value is refused rather than clamped, because clamping is
// the silent behaviour this exists to remove.
//
// Unlike From/To, a bad Limit does not merely match nothing or everything *as
// asked*: only the LIMIT clause reads it, and that clause is emitted on
// `Limit > 0`, so a negative value silently **widens** the result to the whole
// journal. Exported next to ValidateFilter and for the same reason — the CLI
// must refuse before withPager spawns $PAGER and before the streaming
// renderer writes its opening bytes, and one rule read from two altitudes has
// to be one function or the two wordings drift.
func ValidateLimit(n int) error {
	if n < 0 {
		return fmt.Errorf("limit cannot be negative, got %d (0 means no limit)", n)
	}
	return nil
}

func buildListQuery(opts ListOpts) (string, []any, error) {
	if err := ValidateLimit(opts.Limit); err != nil {
		return "", nil, err
	}

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

	// A Limit always keeps the newest N, and Ascending only decides how they
	// are displayed — so the inner sort is free to follow Ascending only when
	// there is no limit for it to select through. Sorting ascending before
	// LIMIT would let the display flag choose *which* rows survive, and
	// `fngr -n 20 -r` — read by anyone as "recent activity, chronological" —
	// returned the 20 oldest events in the database.
	if opts.Ascending && opts.Limit == 0 {
		query += " ORDER BY e.created_at ASC"
	} else {
		query += " ORDER BY e.created_at DESC"
	}
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
		if opts.Ascending {
			// SELECT * rather than the column list again: the subquery already
			// fixes both the columns and their order, and restating them here
			// is a second place to update when the base query grows one.
			// SQLite cannot flatten this, so `-n N -r` buffers N rows before
			// yielding any — the one case where ListSeq is not O(1) memory.
			query = "SELECT * FROM (" + query + ") ORDER BY created_at ASC"
		}
	}
	return query, args, nil
}

// GetSubtree returns the event with id == rootID plus every transitive
// descendant via parent_id, sorted by created_at ascending. Returns
// ErrNotFound when no event has id == rootID, and ErrCorruptTree when the
// descendants loop back on themselves.
//
// The recursive term is UNION, not UNION ALL. On a cyclic parent chain an
// UNION ALL does not merely recurse deeply — it never terminates, producing no
// output and no error while pinning a core until the process is killed. UNION
// discards a row already in the result, so going round the loop a second time
// adds nothing and the queue drains. On a well-formed tree the two are
// identical (id is the primary key, so no two rows can collide) and UNION costs
// ~15% for the dedup index; that buys termination on the one input that hangs.
func GetSubtree(ctx context.Context, db *sql.DB, rootID int64) ([]Event, error) {
	rows, err := db.QueryContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, parent_id, title, body, created_at
			FROM events WHERE id = ?
			UNION
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

	// Terminating is not the same as answering, so say so rather than hand
	// back a "subtree" of a graph that has no such thing. A cycle is only
	// ever reachable from a member of it — every member's parent is another
	// member, so no descent from outside can enter one — which reduces the
	// whole test to whether rootID's own parent came back as one of rootID's
	// descendants. If it did, following parent_id from rootID returns to
	// rootID.
	ids := make(map[int64]struct{}, len(events))
	var root Event
	for _, ev := range events {
		ids[ev.ID] = struct{}{}
		if ev.ID == rootID {
			root = ev
		}
	}
	if root.ParentID != nil {
		if _, loops := ids[*root.ParentID]; loops {
			return nil, fmt.Errorf("subtree of event %d loops back through event %d: %w",
				rootID, *root.ParentID, ErrCorruptTree)
		}
	}
	return events, nil
}

// CountSubtree returns the size of rootID's subtree, rootID included. It
// answers what `len(GetSubtree(...))` answers and reads no row to do it:
// `delete -r` needs the number only to name what it is about to take, and
// materializing every title, body and meta row of a 100k subtree to print one
// integer cost ~950 ms and ~110 MB of live heap against ~120 ms and nothing.
//
// The recursion, the UNION and the loop test are GetSubtree's, for the reasons
// documented there — a cyclic chain must terminate, and terminating is not
// answering. Both scalar subqueries read the same materialized CTE.
func CountSubtree(ctx context.Context, db *sql.DB, rootID int64) (int64, error) {
	var (
		total  int64
		loops  int64
		parent sql.NullInt64
	)
	err := db.QueryRowContext(ctx, `
		WITH RECURSIVE subtree AS (
			SELECT id, parent_id FROM events WHERE id = ?1
			UNION
			SELECT e.id, e.parent_id FROM events e JOIN subtree s ON e.parent_id = s.id
		)
		SELECT
			(SELECT count(*) FROM subtree),
			(SELECT count(*) FROM subtree WHERE id = (SELECT parent_id FROM events WHERE id = ?1)),
			(SELECT parent_id FROM events WHERE id = ?1)
	`, rootID).Scan(&total, &loops, &parent)
	if err != nil {
		return 0, fmt.Errorf("count subtree: %w", err)
	}
	if total == 0 {
		return 0, fmt.Errorf("event %d: %w", rootID, ErrNotFound)
	}
	if loops > 0 {
		return 0, fmt.Errorf("subtree of event %d loops back through event %d: %w",
			rootID, parent.Int64, ErrCorruptTree)
	}
	return total, nil
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
