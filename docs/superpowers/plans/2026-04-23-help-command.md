# `help` Command + Compact Help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `fngr help [<cmd>...]` verb that's a true alias for `fngr <cmd> --help`, and switch every help screen to Kong's `Compact: true` layout (one line per command, column-aligned).

**Architecture:** Three small touches. New `cmd/fngr/help.go` (~15 LoC) defines `HelpCmd` with a `Run` that re-invokes `kong.Parse(append(args, "--help"))`. `cmd/fngr/main.go` adds `kong.ConfigureHelp(kong.HelpOptions{Compact: true})` to the parser options, registers `Help HelpCmd` in the `CLI` struct, and skips `db.Open` when `ctx.Command()` starts with `help`. Three new dispatch test rows + a focused `cmd/fngr/help_test.go` cover wiring + alias semantics.

**Tech Stack:** Go 1.26, Kong (`github.com/alecthomas/kong`).

**Spec:** `docs/superpowers/specs/2026-04-23-help-command-design.md`

**File map:**
- Create: `cmd/fngr/help.go` — `HelpCmd` struct + Run.
- Create: `cmd/fngr/help_test.go` — alias-equality tests + unknown-command error test.
- Modify: `cmd/fngr/main.go` — Compact opt-in, `Help` field on `CLI`, `db.Open` skip for help paths.
- Modify: `cmd/fngr/dispatch_test.go` — three new rows in `TestKongDispatch_AllCommands`.
- Modify: `README.md` — mention `fngr help` as the alias for `--help`.
- Modify: `docs/superpowers/roadmap.md` — move both "CLI surface alignment" entries (compact help + help alias) from open to Done.

---

## Task 1: HelpCmd + main.go wiring + tests

Single cohesive commit since the pieces are tightly interlocked: `HelpCmd` is meaningless without being registered in the `CLI` struct, the dispatch tests need it wired to even parse, and the unit tests need the parser configured with `Compact: true` to assert byte-for-byte equality vs `--help`.

**Files:**
- Create: `cmd/fngr/help.go`
- Create: `cmd/fngr/help_test.go`
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 1.1: Create `cmd/fngr/help.go`**

```go
// cmd/fngr/help.go
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

- [ ] **Step 1.2: Wire `HelpCmd` into the `CLI` struct in `cmd/fngr/main.go`**

Find the `CLI` struct (currently lines 18–27) and add the `Help` field at the bottom:

```go
type CLI struct {
	DB      string           `help:"Path to database file." env:"FNGR_DB" type:"path"`
	Version kong.VersionFlag `help:"Print version and exit."`

	Add    AddCmd    `cmd:"" help:"Add an event."`
	List   ListCmd   `cmd:"" default:"withargs" help:"List events (default command)."`
	Event  EventCmd  `cmd:"" help:"Show or modify a single event."`
	Delete DeleteCmd `cmd:"" help:"Delete an event."`
	Meta   MetaCmd   `cmd:"" help:"List all metadata keys and values."`
	Help   HelpCmd   `cmd:"" help:"Show help for a command."`
}
```

- [ ] **Step 1.3: Opt into Kong's compact help in `cmd/fngr/main.go`**

Find the `kong.Parse(...)` call in `main()` (currently lines 58–63) and add `kong.ConfigureHelp(kong.HelpOptions{Compact: true})`:

```go
ctx := kong.Parse(&cli,
	kong.Name("fngr"),
	kong.Description("A CLI to log and track events."),
	kongVars(version, username),
	kong.UsageOnError(),
	kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
)
```

- [ ] **Step 1.4: Skip `db.Open` for help command paths in `cmd/fngr/main.go`**

Find the `db.Open` call (currently around line 71) and add the help-bypass guard immediately before it:

```go
dbPath, err := db.ResolvePath(cli.DB)
if err != nil {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// Help shouldn't require a DB. Kong's --help flag exits during Parse;
// the explicit `help` verb returns from Parse normally and reaches here.
if strings.HasPrefix(ctx.Command(), "help") {
	ctx.FatalIfErrorf(ctx.Run())
	return
}

database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))
```

- [ ] **Step 1.5: Add three rows to `TestKongDispatch_AllCommands` in `cmd/fngr/dispatch_test.go`**

Insert just before the closing `}` of the `cases` slice (currently line 90, after the `meta-delete` row):

```go
		{name: "meta-delete", argv: []string{"meta", "delete", "tag=a", "-f"}, isTTY: true, want: ""},
		{name: "help-bare", argv: []string{"help"}, isTTY: true, want: ""},
		{name: "help-cmd", argv: []string{"help", "add"}, isTTY: true, want: ""},
		{name: "help-verb-tree", argv: []string{"help", "event", "show"}, isTTY: true, want: ""},
	}
```

- [ ] **Step 1.6: Create `cmd/fngr/help_test.go` with the alias-equality + unknown-command tests**

```go
// cmd/fngr/help_test.go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// newTestParser builds a kong.Parser identical to main()'s, with Compact
// help and a captured stdout/stderr writer so help output can be diffed.
func newTestParser(t *testing.T, out *bytes.Buffer) *kong.Kong {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kongVars("test", "tester"),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Writers(out, out),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	return parser
}

