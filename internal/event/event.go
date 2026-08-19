// Package event is fngr's journal store: events, their parent-child tree, and
// their key-value metadata.
//
// The files split at the read/write seam. event.go holds the sentinels, Event
// and AddInput, and the Add path; query.go the standalone reads of an event;
// mutate.go Update, Reparent and Delete; tx.go the transaction layer; store.go
// the *sql.DB wrapper the CLI depends on; filter.go the -S expression
// compiler.
//
// meta.go is the one file named for a domain rather than a side of that seam,
// and it answers every metadata question — the rules, the storage verbs, and
// metadata's own reads (ListMeta, CountMeta) alike. Splitting it would leave a
// reader guessing which of two files to open.
package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// ErrCorruptTree is returned when a parent chain already in the database loops
// back on itself. Distinct from ErrCycle: that one refuses a requested change,
// this one reports a file that was already broken — by a hand edit or a partial
// write, since Reparent is the only writer of parent_id and refuses.
var ErrCorruptTree = errors.New("corrupt parent chain")

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
	return inTx(ctx, db, func(tx *sql.Tx) (int64, error) {
		ids, err := addInTx(ctx, tx, []AddInput{in})
		if err != nil {
			return 0, err
		}
		return ids[0], nil
	})
}

// AddMany inserts the given events in a single transaction. Empty input
// is a no-op returning (nil, nil). Any per-record error rolls the entire
// batch back. Returns generated IDs in input order.
func AddMany(ctx context.Context, db *sql.DB, inputs []AddInput) ([]int64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	return inTx(ctx, db, func(tx *sql.Tx) ([]int64, error) {
		return addInTx(ctx, tx, inputs)
	})
}

// addInTx inserts events using the given tx. Caller owns commit/rollback.
// Each input becomes one INSERT into events, zero-or-more INSERTs into
// event_meta, and one INSERT into events_fts. Per-record errors abort
// the loop with a wrapped error; the caller's deferred Rollback fires.
func addInTx(ctx context.Context, tx *sql.Tx, inputs []AddInput) ([]int64, error) {
	if err := validateAddInputs(inputs); err != nil {
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
		"INSERT INTO events_fts (rowid, content, meta) VALUES (?, ?, ?)",
	)
	if err != nil {
		return nil, fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer insertFTS.Close()

	// Prepared like the two inserts above rather than re-sent per record: an
	// import that names a --parent asks this once per record, and re-parsing it
	// 10 000 times cost ~5% of the run.
	selectParent, err := tx.PrepareContext(ctx, "SELECT 1 FROM events WHERE id = ?")
	if err != nil {
		return nil, fmt.Errorf("prepare parent lookup: %w", err)
	}
	defer selectParent.Close()

	ids := make([]int64, 0, len(inputs))
	for i, in := range inputs {
		if in.ParentID != nil {
			var exists int
			err := selectParent.QueryRowContext(ctx, *in.ParentID).Scan(&exists)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("%sparent event %d: %w", recordPrefix(i, len(inputs)), *in.ParentID, ErrNotFound)
				}
				return nil, fmt.Errorf("%squery parent event: %w", recordPrefix(i, len(inputs)), err)
			}
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

		content, metaTokens := parse.FTSColumns(in.Title, in.Body, in.Meta)
		if _, err := insertFTS.ExecContext(ctx, id, content, metaTokens); err != nil {
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

// validateAddInputs vets every record before addInTx writes anything, and it
// is the only place those checks live: an AddInput can be built directly, so
// the writer is what nothing bypasses (see requireOneAuthor).
//
// Every message in a batch names the record. The per-record half used to run
// inside the insert loop and say only `title cannot be empty`, which a
// --format=json import of 10 000 records leaves nobody able to act on.
// Hoisting it also means a batch that cannot be stored is refused before the
// first INSERT rather than rolled back halfway through one.
func validateAddInputs(inputs []AddInput) error {
	for i, in := range inputs {
		if err := validateAddInput(in, len(inputs)); err != nil {
			return fmt.Errorf("%s%w", recordPrefix(i, len(inputs)), err)
		}
	}
	return requireAcyclicParentIndexes(inputs)
}

// recordPrefix names the offending record in a batch of n. A single-record
// batch gets no prefix: Add is the common caller and `record 0: title cannot
// be empty` is noise in front of a message that already describes the only
// record there was.
func recordPrefix(i, n int) string {
	if n < 2 {
		return ""
	}
	return fmt.Sprintf("record %d: ", i)
}

// validateAddInput vets one record against a batch of batchSize.
func validateAddInput(in AddInput, batchSize int) error {
	switch {
	case in.ParentIndex == nil:
	case in.ParentID != nil:
		return errors.New("ParentID and ParentIndex are mutually exclusive")
	case *in.ParentIndex < 0 || *in.ParentIndex >= batchSize:
		return fmt.Errorf("parent index %d out of range", *in.ParentIndex)
	}
	if in.Title == "" {
		return errors.New("title cannot be empty")
	}
	if err := requireOneAuthor(in.Meta); err != nil {
		return err
	}
	return requireStorableMeta(in.Meta)
}

// requireAcyclicParentIndexes rejects a batch whose ParentIndex links form a
// cycle. It runs before any INSERT because the UPDATE pass that applies them
// cannot fail on a cycle the way an INSERT would — SQLite's foreign key only
// checks that the parent row exists, and in a cycle every row does. An
// unchecked cycle would commit a clump of events unreachable from any root.
func requireAcyclicParentIndexes(inputs []AddInput) error {
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
