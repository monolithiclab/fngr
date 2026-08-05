-- Provenance for event_meta rows, so a body edit can only retract the tags
-- the body itself put there. 'body' marks a tuple derived from an inline
-- `@person` / `#tag`; 'explicit' marks one the operator supplied (--meta,
-- `event tag`, `meta rename`). Update's sync deletes 'body' rows only.
--
-- 'explicit' is the safe default for the rows already on disk — it can cost
-- a stale tag, never a lost one — and the Go step attached to this migration
-- demotes the subset the event's own text still yields.
--
-- The CHECK pins the enum in the schema rather than only in Go: the two
-- values are also written as string literals by this migration's Go step, and
-- a third value would be a row deleteBodyMetaTuples can never match and
-- nothing would report. No index — every statement that filters on source
-- also gives (key, value, event_id), which migration 2's unique index already
-- covers, so one here would be write amplification for zero reads.
ALTER TABLE event_meta ADD COLUMN source TEXT NOT NULL DEFAULT 'explicit'
    CHECK (source IN ('body', 'explicit'));
