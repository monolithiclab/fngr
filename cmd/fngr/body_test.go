package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadStdin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "plain", input: "hello", want: "hello"},
		{name: "trim-trailing-newline", input: "hello\n", want: "hello"},
		{name: "trim-leading-and-trailing-whitespace", input: "  \n hello world \n\n", want: "hello world"},
		{name: "preserve-internal-newlines", input: "line one\nline two\n", want: "line one\nline two"},
		{name: "empty-input", input: "", wantErr: "event title cannot be empty"},
		{name: "whitespace-only", input: "   \n\t\n", wantErr: "event title cannot be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := readStdin(strings.NewReader(tc.input))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readStdin: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadStdin_ReadError(t *testing.T) {
	t.Parallel()
	_, err := readStdin(errReader{})
	if err == nil || !strings.Contains(err.Error(), "read stdin") {
		t.Errorf("err = %v, want 'read stdin'", err)
	}
}

func TestReadStdin_ExceedsLimit(t *testing.T) {
	t.Parallel()
	// One byte over the cap is enough to trip the limit branch.
	huge := strings.Repeat("a", maxStdinBytes+1)
	_, err := readStdin(strings.NewReader(huge))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want 'exceeds' error", err)
	}
}

// forbiddenReader fails the test if anything reads it. It stands in for a
// non-TTY stdin that is open but idle — a terminal misdetected as a pipe, or
// the read half of a pipe whose writer has not written yet. A real one of
// those delivers neither a byte nor EOF, so a read never returns; here the
// read is reported instead of hung, which fails in microseconds rather than
// on a timeout.
type forbiddenReader struct{ t *testing.T }

func (r forbiddenReader) Read(_ []byte) (int, error) {
	r.t.Helper()
	r.t.Error("stdin was read although the body was already determined; a real idle pipe would hang here")
	return 0, io.EOF
}

