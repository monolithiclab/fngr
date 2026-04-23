# `help` command + compact help — Design

**Status:** Draft
**Date:** 2026-04-23
**Roadmap items:** "CLI surface alignment — Compact help" + "CLI surface
alignment — `help` alias" (`docs/superpowers/roadmap.md`)

## Goal

1. Make `fngr` use Kong's compact help layout for every help screen
   (top-level, sub-commands, verb trees) — one line per command,
   column-aligned.
2. Add a `help` verb so `fngr help` ≡ `fngr --help` and
   `fngr help <cmd>` ≡ `fngr <cmd> --help`. Multi-arg paths
   (`fngr help event show`) work too.

## Non-goals

- Custom `kong.HelpPrinter` to reproduce a specific
  `command args [flags]   description` format. Kong's `Compact: true`
  produces `command   description` (no args/flags signature on the
  command-list lines). The roadmap's wording was aspirational; the
  args/flags signature is rarely the discriminator a user needs at
  the command-list stage — they pick a command then `--help` it for
  the full signature.
- Custom error message for `fngr help <unknown>`. Kong's natural
  "unknown command" error from the re-parse is fine.
- Help bypass for `--db` flag handling. Help should not require an
  existing DB or even a writable DB path — `db.Open` is skipped for
  the help command path, but the `--db` flag is still parsed (no
  effect since we never open it).

## Architecture

Two file changes plus one new file. No package-boundary changes,
no new dependencies.

### `cmd/fngr/main.go` — two single-line additions

1. Add `kong.ConfigureHelp(kong.HelpOptions{Compact: true})` to the
   `kong.Parse(...)` options. Affects every help screen uniformly:
   `fngr --help`, `fngr add --help`, `fngr event --help`, `fngr help`,
   `fngr help <cmd>`.
2. Skip `db.Open` when the parsed command starts with `help`. Help
   should not require an existing DB. The current pattern:

   ```go
   database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))
   ```

   becomes (paraphrased — exact splice at the implementation step):

   ```go
   if strings.HasPrefix(ctx.Command(), "help") {
       ctx.FatalIfErrorf(ctx.Run())
       return
   }
   database, err := db.Open(...)
   ```

### `CLI` struct in `main.go`

Append one field:

```go
Help HelpCmd `cmd:"" help:"Show help for a command."`
```

Position in the struct doesn't affect Kong's command-list ordering
(Kong sorts alphabetically by default — `help` will land between
`event` and `list`).

### New file `cmd/fngr/help.go`

```go
package main

import "github.com/alecthomas/kong"

// HelpCmd is the `fngr help [<command>...]` verb. It re-invokes Kong's
// parser with `--help` appended, so the existing context-sensitive help
// printer renders the same output as `fngr <command> --help`.
type HelpCmd struct {
	Args []string `arg:"" optional:"" help:"Command path to show help for (e.g. 'add' or 'event show'). Empty shows top-level help."`
}

// Run prepends --help to the requested args and re-parses. Kong's --help
// flag is a before-resolve hook that prints help and triggers Exit, so
// the second Parse never returns to user code on success.
func (c *HelpCmd) Run(realCtx *kong.Context) error {
	args := append([]string(nil), c.Args...)
	args = append(args, "--help")
	_, err := realCtx.Kong.Parse(args)
	return err
}
```

**Key choices:**

- `Args []string` is `arg:"" optional:""` so `fngr help` with no
  argument parses cleanly. `optional:""` only works on the last
  positional, which it is.
- `realCtx *kong.Context` is bound by Kong automatically — it's the
  parsed context from the *first* invocation. We pull `realCtx.Kong`
  to get the parser singleton and re-invoke it.
- `append([]string(nil), c.Args...)` avoids mutating the underlying
  slice (Kong might re-use it).
- Returning `err` from the second Parse propagates any usage error
  cleanly to `kctx.FatalIfErrorf` in `main`.
- No imports beyond `kong` itself. No deps on `eventStore` /
  `ioStreams` (Kong's binding system only injects what the Run
  signature names).

## Edge cases

