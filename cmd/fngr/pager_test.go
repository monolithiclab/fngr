package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestWithPager_BuffersUntilClose covers all three paths that start no pager:
// --no-pager, an Out with no file descriptor (every pipe), and a file that has
// one but is not a terminal (every redirect). All three still buffer — piping
// 250k events was one write(2) per line, and those are the paths a large
// listing actually takes.
func TestWithPager_BuffersUntilClose(t *testing.T) {
	t.Parallel()

	tempFile := func(t *testing.T) (io.Writer, func() string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "out.txt")
		f, err := os.Create(path) // #nosec G304 -- path is t.TempDir()
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f, func() string {
			b, err := os.ReadFile(path) // #nosec G304
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			return string(b)
		}
	}
	buffer := func(*testing.T) (io.Writer, func() string) {
		var b bytes.Buffer
		return &b, b.String
	}

	for _, tt := range []struct {
		name     string
		out      func(*testing.T) (io.Writer, func() string)
		disabled bool
	}{
		{"disabled", buffer, true},
		{"out has no fd", buffer, false},
		{"out is a redirect to a file", tempFile, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, read := tt.out(t)

			wrapped, closer := withPager(ioStreams{In: strings.NewReader(""), Out: out, Err: io.Discard}, tt.disabled)
			if _, err := io.WriteString(wrapped.Out, "one line\n"); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := read(); got != "" {
				t.Errorf("out = %q before close, want it held in the buffer", got)
			}
			if err := closer(); err != nil {
				t.Errorf("closer returned error: %v", err)
			}
			if got := read(); got != "one line\n" {
				t.Errorf("out = %q after close, want the buffer flushed", got)
			}
		})
	}
}

// TestWithPager_CloserReportsAFailedFlush pins the reason the closer's error is
// returned rather than logged: with Out buffered, the last chunk of a listing
// is written at flush and nowhere else, so swallowing that error would exit 0
// over output the user never received.
func TestWithPager_CloserReportsAFailedFlush(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("disk full")
	streams := ioStreams{In: strings.NewReader(""), Out: errWriter{err: wantErr}, Err: io.Discard}

	wrapped, closer := withPager(streams, true)
	if _, err := io.WriteString(wrapped.Out, "short line\n"); err != nil {
		t.Fatalf("write should be buffered, not attempted: %v", err)
	}
	if err := closer(); !errors.Is(err, wantErr) {
		t.Errorf("closer err = %v, want %v", err, wantErr)
	}
}

func TestWithPager_PreservesErrAndIsTTY(t *testing.T) {
	t.Parallel()
	var out, errBuf bytes.Buffer
	in := strings.NewReader("")

	original := ioStreams{In: in, Out: &out, Err: &errBuf, IsTTY: true}
	wrapped, closer := withPager(original, true) // disabled=true → returns original io
	defer func() { _ = closer() }()

	if wrapped.Err != &errBuf {
		t.Errorf("wrapped.Err = %v, want original errBuf", wrapped.Err)
	}
	if !wrapped.IsTTY {
		t.Errorf("wrapped.IsTTY = false, want true")
	}
}

// stubTerminal makes pagerWriter take its pager branch. The terminal check is
// the only gate on it and no test process has a terminal on stdout, so these
// tests cannot be parallel.
func stubTerminal(t *testing.T) {
	t.Helper()
	orig := isTerminal
	isTerminal = func(int) bool { return true }
	t.Cleanup(func() { isTerminal = orig })
}

// terminalStandIn returns an *os.File to pass as Out: pagerWriter needs a file
// descriptor before it consults isTerminal, and nothing is ever written to it
// on the pager path.
func terminalStandIn(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdout")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestPagerWriter_PipesToPagerProcess(t *testing.T) {
	dir := t.TempDir()
	captured := filepath.Join(dir, "captured.txt")

	// Fake pager: dump stdin to a file we can read.
	script := writeShellStub(t, "fake-pager.sh", "cat > "+captured+"\n")
	t.Setenv("PAGER", script)
	stubTerminal(t)

	out, closer := pagerWriter(terminalStandIn(t), io.Discard, false)
	if _, err := io.WriteString(out, "hello pager\n"); err != nil {
		t.Fatalf("write to pager: %v", err)
	}
	if err := closer(); err != nil {
		t.Fatalf("closer: %v", err)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read captured: %v", err)
	}
	if string(got) != "hello pager\n" {
		t.Errorf("captured = %q, want %q", string(got), "hello pager\n")
	}
}

// TestWithPager_WarnsWhenThePagerExitsNonZero pins the other half of the
// closer's contract: a pager that quit is the user's decision, so it is a
// warning on stderr and not the command's exit status.
func TestWithPager_WarnsWhenThePagerExitsNonZero(t *testing.T) {
	script := writeShellStub(t, "quitting-pager.sh", "exit 3\n")
	t.Setenv("PAGER", script)
	stubTerminal(t)

	var errBuf bytes.Buffer
	streams := ioStreams{In: strings.NewReader(""), Out: terminalStandIn(t), Err: &errBuf}
	wrapped, closer := withPager(streams, false)
	// The pager is gone by the time this is flushed; a lost write to it is not
	// the command's problem either, so only the buffer's own error matters.
	_, _ = io.WriteString(wrapped.Out, "one line\n")
	if err := closer(); err != nil && !errors.Is(err, syscall.EPIPE) {
		t.Errorf("closer err = %v, want nil or EPIPE", err)
	}
	if got := errBuf.String(); !strings.Contains(got, "pager exited with error") {
		t.Errorf("stderr = %q, want a pager warning", got)
	}
}

// TestPagerWriter_FallsBackWhenThePagerCannotStart pins the fallback: a $PAGER
// that will not run is worth a warning, not a failed listing.
func TestPagerWriter_FallsBackWhenThePagerCannotStart(t *testing.T) {
	t.Setenv("PAGER", "/no/such/pager-binary-that-cannot-exist-xyz")
	stubTerminal(t)

	var errBuf bytes.Buffer
	stdout := terminalStandIn(t)
	out, closer := pagerWriter(stdout, &errBuf, false)
	if out != io.Writer(stdout) {
		t.Errorf("out = %v, want the original writer back", out)
	}
	if err := closer(); err != nil {
		t.Errorf("closer: %v", err)
	}
	if got := errBuf.String(); !strings.Contains(got, errPagerStartFailed.Error()) {
		t.Errorf("stderr = %q, want it to name %q", got, errPagerStartFailed)
	}
}

func TestPagerCommand_MultiTokenPagerEnv(t *testing.T) {
	t.Setenv("PAGER", "less -FRX")
	got := pagerCommand()
	want := []string{"less", "-FRX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pagerCommand() = %v, want %v", got, want)
	}
}

func TestPagerCommand_FallbackWhenUnset(t *testing.T) {
	t.Setenv("PAGER", "")
	got := pagerCommand()
	want := []string{"less", "-FRX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pagerCommand() = %v, want %v", got, want)
	}
}
