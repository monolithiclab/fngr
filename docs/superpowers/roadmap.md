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
- **`add --format=json` import + meta JSON shape** — `fngr add --format=json`
  accepts a single event object or an array on stdin or args; per-record
  defaults flow JSON value > CLI flag > built-in; batches are atomic. JSON
  meta shape across both input and `fngr list --format=json` output is now
  `[[key, value], ...]` sorted by `(key, value)` — replaces the prior
  `{key: [values]}` map.
- **Markdown output** (`--format=md`) — `fngr list` and `fngr event N`
  emit a Markdown digest grouped by local date: one `## YYYY-MM-DD`
  header per date followed by `- <time> — <body>` bullets. Multi-line
  bodies and meta render as 2-space-indented continuation lines.

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

## Project infrastructure

- **GitHub Actions CI** — workflow on push and pull requests (against
  `main`) running `make ci` (codefix + format + lint + test) on the
  `monolithiclab/fngr` repo. Matrix on `ubuntu-latest` + `macos-latest`
  since end users run the binary on both. Single Go version, taken from
  `go.mod` via `actions/setup-go@v5`'s `go-version-file`. Cache
  `~/go/pkg/mod` + `~/.cache/go-build` keyed on `go.sum`. Run
  `go test -race -coverprofile=coverage.out` and upload the profile as
  an artifact (or as a PR comment via a coverage action). Open
  questions: split lint/test into parallel jobs vs single `make ci`
  call; add `govulncheck` and `staticcheck` standalone steps or rely
  on the existing `make lint` umbrella; whether to gate merge on
  per-function coverage thresholds (project's standing rule today is
  a manual check, not a CI gate).

- **GitHub Actions release** — workflow triggered on annotated tag
  push matching `v*.*.*`. Cross-compiles via the existing `make build`
  (which already injects version through `-ldflags '-X main.version=<tag>'`
  per CLAUDE.md) for `darwin/amd64`, `darwin/arm64`, `linux/amd64`,
  `linux/arm64`. Each artifact named `fngr_<version>_<os>_<arch>` (or
  packaged as a `.tar.gz` with the binary + LICENSE + README). Generates
  a `SHA256SUMS` file alongside. Drafts a GitHub release for the tag
  with notes auto-populated from `git log <prev-tag>..<tag>` (or
  `gh release create --generate-notes`); attaches the binaries +
  checksums as release assets. Open questions: signing strategy
  (cosign vs minisign vs unsigned for now); whether to mirror to a
  Homebrew tap or `go install`-only for distribution; whether to
  publish a pre-release (`-rc1`) flow when the tag matches `v*.*.*-*`.

## Deferred

- **Auto-tag character expansion** — explore whether other shorthand symbols
  (e.g. `^location`, `+company`, `~mood`) are worth adding alongside the
  existing `@person` / `#tag` system, and which symbols are unambiguous
  enough. Open question; brainstorm separately before commitment.
