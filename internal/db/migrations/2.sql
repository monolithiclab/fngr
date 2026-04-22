-- Pre-emptive dedupe (no-op when Add already deduped via
-- parse.CollectMeta, which is the only known insert path).
DELETE FROM event_meta
 WHERE rowid NOT IN (
   SELECT MIN(rowid) FROM event_meta
    GROUP BY event_id, key, value
 );

-- Replace the non-unique (key, value) index with a UNIQUE
-- index on (key, value, event_id). Same prefix-lookup
-- performance for ListMeta / CountMeta plus DB-level
-- uniqueness so INSERT ... ON CONFLICT DO NOTHING works.
DROP INDEX IF EXISTS idx_event_meta_key_value;
CREATE UNIQUE INDEX idx_event_meta_key_value_event_id
    ON event_meta(key, value, event_id);
