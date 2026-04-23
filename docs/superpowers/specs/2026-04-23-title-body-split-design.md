# Title + body data-model split — design

**Status:** Spec, awaiting plan.
**Date:** 2026-04-23.
**Roadmap entry:** `docs/superpowers/roadmap.md` → "Data model" → "Title + body
split".

## Goal

Replace the single `events.text` column with separate `title` + `body`
columns. The split rule on input is **literal `". "` (dot + space)**:
everything before the first `". "` is the title, everything after is the
body, and the `". "` itself is dropped. When no `". "` appears the whole
input is the title and the body is empty. Both sides are trimmed of
leading/trailing whitespace.

This carves out a "headline" so list views can scan as titles while the
body remains available in detail views (`fngr event N`) and structured
outputs (JSON, CSV, Markdown).

Pre-1.0: no backward compatibility for the wire format or the schema.
Migration is destructive on the old `text` column (renamed to `title`).

## Split rule

Implemented as a pure helper in `internal/parse/parse.go`:

```go
// SplitTitleBody splits text on the first occurrence of ". " (dot
// followed by a space). The dot+space sequence is dropped; both sides
// are trimmed of surrounding whitespace. When no ". " appears, the
// whole input becomes the title and the body is empty.
func SplitTitleBody(text string) (title, body string)
```

Examples (canonical truth-table — also the test cases for `TestSplitTitleBody`):

| Input                          | Title             | Body                |
| ------------------------------ | ----------------- | ------------------- |
| `""`                           | `""`              | `""`                |
| `"   "`                        | `""`              | `""`                |
| `"hello"`                      | `"hello"`         | `""`                |
| `"hello."`                     | `"hello."`        | `""`                |
| `"hello. world"`               | `"hello"`         | `"world"`           |
| `"  hello  .  world  "`        | `"hello"`         | `"world"`           |
| `"v1.2 done. Hotfix"`          | `"v1.2 done"`     | `"Hotfix"`          |
| `"v1.2.3 released"`            | `"v1.2.3 released"` | `""`              |
| `"first. second. third"`       | `"first"`         | `"second. third"`   |
| `". hello"`                    | `""`              | `"hello"`           |
| `"hello. "`                    | `"hello"`         | `""`                |

The split happens at the CLI boundary for `fngr add` (text/args/stdin/editor
sources) and for `fngr event text`. The JSON paths (`fngr add --format=json`
and `fngr --format=json` output) bypass the split entirely — `title` and
`body` cross the wire as separate fields.

## Schema and migration

### Final schema (post-migration 3)

```sql
CREATE TABLE events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    parent_id  INTEGER REFERENCES events(id) ON DELETE CASCADE,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
```

`event_meta` and `events_fts` retain their existing shapes; `events_fts`
content gets rebuilt with the new formula (see below).

### Migration 3 (`internal/db/migrations/3.sql`)

Pure SQL — no Go pass over rows. Gated by `PRAGMA user_version`, applied
after migration 2.

```sql
ALTER TABLE events RENAME COLUMN text TO title;
ALTER TABLE events ADD COLUMN body TEXT NOT NULL DEFAULT '';

-- Two-pass split on first '. ' separator. Order matters: populate body
-- first, then truncate title — pass 2 destroys the INSTR landmark.
UPDATE events
   SET body = SUBSTR(title, INSTR(title, '. ') + 2)
 WHERE INSTR(title, '. ') > 0;

UPDATE events
   SET title = SUBSTR(title, 1, INSTR(title, '. ') - 1)
 WHERE INSTR(title, '. ') > 0;

-- Rebuild events_fts content from the new columns + meta values.
-- Formula must match internal/parse/parse.go::FTSContent (cross-linked
-- by a comment on that function).
DELETE FROM events_fts;
INSERT INTO events_fts(rowid, content)
SELECT e.id,
       TRIM(e.title || ' ' || e.body || COALESCE(' ' || GROUP_CONCAT(em.value, ' '), ''))
  FROM events e
  LEFT JOIN event_meta em ON em.event_id = e.id
 GROUP BY e.id;

PRAGMA user_version = 3;
```

