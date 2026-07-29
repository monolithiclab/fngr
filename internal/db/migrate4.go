package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/monolithiclab/fngr/internal/parse"
)

// untitledPlaceholder stands in for an event that migration 3 left with no
// title and no body to promote one from. Every other path guarantees a
// non-empty title, and neither `add` nor `event title` accepts an empty
// string, so such a row cannot be repaired by hand once written.
const untitledPlaceholder = "(untitled)"

// legacyMetaNameRe is the @person / #tag name pattern fngr shipped with
// before the Unicode fix. Go reads \w as ASCII-only, so matching stopped at
// the first non-ASCII byte and `@josé` was stored as `people=jos`. Frozen
// deliberately: it describes what old versions wrote, which is the only way
// to recognise a truncated row and tell it apart from a short tag someone
// added on purpose. Do not "fix" it to match parse.metaNamePattern.
var legacyMetaNameRe = regexp.MustCompile(`^[0-9A-Za-z_][0-9A-Za-z_/\-]*`)

// eventText is one row of the events table as migration 4 finds it.
type eventText struct {
	id    int64
	title string
	body  string
}

// repairLegacyText is the Go half of migration 4. It re-derives every
// event's title and body, restores metadata the ASCII-only name pattern
// truncated, and rebuilds events_fts from the same helper the running code
// uses.
//
// Migration 3 split the old single `text` column with SQL, and SQL got two
// things wrong that only Go can put right. TRIM() strips U+0020 and nothing
// else, where strings.TrimSpace strips tabs, newlines, NBSP and the rest of
// Unicode space — so a legacy `"title. \n  body"` migrated to a body that
// still began with a newline, breaking the one-line-per-event contract of
// the flat and tree renderers. And its events_fts rebuild was a second
// transliteration of parse.FTSContent, free to drift from the original.
//
// The pass is a no-op in effect for a database whose rows are already
// clean, which is every database written by a current version.
//
// It deliberately calls the live parse.BodyTags and parse.FTSContent rather
// than a snapshot of them: reproducing their rules in a second place is the
// mistake being corrected here. The cost is that changing either helper also
// changes what this migration writes to a database that has not run it yet.
func repairLegacyText(tx *sql.Tx) error {
	events, err := loadEventText(tx)
	if err != nil {
		return err
	}

	for _, e := range events {
		title, body := repairTitleBody(e.title, e.body)
		if title != e.title || body != e.body {
			if _, err := tx.Exec(
				"UPDATE events SET title = ?, body = ? WHERE id = ?", title, body, e.id,
			); err != nil {
				return fmt.Errorf("rewrite event %d: %w", e.id, err)
			}
		}
		if err := repairEventMeta(tx, e.id, title+" "+body); err != nil {
			return err
		}
	}

	return rebuildAllFTS(tx)
}

// repairTitleBody re-derives one event's title and body the way
// parse.SplitTitleBody would have, given that migration 3 already consumed
// the ". " separator.
func repairTitleBody(title, body string) (string, string) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if title != "" {
		return title, body
	}
	// A legacy `". body"` split to an empty title. Promote the body's first
	// line, which is what the split would have produced had the text not
	// opened with the separator.
	first, rest, _ := strings.Cut(body, "\n")
	title, body = strings.TrimSpace(first), strings.TrimSpace(rest)
	if title == "" {
		title = untitledPlaceholder
	}
	return title, body
}

// repairEventMeta re-extracts @person / #tag rows from one event's text and
// removes the truncated forms an older fngr wrote in their place.
//
// A row is only deleted when it is exactly what legacyMetaNameRe would have
// produced from a name the current extractor reads in full — `people=jos`
// next to a body saying `@josé`. A tag that merely happens to be a prefix of
// another (`#work` alongside `#workflow`) is left alone, because the old
// pattern would have captured `workflow` whole and so cannot be the source
// of a bare `work`.
func repairEventMeta(tx *sql.Tx, id int64, text string) error {
	for _, m := range parse.BodyTags(text) {
		if _, err := tx.Exec(
			"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
			id, m.Key, m.Value,
		); err != nil {
			return fmt.Errorf("restore %s=%s on event %d: %w", m.Key, m.Value, id, err)
		}

		truncated := legacyMetaNameRe.FindString(m.Value)
		if truncated == "" || truncated == m.Value {
			continue
		}
		if _, err := tx.Exec(
			"DELETE FROM event_meta WHERE event_id = ? AND key = ? AND value = ?",
			id, m.Key, truncated,
		); err != nil {
			return fmt.Errorf("drop truncated %s=%s on event %d: %w", m.Key, truncated, id, err)
		}
	}
	return nil
}

func loadEventText(tx *sql.Tx) ([]eventText, error) {
	rows, err := tx.Query("SELECT id, title, body FROM events ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []eventText
	for rows.Next() {
		var e eventText
		if err := rows.Scan(&e.id, &e.title, &e.body); err != nil {
			return nil, fmt.Errorf("read event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return out, nil
}

// rebuildAllFTS regenerates every events_fts row through parse.FTSContent.
// Unconditional rather than limited to the rows this migration touched:
// migration 3's SQL rebuild reimplemented the same formula, so every row it
// wrote is suspect, and one pass over the events table restores the
// guarantee that the index says exactly what the Go code would write.
//
// It re-reads the events itself rather than taking the caller's slice, so
// both halves of the content come from the transaction and neither can be a
// stale mirror of what the repair loop already wrote.
func rebuildAllFTS(tx *sql.Tx) error {
	events, err := loadEventText(tx)
	if err != nil {
		return err
	}
	meta, err := loadAllMeta(tx)
	if err != nil {
		return err
	}

	if _, err := tx.Exec("DELETE FROM events_fts"); err != nil {
		return fmt.Errorf("clear FTS index: %w", err)
	}
	insert, err := tx.Prepare("INSERT INTO events_fts (rowid, content) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("prepare FTS insert: %w", err)
	}
	defer func() { _ = insert.Close() }()

	for _, e := range events {
		if _, err := insert.Exec(e.id, parse.FTSContent(e.title, e.body, meta[e.id])); err != nil {
			return fmt.Errorf("index event %d: %w", e.id, err)
		}
	}
	return nil
}

func loadAllMeta(tx *sql.Tx) (map[int64][]parse.Meta, error) {
	rows, err := tx.Query("SELECT event_id, key, value FROM event_meta ORDER BY event_id, key, value")
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[int64][]parse.Meta)
	for rows.Next() {
		var id int64
		var m parse.Meta
		if err := rows.Scan(&id, &m.Key, &m.Value); err != nil {
			return nil, fmt.Errorf("read metadata row: %w", err)
		}
		out[id] = append(out[id], m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	return out, nil
}
