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

// TestWithPager_NoPagerLeavesTheBufferAlone covers all three paths that start no
// pager: --no-pager, an Out with no file descriptor (every pipe), and a file
// that has one but is not a terminal (every redirect). The buffer is the
// process's now, so what each of them has to leave behind is that buffer, still
// pointed at its original destination and still holding what was written.
func TestWithPager_NoPagerLeavesTheBufferAlone(t *testing.T) {
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
		dest     func(*testing.T) (io.Writer, func() string)
		disabled bool
	}{
		{"disabled", buffer, true},
		{"dest has no fd", buffer, false},
		{"dest is a redirect to a file", tempFile, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dest, read := tt.dest(t)
			out := newBufferedOut(dest)

			closer := withPager(ioStreams{In: strings.NewReader(""), Out: out, Err: io.Discard}, tt.disabled)
			if _, err := io.WriteString(out, "one line\n"); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := read(); got != "" {
				t.Errorf("dest = %q before the flush, want it held in the buffer", got)
			}
			if err := closer(); err != nil {
				t.Errorf("closer returned error: %v", err)
			}
			if err := flushOut(out); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if got := read(); got != "one line\n" {
				t.Errorf("dest = %q after the flush, want the buffer's contents", got)
			}
		})
	}
}

// TestWithPager_UnbufferedOutGetsNoPager pins the one thing the type assertion
// gives up. Out is a *bufferedOut in every production run, so a plain writer
// means a test — and there is nothing to redirect. The terminal is stubbed
// *available* here on purpose: that is what makes this a test of the assertion
// rather than of the gate that would have refused anyway.
func TestWithPager_UnbufferedOutGetsNoPager(t *testing.T) {
	stubTerminal(t)
	var errBuf bytes.Buffer
	streams := ioStreams{In: strings.NewReader(""), Out: terminalStandIn(t), Err: &errBuf}

	if err := withPager(streams, false)(); err != nil {
		t.Errorf("closer err = %v, want nil", err)
	}
	if got := errBuf.String(); got != "" {
		t.Errorf("stderr = %q, want nothing said about a pager that never ran", got)
	}
}

// stubTerminal makes withPager take its pager branch. The terminal check is
// the last gate on it and no test process has a terminal on stdout, so these
// tests cannot be parallel.
func stubTerminal(t *testing.T) {
	t.Helper()
	orig := isTerminal
	isTerminal = func(int) bool { return true }
	t.Cleanup(func() { isTerminal = orig })
}

// terminalStandIn returns an *os.File to use as the buffer's destination:
// withPager needs a file descriptor before it consults isTerminal, and it is
// also what the child pager's own stdout is wired to.
func terminalStandIn(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stdout")) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestWithPager_PipesToPagerProcess is the pager path end to end, and with it
// two things nothing else covers: the half of redirect only the pager
// exercises (the buffer comes back pointed at dest, so run's own flush cannot
// write into a closed pipe), and the child's *stdout* being dest rather than
// the process's os.Stdout.
//
// The fake pager therefore writes on both channels: what it read goes to a
// file, and a marker goes to its own stdout, which must land in dest ahead of
// what fngr writes after the pager exits.
func TestWithPager_PipesToPagerProcess(t *testing.T) {
	captured := filepath.Join(t.TempDir(), "captured.txt")
	script := writeShellStub(t, "fake-pager.sh", "cat > "+captured+"\necho paged-by-the-child\n")
	t.Setenv("PAGER", script)
	stubTerminal(t)

	dest := terminalStandIn(t)
	out := newBufferedOut(dest)
	closer := withPager(ioStreams{In: strings.NewReader(""), Out: out, Err: io.Discard}, false)
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

	if _, err := io.WriteString(out, "after the pager\n"); err != nil {
		t.Fatalf("write after close: %v", err)
	}
	if err := flushOut(out); err != nil {
		t.Fatalf("flush after close: %v", err)
	}
	rest, err := os.ReadFile(dest.Name()) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if want := "paged-by-the-child\nafter the pager\n"; string(rest) != want {
		t.Errorf("dest = %q, want %q", string(rest), want)
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
	out := newBufferedOut(terminalStandIn(t))
	closer := withPager(ioStreams{In: strings.NewReader(""), Out: out, Err: &errBuf}, false)
	// The pager is gone by the time this is flushed; a lost write to it is not
	// the command's problem either, so only the buffer's own error matters.
	_, _ = io.WriteString(out, "one line\n")
	if err := closer(); err != nil && !errors.Is(err, syscall.EPIPE) {
		t.Errorf("closer err = %v, want nil or EPIPE", err)
	}
	if got := errBuf.String(); !strings.Contains(got, "pager exited with error") {
		t.Errorf("stderr = %q, want a pager warning", got)
	}
}

// TestWithPager_FallsBackWhenThePagerCannotStart pins the fallback: a $PAGER
// that will not run is worth a warning, not a failed listing. The buffer must
// come out pointed where it started, since nothing was redirected.
func TestWithPager_FallsBackWhenThePagerCannotStart(t *testing.T) {
	t.Setenv("PAGER", "/no/such/pager-binary-that-cannot-exist-xyz")
	stubTerminal(t)

	var errBuf bytes.Buffer
	dest := terminalStandIn(t)
	out := newBufferedOut(dest)

	if err := withPager(ioStreams{In: strings.NewReader(""), Out: out, Err: &errBuf}, false)(); err != nil {
		t.Errorf("closer err = %v, want nil", err)
	}
	if got := errBuf.String(); !strings.Contains(got, errPagerStartFailed.Error()) {
		t.Errorf("stderr = %q, want it to name %q", got, errPagerStartFailed)
	}
	if _, err := io.WriteString(out, "unpaged\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := flushOut(out); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got, err := os.ReadFile(dest.Name()) // #nosec G304 -- t.TempDir()
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "unpaged\n" {
		t.Errorf("dest = %q, want the buffer never redirected", string(got))
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