// TestResolveBody_NeverTouchesStdinWhenBodyIsDecided is the H4 regression
// guard. resolveBody used to peek stdin up front to detect args+stdin and
// -e+stdin conflicts, so every non-TTY invocation paid a read — and an idle
// pipe hung `fngr add "note"` forever with no output and no timeout. The peek
// is gone; none of the cases below may go near stdin, including the one that
// errors (H7 rejects -e without a terminal, and it must do so without
// reading, or it reintroduces the very hang H4 removed).
func TestResolveBody_NeverTouchesStdinWhenBodyIsDecided(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		useEditor bool
		isTTY     bool
		want      string
		wantErr   string
	}{
		{name: "args", args: []string{"note"}, want: "note"},
		{name: "args-and-editor", args: []string{"note"}, useEditor: true, isTTY: true, want: "note::edited"},
		{name: "editor", useEditor: true, isTTY: true, want: "::edited"},
		{name: "tty", isTTY: true, want: "::edited"},
		{name: "editor-without-terminal", useEditor: true, wantErr: errEditNeedsTTY.Error()},
		{name: "args-editor-without-terminal", args: []string{"note"}, useEditor: true, wantErr: errEditNeedsTTY.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// NOTE: no t.Parallel() — launchEditor is package-level state.
			stubEditor(t, func(initial string) (string, error) { return initial + "::edited", nil })

			io := ioStreams{In: forbiddenReader{t: t}, IsTTY: tc.isTTY}
			got, err := resolveBody(tc.args, tc.useEditor, io)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBody: %v", err)
			}
			if got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRealLaunchEditor_ExecAndReadback(t *testing.T) {
	// The fake editor appends "::edited" to whatever's in the file.
	editor := writeShellStub(t, "fake-editor.sh", "printf '%s::edited' \"$(cat \"$1\")\" > \"$1\"\n")

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	got, err := realLaunchEditor("seed")
	if err != nil {
		t.Fatalf("realLaunchEditor: %v", err)
	}
	if got != "seed::edited" {
		t.Errorf("body = %q, want %q", got, "seed::edited")
	}
}

func TestRealLaunchEditor_NoEditorConfigured(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	_, err := realLaunchEditor("")
	if err == nil || !strings.Contains(err.Error(), "no editor configured") {
		t.Errorf("err = %v, want 'no editor configured'", err)
	}
}

func TestRealLaunchEditor_EmptySaveCancels(t *testing.T) {
	editor := writeShellStub(t, "fake-editor.sh", ": > \"$1\"\n") // truncate to empty

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	_, err := realLaunchEditor("seed")
	if err != errCancel {
		t.Errorf("err = %v, want errCancel", err)
	}
}

func TestRealLaunchEditor_VisualOverridesEditor(t *testing.T) {
	visual := writeShellStub(t, "visual.sh", "printf 'from-visual' > \"$1\"\n")
	editor := writeShellStub(t, "editor.sh", "printf 'from-editor' > \"$1\"\n")

	t.Setenv("VISUAL", visual)
	t.Setenv("EDITOR", editor)

	got, err := realLaunchEditor("")
	if err != nil {
		t.Fatalf("realLaunchEditor: %v", err)
	}
	if got != "from-visual" {
		t.Errorf("body = %q, want 'from-visual'", got)
	}
}

// TestRealLaunchEditor_NonZeroExitIsAnError covers the path the signal bracket
// made reachable: an editor that quits on Ctrl-C instead of handling it now
// leaves fngr alive to report the failure, where before both died together.
// `signal: interrupt` arrives wrapped this same way.
func TestRealLaunchEditor_NonZeroExitIsAnError(t *testing.T) {
	editor := writeShellStub(t, "quitting-editor.sh", "exit 3\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	_, err := realLaunchEditor("seed")
	if err == nil || !strings.Contains(err.Error(), "editor exited:") {
		t.Errorf("err = %v, want it to report the editor's own failure", err)
	}
}

// TestRealLaunchEditor_TokenizesArgs pins the half that used to differ from
// $PAGER: an editor value carrying flags. `EDITOR="code -w"` was passed whole
// as a filename and failed with `fork/exec .../code -w: no such file or
// directory`, while `PAGER="less -R"` had worked since the pager landed.
func TestRealLaunchEditor_TokenizesArgs(t *testing.T) {
	// Writes its own leading arguments, so the test fails if they are lost as
	// well as if the whole value was taken for a filename.
	editor := writeShellStub(t, "fake-editor.sh", "printf 'flags:%s,%s' \"$1\" \"$2\" > \"$3\"\n")

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor+"  -w --wait")

	got, err := realLaunchEditor("")
	if err != nil {
		t.Fatalf("realLaunchEditor: %v", err)
	}
	if got != "flags:-w,--wait" {
		t.Errorf("body = %q, want the editor's own flags to have reached it", got)
	}
}

// signalHelperEnv names the mode a re-exec'd test binary should run as the
// child of TestIgnoreTerminalSignals. Empty means "not the helper".
const signalHelperEnv = "FNGR_TEST_SIGNAL_HELPER"

// TestIgnoreTerminalSignals checks both halves of the bracket in a subprocess,
// because both halves are assertions about whether *this process* survives a
// signal — in-process, "the bracket works" is unobservable (nothing happens)
// and "the restore works" kills the test binary, taking every other result in
// the package with it and naming no culprit.
//
// The restore half is not ceremony. The first version of ignoreTerminalSignals
// used signal.Ignore/signal.Reset, whose restore silently does nothing, and an
// in-process test passed green against it.
func TestIgnoreTerminalSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals only")
	}
	if os.Getenv(signalHelperEnv) != "" {
		runSignalHelper(t)
		return
	}

	for _, tt := range []struct {
		mode     string
		wantExit int // -1 for "killed by SIGINT"
	}{
		{"bracketed", 0},
		{"restored", -1},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			// Not parallel: each subtest forks the test binary.
			cmd := exec.Command(os.Args[0], "-test.run=^TestIgnoreTerminalSignals$") // #nosec G204 -- os.Args[0] is this test binary.
			cmd.Env = append(os.Environ(), signalHelperEnv+"="+tt.mode)
			out, err := cmd.CombinedOutput()

			killed := false
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				ws, ok := exitErr.Sys().(syscall.WaitStatus)
				killed = ok && ws.Signaled() && ws.Signal() == syscall.SIGINT
			}
			switch {
			case tt.wantExit == 0 && err != nil:
				t.Errorf("helper died (%v), want it to survive SIGINT inside the bracket\n%s", err, out)
			case tt.wantExit == -1 && !killed:
				t.Errorf("helper survived (err=%v), want SIGINT to kill it once restored — "+
					"a restore that does not restore leaves fngr deaf to Ctrl-C for good\n%s", err, out)
			}
		})
	}
}

// runSignalHelper is the child half of TestIgnoreTerminalSignals: raise SIGINT
// at ourselves either inside the bracket or after lifting it, and let the exit
// status carry the answer back.
func runSignalHelper(t *testing.T) {
	t.Helper()
	restore := ignoreTerminalSignals()
	if os.Getenv(signalHelperEnv) == "restored" {
		restore()
	} else {
		defer restore()
	}

	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := self.Signal(os.Interrupt); err != nil {
		t.Fatalf("raise SIGINT: %v", err)
	}
	// Delivery is asynchronous, so give it a window to have killed us in.
	time.Sleep(200 * time.Millisecond)
}

// TestIgnoreTerminalSignals_ChildIsStillInterruptible pins the second reason
// the bracket cannot be signal.Ignore: exec preserves an ignored disposition
// where it resets a caught one, so an editor launched under Ignore inherits
// SIG_IGN and cannot be Ctrl-C'd — least of all `EDITOR="code -w"`, a wrapper
// script with no SIGINT handling of its own.
func TestIgnoreTerminalSignals_ChildIsStillInterruptible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals only")
	}
	defer ignoreTerminalSignals()()

	child := exec.Command("/bin/sh", "-c", "sleep 30")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()

	// Started, not necessarily scheduled; a signal to a live process is
	// delivered either way, so no wait for readiness is needed.
	if err := child.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal child: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = child.Process.Kill()
		t.Error("child ignored SIGINT: the bracket leaked SIG_IGN across exec, so the editor cannot be interrupted")
	}
}

func TestResolveBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		args      []string
		useEditor bool
		isTTY     bool
		stdin     string
		// Stub return values for launchEditor.
		stubBody string
		stubErr  error
		// Expectations.
		wantInit string // expected initial passed to stub editor; "" means stub should not be called
		wantBody string
		wantErr  string // substring; "" means no error
	}{
		// -e without a terminal: rejected whatever else is on offer. Args
		// present and stdin holding data both lose to it, so this covers the
		// spec's "any / present / non-TTY" row on its widest input.
		{name: "args-editor-no-terminal", args: []string{"x"}, useEditor: true, isTTY: false, stdin: "y", wantErr: errEditNeedsTTY.Error()},
		// Args alone, TTY.
		{name: "args-only-tty", args: []string{"foo", "bar"}, isTTY: true, wantBody: "foo bar"},
		// Args + piped stdin. Args win and stdin is left unread — noticing the
		// conflict would cost a blocking peek on every run.
		{name: "args-and-stdin-args-win", args: []string{"x"}, isTTY: false, stdin: "y", wantBody: "x"},
		// Args + editor, TTY = pre-fill.
		{name: "args-and-editor", args: []string{"foo", "bar"}, useEditor: true, isTTY: true, stubBody: "foo bar baz", wantInit: "foo bar", wantBody: "foo bar baz"},
		// -e in TTY = editor opened empty.
		{name: "edit-flag-tty", useEditor: true, isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Bare add in TTY = editor opened empty.
		{name: "bare-tty-launches-editor", isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Bare add piped = stdin.
		{name: "bare-piped-reads-stdin", isTTY: false, stdin: "piped body", wantBody: "piped body"},
		// Editor cancel (empty save) propagates errCancel.
		{name: "editor-cancel", useEditor: true, isTTY: true, stubErr: errCancel, wantInit: "", wantErr: "cancelled"},
		{name: "empty-arg-rejected", args: []string{""}, isTTY: true, wantErr: "event title cannot be empty"},
		{name: "whitespace-only-arg-rejected", args: []string{" ", "\t"}, isTTY: true, wantErr: "event title cannot be empty"},
		// Non-TTY with EMPTY stdin (scripts, CI, cron): args must win, not
		// trip the ambiguity guard. This is the dogfooding bug fix.
		{name: "args-nontty-empty-stdin", args: []string{"foo", "bar"}, isTTY: false, stdin: "", wantBody: "foo bar"},
		// Bare add, non-TTY, nothing piped: no body source at all → reject,
		// don't fall through to launching an editor in a non-interactive context.
		{name: "bare-nontty-empty-stdin", isTTY: false, stdin: "", wantErr: "event title cannot be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// NOTE: no t.Parallel() — launchEditor is package-level state
			// shared across cases; race detector flags concurrent swaps.
			var stubCalled bool
			var gotInit string
			stubEditor(t, func(initial string) (string, error) {
				stubCalled = true
				gotInit = initial
				return tc.stubBody, tc.stubErr
			})

			io, _, _ := newTestIOFull(tc.stdin, tc.isTTY)
			got, err := resolveBody(tc.args, tc.useEditor, io)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBody: %v", err)
			}
			if got != tc.wantBody {
				t.Errorf("body = %q, want %q", got, tc.wantBody)
			}
			// wantInit "" with stubCalled=true is a valid expectation
			// (editor opened empty); only check init equality when stub fired.
			if stubCalled && gotInit != tc.wantInit {
				t.Errorf("editor initial = %q, want %q", gotInit, tc.wantInit)
			}
		})
	}
}

// TestEditBody_FlushesBeforeLaunchingTheEditor is the editor's half of the
// buffered-stdout contract, the same one confirm keeps: a child that takes the
// screen must not leave fngr's own output stranded behind it.
func TestEditBody_FlushesBeforeLaunchingTheEditor(t *testing.T) {
	// NOTE: no t.Parallel() — stubEditor swaps package-level state.
	var dest bytes.Buffer
	out := newBufferedOut(&dest)
	if _, err := io.WriteString(out, "already written\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	stubEditor(t, func(string) (string, error) {
		if got := dest.String(); got != "already written\n" {
			t.Errorf("stdout = %q when the editor took the terminal, want it flushed", got)
		}
		return "edited", nil
	})

	got, err := editBody("", out)
	if err != nil {
		t.Fatalf("editBody: %v", err)
	}
	if got != "edited" {
		t.Errorf("body = %q, want %q", got, "edited")
	}
}

// TestEditBody_ReportsAFailedFlush pins the other direction: output that could
// not be written is an error, and there is no point handing the terminal to an
// editor whose result is going nowhere either.
func TestEditBody_ReportsAFailedFlush(t *testing.T) {
	// NOTE: no t.Parallel() — forbidEditor swaps package-level state.
	forbidEditor(t)
	wantErr := errors.New("disk full")
	out := newBufferedOut(errWriter{err: wantErr})
	if _, err := io.WriteString(out, "held back\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := editBody("", out); !errors.Is(err, wantErr) {
		t.Errorf("editBody err = %v, want %v", err, wantErr)
	}
}
