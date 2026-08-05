package db

import (
	"database/sql"
	"fmt"

	"github.com/monolithiclab/fngr/internal/parse"
)

// splitFTSIndex is the Go half of migration 6. 6.sql dropped and recreated
// events_fts with a second column; this fills the empty index back in, one
// row per event, through parse.FTSColumns.
//
// No DELETE first: the table 6.sql leaves behind is new and empty. And no
// SQL formulation of the split for the same reason migration 4 exists — a
// transliteration of a Go helper into SQL is free to drift from it, and the
// two halves of a search index disagreeing is invisible until a query
// silently returns nothing.
func splitFTSIndex(tx *sql.Tx) error {
	events, err := loadEventText(tx)
	if err != nil {
		return err
	}
	meta, err := loadAllMeta(tx)
	if err != nil {
		return err
	}

	insert, err := tx.Prepare("INSERT INTO events_fts (rowid, content, meta) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer func() { _ = insert.Close() }()

	for _, e := range events {
		content, tokens := parse.FTSColumns(e.title, e.body, meta[e.id])
		if _, err := insert.Exec(e.id, content, tokens); err != nil {
			return fmt.Errorf("index event %d: %w", e.id, err)
		}
	}
	return nil
}