// helpOutput captures the help text Kong emits for argv when the parser
// is asked to render it (either via the explicit `help` verb's Run or
// the bare `--help` flag).
func helpOutput(t *testing.T, argv []string) string {
	t.Helper()
	var buf bytes.Buffer
	parser := newTestParser(t, &buf)
	kctx, err := parser.Parse(argv)
	if err != nil {
		// `--help` exits via the Exit hook (no error). The `help` verb
		// re-parses internally and returns nil on success too. Any error
		// here is a real parse failure that the caller wants to see.
		return "ERR:" + err.Error()
	}
	// `--help` short-circuits before Run; only the `help` verb path needs
	// the explicit Run to actually render.
	if kctx != nil {
		_ = kctx.Run()
	}
	return buf.String()
}

func TestHelpCmd_BareEqualsTopLevel(t *testing.T) {
	t.Parallel()
	viaFlag := helpOutput(t, []string{"--help"})
	viaVerb := helpOutput(t, []string{"help"})
	if viaFlag == "" {
		t.Fatal("--help produced empty output; test setup is broken")
	}
	if viaFlag != viaVerb {
		t.Errorf("`fngr help` output differs from `fngr --help`:\n--- --help ---\n%s\n--- help ---\n%s", viaFlag, viaVerb)
	}
}

func TestHelpCmd_TargetEqualsCmdHelp(t *testing.T) {
	t.Parallel()
	viaFlag := helpOutput(t, []string{"add", "--help"})
	viaVerb := helpOutput(t, []string{"help", "add"})
	if viaFlag == "" {
		t.Fatal("`add --help` produced empty output; test setup is broken")
	}
	if viaFlag != viaVerb {
		t.Errorf("`fngr help add` output differs from `fngr add --help`:\n--- add --help ---\n%s\n--- help add ---\n%s", viaFlag, viaVerb)
	}
}

func TestHelpCmd_UnknownCommandErrors(t *testing.T) {
	t.Parallel()
	out := helpOutput(t, []string{"help", "totally-not-a-command"})
	if !strings.HasPrefix(out, "ERR:") {
		t.Errorf("expected an error from `help <unknown>`, got plain output:\n%s", out)
	}
}
```

- [ ] **Step 1.7: Run the new + existing tests to verify everything passes**

Run: `make test`

Expected: PASS across all packages. Coverage on `cmd/fngr` should tick up slightly (HelpCmd.Run is fully exercised). Coverage report should show `Run 100.0%` for `HelpCmd`.

If a test fails, common culprits:
- `helpOutput` returning empty string for `--help` → check `kong.Writers(out, out)` was passed.
- `BareEqualsTopLevel` mismatch → confirm both invocations go through the same `Compact: true` parser.
- `UnknownCommandErrors` returning non-error output → check that `ctx.Run()` is invoked for the `help` verb path so `HelpCmd.Run` re-parses and surfaces Kong's "unknown command" error.

- [ ] **Step 1.8: Run `make lint`**

Expected: PASS.

- [ ] **Step 1.9: Manual smoke test**

```bash
make build
echo "=== top-level help (compact, with `help` listed) ==="
./build/fngr --help
echo
echo "=== same via the verb ==="
./build/fngr help
echo
echo "=== sub-command help ==="
./build/fngr help add
echo
echo "=== verb-tree help ==="
./build/fngr help event show
echo
echo "=== unknown command (expect error, non-zero exit) ==="
./build/fngr help totally-not-a-command; echo "exit: $?"
echo
echo "=== help works without a DB ==="
FNGR_DB=/nonexistent/path/to.db ./build/fngr help; echo "exit: $?"
```

Expected:
- The top-level help and `help` verb output are byte-for-byte identical and include a `help` row in the Commands list.
- `help add` / `help event show` render the same as their `--help` counterparts.
- `help totally-not-a-command` exits non-zero with a Kong error message.
- `FNGR_DB=/nonexistent/path/to.db ./build/fngr help` exits 0 (db path resolution doesn't even try to open the file for the help path).

- [ ] **Step 1.10: Run `/simplify` review against the staged diff**

You don't have direct access to `/simplify`. Inspect the diff manually with three angles:
1. **Reuse**: any existing helper in `cmd/fngr/` you should be using? `kong.Context` re-parse pattern is novel; nothing to reuse.
2. **Quality**: hacky patterns? The `Run(realCtx *kong.Context)` parameter is Kong-idiomatic for accessing the parser singleton; not hacky.
3. **Efficiency**: hot-path concerns? Help is a one-shot CLI invocation; no hot path.

Apply any actionable findings inline. Re-run `make test && make lint` after edits.

- [ ] **Step 1.11: Commit**

```bash
git add cmd/fngr/help.go cmd/fngr/help_test.go cmd/fngr/main.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): add `help` verb + Kong compact help

