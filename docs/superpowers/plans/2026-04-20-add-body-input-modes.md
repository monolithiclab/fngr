# Add body-input modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land three Add command ergonomics items — multi-arg body, stdin body, and `$EDITOR` support — driven by a single `resolveBody` dispatch in a new `cmd/fngr/body.go`.

**Architecture:** New `cmd/fngr/body.go` owns body-source resolution; `AddCmd.Run` calls it and treats `errCancel` as a clean exit. `ioStreams` grows `Err` (cancel notice destination) and `IsTTY` (injected stdin TTY-ness for testability). `launchEditor` is a package-level `var` so tests can stub the exec without shelling out.

**Tech Stack:** Go 1.22+, Kong (CLI), `golang.org/x/term` (promoted to direct dep), `os/exec` (editor invocation), `os.CreateTemp` (editor temp file).

---

## File Structure

- **Create**:
  - `cmd/fngr/body.go` — `resolveBody`, `readStdin`, `launchEditor` (var), `realLaunchEditor`, `errCancel`.
  - `cmd/fngr/body_test.go` — table-driven tests for `resolveBody` and `readStdin`; integration test for `realLaunchEditor` via shell-script trampoline.
- **Modify**:
  - `cmd/fngr/store.go` — `ioStreams` gains `Err io.Writer` and `IsTTY bool`.
  - `cmd/fngr/main.go` — wire `Err` (`os.Stderr`) and `IsTTY` (`term.IsTerminal`).
  - `cmd/fngr/testhelpers_test.go` — `newTestIO` populates `Err` with a discard buffer and `IsTTY: true`; new `newTestIOFull(stdin, isTTY)` returns the err buffer too.
  - `cmd/fngr/pager_test.go` — two literal `ioStreams{...}` constructions need `Err`/`IsTTY` fields.
  - `cmd/fngr/dispatch_test.go` — `dispatch` helper grows `isTTY bool` param; two literal `ioStreams{...}` updated; three new `add-multiarg`/`add-stdin`/`add-editor` test entries.
  - `cmd/fngr/add.go` — `Text string` → `Args []string`, new `Edit bool` flag, `Run` uses `resolveBody`.
  - `cmd/fngr/add_test.go` — five existing `&AddCmd{Text: ...}` literals migrate to `Args: []string{...}`; new tests for each body source.
  - `go.mod` / `go.sum` — promote `golang.org/x/term` from indirect to direct.
  - `CLAUDE.md` — three bullet updates (`add.go`, `store.go`, new `body.go`).
  - `README.md` — Add command examples refresh.
  - `docs/superpowers/roadmap.md` — mark three items done in the "Add command ergonomics" section.

---

## Task 1: Extend `ioStreams` (Err + IsTTY)

