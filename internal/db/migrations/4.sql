-- Refresh planner statistics for event_meta. Migration 2 dropped
-- idx_event_meta_key_value and created idx_event_meta_key_value_event_id
-- in its place without re-running ANALYZE, so sqlite_stat1 has never had a
-- row for the current index and `meta` / `meta -S` are planned on SQLite's
-- built-in guesses. (The leftover row naming the dropped index is inert —
-- SQLite ignores statistics for indexes that no longer exist.) ANALYZE
-- writes nothing for an empty table, so a freshly created database is not
-- saddled with zero-row statistics; it just keeps the built-in guesses,
-- which is what it would have had anyway.
ANALYZE event_meta;

-- The rest of migration 4 is Go: see repairLegacyText in migrate4.go.
-- SQLite's TRIM() strips only U+0020, so migration 3 could not reproduce
-- strings.TrimSpace, and the repair needs the real @person / #tag
-- extractor rather than a second SQL transliteration of it.
