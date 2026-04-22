package main

import (
	"errors"
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
		{name: "empty-input", input: "", wantErr: "event text cannot be empty"},
		{name: "whitespace-only", input: "   \n\t\n", wantErr: "event text cannot be empty"},
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

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("boom") }

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
		// Row 2: args + piped stdin = error.
		{name: "args-and-stdin-error", args: []string{"x"}, isTTY: false, stdin: "y", wantErr: "ambiguous"},
		// Row 3: args + editor, TTY = pre-fill.
		{name: "args-and-editor", args: []string{"foo", "bar"}, useEditor: true, isTTY: true, stubBody: "foo bar baz", wantInit: "foo bar", wantBody: "foo bar baz"},
		// Row 4: args + editor + piped = error (caught by args+stdin first).
		{name: "args-editor-stdin-error", args: []string{"x"}, useEditor: true, isTTY: false, stdin: "y", wantErr: "ambiguous"},
		// Row 5: bare add in TTY = editor opened empty.
		{name: "bare-tty-launches-editor", isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Row 6: bare add piped = stdin.
		{name: "bare-piped-reads-stdin", isTTY: false, stdin: "piped body", wantBody: "piped body"},
		// Row 7: -e in TTY = editor empty.
		{name: "edit-flag-tty", useEditor: true, isTTY: true, stubBody: "from editor", wantInit: "", wantBody: "from editor"},
		// Row 8: -e piped = error.
		{name: "edit-flag-piped-error", useEditor: true, isTTY: false, stdin: "y", wantErr: "--edit conflicts"},
		// Editor cancel (empty save) propagates errCancel.
		{name: "editor-cancel", useEditor: true, isTTY: true, stubErr: errCancel, wantInit: "", wantErr: "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// NOTE: no t.Parallel() — launchEditor is package-level state
			// shared across cases; race detector flags concurrent swaps.
			var stubCalled bool
			var gotInit string
			origEditor := launchEditor
			launchEditor = func(initial string) (string, error) {
				stubCalled = true
				gotInit = initial
				return tc.stubBody, tc.stubErr
			}
			t.Cleanup(func() { launchEditor = origEditor })

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