**Files:**
- Modify: `cmd/fngr/store.go`
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/testhelpers_test.go`
- Modify: `cmd/fngr/pager_test.go`
- Modify: `cmd/fngr/dispatch_test.go`

This is a plumbing-only task. No behavior changes; the new fields are unused after the task lands. The `make ci` pass at the end is the success signal — if every package still builds and every existing test still passes, the wiring is right.

- [ ] **Step 1.1: Extend `ioStreams` struct**

Edit `cmd/fngr/store.go` and replace the struct with:

```go
type ioStreams struct {
	In    io.Reader
	Out   io.Writer
	Err   io.Writer // editor cancel notice; stays out of stdout for script piping
	IsTTY bool      // true when stdin is an interactive terminal
}
```

- [ ] **Step 1.2: Wire fields in `main.go`**

Replace the existing `ctx.Bind(ioStreams{In: os.Stdin, Out: os.Stdout})` line with:

```go
ctx.Bind(ioStreams{
	In:    os.Stdin,
	Out:   os.Stdout,
	Err:   os.Stderr,
	IsTTY: term.IsTerminal(int(os.Stdin.Fd())),
})
```

Add the import: `"golang.org/x/term"`.

- [ ] **Step 1.3: Update `newTestIO` to default the new fields**

Edit `cmd/fngr/testhelpers_test.go`:

```go
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
```

Add the `"io"` import to `testhelpers_test.go`.

- [ ] **Step 1.4: Update `pager_test.go` literal constructions**

Edit the two `ioStreams{In: strings.NewReader(""), Out: &out}` literals in `cmd/fngr/pager_test.go` to:

```go
io := ioStreams{In: strings.NewReader(""), Out: &out, Err: io.Discard, IsTTY: false}
```

Note: this file already imports `"io"`.

- [ ] **Step 1.5: Update `dispatch` helper signature in `dispatch_test.go`**

Replace the `dispatch` function header and body:

```go
func dispatch(t *testing.T, argv []string, stdin string, isTTY bool) (string, error) {
	t.Helper()

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kongVars("test", "tester"),
		kong.Exit(func(int) {}),
		kong.Writers(&bytes.Buffer{}, &bytes.Buffer{}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	kctx, err := parser.Parse(argv)
	if err != nil {
		return "", err
	}

	out := &bytes.Buffer{}
	kctx.BindTo(newTestStore(t), (*eventStore)(nil))
	kctx.Bind(ioStreams{
		In:    strings.NewReader(stdin),
		Out:   out,
		Err:   io.Discard,
		IsTTY: isTTY,
	})

	if err := kctx.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}
```

Add `"io"` import.

Update the existing call in `TestKongDispatch_AllCommands`:

```go
_, err := dispatch(t, tc.argv, tc.stdin, true)
```

Find the second `ioStreams{...}` literal in `TestKongDispatch_AddThenListEndToEnd` (around line 116) and update similarly:

```go
kctx.Bind(ioStreams{
	In:    strings.NewReader(""),
	Out:   out,
	Err:   io.Discard,
	IsTTY: true,
})
```

- [ ] **Step 1.6: Run full CI**

```
make ci
```

Expected: all packages build, all existing tests pass, no lint warnings. (The new `Err` and `IsTTY` fields are written but unread — that's fine, no warning fires for unused struct fields.)

- [ ] **Step 1.7: Commit**

```bash
git add cmd/fngr/store.go cmd/fngr/main.go cmd/fngr/testhelpers_test.go cmd/fngr/pager_test.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
refactor(cmd): ioStreams gains Err and IsTTY for body-input modes

Plumbing for the upcoming add-command body resolution work. Production
wires Err to os.Stderr and IsTTY to term.IsTerminal(stdin.Fd); the
dispatch helper grows an isTTY parameter so future add-body tests can
simulate piped vs interactive without touching real fds. No behavior
change today — every existing call site preserves prior semantics.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `body.go` — `errCancel`, `readStdin`, `resolveBody` (TDD)

**Files:**
- Create: `cmd/fngr/body.go`
- Create: `cmd/fngr/body_test.go`

This task implements the dispatch logic and the pure stdin reader. `launchEditor` is introduced as a stubbable `var` (returning a placeholder error in production for now); the real implementation lands in Task 3.

- [ ] **Step 2.1: Write the failing test for `readStdin`**

Create `cmd/fngr/body_test.go`:

```go
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
```

- [ ] **Step 2.2: Run the test to confirm it fails**

```
go test ./cmd/fngr/ -run TestReadStdin -v
```

Expected: `FAIL` with `undefined: readStdin`.

- [ ] **Step 2.3: Implement `readStdin` and the file skeleton**

Create `cmd/fngr/body.go`:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// errCancel signals a deliberate user cancel (empty editor save). AddCmd.Run
// recognises it and converts to (nil error + status 0).
var errCancel = errors.New("cancelled")

// launchEditor is overridable so tests can stub the editor exec without
// shelling out. Task 3 replaces this with realLaunchEditor.
var launchEditor = func(initial string) (string, error) {
	return "", errors.New("editor not yet implemented")
}

// resolveBody applies the body-source dispatch table from the spec
// (docs/superpowers/specs/2026-04-20-add-body-input-modes-design.md).
// Returns the body string or an error. errCancel signals a deliberate
// editor cancel.
func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
	hasArgs := len(args) > 0
	piped := !io.IsTTY

	switch {
	case hasArgs && piped:
		return "", fmt.Errorf("ambiguous: body via both args and stdin; pick one")
	case !hasArgs && useEditor && piped:
		return "", fmt.Errorf("--edit conflicts with piped stdin")
	case hasArgs && useEditor:
		return launchEditor(strings.Join(args, " "))
	case hasArgs:
		return strings.Join(args, " "), nil
	case useEditor:
		return launchEditor("")
	case piped:
		return readStdin(io.In)
	default:
		return launchEditor("")
	}
}

