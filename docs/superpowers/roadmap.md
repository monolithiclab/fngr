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
- **GitHub Actions CI + release pipeline** — every push to `main` and
  every PR validates against `make lint test` on a Linux + macOS
  matrix; every `v*.*.*` tag triggers a GoReleaser-driven multi-channel
  release (GitHub Release with cross-compiled binaries + cosign-signed
  SHA256SUMS, multi-arch container image on `ghcr.io/monolithiclab/fngr`,
  Homebrew formula on `monolithiclab/homebrew-tap`). Pre-release tags
  (`v*.*.*-rc1` etc.) skip the `:latest` Docker tag and the brew
  formula bump.

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

## Data model

- **Title + body split** — replace the single `text` column with `title`
  - `body`. The split rule on input is "everything before the first `.`
    is the title; the rest is the body" (whole input is the title when no
    `.` is present). Migration is pure SQL via a new
    `internal/db/migrations/3.sql`: `ALTER TABLE events RENAME COLUMN text
TO title`, `ALTER TABLE events ADD COLUMN body TEXT NOT NULL DEFAULT
''`, then a single `UPDATE events SET title = ..., body = ...` using
    SQLite's `instr()` / `substr()` to perform the split — no Go pass over
    rows. Rebuild `events_fts` content from the new columns. Open
    brainstorm questions: how do renderers display the split (markdown
    bullet shows title only, with body indented; tree shows title; event
    detail shows both); what does `fngr add "no dot"` produce (title-only
    event, empty body); does `event text` become `event title` + `event
body` (or stay as `event text` and re-split each time)?

## Publishing pipeline polish

Follow-ups from the v0.0.1 release rollout (full context in
`docs/PUBLISHING.md` "Gotchas"). Each is functional today; the
migrations are quality-of-life cleanups that can wait until the
deprecated keys are actually removed by upstream.

- **`dockers:` + `docker_manifests:` → `dockers_v2:`** — current
  config uses the deprecated GoReleaser keys (warnings every release).
  The new shape needs a multi-stage Dockerfile that uses buildx's
  `TARGETOS` / `TARGETARCH` build args to pick the right per-platform
  binary. Non-trivial Dockerfile rewrite; defer until removal of
  `dockers:` becomes urgent.
- **`brews:` → `homebrew_formulas:`** — `brews:` is deprecated in
  favor of `homebrew_casks:`, but Casks are macOS-only and require
  `brew install --cask`, which would break the cross-platform install
  path we promise (`brew install monolithiclab/tap/fngr` from Linux
  too). Wait for GoReleaser to ship a `homebrew_formulas:` key.
- **Cosign `signs:` → v4 bundle format** — pinned to
  `sigstore/cosign-installer@v3` because cosign v4 deprecated the
  `--output-signature` / `--output-certificate` flags in favor of a
  single `.sigstore.json` bundle. Migration touches the
  `.goreleaser.yaml` `signs:` block, the README's verification
  example (current `cosign verify-blob --signature SHA256SUMS.sig
--certificate SHA256SUMS.pem` would become a single `--bundle`
  flag), and `docs/PUBLISHING.md`'s downstream-verification section.
- **Brew formula path** — GoReleaser writes `<name>.rb` at the tap
  root by default. Both layouts work for `brew install`, but
  `Formula/<name>.rb` is the conventional Homebrew tap structure.
  Add `directory: Formula` to the `brews:` block and re-tag.

## Considered (not pursued)

Feature ideas that have come up across reviews and brainstorms.
Each was deliberately deferred or rejected with the reasoning below;
re-listed here so future passes don't re-propose them without new
information. Not commitments — items move to a real section above
only on real demand.

- **Config file (`~/.fngr.config`)** — env vars + CLI flags already
  cover persistent defaults. A config layer means precedence rules,
  parsing, and a third place to look for behavior.
- **Multiple databases / workspaces** — `cd` plus `FNGR_DB` already
  implements this. No need for a separate workspace concept.
- **Relative dates** (`today`, `yesterday`) — `timefmt` is the
  natural place to add them later if requested. Shell handles it
  fine: `--time "$(date -d yesterday +%F)"`.
- **Auto-tag character expansion** — explore whether other shorthand
  symbols (e.g. `^location`, `+company`, `~mood`) are worth adding
  alongside the existing `@person` / `#tag` system, and which symbols
  are unambiguous enough. Open question; brainstorm separately
  before commitment.
- **Soft delete / undo** — adds schema complexity and a parallel
  "alive" view path for marginal benefit. Backups (`cp ~/.fngr.db`)
  cover recovery.
- **Stats / summary command** — anything useful is a one-liner
  against the SQLite file. Bloats the CLI surface for a workflow
  most users will run rarely.
- **Author normalization / user registry** — belongs to user data
  hygiene, not the tool. `meta rename` already exists for cleanup.
- **Shell completion** — Kong supports it natively if needed. Not
  load-bearing; revisit on user request.
- **Snapshot / backup command** — a copy of the SQLite file is the
  backup. Don't reinvent.
- **Database maintenance commands** (`vacuum`, etc.) — single SQL
  statement; not worth a CLI surface. Document in README only if a
  user actually asks.
- **Bulk operations / filtered delete** — composes from
  `fngr -S '...' --format json | jq | xargs fngr delete` for the
  rare case. Adding `--filter` to `delete` adds destructive surface
  area for marginal value.
- **`fngr add -` as explicit stdin form** — auto-detect via non-TTY
  pipe handles every real workflow; explicit form would only force
  stdin in a TTY, no use case today.
- **Tokenize `$EDITOR` / `$VISUAL` for `vim -u NONE`-style values** —
  plausible follow-up (matches `pagerCommand`'s tokenization), but
  not in the body-input modes spec. Most users set `EDITOR=vim`
  (single token); revisit on real demand.
