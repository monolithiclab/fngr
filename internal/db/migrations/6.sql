-- Split events_fts into `content` (the event's own text) and `meta` (its
-- key=value tokens). Both used to share one column, so a body that spelled
-- out `tag=ops` produced a token no query could tell from a real tag.
--
-- A virtual table takes no ALTER TABLE ADD COLUMN, so the index is dropped and
-- recreated; the Go half re-populates it from the events table. The delete
-- trigger is ON events, not ON events_fts, so it survives the drop and its
-- body still matches the new table.
DROP TABLE IF EXISTS events_fts;

CREATE VIRTUAL TABLE events_fts USING fts5(
    content,
    meta,
    tokenize = "unicode61 tokenchars '=/'"
);