func readStdin(in io.Reader) (string, error) {
	raw, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", fmt.Errorf("event text cannot be empty")
	}
	return body, nil
}
```

- [ ] **Step 2.4: Run readStdin tests; expect pass**

```
go test ./cmd/fngr/ -run TestReadStdin -v
```

Expected: PASS for all subtests.

- [ ] **Step 2.5: Write the table-driven `resolveBody` test**

Append to `cmd/fngr/body_test.go`:

```go
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
			t.Parallel()

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
```

**Note:** `t.Parallel()` inside the loop and a per-case `launchEditor` swap together require Go 1.22+ loop-var semantics (each iteration captures its own `tc`). The repo is on Go 1.22+ (modules pin via `go.mod`).

**Caveat:** `launchEditor` is package-level state. Even with `t.Cleanup` restoring it, two parallel cases could race on the swap. Make this test file's cases run sequentially under a parent `t.Run` if the race detector flags it; first try the parallel form because the assignment is single-pointer-sized and observably stable per-case if Go runs subtests serially.

If the race detector complains, replace `t.Parallel()` inside the loop with sequential execution by removing that call. Outer `t.Parallel()` on `TestResolveBody` itself stays.

No import changes needed at this step.

- [ ] **Step 2.6: Run resolveBody tests; expect pass**

```
go test ./cmd/fngr/ -run TestResolveBody -v -race
```

Expected: PASS for all 9 subtests. If race detector flags `launchEditor`, drop the inner `t.Parallel()` and re-run.

- [ ] **Step 2.7: Run full body_test suite + lint**

```
go test ./cmd/fngr/ -run 'TestReadStdin|TestResolveBody' -v && make lint
```

Expected: all tests pass, no lint complaints.

- [ ] **Step 2.8: Commit**

```bash
git add cmd/fngr/body.go cmd/fngr/body_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): body.go dispatches add-event body source

Resolve the body string from one of three sources — args (joined with
space), stdin (when piped), or $EDITOR (when -e or bare-in-TTY) — per
the eight-row dispatch table in the design spec. Conflict cases
(args+stdin, --edit+stdin) error loudly. Editor stub returns
"not yet implemented" until Task 3 wires the real exec path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `realLaunchEditor` (real `exec.Command`) + integration test

**Files:**
- Modify: `cmd/fngr/body.go`
- Modify: `cmd/fngr/body_test.go`

- [ ] **Step 3.1: Write the failing integration test**

Append to `cmd/fngr/body_test.go`:

```go
import (
	"os"
	"path/filepath"
	"runtime"
)

func TestRealLaunchEditor_ExecAndReadback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script editor stub is POSIX-only")
	}

	dir := t.TempDir()
	editor := filepath.Join(dir, "fake-editor.sh")
	// The fake editor appends "::edited" to whatever's in the file.
	script := "#!/bin/sh\nprintf '%s::edited' \"$(cat \"$1\")\" > \"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
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
	if err := os.WriteFile(editor, []byte(script), 0o755); err != nil {
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
	if err := os.WriteFile(visual, []byte("#!/bin/sh\nprintf 'from-visual' > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write visual: %v", err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'from-editor' > \"$1\"\n"), 0o755); err != nil {
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
```

These tests are NOT `t.Parallel()` because they all touch process env via `t.Setenv` (which is automatically cleaned up; parallel use is allowed but only when `t.Setenv` is called from a `t.Parallel` subtest which Go 1.22 supports — but mixing here adds no value).

**Important:** the existing `import` block at the top of `body_test.go` does NOT include `os`/`path/filepath`/`runtime`. Add them to the existing import block.

Final import block at the top of `body_test.go`:

```go
import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)
```

- [ ] **Step 3.2: Run the new tests; expect failure**

```
go test ./cmd/fngr/ -run TestRealLaunchEditor -v
```

Expected: FAIL with `undefined: realLaunchEditor`.

- [ ] **Step 3.3: Implement `realLaunchEditor` and rewire `launchEditor`**

Edit `cmd/fngr/body.go`. Add `"os"` and `"os/exec"` to the imports. Replace the temporary `launchEditor` var assignment with:

```go
var launchEditor = realLaunchEditor

func realLaunchEditor(initial string) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return "", fmt.Errorf("no editor configured: set $EDITOR or $VISUAL")
	}

	f, err := os.CreateTemp("", "fngr-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)

	if initial != "" {
		if _, err := f.WriteString(initial); err != nil {
			f.Close()
			return "", fmt.Errorf("write initial: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	cmd := exec.Command(editor, name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited: %w", err)
	}

	raw, err := os.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read temp file: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", errCancel
	}
	return body, nil
}
```

