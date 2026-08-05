package db

import (
	"database/sql"
	"slices"
	"testing"
)

// ftsMatches returns the event ids an FTS5 MATCH expression selects.
func ftsMatches(t *testing.T, db *sql.DB, match string) []int64 {
	t.Helper()
	rows, err := db.Query("SELECT rowid FROM events_fts WHERE events_fts MATCH ? ORDER BY rowid", match)
	if err != nil {
		t.Fatalf("fts match %q: %v", match, err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan rowid: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("fts match %q: %v", match, err)
	}
	return out
}

// TestMigrate6_SplitsForgedTokensOutOfTheMetaColumn is M7 seen from an
// existing database: event 1 is tagged, event 2 only says it is. Before the
// split both produced the token `tag=ops` in the one column there was.
func TestMigrate6_SplitsForgedTokensOutOfTheMetaColumn(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "standup notes", "release write-up. mentions tag=ops literally")
	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'tag', 'ops')",
	); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, tt := range []struct {
		name  string
		match string
		want  []int64
	}{
		{"the meta column holds only real metadata", `meta:"tag=ops"`, []int64{1}},
		{"the forged token stays in the content column", `content:"tag=ops"`, []int64{2}},
		{"body text is still searchable", `content:"literally"`, []int64{2}},
		{"an untagged event contributes no meta", `meta:"literally"`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ftsMatches(t, db, tt.match); !slices.Equal(got, tt.want) {
				t.Errorf("MATCH %s = %v, want %v", tt.match, got, tt.want)
			}
		})
	}
}

// TestMigrate6_IndexesEveryEvent guards the half of the migration that is
// easy to get silently wrong: 6.sql drops the index, so an event the Go step
// skips is not stale, it is unsearchable.
func TestMigrate6_IndexesEveryEvent(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "one", "two. with a body", "three")

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var events, indexed int
	if err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM events_fts").Scan(&indexed); err != nil {
		t.Fatalf("count fts rows: %v", err)
	}
	if indexed != events {
		t.Errorf("%d events, %d indexed", events, indexed)
	}
}
