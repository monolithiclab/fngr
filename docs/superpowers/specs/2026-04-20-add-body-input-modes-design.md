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

> **Amended in v0.0.3 (review issue H4); this section states the current
> design.** As shipped, resolution keyed off whether stdin "was piped",
> which took two tries to get right and was the wrong question both
> times. First `!IsTTY` alone, which wrongly hit the args+stdin
> ambiguity error for any script, cron job or CI step (stdin bound to an
> empty `/dev/null` is non-TTY with zero bytes). Then non-TTY **with
> data**, via a `peekHasData` `bufio.Reader` peek — but reading an
> open-but-idle pipe returns neither a byte nor EOF, so the peek hung
> every non-TTY `fngr add "note"` forever, with no output and no
> timeout, purely to report an error. Both conflict rows are gone with
> it. Precedence replaces conflict detection, and stdin is not a
> question anyone asks up front.

Resolution is strict precedence: **args > `-e` > TTY > stdin**. The body
source is always exactly one of {args, stdin, editor}, and the first
branch that can supply one wins.

| Args | `-e` | Stdin | Resolution |
|------|------|-------|------------|
| present | absent | any | Args joined with single space |
| present | present | any | Editor pre-filled with joined args |
| absent | present | any | Editor opened empty |
| absent | absent | TTY | Editor opened empty |
| absent | absent | non-TTY | Read stdin to EOF |

Stdin is read in the last row only, where it is the sole possible body
source and blocking to wait for the body is the whole point. Anywhere
else it is left untouched — deliberately, since noticing that data is
sitting there costs a read that may never return. Extra stdin alongside
args or `-e` is therefore silently unread; that lost feedback is the
accepted price of never hanging.

An empty stdin needs no separate check: `/dev/null` hits EOF at once and
`readStdin` reports `event title cannot be empty` after trimming. Empty
editor save cancels instead (the user can `:q!` to indicate intent; an
empty pipe has no equivalent).

### `cmd/fngr/body.go` — new file

The dispatch logic lives in its own file alongside `add.go`. Branch
order *is* the precedence rule above, so it is load-bearing: the two
`len(args) > 0` cases must precede the `useEditor`/`IsTTY` case, which
must precede the `default` that reads stdin.

```go
// resolveBody applies the dispatch table above. It owns no I/O state of
// its own — every dependency arrives via the ioStreams arg.
func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
    switch {
    case len(args) > 0 && useEditor:
        return launchEditor(strings.Join(args, " "))
    case len(args) > 0:
        body := strings.Join(args, " ")
        if strings.TrimSpace(body) == "" {
            return "", fmt.Errorf("event title cannot be empty")
        }
        return body, nil
    case useEditor, io.IsTTY:
        return launchEditor("")
    default:
        return readStdin(io.In)
    }
}

// readStdin caps the read at maxStdinBytes (16 MiB) so a runaway pipe
// cannot OOM the process.
func readStdin(in io.Reader) (string, error) {
    raw, err := io.ReadAll(io.LimitReader(in, maxStdinBytes+1))
    if err != nil {
        return "", fmt.Errorf("read stdin: %w", err)
    }
    if len(raw) > maxStdinBytes {
        return "", fmt.Errorf("stdin exceeds %d-byte limit", maxStdinBytes)
    }
    body := strings.TrimSpace(string(raw))
    if body == "" {
        return "", fmt.Errorf("event title cannot be empty")
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
- `args-win-over-stdin`: `Args: []string{"x"}`, `IsTTY: false`,
  stdin `"y"` → body is `"x"`; stdin is never read.
- `editor-wins-over-stdin`: `Edit: true`, `IsTTY: false`, stdin `"y"`
  → the editor opens; stdin is never read.
- `stdin-untouched-when-body-decided`: each of {args, args+`-e`, `-e`,
  TTY} against a reader that fails the test if anything reads it. Guards
  the hang itself, not just the wording — a real idle pipe would block
  here forever, so any read at all is the defect.

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