The full `cmd/fngr/body.go` after this step:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var errCancel = errors.New("cancelled")

var launchEditor = realLaunchEditor

func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
	hasArgs := len(args) > 0
	piped := !io.IsTTY

	switch {
	case hasArgs && piped:
		return "", fmt.Errorf("ambiguous: body via both args and stdin; pick one")
	case !hasArgs && useEditor && piped:
		return "", fmt.Errorf("--edit conflicts with piped stdin")
	case hasArgs && useEditor:
		return launchEditor(strings.Join(args, " "))
	case hasArgs:
		return strings.Join(args, " "), nil
	case useEditor:
		return launchEditor("")
	case piped:
		return readStdin(io.In)
	default:
		return launchEditor("")
	}
}

func readStdin(in io.Reader) (string, error) {
	raw, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", fmt.Errorf("event text cannot be empty")
	}
	return body, nil
}

func realLaunchEditor(initial string) (string, error) {
	// (body as above)
}
```

- [ ] **Step 3.4: Run editor tests; expect pass**

```
go test ./cmd/fngr/ -run TestRealLaunchEditor -v
```

Expected: PASS for all 4 subtests.

- [ ] **Step 3.5: Run full body_test + lint**

```
go test ./cmd/fngr/ -v && make lint
```

Expected: all tests pass.

- [ ] **Step 3.6: Commit**

```bash
git add cmd/fngr/body.go cmd/fngr/body_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): realLaunchEditor exec path with $VISUAL/$EDITOR resolution

Replaces the temporary launchEditor stub with a real exec.Command-based
implementation: $VISUAL > $EDITOR > error, temp file via os.CreateTemp
with .txt suffix, defer cleanup, empty-save returns errCancel. Tests
use a tiny shell-script trampoline (POSIX-only; Windows skipped) to
exercise exec without coupling to a real editor.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Wire `AddCmd` to use `resolveBody`

**Files:**
- Modify: `cmd/fngr/add.go`
- Modify: `cmd/fngr/add_test.go`

- [ ] **Step 4.1: Update existing `add_test.go` literals (mechanical)**

Find every `&AddCmd{Text: ...}` in `cmd/fngr/add_test.go` (5 sites: lines ~14, ~36, ~60, ~72, ~98 per pre-task grep) and convert each to use `Args`. The mechanical edit:

| Before | After |
|---|---|
| `&AddCmd{Text: "hello world", Author: "alice"}` | `&AddCmd{Args: []string{"hello world"}, Author: "alice"}` |
| `&AddCmd{Text: "hi"}` | `&AddCmd{Args: []string{"hi"}}` |
| `&AddCmd{Text: "hi", Author: "alice", Time: "not-a-time"}` | `&AddCmd{Args: []string{"hi"}, Author: "alice", Time: "not-a-time"}` |
| `&AddCmd{Text: "deploy #ops", Author: "alice", Meta: []string{"env=prod"}}` | `&AddCmd{Args: []string{"deploy #ops"}, Author: "alice", Meta: []string{"env=prod"}}` |
| `&AddCmd{Text: "hi", Author: "alice", Meta: []string{"noequals"}}` | `&AddCmd{Args: []string{"hi"}, Author: "alice", Meta: []string{"noequals"}}` |

Note: keeping each as a one-element `[]string` slice preserves the joined-with-space semantics for already-quoted single-string inputs.

- [ ] **Step 4.2: Update `AddCmd` struct and `Run`**

Replace the entire `cmd/fngr/add.go` contents with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type AddCmd struct {
	Args   []string `arg:"" optional:"" help:"Event text (joined with spaces). Omit and pipe to stdin, or use -e."`
	Edit   bool     `short:"e" help:"Open $VISUAL or $EDITOR for the body."`
	Author string   `help:"Event author." env:"FNGR_AUTHOR" default:"${USER}"`
	Parent *int64   `help:"Parent event ID to create a child event."`
	Meta   []string `help:"Metadata key=value pairs (e.g. --meta env=prod)." short:"m"`
	Time   string   `help:"Override event timestamp (YYYY-MM-DD, ISO 8601, RFC3339, or HH:MM for today)." short:"t"`
}