Closes both CLI surface alignment roadmap items.

`fngr help [<cmd>...]` re-invokes Kong's parser with --help
appended, so output is byte-for-byte identical to
`fngr <cmd> --help`. Multi-arg paths (`fngr help event show`)
work because Args is variadic. Unknown commands surface Kong's
natural "unknown command" error.

main.go also opts into Kong's HelpOptions{Compact: true} so every
help screen — top-level, sub-commands, verb trees, and the new
`help` verb — uses the one-line-per-command, column-aligned
layout. The args/flags signature is dropped from the
command-list lines (Kong's choice) but stays on each command's
own --help screen where users actually need it.

db.Open is skipped when the parsed command starts with `help` so
help works without an existing or even writable DB. Kong's
built-in --help flag already exits during Parse and never reaches
db.Open, so only the new explicit verb needed the bypass.

New cmd/fngr/help_test.go: alias-equality vs --help (top-level
+ leaf), unknown-command error. dispatch_test.go gains three
rows: help-bare, help-cmd, help-verb-tree.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Documentation closeout

README mention of the `help` verb + roadmap entries move from "CLI surface alignment" to "Done". Single docs-only commit (no `/simplify` per project rule).

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/roadmap.md`

- [ ] **Step 2.1: Read README's Quick start section to find the right insertion point**

Run: `grep -n '^# Print version' README.md`

You should see one match around line 154 in the Quick start block. The new lines go just before that one.

- [ ] **Step 2.2: Add `help` examples to README's Quick start**

Find the existing `# Print version` line in `README.md` (in the Quick start fenced block) and add three lines just above it:

```
# Show help (alias for --help; takes a command path, including verb trees)
fngr help
fngr help add
fngr help event show

# Print version
fngr --version
```

(Keep the existing `fngr --version` line right after.)

- [ ] **Step 2.3: Update `docs/superpowers/roadmap.md`** — move CLI surface alignment items to Done.

Read `docs/superpowers/roadmap.md`. The `## CLI surface alignment` section currently has three bullets; the load-bearing change is removing **Compact help** and **`help` alias** (the third bullet, `-S` for search everywhere, is already shipped — leave it where it is or remove the whole section if both load-bearing items go).

Replace the entire `## CLI surface alignment` section. Before:

```markdown
## CLI surface alignment

- **Compact help** — reformat help output to
  `command args [flags]   description`, one line per command, column-aligned.
- **`-S` for search everywhere** — `fngr -S "..."` for list and
  `fngr meta -S "..."` for meta share the same flag spelling. Meta requires
  `-S` because of its subcommand tree; list mirrors the idiom so users learn
  one form. Load-bearing: `fngr add` now accepts multi-arg input, so
  positional search on the bare command would be ambiguous.
- **`help` alias** — `fngr help` ≡ `fngr --help`; `fngr help <cmd>` ≡
  `fngr <cmd> --help`.
```

After: section removed entirely (the `-S` bullet describes already-shipped behavior; it doesn't need a separate roadmap entry).

In the `## Done` section, append after the existing last bullet (GitHub Actions CI + release pipeline):

```markdown
- **CLI surface alignment** — `fngr help [<cmd>...]` is a verb-form alias
  for `--help` (multi-arg paths supported: `fngr help event show`); every
  help screen uses Kong's `HelpOptions{Compact: true}` layout (one line
  per command in the command list, with full per-command details on the
  `--help` of each command).
```

- [ ] **Step 2.4: Verify the roadmap reads cleanly**

Run: `cat docs/superpowers/roadmap.md`

Expected: Done has a new bullet at the end; CLI surface alignment section is gone; Data model + Publishing pipeline polish + Considered (not pursued) sections are unchanged.

- [ ] **Step 2.5: Commit**

```bash
git add README.md docs/superpowers/roadmap.md
git commit -m "$(cat <<'EOF'
docs: README + roadmap for `help` verb

README Quick start gains a `fngr help` example block (bare, named
command, verb tree) right above the version example.

Roadmap consolidates the two shipped CLI surface alignment items
(compact help + help alias) into one Done bullet. The third item
in that section (`-S` for search everywhere) was already shipped
behavior described in past tense — section now empty so it's
removed entirely.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist (after both tasks complete)

- [ ] `fngr help` and `fngr --help` produce byte-identical output.
- [ ] `fngr help add` and `fngr add --help` produce byte-identical output.
- [ ] `fngr help event show` works (verb-tree path).
- [ ] `fngr help totally-not-a-command` exits non-zero with a Kong error.
- [ ] `FNGR_DB=/nonexistent ./build/fngr help` exits 0 (no DB required).
- [ ] `make test` passes; coverage on `HelpCmd.Run` is 100%.
- [ ] `make lint` passes; no new warnings.
- [ ] `README.md` mentions `fngr help` in Quick start.
- [ ] `docs/superpowers/roadmap.md` `Done` section has the new bullet; the old `CLI surface alignment` section is gone.
