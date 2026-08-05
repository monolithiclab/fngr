package db

import (
	"database/sql"
	"testing"
)

// metaSources returns the (key, value) → source map for one event.
func metaSources(t *testing.T, db *sql.DB, id int64) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT key, value, source FROM event_meta WHERE event_id = ?", id)
	if err != nil {
		t.Fatalf("select meta: %v", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var key, value, source string
		if err := rows.Scan(&key, &value, &source); err != nil {
			t.Fatalf("scan meta: %v", err)
		}
		out[key+"="+value] = source
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	return out
}

// TestMigrate5_ClassifiesExistingMeta covers the reconstruction: rows the
// event's own text still yields become body-derived, everything else keeps
// the 'explicit' default. Getting this backwards would make the first edit
// of a migrated event delete metadata nothing put back.
func TestMigrate5_ClassifiesExistingMeta(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "standup with @sarah. shipped #ops work")
	// A tag the text does not mention, as `--meta` or `event tag` would
	// have left it, plus the author row every event carries.
	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'author', 'nicolas'), (1, 'tag', 'manual')",
	); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	want := map[string]string{
		"people=sarah":   "body",
		"tag=ops":        "body",
		"author=nicolas": "explicit",
		"tag=manual":     "explicit",
	}
	got := metaSources(t, db, 1)
	if len(got) != len(want) {
		t.Fatalf("meta = %v, want %v", got, want)
	}
	for tuple, source := range want {
		if got[tuple] != source {
			t.Errorf("%s source = %q, want %q", tuple, got[tuple], source)
		}
	}
}

// TestMigrate5_DefaultsToExplicit pins the safe half of the trade for a
// database with no events at all to re-derive from: a row nothing claims is
// explicit, because a stale tag costs a line of output and a lost one costs
// the note.
func TestMigrate5_DefaultsToExplicit(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "no tags here")
	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value) VALUES (1, 'people', 'bob')",
	); err != nil {
		t.Fatalf("seed meta: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if got := metaSources(t, db, 1)["people=bob"]; got != "explicit" {
		t.Errorf("people=bob source = %q, want %q", got, "explicit")
	}
}

// TestMigrate5_RejectsUnknownSource pins the CHECK. The two values are written
// as bare string literals from both Go and SQL, so a typo has no compiler to
// catch it — a row it produced would be one the body-tag sync could never
// match, silently and forever.
func TestMigrate5_RejectsUnknownSource(t *testing.T) {
	t.Parallel()

	db := testDB(t)
	seedLegacy(t, db, "a note")
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.Exec(
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (1, 'tag', 'x', 'derived')",
	); err == nil {
		t.Error("inserted source = 'derived', want a CHECK constraint failure")
	}
}