func (c *AddCmd) Run(s eventStore, io ioStreams) error {
	if c.Author == "" {
		return fmt.Errorf("author is required: use --author, FNGR_AUTHOR, or ensure $USER is set")
	}

	text, err := resolveBody(c.Args, c.Edit, io)
	if errors.Is(err, errCancel) {
		fmt.Fprintln(io.Err, "cancelled (empty body)")
		return nil
	}
	if err != nil {
		return err
	}

	meta, err := event.CollectMeta(text, c.Meta, c.Author)
	if err != nil {
		return err
	}

	var createdAt *time.Time
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return fmt.Errorf("invalid --time value: %w", err)
		}
		createdAt = &t
	}

	id, err := s.Add(context.Background(), text, c.Parent, meta, createdAt)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Added event %d\n", id)
	return nil
}
```

- [ ] **Step 4.3: Run existing add_test suite; expect pass after the rename**

```
go test ./cmd/fngr/ -run TestAddCmd -v
```

Expected: all existing tests pass with the renamed field.

- [ ] **Step 4.4: Add new tests for the body modes**

Append to `cmd/fngr/add_test.go`:

```go
func TestAddCmd_MultiArgJoinsWithSpace(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Args: []string{"deploy", "v1.2", "to", "staging"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "deploy v1.2 to staging" {
		t.Errorf("text = %q, want %q", ev.Text, "deploy v1.2 to staging")
	}
}

func TestAddCmd_StdinBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull("piped body content", false) // isTTY=false

	cmd := &AddCmd{Author: "alice"} // no args
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "piped body content" {
		t.Errorf("text = %q, want %q", ev.Text, "piped body content")
	}
}

func TestAddCmd_EditorBody(t *testing.T) {
	s := newTestStore(t)
	io, out, _ := newTestIOFull("", true) // isTTY=true

	origEditor := launchEditor
	launchEditor = func(initial string) (string, error) {
		if initial != "" {
			t.Errorf("editor called with non-empty initial %q", initial)
		}
		return "from editor", nil
	}
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Author: "alice"} // no args, no -e — bare TTY auto-launches editor
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "from editor" {
		t.Errorf("text = %q, want 'from editor'", ev.Text)
	}
}

func TestAddCmd_ArgsPlusEditorPrefills(t *testing.T) {
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true)

	var gotInit string
	origEditor := launchEditor
	launchEditor = func(initial string) (string, error) {
		gotInit = initial
		return initial + " z", nil
	}
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Args: []string{"x", "y"}, Edit: true, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotInit != "x y" {
		t.Errorf("editor initial = %q, want %q", gotInit, "x y")
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "x y z" {
		t.Errorf("text = %q, want %q", ev.Text, "x y z")
	}
}

func TestAddCmd_EditorCancel(t *testing.T) {
	s := newTestStore(t)
	io, out, errBuf := newTestIOFull("", true)

	origEditor := launchEditor
	launchEditor = func(string) (string, error) { return "", errCancel }
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run returned err = %v, want nil (cancel is clean exit)", err)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errBuf.String(), "cancelled (empty body)") {
		t.Errorf("stderr = %q, want 'cancelled (empty body)'", errBuf.String())
	}

	// Confirm no event was created.
	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0", len(events))
	}
}

func TestAddCmd_ArgsAndStdinError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("piped", false) // isTTY=false

	cmd := &AddCmd{Args: []string{"argbody"}, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v, want 'ambiguous'", err)
	}
}

func TestAddCmd_EditFlagPipedError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("piped", false)

	cmd := &AddCmd{Edit: true, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--edit conflicts") {
		t.Errorf("err = %v, want '--edit conflicts'", err)
	}
}
```

The four tests that swap `launchEditor` are NOT `t.Parallel()` because they mutate package-level state. The conflict-error tests (no editor swap) keep `t.Parallel()`.

If `add_test.go`'s import block lacks `"context"` or `event` (the `internal/event` package), add them. After the additions, the imports should include `context`, `strings`, `testing`, `parse`, and `event`.

- [ ] **Step 4.5: Run full add_test suite + race**

```
go test ./cmd/fngr/ -run TestAddCmd -v -race
```

Expected: PASS for all subtests, no race warnings.

- [ ] **Step 4.6: Run full CI**

```
make ci
```

Expected: green across all packages.

- [ ] **Step 4.7: Commit**

```bash
git add cmd/fngr/add.go cmd/fngr/add_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): add command accepts args/stdin/$EDITOR for body

