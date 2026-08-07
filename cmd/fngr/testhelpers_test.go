package main

import (
	"bytes"
	"context"
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

// forgeParent wires parent_id directly, past Reparent's cycle guard, to build
// the corrupt tree no fngr command can write but any `.fngr.db` can be handed:
// a hand-edit, a half-written file, someone else's database in the cwd.
func forgeParent(t *testing.T, s *event.Store, child, parent int64) {
	t.Helper()
	if _, err := s.DB.Exec("UPDATE events SET parent_id = ? WHERE id = ?", parent, child); err != nil {
		t.Fatalf("forge parent of %d as %d: %v", child, parent, err)
	}
}

// stubEditor swaps the package-level launchEditor for the duration of the
// test and restores it afterwards. Because the seam is package state, a test
// that calls this must NOT call t.Parallel() — the race detector flags
// concurrent swaps, and two parallel tests would see each other's stub.
func stubEditor(t *testing.T, fn func(initial string) (string, error)) {
	t.Helper()
	orig := launchEditor
	launchEditor = fn
	t.Cleanup(func() { launchEditor = orig })
}

// forbidEditor fails the test if the editor is launched. It is the editor-side
// mirror of body_test.go's forbiddenReader: for cases that must be rejected
// before any editor runs, the launch itself is the defect.
func forbidEditor(t *testing.T) {
	t.Helper()
	stubEditor(t, func(string) (string, error) {
		t.Error("editor launched although there is no terminal to run it in")
		return "", nil
	})
}

// assertNoEvents fails the test unless the store is empty. A rejected `add`
// must not leave a partial write behind.
func assertNoEvents(t *testing.T, s *event.Store) {
	t.Helper()
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("created %d events, want 0", len(events))
	}
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
