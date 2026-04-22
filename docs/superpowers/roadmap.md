# fngr roadmap

Tool is pre-public; backward compatibility is **not** a constraint. Each
sub-project below ships through its own brainstorm → spec → plan → implement
cycle. Specs land under `docs/superpowers/specs/`, plans under
`docs/superpowers/plans/`.

## S1 — `list` UX overhaul (done)

- `fngr` (no args) ≡ `fngr list`.
- Default sort is descending on `created_at` (newest first); replace
  `--sort asc|desc` with `-r` / `--reverse`.
- Time display in human formats: `Dec 09 9.32pm` (no year, period in time,
  lowercase am/pm).
- Stream rows from the database to the renderer wherever the renderer allows
  (tree must still load all events to compute parent/child topology; flat,
  json, csv can stream in batches).
- Auto-paginate when stdout is a TTY.

## S2 — `event` namespace + subcommands (done)

- Rename `show` → `event`. `fngr event 5` shows event 5 (replaces current
  `fngr show 5`); `fngr event 5 --tree` keeps the subtree view.
- Subcommand verbs:
  - `fngr event 5 text "..."` — replace text. Re-parse `@person` / `#tag`
    body tags and merge with existing meta (`INSERT … ON CONFLICT DO NOTHING`
    semantics).
  - `fngr event 5 time "..."` — replace clock time, keep original date when
    only a time is given; full timestamp replaces both.
  - `fngr event 5 date "..."` — replace date, keep original time when only a
    date is given; full timestamp replaces both.
  - `fngr event 5 attach <id>` — set `parent_id`.
  - `fngr event 5 detach` — clear `parent_id`.
  - `fngr event 5 tag @Mila env=prod` — add one or more tags (n args, mixes
    `@`/`#`/`key=value`).
  - `fngr event 5 untag @Mila tag=ops` — remove one or more tags (n args).
- The current `fngr edit` is removed (its behaviour folds into
  `fngr event N text/time/date`).

## S3 — `meta` UX (done)

- `fngr meta` (no subcommand) ≡ `fngr meta list`.
- `fngr meta` accepts a filter argument (key only, value only, or `key=value`).
- `meta update` renamed to `meta rename`.

## S4 — `help` alias

- `fngr help` ≡ `fngr --help`.
- `fngr help <cmd>` ≡ `fngr <cmd> --help`.

## S5 — Auto-tag character expansion (deferred)

Open question, not a feature. Brainstorm separately before commitment. Goal:
explore whether other shorthand symbols (e.g. `^location`, `+company`,
`~mood`) are worth adding given the existing `@person` / `#tag` system, and
which symbols are unambiguous enough to use.
