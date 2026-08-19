// This file is the package's transaction layer: the bracket that opens a
// transaction, plus the tx-taking helpers that are shared across files and
// belong to no one domain. Both halves of that rule matter — meta.go's
// readMetaTx and execBodyMetaTuples also take a *sql.Tx and also touch one
// event's rows, and they stay there because they are about metadata. The rule
// is what the name buys: internal.go, which this file started out as, named a
// visibility every declaration in a package under internal/ already has, so
// nothing could fail it. See CLAUDE.md for the longer version.

package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monolithiclab/fngr/internal/parse"
)

// inTx runs fn inside a transaction, committing when it returns nil and
// rolling back otherwise. It is the only place in this package that opens
// one.
//
// That is what the helper is for, rather than the eight lines it saves each
// caller. The bracket was hand-written at seven sites, and two of them
// returned tx.Commit() bare — so a write that failed at the very last step
// reported `database is locked` with nothing to say which write it was, and
// the eighth site could regress the same way. Wording the begin and commit
// errors here makes that structural.
//
// Every caller pairs it with a private fooInTx doing the work, the shape
// addInTx already had. Keeping the body in its own function rather than a
// closure is what lets a mutation be tested against a transaction directly.
func inTx[T any](ctx context.Context, db *sql.DB, fn func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	v, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, fmt.Errorf("commit transaction: %w", err)
	}
	return v, nil
}

// inTxVoid is inTx for a mutation with nothing to return.
func inTxVoid(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	_, err := inTx(ctx, db, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, fn(tx)
	})
	return err
}

// requireEventExists returns ErrNotFound (wrapped) if id has no row in
// events. Used by every mutation that reads before it writes — Update,
// Reparent, AddTags, RemoveTags — so a missing row surfaces the same sentinel
// before any other work begins. Delete needs no call: its own RowsAffected
// says whether the row was there.
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
// tx and writes parse.FTSColumns into events_fts.
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
	content, metaTokens := parse.FTSColumns(title, body, meta)
	if _, err := tx.ExecContext(ctx,
		"UPDATE events_fts SET content = ?, meta = ? WHERE rowid = ?",
		content, metaTokens, id,
	); err != nil {
		return fmt.Errorf("update FTS: %w", err)
	}
	return nil
}
