package db

import (
	"database/sql"
	"fmt"

	"github.com/monolithiclab/fngr/internal/parse"
)

// classifyMetaSource is the Go half of migration 5. `5.sql` gives every
// existing event_meta row `source = 'explicit'`; this pass demotes the ones
// the event's own title+body still yields to `source = 'body'`.
//
// Provenance was never recorded, so this is a reconstruction, not a lookup.
// It is the closest one available: a tuple the current text produces is
// exactly the tuple the pre-migration Update would have deleted and
// re-inserted on the next edit, so calling it body-derived preserves the
// behaviour those rows already had. The rows it cannot recover are the ones
// added *both* ways — `event tag @bob` on a body that mentions @bob is a
// single row either way — and those come out 'body', matching how addInTx
// resolves the same tie for a new event. Re-asserting with `event tag`
// promotes such a row back to 'explicit'.
//
// Like repairLegacyText it calls the live parse.BodyTags rather than a frozen
// copy; the same trade applies, and here it matters less because a drift only
// changes which side of a stale-vs-lost trade a given row lands on.
func classifyMetaSource(tx *sql.Tx) error {
	events, err := loadEventText(tx)
	if err != nil {
		return err
	}

	demote, err := tx.Prepare(
		"UPDATE event_meta SET source = 'body' WHERE event_id = ? AND key = ? AND value = ?",
	)
	if err != nil {
		return fmt.Errorf("prepare source update: %w", err)
	}
	defer func() { _ = demote.Close() }()

	for _, e := range events {
		for _, m := range parse.BodyTags(parse.EventText(e.title, e.body)) {
			if _, err := demote.Exec(e.id, m.Key, m.Value); err != nil {
				return fmt.Errorf("mark %s=%s body-derived on event %d: %w", m.Key, m.Value, e.id, err)
			}
		}
	}
	return nil
}