AddCmd.Text becomes Args []string; new -e/--edit flag forces editor
launch. Run delegates to resolveBody for source dispatch; errCancel
from the editor path converts to a clean exit-0 with "cancelled
(empty body)" on stderr. Existing single-arg invocations
(fngr add "some text") keep working — Kong sees one positional and
the joined-with-space helper preserves the input.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Dispatch tests for the new add modes

**Files:**
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 5.1: Add three new entries to `TestKongDispatch_AllCommands`**

Locate the `cases` slice in `TestKongDispatch_AllCommands` (around line 50). Add these three entries to the slice:

```go
{name: "add-multiarg", argv: []string{"add", "deployed", "v1.2"}, want: "Added event 1"},
{name: "add-stdin", argv: []string{"add"}, stdin: "piped body", want: ""},
{name: "add-editor", argv: []string{"add", "-e"}, want: "Added event 1"},
```

The test loop currently calls `dispatch(t, tc.argv, tc.stdin, true)` (after Task 1). The `add-stdin` case needs `isTTY: false` to take the stdin branch — extend the case struct to include `isTTY` with a default of `true`:

Change the case struct definition (around line 49) from:

```go
cases := []struct {
    name  string
    argv  []string
    stdin string
    want  string
}{
```

to:

```go
cases := []struct {
    name  string
    argv  []string
    stdin string
    isTTY bool
    want  string
}{
```

Update every existing case to set `isTTY: true` explicitly. The new `add-stdin` case sets `isTTY: false`. The dispatch call becomes:

```go
_, err := dispatch(t, tc.argv, tc.stdin, tc.isTTY)
```

For the `add-editor` case, the test must stub `launchEditor` for the duration of that subtest. Add a small per-case hook:

Replace the existing case-loop body with:

```go
for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) {
        t.Parallel()

        if tc.name == "add-editor" {
            origEditor := launchEditor
            launchEditor = func(string) (string, error) { return "from editor", nil }
            t.Cleanup(func() { launchEditor = origEditor })
        }

        _, err := dispatch(t, tc.argv, tc.stdin, tc.isTTY)
        if err != nil && strings.Contains(err.Error(), "couldn't find binding") {
            t.Fatalf("kong binding error for %q: %v", tc.argv, err)
        }
        // Other errors are fine; this test only guards the wiring contract.
    })
}
```

**Note on parallelism**: only one case mutates `launchEditor` and it runs under `t.Parallel`, but no other parallel case touches `launchEditor`. The cleanup restores it. If the race detector flags this, drop `t.Parallel()` from the loop and re-run.

- [ ] **Step 5.2: Run dispatch tests with race detector**

```
go test ./cmd/fngr/ -run TestKongDispatch -v -race
```

Expected: PASS for all entries including the three new add-* cases.

- [ ] **Step 5.3: Run full CI**

```
make ci
```

Expected: green.

- [ ] **Step 5.4: Commit**

```bash
git add cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
test(cmd): dispatch coverage for add-multiarg / add-stdin / add-editor

Three new entries in TestKongDispatch_AllCommands plus a per-case TTY
toggle so the stdin branch runs with isTTY=false. The add-editor case
swaps launchEditor for the duration of that subtest. Closes the wiring
loop on the body-input dispatch table.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `go.mod` promotion + docs

**Files:**
- Modify: `go.mod`
- Modify: `CLAUDE.md`
- Modify: `README.md`

- [ ] **Step 6.1: Promote `golang.org/x/term` to direct dep**

Run:

```
go mod tidy
```

This should move `golang.org/x/term` from `// indirect` to a direct require (because Task 1 added a direct import in `main.go`). Verify with:

```
grep "golang.org/x/term" go.mod
```

Expected: line WITHOUT `// indirect`.

- [ ] **Step 6.2: Update CLAUDE.md bullets**

In `/Users/nicolasm/Work/Monolithic/repositories/fngr/CLAUDE.md`, find the `cmd/fngr/{add,list,event,delete,meta}.go` bullet (around line 22-28) and add a sentence about the new `add` dispatch:

