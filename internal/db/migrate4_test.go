package db

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

// seedLegacy brings db up to the v1 schema, inserts each text as a legacy
// `events.text` row, and returns with user_version at 1 so a later migrate()
// runs 2, 3 and 4 over the data.
func seedLegacy(t *testing.T, db *sql.DB, texts ...string) {
	t.Helper()
	if _, err := db.Exec(migrationSQL(t, 0)); err != nil {
		t.Fatalf("seed v1 schema: %v", err)
	}
	if err := setUserVersion(db, 1); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	for _, text := range texts {
		if _, err := db.Exec("INSERT INTO events (text) VALUES (?)", text); err != nil {
			t.Fatalf("insert %q: %v", text, err)
		}
	}
}

func titleBody(t *testing.T, db *sql.DB, id int64) (string, string) {
	t.Helper()
	var title, body string
	if err := db.QueryRow("SELECT title, body FROM events WHERE id = ?", id).Scan(&title, &body); err != nil {
		t.Fatalf("select event %d: %v", id, err)
	}
	return title, body
}

// TestMigrate4_MatchesSplitTitleBody is the regression test for the whole
// point of migration 4: SQLite's TRIM() strips U+0020 and nothing else, so
// migration 3 stored values parse.SplitTitleBody would never produce —
// bodies still carrying a leading newline, titles still carrying a tab.
// SplitTitleBody itself is the oracle, so the two cannot drift.
func TestMigrate4_MatchesSplitTitleBody(t *testing.T) {
	t.Parallel()

	texts := []string{
		"title. \n  body after newline",
		"\ttab title no sep",
		"\n\nleading newlines. body",
		"body has trailing newline. line1\nline2\n",
		"unicode nbsp . body",
		"em space .  body",
		"plain title. plain body",
		"no separator at all",
		"\r\ncarriage. return\r\n",
	}

	db := testDB(t)
	seedLegacy(t, db, texts...)
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for i, text := range texts {
		t.Run(text, func(t *testing.T) {
			wantTitle, wantBody := parse.SplitTitleBody(text)
			gotTitle, gotBody := titleBody(t, db, int64(i+1))
			if gotTitle != wantTitle || gotBody != wantBody {
				t.Errorf("stored (%q, %q), want SplitTitleBody(%q) = (%q, %q)",
					gotTitle, gotBody, text, wantTitle, wantBody)
			}
		})
	}
}

// TestMigrate4_PromotesEmptyTitle covers the one case migration 4 cannot
// delegate to SplitTitleBody: a legacy text opening with the separator
// splits to an empty title, which every other path rejects — `fngr add
// ". body"` errors and `event title N ”` refuses to set it, so the row is
// unfixable by hand once written.
func TestMigrate4_PromotesEmptyTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		text      string
		wantTitle string
		wantBody  string
	}{
		{"single-line body", ". body only", "body only", ""},
		{"leading whitespace", "  \t. body only", "body only", ""},
		{"multi-line body", ". first line\nsecond line", "first line", "second line"},
		{"nothing to promote", ". ", untitledPlaceholder, ""},
		{"separator only", ".  ", untitledPlaceholder, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := testDB(t)
			seedLegacy(t, db, tt.text)
			if err := migrate(db); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			gotTitle, gotBody := titleBody(t, db, 1)
			if gotTitle != tt.wantTitle || gotBody != tt.wantBody {
				t.Errorf("stored (%q, %q), want (%q, %q)",
					gotTitle, gotBody, tt.wantTitle, tt.wantBody)
			}
			if gotTitle == "" {
				t.Error("title is still empty; no command can repair it")
			}
		})
	}
}

// TestRepairTitleBody exercises the pure half directly, including inputs the
// migration path cannot reach because migration 3 already trimmed U+0020.
func TestRepairTitleBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		title, body string
		wantTitle   string
		wantBody    string
	}{
		{"already clean", "title", "body", "title", "body"},
		{"tab-prefixed title", "\ttitle", "", "title", ""},
		{"newline-prefixed body", "title", "\n  body", "title", "body"},
		{"nbsp-suffixed title", "title ", "", "title", ""},
		{"trailing newline in body", "title", "line1\nline2\n", "title", "line1\nline2"},
		{"empty title promotes body", "", "promoted", "promoted", ""},
		{"empty title promotes first line", "", "one\ntwo", "one", "two"},
		{"whitespace-only title", " \t ", "promoted", "promoted", ""},
		{"nothing at all", "", "", untitledPlaceholder, ""},
		{"whitespace only", "  ", "\n\t", untitledPlaceholder, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotTitle, gotBody := repairTitleBody(tt.title, tt.body)
			if gotTitle != tt.wantTitle || gotBody != tt.wantBody {
				t.Errorf("repairTitleBody(%q, %q) = (%q, %q), want (%q, %q)",
					tt.title, tt.body, gotTitle, gotBody, tt.wantTitle, tt.wantBody)
			}
		})
	}
}