Notes:

- `title` is `NOT NULL` with no default — the rename + UPDATE preserves
  existing values, so no row hits the constraint.
- `body` defaults to `''` for safety; nothing post-migration writes pre-3
  SQL.
- `WHERE INSTR(title, '. ') > 0` skips rows without `". "` — they keep
  the full text in `title` and empty `body`.
- `TRIM(...)` collapses the trailing/leading whitespace that
  `e.title || ' ' || e.body` would emit when `body` is empty (matches
  the Go `strings.TrimSpace` applied by `parse.FTSContent`).

## Go API changes

### `internal/parse/parse.go`

**New** function `SplitTitleBody` (signature above).

**Changed** function `FTSContent` — signature becomes
`FTSContent(title, body string, meta Meta) string`. Output:

```go
strings.TrimSpace(title + " " + body + " " + strings.Join(metaValues, " "))
```

A doc comment on `FTSContent` declares the contract:

> The SQL rebuild query in `internal/db/migrations/3.sql` must match
> this formula. If you change one, change the other.

`BodyTags` continues to take a single string. Callers that want tags
extracted from "everything the user typed" pass `title + " " + body`.

### `internal/event/event.go`

**`Event` struct:** drop `Text string`, add `Title string` and `Body string`.

**`AddInput` struct:** drop `Text string`, add `Title string` and `Body string`.

**`Add` / `AddMany` / `addInTx`:**
- INSERT statements switch from `(parent_id, text, ...)` to
  `(parent_id, title, body, ...)`.
- Body-tag extraction calls `parse.BodyTags(in.Title + " " + in.Body)`.
- FTS content built via `parse.FTSContent(in.Title, in.Body, mergedMeta)`.
- Reject `strings.TrimSpace(in.Title) == ""` with a clear error
  (`event title cannot be empty`).

**`Update` signature:** changes from
`Update(ctx, db, id int64, text *string, createdAt *time.Time) error`
to
`Update(ctx, db, id int64, title, body *string, createdAt *time.Time) error`.

Semantics:
- nil pointer = field unchanged.
- Empty-string title is rejected (same rule as `Add`).
- Empty-string body is allowed (clears the body).
- When either title or body changes, body-tag sync runs by computing
  `parse.BodyTags(oldTitle+" "+oldBody)` and
  `parse.BodyTags(newTitle+" "+newBody)`, deleting the diff, inserting
  the additions via `INSERT ... ON CONFLICT DO NOTHING`.
- When title or body changes (regardless of meta change), `events_fts`
  for the row is rebuilt via `rebuildEventFTS` using the new
  `parse.FTSContent` signature.
- When only `createdAt` changes, no body-tag sync, no FTS rebuild
  (matches existing behavior).

**`SELECT` rows:** every query that scanned `text` now scans `title, body`
(`Get`, `List`, `ListSeq`, `GetSubtree`, the body-tag sync read in
`Update`).

### `internal/event/store.go`

`Store.Update` signature follows the underlying function (title and body
pointers).

### `cmd/fngr/store.go`

The `eventStore` interface mirrors the new `Update` signature.

## CLI surface

### `fngr add`

Body-source resolution unchanged (args / stdin / editor / `--format=json`).
After resolution, the CLI calls `parse.SplitTitleBody` on the resolved
string and populates `event.AddInput.Title` + `.Body`.

```
fngr add "deployed v1.2 to staging. needed a manual restart on host-3 #ops"
fngr add "v1.2 release done"          # title only, body=""
fngr add "v1.2.3 released"            # no '. ', title only
fngr add ""                           # error (existing rule)
fngr add ". body"                     # error (empty title after split)
```

### `fngr add --format=json`

Wire shape changes — old `text` field removed, replaced by `title` +
`body`:

