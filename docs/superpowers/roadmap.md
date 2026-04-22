# fngr roadmap

Tool is pre-public; backward compatibility is **not** a constraint. Each
sub-project below ships through its own brainstorm → spec → plan → implement
cycle. Specs land under `docs/superpowers/specs/`, plans under
`docs/superpowers/plans/`.

## Done

- **`list` UX overhaul** — `fngr` ≡ `fngr list`, descending default, human
  time formats (`Dec 09 9.32pm`), streaming renderers, auto-pagination on TTY.
- **`event` namespace** — `fngr event N` shows event N; verbs
  `text` / `time` / `date` / `attach` / `detach` / `tag` / `untag` mutate.
  Old `fngr edit` and `fngr show` removed.
- **`meta` UX** — `fngr meta` ≡ `fngr meta list`; `-S` filter accepts bare
  key / `key=value` / `@person` / `#tag`; `meta rename` (was `meta update`)
  and `meta delete` mutate.
- **`add` body-input modes** — `fngr add foo bar` joins multi-arg into a
  single body; `cmd | fngr add` reads stdin; bare `fngr add` in a TTY (or
  with `-e`) launches `$VISUAL`/`$EDITOR`; empty editor save cancels
  cleanly. Conflicts (args+stdin, --edit+stdin) error loudly.

## Add command ergonomics

- **`--format=json` import** — accept a single event or an array of events on
  stdin / in a file for bulk import.

## Output format polish

- **JSON tag shape** — switch `meta` from `{key: [values]}` to
  `[[key, value], ...]`. Shorter on the wire and naturally extends to
  per-tuple multi-value semantics down the road.
- **Markdown format** (`--format=md`) — one `##` header per date followed by
  a bullet list of `<time> — <content>` entries.

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

## Deferred

- **Auto-tag character expansion** — explore whether other shorthand symbols
  (e.g. `^location`, `+company`, `~mood`) are worth adding alongside the
  existing `@person` / `#tag` system, and which symbols are unambiguous
  enough. Open question; brainstorm separately before commitment.