> `cmd/fngr/add.go` accepts variadic positional `Args` (joined with spaces); body source resolved by `cmd/fngr/body.go::resolveBody` via the (args, -e, stdin TTY-ness) dispatch table; `-e/--edit` forces the editor; bare `fngr add` in a TTY auto-launches `$VISUAL`/`$EDITOR`.

Find the `cmd/fngr/store.go` bullet (around line 35) and update the `ioStreams` description:

> `cmd/fngr/store.go` — Defines the narrow `eventStore` interface that commands depend on plus the injectable `ioStreams` (`In io.Reader`, `Out io.Writer`, `Err io.Writer`, `IsTTY bool`).

Add a new bullet for `body.go` after the `cmd/fngr/prompt.go` line:

> `cmd/fngr/body.go` — Body-source dispatch for `fngr add`. `resolveBody` returns the body string from one of {joined args, stdin, editor} per the (args, -e, IsTTY) dispatch table. `launchEditor` is a `var` for test stubbing; `realLaunchEditor` execs `$VISUAL`/`$EDITOR` on a temp file; `errCancel` signals empty-save (handled as exit-0 by `AddCmd.Run`).

- [ ] **Step 6.3: Update README.md add examples**

Find the `Adding events` (or similar) section in `README.md`. Add three new examples:

```markdown
### Adding events

```
# Multi-arg body (no quoting needed for casual entries)
fngr add deployed v1.2 to staging #ops @alice

# From a pipe
echo "build broken on main" | fngr add

# From a file
fngr add < notes.md

# Open $EDITOR (or auto-launches when bare `fngr add` runs in a TTY)
fngr add -e
```
```

(If the README doesn't yet have an `Adding events` section, add one with the four examples above plus the existing `--meta`/`--parent`/`--time` flags documented.)

- [ ] **Step 6.4: Run CI to confirm doc edits don't break anything**

```
make ci
```

Expected: green.

- [ ] **Step 6.5: Commit**

```bash
git add go.mod go.sum CLAUDE.md README.md
git commit -m "$(cat <<'EOF'
docs: README + CLAUDE.md for add body-input modes; x/term direct

go.mod promotion of golang.org/x/term reflects its direct use in main.go
(stdin TTY detection). README gains four add-command examples covering
multi-arg, pipe, file, and editor. CLAUDE.md picks up the new body.go
file and the ioStreams Err/IsTTY fields.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Roadmap update

**Files:**
- Modify: `docs/superpowers/roadmap.md`

- [ ] **Step 7.1: Mark three items done**

In `docs/superpowers/roadmap.md`, the `## Add command ergonomics` section currently lists four bullets. Move three to the `## Done` section as a single consolidated entry, and leave the fourth (`--format=json` import) where it is.

The new `## Done` entry to add:

> **`add` body-input modes** — `fngr add foo bar` joins multi-arg into a single body; `cmd | fngr add` reads stdin; bare `fngr add` in a TTY (or with `-e`) launches `$VISUAL`/`$EDITOR`; empty editor save cancels cleanly. Conflicts (args+stdin, --edit+stdin) error loudly.

The remaining `## Add command ergonomics` section becomes:

```markdown
## Add command ergonomics

- **`--format=json` import** — accept a single event or an array of events on
  stdin / in a file for bulk import.
```

- [ ] **Step 7.2: Commit**

```bash
git add docs/superpowers/roadmap.md
git commit -m "$(cat <<'EOF'
docs: mark add body-input modes done in roadmap

Three of four "Add command ergonomics" items shipped. --format=json
import remains as the lone open item under that heading; covered by
its own future spec.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Checklist (post-implementation)

After all tasks land:

1. **Per-function coverage**: `make test` should report `resolveBody` ≥ 95%, `readStdin` 100%, `realLaunchEditor` ≥ 80% (env-resolution + happy-path covered; error branches like `os.CreateTemp` failure remain defensive).
2. **Manual smoke test**: in a real terminal,
   - `fngr add hello world` → adds event with text "hello world"
   - `echo body | fngr add` → adds event with text "body"
   - `fngr add -e` (with `$EDITOR=vim`) → opens vim; type, save, quit → adds event
   - `fngr add` (no args, in TTY) → opens editor; save empty → "cancelled (empty body)" on stderr, exit 0
   - `echo body | fngr add foo` → errors with "ambiguous"
3. **`go.mod`**: `golang.org/x/term` listed as direct require.
4. **CLAUDE.md / README / roadmap**: all reflect the new state.