```json
{
  "title": "deployed v1.2 to staging",
  "body":  "needed a manual restart on host-3 #ops",
  "parent_id": 12,
  "created_at": "2026-04-22T09:00:00Z",
  "meta": [["env", "prod"]]
}
```

Rules:
- `title` required; missing or empty (after trim) → error.
- `body` optional; defaults to `""`.
- Old `text` field absent. `DisallowUnknownFields` rejects it loudly.
- Array form (atomic batch) and single-object form both supported, same
  as today.
- Per-record body is taken verbatim — JSON path never calls
  `SplitTitleBody`. Round-trip (`fngr --format=json | fngr add --format=json`)
  preserves title and body exactly.

### `fngr event` verbs — three text mutators

Replaces the single `event text` verb with three:

```
fngr event title <id> "new title only"
fngr event body  <id> "new body only"
fngr event text  <id> "fresh title. fresh body"
```

- `event title` — passes `&title` only to `event.Update`. Errors on empty.
- `event body` — passes `&body` only. Empty arg clears the body.
- `event text` — calls `parse.SplitTitleBody(arg)`, passes both pointers.
  Errors on empty title (same rule as `event title`).

All three trigger the same body-tag sync path inside `event.Update`
(diff over `oldTitle+" "+oldBody` vs `newTitle+" "+newBody`).

Existing verbs (`time`, `date`, `attach`, `detach`, `tag`, `untag`)
unchanged.

### `fngr event N` (detail view)

Existing layout uses one-line labels (`ID:` / `Parent:` / `Date:` /
`Text:` / `Meta:`). Replace the single `Text:` line with a `Title:`
labeled line followed (when body is non-empty) by a blank line and the
raw body block. `Meta:` block stays as-is.

Layout when body is non-empty:

```
ID:     1
Parent: 12
Date:   2026-04-22 09:00:00
Title:  deployed v1.2 to staging

needed a manual restart on host-3 #ops
Meta:
  author=nicolasm
  tag=ops
```

When body is empty, the blank line and body block are omitted — `Meta:`
follows directly after `Title:`:

```
ID:     1
Parent: 12
Date:   2026-04-22 09:00:00
Title:  deployed v1.2 to staging
Meta:
  author=nicolasm
```

### Filter (`-S`)

No change. FTS still indexes title + body + meta as one bag of words, so
`fngr -S 'deploy'` still hits events with "deploy" in either field. No
new `title:` or `body:` operators (out of scope).

## Renderer behavior

### Tree (`fngr` default, `--format=tree`)

Title only on each line. Body invisible — no inline marker for
body-bearing events. Use `fngr event N` for detail.

```
1  9.32pm  deployed v1.2 to staging
└─ 2  9.45pm  rollback needed
```

### Flat (`--format=flat`)

Title only, same treatment as tree.

```
1  Apr 22 9.32pm  deployed v1.2 to staging
2  Apr 22 9.45pm  rollback needed
```

### JSON (`--format=json`)

Output mirrors input wire shape — round-trippable.

```json
[
  {
    "id": 1,
    "parent_id": null,
    "created_at": "2026-04-22T09:00:00Z",
    "title": "deployed v1.2 to staging",
    "body": "needed a manual restart on host-3 #ops",
    "meta": [["author", "nicolasm"], ["tag", "ops"]]
  }
]
```

`text` field removed. `body` always present (empty string when no body).

### CSV (`--format=csv`)

Existing columns are `id, parent_id, created_at, author, text` (author
extracted from meta; other meta omitted). Replace `text` with `title, body`:

New columns: `id, parent_id, created_at, author, title, body`. Header
line emitted. Multi-line bodies CSV-escaped per RFC 4180. Adding a
`meta` column for the rest of meta is out of scope (CSV stays
author+narrative only, matching the existing shape).

### Markdown (`--format=md`)

Bullet shows title; body renders as 2-space-indented continuation lines
under the bullet. Meta line follows body, also 2-space-indented (matches
existing convention).

