# `add` body-input modes

Sub-project of the [roadmap](../roadmap.md) "Add command ergonomics" epic.
Three of the four bulleted items land here: multi-arg body, stdin body,
and `$EDITOR` support. The fourth (`--format=json` import) is deliberately
deferred to its own spec — different design surface (record schema, batch
error handling, transactional semantics) and no precedence interactions
with the body-text resolution covered here.

The tool is pre-public; no compatibility shims for the `Text string` →
`Args []string` field rename. Existing CLI invocations
(`fngr add "some text"`, `fngr add foo --meta x=y`) keep working unchanged
because Kong sees a single positional arg either way.

## Goals

- `fngr add foo bar baz` consolidates positional args into a single
  body string `"foo bar baz"` (joined with one ASCII space). No quoting
  needed for casual entries.
- `echo body | fngr add` reads the body from stdin when stdin is not a TTY.
- `fngr add` (no args, no flags, interactive TTY) launches `$VISUAL` or
  `$EDITOR` on a temp file; saved contents become the body.
- `fngr add -e` (or `--edit`) launches the editor explicitly. Args present
  with `-e` pre-fill the editor with the joined args.
- Editor save-empty (or quit-without-save) cancels: no event added, exit
  status 0, single-line `cancelled (empty body)` notice on stderr.
- Conflict cases that combine a body source with stdin error loudly rather
  than silently dropping the pipe.

## Non-goals

- No `--format=json` import. Separate spec.
- No bare-`-` stdin form (`fngr add -`). Auto-detect via non-TTY stdin
  handles every real workflow; the explicit form would only force stdin
  in a TTY, which has no use case today.
- No hardcoded editor fallback (`vi`/`nano`). On minimal containers and
  CI, the unfamiliar editor would be worse than a clear "set $EDITOR or
  $VISUAL" error.
- No comment-stripping in the editor (`git commit`-style `# Lines starting
  with '#' are stripped`). Adds parsing surface for marginal gain — the
  user typed the flags themselves seconds ago and doesn't need a context
  block.
- No `--edit-from <file>` flag. Composes from `cat file | fngr add` plus
  shell redirection if needed.
- No multi-event body (one editor session = one event). Bulk import is
  the JSON spec's territory.

## Architecture

### Body source resolution table

The eight `(args, -e, stdin)` combinations resolve as follows. The body
source is always exactly one of {args, stdin, editor}; conflicts error.

| Args | `-e` | Stdin | Resolution |
|------|------|-------|------------|
| present | absent | TTY | Args joined with single space |
| present | absent | piped | **Error**: `ambiguous: body via both args and stdin; pick one` |
| present | present | TTY | Editor pre-filled with joined args |
| present | present | piped | **Error** (same wording as above) |
| absent | absent | TTY | Editor opened empty |
| absent | absent | piped | Read stdin to EOF |
| absent | present | TTY | Editor opened empty |
| absent | present | piped | **Error**: `--edit conflicts with piped stdin` |

Empty stdin (zero bytes after trimming) errors with the existing
`event text cannot be empty` message — empty pipe is almost always a
script bug. Empty editor save cancels (the user can `:q!` to indicate
intent; an empty pipe has no equivalent).

### `cmd/fngr/body.go` — new file

The dispatch logic lives in its own file alongside `add.go`. The switch
order matters: `hasArgs && piped` must fire before any `useEditor` branch
so that `echo X | fngr add foo -e` reports the args+stdin conflict
(actionable: drop one input) rather than the args+edit case (which would
otherwise be a non-conflict).

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

// errCancel signals a deliberate user cancel (empty editor save). AddCmd.Run
// recognises it and converts to (nil error + status 0).
var errCancel = errors.New("cancelled")

// launchEditor is overridable so tests can stub the editor exec without
// shelling out. Production points at the exec.Command-based implementation.
var launchEditor = realLaunchEditor

// resolveBody applies the dispatch table above. It owns no I/O state of
// its own — every dependency arrives via the ioStreams arg.
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

### `cmd/fngr/add.go` — slimmed Run

