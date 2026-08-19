package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
)

// newTestParser builds fngr's parser from the same kongOptions main() uses,
// with the writers and the exit function redirected. A nil exit is a no-op,
// which is what a test that only reads help text wants — Kong's --help hook
// calls Exit and expects it not to return.
//
// This lives here rather than beside any one of its callers because all three
// used to build the parser by hand and the oldest copy had already drifted,
// answering a parse error with the full command list where the shared options
// answer with a short usage — the difference the help tests exist to notice.
func newTestParser(t *testing.T, stdout, stderr io.Writer, exit func(int)) *kong.Kong {
	t.Helper()
	if exit == nil {
		exit = func(int) {}
	}
	var cli CLI
	parser, err := kong.New(&cli, append(kongOptions("test", "tester", exit),
		kong.Writers(stdout, stderr),
	)...)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	return parser
}

// tempDB is a database path under the test's own directory. Named rather than
// inlined so that no test can accidentally omit --db and hit $HOME:
// db.ResolvePath falls back to ~/.fngr.db, and a test is not allowed anywhere
// near the developer's journal. The file is not created — a caller that wants
// one opens it.
func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "fngr.db")
}

// newTestStore returns a store backed by a per-test SQLite file so streaming
// queries that hold open one connection while issuing a follow-up on another
// (e.g. event.ListSeq + loadMetaBatch) see the same data. Bare `:memory:`
// gives each pool connection its own empty database.
func newTestStore(t *testing.T) *event.Store {
	t.Helper()
	database, err := db.Open(tempDB(t), true)
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

// writeShellStub writes an executable /bin/sh script into a fresh temp dir and
// returns its path, skipping the test where there is no shell to run it.
//
// Six tests across body_test.go and pager_test.go stand a fake $EDITOR or
// $PAGER up this way, and they had drifted: the editor ones skipped on Windows
// and the pager ones did not, so a Windows run failed the pager tests instead
// of skipping them. The gosec exemption for the 0o755 mode also lived at each
// site, five copies of one annotation.
func writeShellStub(t *testing.T, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stub is POSIX-only")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil { // #nosec G306 -- a test-only stub must be executable
		t.Fatalf("write %s: %v", name, err)
	}
	return path
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

// newTestIOBuffered is the shape production actually has: Out wrapped in the
// *bufferedOut main installs. The two helpers above hand back a bare
// *bytes.Buffer that reads correctly the instant a command writes to it, which
// is what almost every CLI test wants — but it means those tests cannot see a
// missing flush, since there is nothing holding anything back. Returns
// (io, the buffer, the destination behind it, stderr): read dest to assert on
// what a user would have received, out to reach the flush.
func newTestIOBuffered(stdin string) (ioStreams, *bufferedOut, *bytes.Buffer, *bytes.Buffer) {
	dest := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	out := newBufferedOut(dest)
	return ioStreams{
		In:  strings.NewReader(stdin),
		Out: out,
		Err: errBuf,
	}, out, dest, errBuf
}

// errWriter fails every Write, standing in for a closed stdout. Here rather
// than in whichever verb's test file first needed it: five files reach for it
// now, the same reason newTestParser and newTestStore live here.
type errWriter struct{ err error }

func (w errWriter) Write(_ []byte) (int, error) { return 0, w.err }

// errReader fails every Read, standing in for an unreadable stdin.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }
