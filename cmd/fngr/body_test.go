package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }

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
// is gone; none of the cases below may go near stdin.
func TestResolveBody_NeverTouchesStdinWhenBodyIsDecided(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		useEditor bool
		isTTY     bool
		want      string
	}{
		{name: "args", args: []string{"note"}, want: "note"},
		{name: "args-and-editor", args: []string{"note"}, useEditor: true, want: "note::edited"},
		{name: "editor", useEditor: true, want: "::edited"},
		{name: "tty", isTTY: true, want: "::edited"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// NOTE: no t.Parallel() — launchEditor is package-level state.
			stubEditor(t, func(initial string) (string, error) { return initial + "::edited", nil })

			io := ioStreams{In: forbiddenReader{t: t}, IsTTY: tc.isTTY}
			got, err := resolveBody(tc.args, tc.useEditor, io)
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
	if runtime.GOOS == "windows" {
		t.Skip("shell-script editor stub is POSIX-only")
	}

	dir := t.TempDir()
	editor := filepath.Join(dir, "fake-editor.sh")
	// The fake editor appends "::edited" to whatever's in the file.
	script := "#!/bin/sh\nprintf '%s::edited' \"$(cat \"$1\")\" > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil { // #nosec G306 -- test-only fake editor must be executable
		t.Fatalf("write fake editor: %v", err)
	}

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
	if runtime.GOOS == "windows" {
		t.Skip("shell-script editor stub is POSIX-only")
	}

	dir := t.TempDir()
	editor := filepath.Join(dir, "fake-editor.sh")
	// Truncate the file to empty.
	script := "#!/bin/sh\n: > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil { // #nosec G306 -- test-only fake editor must be executable
		t.Fatalf("write fake editor: %v", err)
	}

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", editor)

	_, err := realLaunchEditor("seed")
	if err != errCancel {
		t.Errorf("err = %v, want errCancel", err)
	}
}

func TestRealLaunchEditor_VisualOverridesEditor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script editor stub is POSIX-only")
	}

	dir := t.TempDir()
	visual := filepath.Join(dir, "visual.sh")
	editor := filepath.Join(dir, "editor.sh")
	if err := os.WriteFile(visual, []byte("#!/bin/sh\nprintf 'from-visual' > \"$1\"\n"), 0o755); err != nil { // #nosec G306 -- test-only fake editor must be executable
		t.Fatalf("write visual: %v", err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'from-editor' > \"$1\"\n"), 0o755); err != nil { // #nosec G306 -- test-only fake editor must be executable
		t.Fatalf("write editor: %v", err)
	}

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
		// Row 1: args alone, TTY.
		{name: "args-only-tty", args: []string{"foo", "bar"}, isTTY: true, wantBody: "foo bar"},
		// Row 2: args + piped stdin. Args win and stdin is left unread —
		// noticing the conflict would cost a blocking peek on every run.
		{name: "args-and-stdin-args-win", args: []string{"x"}, isTTY: false, stdin: "y", wantBody: "x"},
		// Row 3: args + editor, TTY = pre-fill.
		{name: "args-and-editor", args: []string{"foo", "bar"}, useEditor: true, isTTY: true, stubBody: "foo bar baz", wantInit: "foo bar", wantBody: "foo bar baz"},
		// Row 4: args + editor + piped = pre-filled editor, stdin unread.
		{name: "args-editor-stdin", args: []string{"x"}, useEditor: true, isTTY: false, stdin: "y", stubBody: "x edited", wantInit: "x", wantBody: "x edited"},
		// Row 5: bare add in TTY = editor opened empty.
		{name: "bare-tty-launches-editor", isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Row 6: bare add piped = stdin.
		{name: "bare-piped-reads-stdin", isTTY: false, stdin: "piped body", wantBody: "piped body"},
		// Row 7: -e in TTY = editor empty.
		{name: "edit-flag-tty", useEditor: true, isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Row 8: -e piped = editor, stdin unread. Empty piped stdin takes the
		// same branch — -e no longer inspects stdin at all, so there is
		// nothing left to distinguish the two.
		{name: "edit-flag-piped", useEditor: true, isTTY: false, stdin: "y", stubBody: "from editor", wantInit: "", wantBody: "from editor"},
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
