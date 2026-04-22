package main

import (
	"bytes"
	"io"
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
	return ioStreams{
		In:    strings.NewReader(stdin),
		Out:   out,
		Err:   io.Discard,
		IsTTY: true,
	}, out
}

// newTestIOFull is for tests that need to inspect stderr (editor cancel
// notices) and/or vary IsTTY independently. Returns (io, stdout, stderr).
//
//lint:ignore U1000 consumed by upcoming add-body tests (task 3+ of body-input plan)
func newTestIOFull(stdin string, isTTY bool) (ioStreams, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	return ioStreams{
		In:    strings.NewReader(stdin),
		Out:   out,
		Err:   errBuf,
		IsTTY: isTTY,
	}, out, errBuf
}