```go
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

### `cmd/fngr/store.go` — `ioStreams` extended

```go
type ioStreams struct {
    In    io.Reader
    Out   io.Writer
    Err   io.Writer  // editor cancel notice; stays out of stdout for script piping
    IsTTY bool       // true when stdin is an interactive terminal
}
```

`main.go` wires the new fields:

```go
ctx.Bind(ioStreams{
    In:    os.Stdin,
    Out:   os.Stdout,
    Err:   os.Stderr,
    IsTTY: term.IsTerminal(int(os.Stdin.Fd())),
})
```

`golang.org/x/term` is already in `go.mod` as a transitive dep; promote
to direct.

The other commands (`list`, `event`, `meta`, `delete`) ignore both new
fields. `withPager` continues to do its own stdout TTY check via
`term.IsTerminal(int(os.Stdout.Fd()))`; no overlap.

### `cmd/fngr/dispatch_test.go` — `dispatch` helper grows `isTTY`

The existing helper already takes a `stdin string` arg. It grows an
`isTTY bool` parameter so add-command tests can simulate piped vs
interactive stdin without touching real fds. Existing call sites pass
`true` (the dispatch tests don't exercise piped behaviour today).

## Testing

### `cmd/fngr/body_test.go` — new

Table-driven coverage of all eight `resolveBody` rows plus the two
conflict errors:

```go
cases := []struct{
    name      string
    args      []string
    useEditor bool
    isTTY     bool
    stdin     string
    editorOut string  // returned by stubbed launchEditor
    editorErr error
    wantBody  string
    wantInit  string  // expected initial passed to launchEditor
    wantErr   string  // substring; "" means no error
}{...}
```

For rows that route through the editor, the test swaps `launchEditor`
with a closure that captures the `initial` arg and returns `editorOut`/
`editorErr`. Restoration via `t.Cleanup`.

`readStdin` and `realLaunchEditor` get their own tests:

- `readStdin`: empty, whitespace-only, leading/trailing whitespace
  trimmed, internal newlines preserved, read-error propagated.
- `realLaunchEditor`: integration test using a tiny shell script
  (`#!/bin/sh\necho "from editor" > "$1"`) written to `t.TempDir()`
  with `$EDITOR` pointed at it. Covers the exec path, temp-file
  cleanup, and pre-fill round-trip. `t.Skip` on Windows (covered by
  the script approach not being portable).

### `cmd/fngr/add_test.go` — extended

Happy-path checks at the `AddCmd.Run` level for each body source:

- `multi-arg`: `Args: []string{"foo", "bar"}` → DB row text is `"foo bar"`.
- `stdin`: `Args: nil`, `IsTTY: false`, stdin `"piped body"` → text is
  `"piped body"`.
- `editor-empty`: `Args: nil`, `IsTTY: true`, swapped editor returns
  `"from editor"` → DB row text is `"from editor"`, no `errCancel`.
- `args-plus-editor`: `Args: []string{"x", "y"}`, `Edit: true`,
  swapped editor verifies `initial == "x y"`, returns `"x y z"`.
- `editor-cancel`: swapped editor returns `("", errCancel)` →
  no event added, `out.String()` empty, `err.String()` contains
  `"cancelled (empty body)"`, `Run` returns `nil`.
- `args-plus-stdin-errors`: `Args: []string{"x"}`, `IsTTY: false`,
  stdin `"y"` → error contains `"ambiguous"`.
- `editor-plus-stdin-errors`: `Edit: true`, `IsTTY: false`,
  stdin `"y"` → error contains `"--edit conflicts"`.

Existing test sites that construct `&AddCmd{Text: "..."}` migrate to
`&AddCmd{Args: []string{"..."}}`. Per earlier grep this is ~6 sites.

### `cmd/fngr/dispatch_test.go` — three new entries

Confirms Kong wiring stays consistent end-to-end:

- `add-multiarg`: `[]string{"add", "foo", "bar"}` (stdin TTY, no editor).
- `add-stdin`: `[]string{"add"}` with stdin `"body"` and `IsTTY: false`.
- `add-editor`: `[]string{"add", "-e"}` with stubbed editor.

## Migration & breaking changes

- **Field rename**: `AddCmd.Text string` → `AddCmd.Args []string`.
  Updates needed in test files only (~6 sites). No CLI surface change.
- **`go.mod`**: `golang.org/x/term` promoted from indirect to direct.
- **`ioStreams` extension**: new `Err` and `IsTTY` fields. Existing
  command Run methods compile unchanged because they don't reference
  the new fields.
- **`dispatch` test helper signature**: gains an `isTTY bool` parameter;
  existing call sites pass `true`.

## Documentation

- `CLAUDE.md`:
  - `cmd/fngr/add.go` bullet — describe new dispatch table at one-line
    granularity ("Args is variadic; Edit flag forces editor; auto via
    stdin/TTY detection").
  - `cmd/fngr/store.go` bullet — note `ioStreams.Err` and `IsTTY`.
  - New `cmd/fngr/body.go` bullet — `resolveBody` + `launchEditor`
    + `errCancel` + `readStdin`.
- `README.md` — Add command examples refresh: pipe usage
  (`echo done | fngr add`), editor usage (`fngr add -e`), multi-arg
  (`fngr add deployed v1.2 to staging #ops`).
- `docs/superpowers/roadmap.md` — once shipped, mark three of the four
  Add ergonomics items done; `--format=json` stays open under its own
  bullet.

## Roadmap impact

- This spec is a prerequisite for the roadmap's "`-S` for search
  everywhere" alignment item under CLI surface alignment. Once `add`
  accepts variadic args, any future thinking about positional shorthands
  at the bare-`fngr` level has to reckon with the ambiguity it would
  create — the alignment item documents the principle that prevents
  that drift.
- The `--format=json` import (the deferred fourth Add ergonomics item)
  will compose with this spec rather than replace any of it: JSON is a
  separate code path triggered by the flag, with its own body-record
  schema.
