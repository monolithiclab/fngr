// Fixtures shared across the package's test files. Named for the convention
// cmd/fngr/testhelpers_test.go already sets: testDB is reached from all seven,
// and leaving it in event_test.go made the Add-path file the one every other
// file silently depended on.

package event

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/db"
)

var ctx = context.Background()

// testDB returns a fresh per-test database backed by a temporary file. We
// avoid SQLite's bare `:memory:` URI because each connection in a *sql.DB
// pool sees its own empty in-memory database — which breaks any function
// that runs a follow-up query (e.g. loadMetaBatch) on a second connection
// while the first still holds an open Rows iterator.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fngr.db")
	database, err := db.Open(path, true)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// forgeParent wires parent_id directly, bypassing Reparent's guard, to build
// the corrupt tree fngr cannot write but can be handed: a `.fngr.db` picked up
// from an untrusted directory, a hand-edit, a half-written file.
func forgeParent(t *testing.T, database *sql.DB, child, parent int64) {
	t.Helper()
	if _, err := database.Exec("UPDATE events SET parent_id = ? WHERE id = ?", parent, child); err != nil {
		t.Fatalf("forge parent of %d as %d: %v", child, parent, err)
	}
}

// boundedCtx caps a traversal that is supposed to terminate on its own. If a
// regression brings the unbounded loop back, the query fails on the deadline
// and the test reports the wrong error in seconds instead of hanging until
// the whole package times out.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(cancel)
	return c
}
