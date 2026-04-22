package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
)

// newTestStore returns a store backed by a per-test SQLite file so streaming
// queries that hold open one connection while issuing a follow-up on another
// (e.g. event.ListSeq + loadMetaBatch) see the same data. Bare `:memory:`
// gives each pool connection its own empty database.
func newTestStore(t *testing.T) *event.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fngr.db")
	database, err := db.Open(path, true)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return event.NewStore(database)
}

func newTestIO(stdin string) (ioStreams, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return ioStreams{In: strings.NewReader(stdin), Out: out}, out
}