| Case                                  | Behavior                                                                                                                |
| ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `fngr help`                           | Re-parses `["--help"]` → top-level help. Same output as `fngr --help`.                                                  |
| `fngr help add`                       | Re-parses `["add", "--help"]` → `add` command help. Same as `fngr add --help`.                                          |
| `fngr help event show`                | Re-parses `["event", "show", "--help"]` → leaf-command help.                                                            |
| `fngr help <unknown>`                 | Re-parses `["unknown", "--help"]` → Kong's "unknown command" error, exit non-zero. No special handling.                 |
| `fngr help help`                      | Re-parses `["help", "--help"]` → prints `HelpCmd`'s own help (since `help` is now a real command in the tree).         |
| `fngr --help` (existing flag)         | Unchanged. Triggered by Kong's built-in `-h` / `--help` handler before our `HelpCmd.Run` ever fires.                    |
| `fngr help` without DB on disk        | Works. `db.Open` is skipped via the `strings.HasPrefix(ctx.Command(), "help")` check in `main.go`.                     |
| `fngr help add` with DB held by other | Works for the same reason. Help shouldn't gate on DB availability.                                                       |

## Output examples (Kong's `Compact: true`)

`fngr --help` and `fngr help` will both render:

```
Usage: fngr <command>

A CLI to log and track events.

Flags:
  -h, --help         Show context-sensitive help.
      --db=STRING    Path to database file ($FNGR_DB).
      --version      Print version and exit.

Commands:
  add           Add an event.
  list          List events (default command).
  event show    Show event detail (default).
  event text    Replace event text.
  event time    Replace clock time (or full timestamp).
  event date    Replace date (or full timestamp).
  event attach  Set parent event.
  event detach  Clear parent.
  event tag     Add tags (one or more @person, #tag, or key=value).
  event untag   Remove tags (one or more @person, #tag, or key=value).
  delete        Delete an event.
  meta list     List metadata.
  meta rename   Rename a (key, value) tuple across every event.
  meta delete   Delete a (key, value) tuple from every event.
  help          Show help for a command.

Run "fngr <command> --help" for more information on a command.
```

(Kong's compact mode keeps flag descriptions on sub-command help
screens — only the top-level command list collapses to one line per
command.)

## Testing

### Dispatch test rows (`cmd/fngr/dispatch_test.go`)

Three rows added to the existing `TestKongDispatch_AllCommands`
table. The dispatch test only asserts absence of `couldn't find
binding` errors — Kong's `os.Exit(0)` on help-print is neutralized
by the test's existing `kong.Exit(func(int){})` hook.

```go
{name: "help-bare", argv: []string{"help"}, isTTY: true, want: ""},
{name: "help-cmd", argv: []string{"help", "add"}, isTTY: true, want: ""},
{name: "help-verb-tree", argv: []string{"help", "event", "show"}, isTTY: true, want: ""},
```

### Unit tests (`cmd/fngr/help_test.go`, new file)

- `TestHelpCmd_BareEqualsTopLevel` — capture output of `help` and
  `--help` (both routed through a fresh `kong.New(...)` with our
  `kongVars` and `Compact: true`); assert byte-for-byte equality.
  Confirms `help` is a true alias.
- `TestHelpCmd_TargetEqualsCmdHelp` — same shape, `help add` vs
  `add --help`. One representative leaf is enough; the dispatch
  test covers the verb-tree path.
- `TestHelpCmd_UnknownCommandErrors` — `help totally-not-a-command`
  returns a non-nil error from `parser.Parse(...)`. No assertion on
  the message text (Kong's wording is theirs to change).

Coverage on `HelpCmd.Run` should land at 100% (it's a 4-line
function, all branches exercised by the three direct tests).

### Manual smoke test (post-build)

```bash
make build
./build/fngr --help              # compact top-level
./build/fngr help                # same as above
./build/fngr help add            # add help
./build/fngr help event show     # verb-tree help
./build/fngr help unknown        # error, non-zero exit
```

## Open questions

None. All design choices resolved during brainstorming.

## Out of scope (deliberate)

- **Custom `kong.HelpPrinter`** for the strict
  `command args [flags]   description` format — see Non-goals.
- **`fngr help <unknown>` error message customization** — Kong's
  default is sufficient.
- **`-h` short alias for `help` verb** — Kong already handles `-h`
  as a flag; `fngr -h` works today and continues to.
- **Bash/zsh completion of `help <cmd>` argument** — would need a
  Kong completion hook; out of scope, not load-bearing.