```markdown
## 2026-04-22

- 9.00am — deployed v1.2 to staging
  needed a manual restart on host-3
  ran into firewall issue first
  author=nicolasm tag=ops
- 9.45am — rollback needed
  author=nicolasm
```

Rules:
- Body present, single line → one indented continuation line.
- Body present, multi-line → each line indented 2 spaces (blank lines
  inside body emit as 2-space-indented blank lines so the bullet doesn't
  break).
- Body empty → no body lines; meta line still appears.

### Streaming variants

`FlatStream`, `JSONStream`, `CSVStream`, `MarkdownStream` get the same
treatment as their buffered counterparts. `iter.Seq2[Event, error]` flow
unchanged.

## Testing

### `internal/parse`

- `TestSplitTitleBody` — table-driven over the truth-table above.
- `TestFTSContent` — extend existing tests for the new
  `(title, body, meta)` signature; assert
  `strings.TrimSpace(title+" "+body+" "+joinedMetaValues)`.

### `internal/event`

- `TestAdd_TitleBody` — `Add` and `AddMany` populate both columns; FTS
  row contains both.
- `TestUpdate_TitleOnly` / `TestUpdate_BodyOnly` /
  `TestUpdate_BothViaText` — each path triggers body-tag sync against
  the new concat; FTS rebuilt.
- `TestUpdate_BodyTagSync_AcrossFields` — moving `#tag` from title to
  body (and vice versa) keeps the meta row stable (no spurious
  delete+insert).
- `TestUpdate_TimestampOnly_NoFTSRebuild` — confirms existing optimization.
- `TestList_ScansTitleBody` — `List` / `ListSeq` / `Get` / `GetSubtree`
  populate both fields on the returned `Event`.
- `TestEmptyTitleRejected` — `Add` and `Update` reject `Title=""` (or
  whitespace-only).
- `TestEmptyBodyAllowed` — `Add` accepts `Body=""`; `Update` with
  `*body=""` clears the body.

### `internal/db`

- `TestMigration3_SplitsAndFTSRebuild` — seed pre-3 schema with v1/v2,
  insert rows like `"hello"`, `"v1.2 done. Hotfix #ops"`, `"no separator
  here"`, `". body only"`; run migrations; assert:
  - `title` / `body` columns populated correctly per row
  - `events_fts` content includes title + body + meta values, trimmed
  - `user_version = 3`

### `cmd/fngr` (through Kong Parse+Run)

- `TestAddCmd_SplitsBody` — `fngr add "title. body"` → stored as
  title/body.
- `TestAddCmd_NoSeparator` — title-only.
- `TestAddCmd_RejectEmptyTitle` — `fngr add ". body"` errors.
- `TestAddJSON_NewShape` — array + single object both work; `text` field
  rejected via `DisallowUnknownFields`; missing `body` defaults to `""`;
  missing/empty `title` errors.
- `TestEventTitleCmd` / `TestEventBodyCmd` / `TestEventTextCmd` — each
  through Parse+Run; verify the right column(s) update and meta sync
  fires.
- `TestEventShow_BodyEmpty` / `TestEventShow_BodyPresent` — detail
  layout matches Section 3.
- `TestRender_TitleOnlyInList` — tree/flat assert body absent.
- `TestRender_MarkdownBodyContinuation` — md format includes body as
  indented continuation lines under the bullet.
- `TestRender_CSVColumns` — header line + sample row, multi-line body
  escaped.
- `TestRender_JSONShape` — round-trip
  `fngr --format=json | fngr add --format=json` preserves title/body
  verbatim.

## Out of scope

- Backward-compat shim for the old `text` JSON field.
- `title:foo` / `body:foo` filter operators.
- Body-presence indicator in tree/flat (e.g. `…`).
- Display options for "expand body in list views" (use `fngr event N` or
  `--format=md`).
- Schema-level `CHECK (LENGTH(title) > 0)` — Go layer enforces the
  invariant; the migration's data is preserved as-is.
