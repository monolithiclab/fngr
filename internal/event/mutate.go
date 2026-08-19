package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

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

	return inTxVoid(ctx, db, func(tx *sql.Tx) error {
		return updateInTx(ctx, tx, id, title, body, createdAt, stamp)
	})
}

// updateInTx is Update's body. stamp is the already-formatted createdAt,
// which is validated before the transaction opens so a bad timestamp costs
// no write lock.
func updateInTx(ctx context.Context, tx *sql.Tx, id int64, title, body *string, createdAt *time.Time, stamp string) error {
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

	return nil
}

// Reparent sets event id's parent to newParent, or clears it when
// newParent is nil. Walks the candidate parent's ancestry chain and
// returns ErrCycle if id appears in it (including newParent == &id).
// Returns ErrNotFound if id or *newParent does not exist.
func Reparent(ctx context.Context, db *sql.DB, id int64, newParent *int64) error {
	return inTxVoid(ctx, db, func(tx *sql.Tx) error {
		return reparentInTx(ctx, tx, id, newParent)
	})
}

// reparentInTx is Reparent's body.
func reparentInTx(ctx context.Context, tx *sql.Tx, id int64, newParent *int64) error {
	if err := requireEventExists(ctx, tx, id); err != nil {
		return err
	}

	if newParent != nil {
		if *newParent == id {
			return fmt.Errorf("attaching event %d to itself: %w", id, ErrCycle)
		}

		// Walk ancestry from *newParent upward; reject if we hit id.
		//
		// The seen set is not part of that check — it bounds the walk. A
		// cycle *upstream* of the chain never contains id, so the id test
		// alone never fires and the loop spins forever, at full CPU, inside
		// this open transaction. (A walk starting inside the cycle does hit
		// id and stops, which is why this looks safe until it isn't.)
		seen := map[int64]struct{}{*newParent: {}}
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
				// The sentinel already says what the problem is; naming it
				// here too printed "... would form a cycle: would create a
				// parent cycle". The prefix's job is the two ids.
				return fmt.Errorf("attaching event %d to event %d: %w", id, *newParent, ErrCycle)
			}
			if _, repeat := seen[parent.Int64]; repeat {
				return fmt.Errorf("ancestry of event %d revisits event %d: %w", *newParent, parent.Int64, ErrCorruptTree)
			}
			seen[parent.Int64] = struct{}{}
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

	return nil
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