// TestMigrate4_RepairsTruncatedMetaNames covers the data half of the
// ASCII-only name pattern fngr shipped with: `@josé` was stored as
// `people=jos`, which collides with `@josa` and can't be searched for. The
// migration restores the full name and drops the stub — but only the stub
// the old pattern would actually have produced, so a short tag someone added
// on purpose next to a longer one survives.
func TestMigrate4_RepairsTruncatedMetaNames(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db,
		"coffee with @josé and #niño", // 1: old pattern wrote people=jos, tag=ni
		"planning #workflow revamp",   // 2: all-ASCII, nothing to repair
		"notes from @bob",             // 3: all-ASCII, nothing to repair
	)

	// Exactly what an old fngr would have written, plus a hand-added `work`
	// tag on the event whose body derives `workflow`.
	legacy := []struct {
		id    int64
		key   string
		value string
	}{
		{1, "author", "nico"},
		{1, "people", "jos"},
		{1, "tag", "ni"},
		{2, "author", "nico"},
		{2, "tag", "workflow"},
		{2, "tag", "work"},
		{3, "author", "nico"},
		{3, "people", "bob"},
	}
	for _, m := range legacy {
		if _, err := db.Exec(
			"INSERT INTO event_meta (event_id, key, value) VALUES (?, ?, ?)", m.id, m.key, m.value,
		); err != nil {
			t.Fatalf("seed meta %s=%s: %v", m.key, m.value, err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tests := []struct {
		name       string
		id         int64
		key, value string
		want       bool
	}{
		{"full name restored", 1, "people", "josé", true},
		{"truncated name dropped", 1, "people", "jos", false},
		{"full tag restored", 1, "tag", "niño", true},
		{"truncated tag dropped", 1, "tag", "ni", false},
		{"author untouched", 1, "author", "nico", true},
		{"derived ascii tag kept", 2, "tag", "workflow", true},
		{"hand-added prefix tag kept", 2, "tag", "work", true},
		{"ascii person kept", 3, "people", "bob", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM event_meta WHERE event_id = ? AND key = ? AND value = ?",
				tt.id, tt.key, tt.value,
			).Scan(&n); err != nil {
				t.Fatalf("count meta: %v", err)
			}
			if got := n > 0; got != tt.want {
				t.Errorf("event %d %s=%s present = %v, want %v", tt.id, tt.key, tt.value, got, tt.want)
			}
		})
	}
}

// TestMigrate4_RebuildsFTSFromGo checks that the search index says exactly
// what parse.FTSContent would write. Migration 3 rebuilt it from a SQL
// transliteration of that helper, free to drift, and it indexed the
// untrimmed values besides.
func TestMigrate4_RebuildsFTSFromGo(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "coffee with @josé. \n  notes about the #niño release")
	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'author', 'nico'), (1, 'people', 'jos')",
	); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	title, body := titleBody(t, db, 1)
	rows, err := db.Query("SELECT key, value FROM event_meta WHERE event_id = 1 ORDER BY key, value")
	if err != nil {
		t.Fatalf("select meta: %v", err)
	}
	defer rows.Close()
	var meta []parse.Meta
	for rows.Next() {
		var m parse.Meta
		if err := rows.Scan(&m.Key, &m.Value); err != nil {
			t.Fatalf("scan meta: %v", err)
		}
		meta = append(meta, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan meta: %v", err)
	}

	// Migration 6 splits the index in two, so what migration 4 rebuilt is
	// read back through the same pair the running code writes.
	var content, metaTokens string
	if err := db.QueryRow(
		"SELECT content, meta FROM events_fts WHERE rowid = 1",
	).Scan(&content, &metaTokens); err != nil {
		t.Fatalf("select fts: %v", err)
	}
	wantContent, wantMeta := parse.FTSColumns(title, body, meta)
	if content != wantContent || metaTokens != wantMeta {
		t.Errorf("fts row = (%q, %q), want (%q, %q)", content, metaTokens, wantContent, wantMeta)
	}

	// The repaired name has to be findable, which is the user-visible point.
	if got := ftsMatches(t, db, `meta:"people=josé"`); !slices.Equal(got, []int64{1}) {
		t.Errorf("searching for the restored name matched %v, want [1]", got)
	}
}

// TestLegacyFTSContent pins the frozen single-column formula migration 4
// writes. It has no observable effect on a database that finishes migrating —
// migration 6 drops the index it fills — so nothing else can catch it
// drifting from what the migration was written to produce.
func TestLegacyFTSContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		title string
		body  string
		meta  []parse.Meta
		want  string
	}{
		{"empty", "", "", nil, ""},
		{"title only", "hello", "", nil, "hello"},
		{"body only", "", "world", nil, "world"},
		{"meta only", "", "", []parse.Meta{{Key: "author", Value: "nico"}}, "author=nico"},
		{
			"everything, meta last and in order",
			"hello", "world",
			[]parse.Meta{{Key: "tag", Value: "ops"}, {Key: "people", Value: "sarah"}},
			"hello world tag=ops people=sarah",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := legacyFTSContent(tt.title, tt.body, tt.meta); got != tt.want {
				t.Errorf("legacyFTSContent(%q, %q, %v) = %q, want %q",
					tt.title, tt.body, tt.meta, got, tt.want)
			}
		})
	}
}

// TestMigrate4_AnalyzesEventMeta covers the SQL half of migration 4.
// Migration 2 swapped the event_meta index without refreshing planner
// statistics, so sqlite_stat1 still described an index that had been
// dropped.
func TestMigrate4_AnalyzesEventMeta(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "one", "two")
	for _, id := range []int{1, 2} {
		if _, err := db.Exec(
			"INSERT INTO event_meta (event_id, key, value) VALUES (?, 'author', 'nico')", id,
		); err != nil {
			t.Fatalf("seed meta: %v", err)
		}
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var idx string
	err := db.QueryRow(
		"SELECT idx FROM sqlite_stat1 WHERE tbl = 'event_meta' AND idx = 'idx_event_meta_key_value_event_id'",
	).Scan(&idx)
	if err != nil {
		t.Fatalf("no statistics for the current event_meta index: %v", err)
	}
}
